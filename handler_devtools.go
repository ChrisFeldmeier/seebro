package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// DevTools-style endpoints: network log, console, dialogs, waiting,
// emulation, PDF export, element inspection and downloads.

const defaultBodyLimit = 100_000 // bytes returned inline for /network/request

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func queryInt(r *http.Request, key string, fallback int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil {
		return n
	}
	return fallback
}

// monitorFor resolves a tab and its recorder.
func (b *Bridge) monitorFor(tabID string) (context.Context, string, *tabMonitor, error) {
	ctx, resolved, err := b.TabContext(tabID)
	if err != nil {
		return nil, "", nil, err
	}
	m := b.Monitor(resolved)
	if m == nil {
		return nil, "", nil, fmt.Errorf("no monitor for tab %s", resolved)
	}
	return ctx, resolved, m, nil
}

// ── GET /network ───────────────────────────────────────────

func (b *Bridge) handleNetwork(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	_, tabID, m, err := b.monitorFor(q.Get("tabId"))
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	f := netFilter{
		all:     q.Get("all") == "true",
		types:   splitList(q.Get("type")),
		urlSub:  q.Get("filter"),
		method:  q.Get("method"),
		status:  q.Get("status"),
		limit:   queryInt(r, "limit", 100),
		offset:  queryInt(r, "offset", 0),
		headers: q.Get("headers") == "true",
	}
	reqs, total := m.Requests(f)
	jsonResp(w, 200, map[string]any{"tabId": tabID, "requests": reqs, "count": len(reqs), "total": total})
}

// ── GET /network/request ───────────────────────────────────

func (b *Bridge) handleNetworkRequest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctx, _, m, err := b.monitorFor(q.Get("tabId"))
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	id := q.Get("id")
	if id == "" {
		jsonErr(w, 400, fmt.Errorf("id required (from /network)"))
		return
	}
	entry, ok := m.Request(id)
	if !ok {
		jsonErr(w, 404, fmt.Errorf("request %s not in buffer", id))
		return
	}
	limit := queryInt(r, "maxBytes", defaultBodyLimit)
	resp := map[string]any{"request": entry}

	tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)
	defer tCancel()

	if entry.HasPostData {
		var post string
		if err := chromedp.Run(tCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			var e error
			post, e = network.GetRequestPostData(entry.reqID).Do(ctx)
			return e
		})); err == nil {
			resp["postData"] = truncate(post, limit)
		}
	}

	if q.Get("body") != "false" && entry.Done && entry.Failed == "" {
		var body []byte
		err := chromedp.Run(tCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			var e error
			body, e = network.GetResponseBody(entry.reqID).Do(ctx)
			return e
		}))
		switch {
		case err != nil:
			resp["bodyError"] = err.Error()
		case q.Get("saveTo") != "":
			path := q.Get("saveTo")
			if e := os.MkdirAll(filepath.Dir(path), 0755); e == nil {
				err = os.WriteFile(path, body, 0644)
			}
			if err != nil {
				resp["bodyError"] = err.Error()
			} else {
				resp["savedTo"], resp["bodySize"] = path, len(body)
			}
		case isTextual(entry.MimeType, body):
			resp["body"] = truncate(string(body), limit)
			resp["bodySize"] = len(body)
			if len(body) > limit {
				resp["bodyTruncated"] = true
			}
		default:
			resp["bodySize"] = len(body)
			if q.Get("base64") == "true" && len(body) <= limit {
				resp["bodyBase64"] = base64.StdEncoding.EncodeToString(body)
			} else {
				resp["body"] = "(binary " + entry.MimeType + ", use saveTo= or base64=true)"
			}
		}
	}
	jsonResp(w, 200, resp)
}

func isTextual(mime string, body []byte) bool {
	mime = strings.ToLower(mime)
	for _, t := range []string{"text/", "json", "javascript", "xml", "html", "css", "csv", "x-www-form-urlencoded", "graphql"} {
		if strings.Contains(mime, t) {
			return true
		}
	}
	if mime == "" && len(body) > 0 {
		return json.Valid(body) || !strings.ContainsRune(string(body[:min(len(body), 512)]), 0)
	}
	return false
}

// ── POST /network/clear, /console/clear ───────────────────

func (b *Bridge) handleMonitorClear(clearNet, clearConsole bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			TabID string `json:"tabId"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req)
		_, _, m, err := b.monitorFor(req.TabID)
		if err != nil {
			jsonErr(w, 404, err)
			return
		}
		m.Clear(clearNet, clearConsole)
		jsonResp(w, 200, map[string]any{"cleared": true})
	}
}

// ── GET /console ───────────────────────────────────────────

func (b *Bridge) handleConsole(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	_, tabID, m, err := b.monitorFor(q.Get("tabId"))
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	msgs, total := m.Console(q.Get("all") == "true", splitList(q.Get("level")), queryInt(r, "limit", 100))
	jsonResp(w, 200, map[string]any{"tabId": tabID, "messages": msgs, "count": len(msgs), "total": total})
}

// ── GET/POST /dialog ───────────────────────────────────────

func (b *Bridge) handleGetDialog(w http.ResponseWriter, r *http.Request) {
	_, tabID, m, err := b.monitorFor(r.URL.Query().Get("tabId"))
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	jsonResp(w, 200, map[string]any{"tabId": tabID, "dialog": m.Dialog()})
}

func (b *Bridge) handleDialog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TabID      string `json:"tabId"`
		Action     string `json:"action"` // accept | dismiss
		PromptText string `json:"promptText"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}
	if req.Action != "accept" && req.Action != "dismiss" {
		jsonErr(w, 400, fmt.Errorf("action must be 'accept' or 'dismiss'"))
		return
	}
	ctx, _, m, err := b.monitorFor(req.TabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	d := m.Dialog()
	if d == nil {
		jsonErr(w, 409, fmt.Errorf("no dialog open"))
		return
	}
	tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)
	defer tCancel()
	p := page.HandleJavaScriptDialog(req.Action == "accept")
	if req.PromptText != "" {
		p = p.WithPromptText(req.PromptText)
	}
	if err := chromedp.Run(tCtx, p); err != nil {
		jsonErr(w, 500, fmt.Errorf("handle dialog: %w", err))
		return
	}
	m.clearDialog()
	jsonResp(w, 200, map[string]any{"handled": req.Action, "dialog": d})
}

// ── POST /wait ─────────────────────────────────────────────

type waitRequest struct {
	TabID       string   `json:"tabId"`
	Text        []string `json:"text"`        // any of these appears in body text
	Selector    string   `json:"selector"`    // element exists (or is gone, see Gone)
	Gone        bool     `json:"gone"`        // invert selector/text: wait until absent
	URL         string   `json:"url"`         // location.href contains
	NetworkIdle bool     `json:"networkIdle"` // ≤2 requests in flight for 500ms
	Timeout     int      `json:"timeout"`     // ms, default 10000, max 60000
}

// UnmarshalJSON lets "text" be a single string or a list.
func (w *waitRequest) UnmarshalJSON(data []byte) error {
	type alias waitRequest
	aux := struct {
		Text any `json:"text"`
		*alias
	}{alias: (*alias)(w)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	switch t := aux.Text.(type) {
	case string:
		if t != "" {
			w.Text = []string{t}
		}
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok && s != "" {
				w.Text = append(w.Text, s)
			}
		}
	}
	return nil
}

func (b *Bridge) handleWait(w http.ResponseWriter, r *http.Request) {
	var req waitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}
	if len(req.Text) == 0 && req.Selector == "" && req.URL == "" && !req.NetworkIdle {
		jsonErr(w, 400, fmt.Errorf("need text, selector, url or networkIdle"))
		return
	}
	timeout := time.Duration(req.Timeout) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	ctx, tabID, err := b.TabContext(req.TabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	m := b.Monitor(tabID)

	textJSON, _ := json.Marshal(req.Text)
	selJSON, _ := json.Marshal(req.Selector)
	urlJSON, _ := json.Marshal(req.URL)
	// Returns the matched condition, or "" while still waiting.
	js := fmt.Sprintf(`(() => {
		const texts = %s, sel = %s, url = %s, gone = %t;
		const body = document.body ? document.body.innerText : "";
		let hit = "";
		if (texts.length) {
			const found = texts.find(t => body.includes(t));
			if (gone ? !found : found) hit = gone ? "text gone" : found; else return "";
		}
		if (sel) {
			const el = document.querySelector(sel);
			if (gone ? !el : el) hit = hit || (gone ? "selector gone" : sel); else return "";
		}
		if (url) { if (location.href.includes(url)) hit = hit || location.href; else return ""; }
		return hit || "ok";
	})()`, textJSON, selJSON, urlJSON, req.Gone)
	needJS := len(req.Text) > 0 || req.Selector != "" || req.URL != ""

	start := time.Now()
	deadline := start.Add(timeout)
	for {
		matched := "networkIdle"
		ok := true
		if needJS {
			var res string
			tCtx, tCancel := context.WithTimeout(ctx, 3*time.Second)
			err := chromedp.Run(tCtx, chromedp.Evaluate(js, &res))
			tCancel()
			ok = err == nil && res != ""
			matched = res
		}
		if ok && req.NetworkIdle && m != nil {
			ok = m.NetworkIdle(2, 500*time.Millisecond)
		}
		if ok {
			jsonResp(w, 200, map[string]any{"matched": matched, "elapsedMs": time.Since(start).Milliseconds()})
			return
		}
		if time.Now().After(deadline) || r.Context().Err() != nil {
			jsonErr(w, 408, fmt.Errorf("wait timed out after %s", timeout))
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ── POST /emulate ──────────────────────────────────────────

type emulateRequest struct {
	TabID           string            `json:"tabId"`
	Viewport        string            `json:"viewport"`    // "390x844x3,mobile,touch" or "reset"
	UserAgent       *string           `json:"userAgent"`   // "" clears
	ColorScheme     string            `json:"colorScheme"` // dark | light | auto
	ReducedMotion   string            `json:"reducedMotion"`
	Geolocation     string            `json:"geolocation"` // "lat,lng" or "reset"
	Network         string            `json:"network"`     // Offline | Slow 3G | Fast 3G | Slow 4G | Fast 4G | none
	CPUThrottling   float64           `json:"cpuThrottling"`
	ExtraHTTPHeader map[string]string `json:"extraHeaders"`
	Timezone        string            `json:"timezone"`
	Locale          string            `json:"locale"`
}

// Latency (ms) and throughput (bytes/s), matching Chrome DevTools presets.
var networkPresets = map[string][3]float64{
	"slow 3g": {2000, 50_000, 50_000},
	"fast 3g": {562.5, 180_000, 84_375},
	"slow 4g": {562.5, 180_000, 84_375},
	"fast 4g": {165, 1_012_500, 168_750},
}

func (b *Bridge) handleEmulate(w http.ResponseWriter, r *http.Request) {
	var req emulateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}
	ctx, _, err := b.TabContext(req.TabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	var actions []chromedp.Action
	applied := []string{}

	if v := strings.TrimSpace(req.Viewport); v != "" {
		if v == "reset" {
			actions = append(actions, emulation.ClearDeviceMetricsOverride(), emulation.SetTouchEmulationEnabled(false))
		} else {
			parts := strings.Split(v, ",")
			dims := strings.Split(parts[0], "x")
			if len(dims) < 2 {
				jsonErr(w, 400, fmt.Errorf("viewport must look like 1280x720 or 390x844x3,mobile,touch"))
				return
			}
			wd, _ := strconv.ParseInt(dims[0], 10, 64)
			ht, _ := strconv.ParseInt(dims[1], 10, 64)
			dpr := 1.0
			if len(dims) > 2 {
				dpr, _ = strconv.ParseFloat(dims[2], 64)
			}
			mobile, touch, landscape := false, false, false
			for _, f := range parts[1:] {
				switch strings.TrimSpace(f) {
				case "mobile":
					mobile = true
				case "touch":
					touch = true
				case "landscape":
					landscape = true
				}
			}
			if landscape && ht > wd {
				wd, ht = ht, wd
			}
			if wd <= 0 || ht <= 0 {
				jsonErr(w, 400, fmt.Errorf("invalid viewport %q", v))
				return
			}
			actions = append(actions,
				emulation.SetDeviceMetricsOverride(wd, ht, dpr, mobile),
				emulation.SetTouchEmulationEnabled(touch).WithMaxTouchPoints(5))
		}
		applied = append(applied, "viewport")
	}
	if req.UserAgent != nil {
		actions = append(actions, emulation.SetUserAgentOverride(*req.UserAgent))
		applied = append(applied, "userAgent")
	}
	if req.ColorScheme != "" || req.ReducedMotion != "" {
		var feats []*emulation.MediaFeature
		if req.ColorScheme != "" && req.ColorScheme != "auto" {
			feats = append(feats, &emulation.MediaFeature{Name: "prefers-color-scheme", Value: req.ColorScheme})
		}
		if req.ReducedMotion != "" && req.ReducedMotion != "auto" {
			feats = append(feats, &emulation.MediaFeature{Name: "prefers-reduced-motion", Value: req.ReducedMotion})
		}
		actions = append(actions, emulation.SetEmulatedMedia().WithFeatures(feats))
		applied = append(applied, "media")
	}
	if g := strings.TrimSpace(req.Geolocation); g != "" {
		if g == "reset" {
			actions = append(actions, emulation.ClearGeolocationOverride())
		} else {
			lat, lng, ok := strings.Cut(g, ",")
			la, e1 := strconv.ParseFloat(strings.TrimSpace(lat), 64)
			lo, e2 := strconv.ParseFloat(strings.TrimSpace(lng), 64)
			if !ok || e1 != nil || e2 != nil || la < -90 || la > 90 || lo < -180 || lo > 180 {
				jsonErr(w, 400, fmt.Errorf("geolocation must be 'lat,lng'"))
				return
			}
			actions = append(actions,
				chromedp.ActionFunc(func(ctx context.Context) error {
					c := chromedp.FromContext(ctx)
					return browser.GrantPermissions([]browser.PermissionType{browser.PermissionTypeGeolocation}).
						Do(cdp.WithExecutor(ctx, c.Browser))
				}),
				emulation.SetGeolocationOverride().WithLatitude(la).WithLongitude(lo).WithAccuracy(10))
		}
		applied = append(applied, "geolocation")
	}
	if n := strings.ToLower(strings.TrimSpace(req.Network)); n != "" {
		switch n {
		case "none", "reset", "no throttling":
			actions = append(actions, network.EmulateNetworkConditions(false, 0, -1, -1))
		case "offline":
			actions = append(actions, network.EmulateNetworkConditions(true, 0, 0, 0))
		default:
			p, ok := networkPresets[n]
			if !ok {
				jsonErr(w, 400, fmt.Errorf("network must be Offline, Slow 3G, Fast 3G, Slow 4G, Fast 4G or none"))
				return
			}
			actions = append(actions, network.EmulateNetworkConditions(false, p[0], p[1], p[2]))
		}
		applied = append(applied, "network")
	}
	if req.CPUThrottling > 0 {
		actions = append(actions, emulation.SetCPUThrottlingRate(req.CPUThrottling))
		applied = append(applied, "cpu")
	}
	if req.ExtraHTTPHeader != nil {
		h := network.Headers{}
		for k, v := range req.ExtraHTTPHeader {
			h[k] = v
		}
		actions = append(actions, network.SetExtraHTTPHeaders(h))
		applied = append(applied, "extraHeaders")
	}
	if req.Timezone != "" {
		actions = append(actions, emulation.SetTimezoneOverride(req.Timezone))
		applied = append(applied, "timezone")
	}
	if req.Locale != "" {
		actions = append(actions, emulation.SetLocaleOverride().WithLocale(req.Locale))
		applied = append(applied, "locale")
	}
	if len(actions) == 0 {
		jsonErr(w, 400, fmt.Errorf("nothing to emulate"))
		return
	}

	tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)
	defer tCancel()
	if err := chromedp.Run(tCtx, actions...); err != nil {
		jsonErr(w, 500, fmt.Errorf("emulate: %w", err))
		return
	}
	jsonResp(w, 200, map[string]any{"applied": applied})
}

// ── GET /pdf ───────────────────────────────────────────────

func (b *Bridge) handlePDF(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctx, _, err := b.TabContext(q.Get("tabId"))
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	tCtx, tCancel := context.WithTimeout(ctx, navigateTimeout)
	defer tCancel()

	var buf []byte
	if err := chromedp.Run(tCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var e error
		buf, _, e = page.PrintToPDF().
			WithLandscape(q.Get("landscape") == "true").
			WithPrintBackground(q.Get("printBackground") != "false").
			Do(ctx)
		return e
	})); err != nil {
		jsonErr(w, 500, fmt.Errorf("pdf: %w", err))
		return
	}
	if q.Get("raw") == "true" {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(buf)
		return
	}
	path := q.Get("path")
	if path == "" {
		path = filepath.Join(stateDir, "pdfs", "page-"+time.Now().Format("20060102-150405")+".pdf")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		jsonErr(w, 500, err)
		return
	}
	if err := os.WriteFile(path, buf, 0644); err != nil {
		jsonErr(w, 500, err)
		return
	}
	jsonResp(w, 200, map[string]any{"path": path, "size": len(buf)})
}

// ── GET /element ───────────────────────────────────────────

const elementInfoJS = `function(html, styles) {
	const r = this.getBoundingClientRect();
	const attrs = {};
	for (const a of this.attributes || []) attrs[a.name] = a.value.slice(0, 300);
	const out = {
		tag: this.tagName ? this.tagName.toLowerCase() : this.nodeName,
		attributes: attrs,
		text: (this.innerText || this.textContent || "").trim().slice(0, 1000),
		box: { x: Math.round(r.x + scrollX), y: Math.round(r.y + scrollY), width: Math.round(r.width), height: Math.round(r.height) },
		visible: r.width > 0 && r.height > 0 && getComputedStyle(this).visibility !== "hidden",
	};
	if ("value" in this) out.value = String(this.value).slice(0, 1000);
	if ("checked" in this) out.checked = this.checked;
	if (html) out.html = this.outerHTML.slice(0, 20000);
	if (styles) {
		const cs = getComputedStyle(this);
		const keys = styles === "all" ? Array.from(cs) : ["display","visibility","opacity","position","z-index","width","height","margin","padding","color","background-color","font-family","font-size","font-weight","line-height","border","overflow","cursor","pointer-events"];
		out.styles = {};
		for (const k of keys) out.styles[k] = cs.getPropertyValue(k);
	}
	return out;
}`

func (b *Bridge) handleElement(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctx, tabID, err := b.TabContext(q.Get("tabId"))
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	nodeID, err := b.resolveRef(tabID, q.Get("ref"))
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	if nodeID == 0 && q.Get("selector") == "" {
		jsonErr(w, 400, fmt.Errorf("need ref or selector"))
		return
	}
	tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)
	defer tCancel()

	styles := q.Get("styles") // "", "true", "all"
	var stylesArg any = false
	if styles == "all" {
		stylesArg = "all"
	} else if styles == "true" {
		stylesArg = true
	}
	res, err := callOnTarget(tCtx, nodeID, q.Get("selector"), elementInfoJS, q.Get("html") == "true", stylesArg)
	if err != nil {
		jsonErr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res)
}

// resolveRef maps a snapshot ref to a backend node ID (0 if ref is empty).
func (b *Bridge) resolveRef(tabID, ref string) (int64, error) {
	if ref == "" {
		return 0, nil
	}
	if cache := b.GetRefCache(tabID); cache != nil {
		if nid, ok := cache.refs[ref]; ok {
			return nid, nil
		}
	}
	return 0, fmt.Errorf("ref %s not found - take a /snapshot first", ref)
}

// callOnTarget runs fn with `this` bound to the element (by backend node ID or
// CSS selector) and returns the JSON result.
func callOnTarget(ctx context.Context, backendNodeID int64, selector, fn string, args ...any) (json.RawMessage, error) {
	var out json.RawMessage
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		objectID, err := resolveTargetObject(ctx, backendNodeID, selector)
		if err != nil {
			return err
		}
		callArgs := make([]*runtime.CallArgument, 0, len(args))
		for _, a := range args {
			v, _ := json.Marshal(a)
			callArgs = append(callArgs, &runtime.CallArgument{Value: v})
		}
		res, exc, err := runtime.CallFunctionOn(fn).
			WithObjectID(runtime.RemoteObjectID(objectID)).
			WithArguments(callArgs).
			WithReturnByValue(true).
			WithAwaitPromise(true).
			Do(ctx)
		if err != nil {
			return err
		}
		if exc != nil {
			return fmt.Errorf("js: %s", exceptionText(exc))
		}
		out = json.RawMessage(res.Value)
		if len(out) == 0 {
			out = json.RawMessage("null")
		}
		return nil
	}))
	return out, err
}

func resolveTargetObject(ctx context.Context, backendNodeID int64, selector string) (string, error) {
	if backendNodeID > 0 {
		return resolveNodeToObject(ctx, backendNodeID)
	}
	sel, _ := json.Marshal(selector)
	obj, exc, err := runtime.Evaluate("document.querySelector(" + string(sel) + ")").Do(ctx)
	if err != nil {
		return "", err
	}
	if exc != nil {
		return "", fmt.Errorf("selector: %s", exceptionText(exc))
	}
	if obj == nil || obj.ObjectID == "" {
		return "", fmt.Errorf("element not found: %s", selector)
	}
	return string(obj.ObjectID), nil
}

func exceptionText(e *runtime.ExceptionDetails) string {
	if e.Exception != nil && e.Exception.Description != "" {
		return e.Exception.Description
	}
	return e.Text
}

// ── Downloads ──────────────────────────────────────────────

type downloadInfo struct {
	GUID     string `json:"guid"`
	URL      string `json:"url"`
	File     string `json:"file"`
	State    string `json:"state"`
	Received int64  `json:"receivedBytes"`
	Total    int64  `json:"totalBytes"`
	Path     string `json:"path,omitempty"`
}

var downloads struct {
	sync.Mutex
	list []*downloadInfo
}

// setupDownloads routes all downloads into downloadDir and tracks progress.
func (b *Bridge) setupDownloads() {
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		slog.Warn("download dir", "err", err)
		return
	}
	chromedp.ListenBrowser(b.browserCtx, func(ev any) {
		downloads.Lock()
		defer downloads.Unlock()
		switch e := ev.(type) {
		case *browser.EventDownloadWillBegin:
			downloads.list = append(downloads.list, &downloadInfo{GUID: e.GUID, URL: truncate(e.URL, maxURLLen), File: e.SuggestedFilename, State: "inProgress"})
			if len(downloads.list) > 50 {
				downloads.list = downloads.list[1:]
			}
		case *browser.EventDownloadProgress:
			for _, d := range downloads.list {
				if d.GUID == e.GUID {
					d.State, d.Received, d.Total = string(e.State), int64(e.ReceivedBytes), int64(e.TotalBytes)
					if e.FilePath != "" {
						d.Path = e.FilePath
					} else if d.State == "completed" {
						d.Path = filepath.Join(downloadDir, d.File)
					}
				}
			}
		}
	})
	if err := chromedp.Run(b.browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		c := chromedp.FromContext(ctx)
		return browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllow).
			WithDownloadPath(downloadDir).WithEventsEnabled(true).
			Do(cdp.WithExecutor(ctx, c.Browser))
	})); err != nil {
		slog.Warn("download behavior", "err", err)
	}
}

func (b *Bridge) handleDownloads(w http.ResponseWriter, r *http.Request) {
	downloads.Lock()
	list := make([]downloadInfo, 0, len(downloads.list))
	for _, d := range downloads.list {
		list = append(list, *d)
	}
	downloads.Unlock()

	files := []map[string]any{}
	if entries, err := os.ReadDir(downloadDir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && !e.IsDir() {
				files = append(files, map[string]any{"name": e.Name(), "size": info.Size(), "modified": info.ModTime().Format(time.RFC3339)})
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i]["modified"].(string) > files[j]["modified"].(string) })
	jsonResp(w, 200, map[string]any{"dir": downloadDir, "downloads": list, "files": files})
}
