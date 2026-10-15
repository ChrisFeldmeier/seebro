package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/performance"
	"github.com/chromedp/chromedp"
)

// WebSocket/SSE messages, HAR export and performance metrics.

// ── GET /network/websocket ─────────────────────────────────

func (b *Bridge) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	_, tabID, m, err := b.monitorFor(q.Get("tabId"))
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	msgs, total := m.WebSocketMessages(q.Get("all") == "true", q.Get("filter"), q.Get("dir"), queryInt(r, "limit", 50))
	jsonResp(w, 200, map[string]any{"tabId": tabID, "messages": msgs, "count": len(msgs), "total": total})
}

// ── GET /network/har ───────────────────────────────────────

type harHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func harHeaders(h map[string]any) []harHeader {
	out := make([]harHeader, 0, len(h))
	for k, v := range h {
		out = append(out, harHeader{Name: k, Value: fmt.Sprint(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// handleHAR exports the tab's network log as HAR 1.2 (importable in Chrome
// DevTools, Charles, Postman …). Bodies are fetched only with bodies=true.
func (b *Bridge) handleHAR(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctx, _, m, err := b.monitorFor(q.Get("tabId"))
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	path, err := outputPath(q.Get("path"), ".har", ".json")
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	withBodies := q.Get("bodies") == "true"
	reqs, _ := m.Requests(netFilter{all: q.Get("all") == "true", headers: true})

	tCtx, tCancel := context.WithTimeout(ctx, navigateTimeout)
	defer tCancel()

	entries := make([]map[string]any, 0, len(reqs))
	bodyBudget := 20 << 20 // stop fetching bodies after 20 MB
	for _, n := range reqs {
		qs := []harHeader{}
		if u, err := neturl.Parse(n.URL); err == nil {
			for k, vs := range u.Query() {
				for _, v := range vs {
					qs = append(qs, harHeader{Name: k, Value: v})
				}
			}
		}
		request := map[string]any{
			"method": n.Method, "url": n.URL, "httpVersion": "", "headers": harHeaders(n.ReqHeaders),
			"queryString": qs, "cookies": []any{}, "headersSize": -1, "bodySize": -1,
		}
		content := map[string]any{"size": n.Size, "mimeType": n.MimeType}
		if withBodies && n.HasPostData {
			var post string
			if chromedp.Run(tCtx, chromedp.ActionFunc(func(ctx context.Context) error {
				var e error
				post, e = network.GetRequestPostData(n.reqID).Do(ctx)
				return e
			})) == nil {
				request["postData"] = map[string]any{"mimeType": fmt.Sprint(n.ReqHeaders["Content-Type"]), "text": post}
			}
		}
		if withBodies && n.Done && n.Failed == "" && bodyBudget > 0 {
			var body []byte
			if chromedp.Run(tCtx, chromedp.ActionFunc(func(ctx context.Context) error {
				var e error
				body, e = network.GetResponseBody(n.reqID).Do(ctx)
				return e
			})) == nil && isTextual(n.MimeType, body) {
				content["text"] = string(body)
				bodyBudget -= len(body)
			}
		}
		entries = append(entries, map[string]any{
			"startedDateTime": n.start.Format(time.RFC3339Nano),
			"time":            n.DurationMs,
			"request":         request,
			"response": map[string]any{
				"status": n.Status, "statusText": n.StatusText, "httpVersion": "", "headers": harHeaders(n.RespHeaders),
				"cookies": []any{}, "content": content, "redirectURL": "", "headersSize": -1, "bodySize": n.Size,
				"_error": n.Failed,
			},
			"cache":           map[string]any{},
			"timings":         map[string]any{"send": 0, "wait": n.DurationMs, "receive": 0},
			"serverIPAddress": n.RemoteAddr,
			"_resourceType":   n.Type,
		})
	}
	har := map[string]any{"log": map[string]any{
		"version": "1.2",
		"creator": map[string]any{"name": "seebro", "version": version},
		"pages":   []any{},
		"entries": entries,
	}}

	if path == "" {
		jsonResp(w, 200, har)
		return
	}
	data, _ := json.Marshal(har)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		jsonErr(w, 500, err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		jsonErr(w, 500, err)
		return
	}
	jsonResp(w, 200, map[string]any{"path": path, "entries": len(entries), "size": len(data)})
}

// ── GET /performance ───────────────────────────────────────

const webVitalsJS = `(() => {
	const nav = performance.getEntriesByType("navigation")[0];
	const paint = Object.fromEntries(performance.getEntriesByType("paint").map(p => [p.name, Math.round(p.startTime)]));
	const lcpEntries = performance.getEntriesByType("largest-contentful-paint");
	let cls = 0;
	for (const s of performance.getEntriesByType("layout-shift")) if (!s.hadRecentInput) cls += s.value;
	const res = performance.getEntriesByType("resource");
	const out = {
		fcpMs: paint["first-contentful-paint"],
		lcpMs: lcpEntries.length ? Math.round(lcpEntries[lcpEntries.length - 1].startTime) : undefined,
		cls: Math.round(cls * 1000) / 1000,
		resources: res.length,
		transferKB: Math.round(res.reduce((n, r) => n + (r.transferSize || 0), (nav && nav.transferSize) || 0) / 1024),
	};
	if (nav) {
		out.ttfbMs = Math.round(nav.responseStart - nav.startTime);
		out.domContentLoadedMs = Math.round(nav.domContentLoadedEventEnd - nav.startTime);
		out.loadMs = Math.round(nav.loadEventEnd - nav.startTime) || undefined;
	}
	return out;
})()`

// handlePerformance returns Chrome's runtime metrics plus page timing and
// Web Vitals (FCP, LCP, CLS) from the Performance Timeline.
func (b *Bridge) handlePerformance(w http.ResponseWriter, r *http.Request) {
	ctx, tabID, err := b.TabContext(r.URL.Query().Get("tabId"))
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)
	defer tCancel()

	metrics := map[string]float64{}
	var vitals map[string]any
	err = chromedp.Run(tCtx,
		performance.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			list, e := performance.GetMetrics().Do(ctx)
			for _, m := range list {
				metrics[m.Name] = m.Value
			}
			return e
		}),
		chromedp.Evaluate(webVitalsJS, &vitals),
	)
	if err != nil {
		b.failTab(w, tabID, fmt.Errorf("performance: %w", err))
		return
	}
	keep := map[string]any{}
	for _, k := range []string{"JSHeapUsedSize", "JSHeapTotalSize", "Nodes", "Documents", "Frames", "JSEventListeners",
		"LayoutCount", "RecalcStyleCount", "LayoutDuration", "RecalcStyleDuration", "ScriptDuration", "TaskDuration"} {
		if v, ok := metrics[k]; ok {
			keep[k] = v
		}
	}
	jsonResp(w, 200, map[string]any{"tabId": tabID, "timing": vitals, "metrics": keep})
}
