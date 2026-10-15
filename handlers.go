package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// ── GET /health ────────────────────────────────────────────

func (b *Bridge) handleHealth(w http.ResponseWriter, r *http.Request) {
	targets, err := b.ListTargets()
	if err != nil {
		jsonResp(w, 200, map[string]any{"status": "disconnected", "error": err.Error(), "cdp": cdpURL})
		return
	}
	jsonResp(w, 200, map[string]any{"status": "ok", "tabs": len(targets), "cdp": cdpURL})
}

// ── GET /tabs ──────────────────────────────────────────────

func (b *Bridge) handleTabs(w http.ResponseWriter, r *http.Request) {
	targets, err := b.ListTargets()
	if err != nil {
		jsonErr(w, 500, err)
		return
	}

	tabs := make([]map[string]any, 0, len(targets))
	for _, t := range targets {
		entry := map[string]any{
			"id":    string(t.TargetID),
			"url":   t.URL,
			"title": t.Title,
			"type":  t.Type,
		}
		if lock := b.locks.Get(string(t.TargetID)); lock != nil {
			entry["owner"] = lock.Owner
			entry["lockedUntil"] = lock.ExpiresAt.Format(time.RFC3339)
		}
		if m := b.Monitor(string(t.TargetID)); m != nil {
			if d := m.Dialog(); d != nil {
				entry["dialog"] = d
			}
		}
		tabs = append(tabs, entry)
	}
	jsonResp(w, 200, map[string]any{"tabs": tabs})
}

// ── GET /screenshot ────────────────────────────────────────

func (b *Bridge) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	tabID := r.URL.Query().Get("tabId")
	output := r.URL.Query().Get("output")
	reqNoAnim := r.URL.Query().Get("noAnimations") == "true"

	ctx, resolvedTabID, err := b.TabContext(tabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}

	tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)
	defer tCancel()
	go cancelOnClientDone(r.Context(), tCancel)

	if reqNoAnim && !noAnimations {
		disableAnimationsOnce(tCtx)
	}

	var buf []byte
	quality := 80
	if q := r.URL.Query().Get("quality"); q != "" {
		if qn, err := strconv.Atoi(q); err == nil {
			quality = qn
		}
	}
	format := page.CaptureScreenshotFormatJpeg
	switch r.URL.Query().Get("format") {
	case "png":
		format = page.CaptureScreenshotFormatPng
	case "webp":
		format = page.CaptureScreenshotFormatWebp
	}

	nodeID, frameID, err := b.resolveRef(tCtx, resolvedTabID, r.URL.Query().Get("ref"))
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	if frameID != "" && frameID != resolvedTabID {
		jsonErr(w, 400, fmt.Errorf("element screenshots inside iframes are not supported; screenshot the iframe element instead"))
		return
	}
	opts := shotOptions{
		format:   format,
		quality:  quality,
		fullPage: r.URL.Query().Get("fullPage") == "true",
		nodeID:   nodeID,
		selector: r.URL.Query().Get("selector"),
	}
	if v, err := strconv.ParseFloat(r.URL.Query().Get("maxHeight"), 64); err == nil {
		opts.maxHeight = v
	}
	if v, err := strconv.ParseFloat(r.URL.Query().Get("maxDim"), 64); err == nil {
		opts.maxDim = v
	}
	buf, err = b.captureScreenshot(tCtx, resolvedTabID, opts)
	if err != nil {
		jsonErr(w, 500, fmt.Errorf("screenshot: %w", err))
		return
	}
	ext := string(format)
	mime := "image/" + ext

	// Handle file output
	if output == "file" {
		screenshotDir := filepath.Join(stateDir, "screenshots")
		if err := os.MkdirAll(screenshotDir, 0755); err != nil {
			jsonErr(w, 500, fmt.Errorf("create screenshot dir: %w", err))
			return
		}

		timestamp := time.Now().Format("20060102-150405")
		filename := fmt.Sprintf("screenshot-%s.%s", timestamp, ext)
		filePath := filepath.Join(screenshotDir, filename)
		if p := r.URL.Query().Get("path"); p != "" {
			safe, err := outputPath(p, ".jpg", ".jpeg", ".png", ".webp")
			if err != nil {
				jsonErr(w, 400, err)
				return
			}
			filePath = safe
			if err := os.MkdirAll(filepath.Dir(safe), 0755); err != nil {
				jsonErr(w, 500, err)
				return
			}
		}

		if err := os.WriteFile(filePath, buf, 0644); err != nil {
			jsonErr(w, 500, fmt.Errorf("write screenshot: %w", err))
			return
		}

		jsonResp(w, 200, map[string]any{
			"path":      filePath,
			"size":      len(buf),
			"format":    ext,
			"timestamp": timestamp,
		})
		return
	}

	if r.URL.Query().Get("raw") == "true" {
		w.Header().Set("Content-Type", mime)
		if _, err := w.Write(buf); err != nil {
			slog.Error("screenshot write", "err", err)
		}
		return
	}

	jsonResp(w, 200, map[string]any{
		"format": ext,
		"base64": base64.StdEncoding.EncodeToString(buf),
	})
}

// ── GET /text ──────────────────────────────────────────────

// handleText returns the page text in one round trip. mode: readability
// (default, main content without navigation/ads), raw (all visible text) or
// markdown (headings, lists, links, tables). maxChars/offset page through
// long pages so a single call can't flood an agent's context.
func (b *Bridge) handleText(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mode := q.Get("mode")
	if mode == "" {
		mode = q.Get("format")
	}
	switch mode {
	case "", "readability", "raw", "markdown":
	default:
		jsonErr(w, 400, fmt.Errorf("mode must be readability, raw or markdown"))
		return
	}

	ctx, tabID, err := b.TabContext(q.Get("tabId"))
	if err != nil {
		jsonErr(w, 404, err)
		return
	}

	tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)
	defer tCancel()
	go cancelOnClientDone(r.Context(), tCancel)

	opts, _ := json.Marshal(map[string]string{"mode": mode})
	var res struct {
		Text  string `json:"text"`
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if err := chromedp.Run(tCtx, chromedp.Evaluate("("+textJS+")("+string(opts)+")", &res)); err != nil {
		jsonErr(w, 500, fmt.Errorf("text extract: %w", err))
		return
	}
	if mode != "raw" {
		res.Text += b.crossOriginFrameText(tCtx, tabID, res.URL, string(opts))
	}

	out := map[string]any{"url": res.URL, "title": res.Title}
	text, total := pageText(res.Text, queryInt(r, "offset", 0), queryInt(r, "maxChars", 0))
	out["text"] = text
	if total > len([]rune(text)) {
		off := max(queryInt(r, "offset", 0), 0)
		out["totalChars"] = total
		out["offset"] = off
		if next := off + len([]rune(text)); next < total {
			out["nextOffset"] = next
			out["truncated"] = true
		}
	}
	jsonResp(w, 200, out)
}

// pageText returns the [offset, offset+maxChars) rune window of text and the
// total length in runes. maxChars <= 0 means no limit.
func pageText(text string, offset, maxChars int) (string, int) {
	runes := []rune(text)
	total := len(runes)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	if maxChars > 0 && offset+maxChars < total {
		end = offset + maxChars
	}
	return string(runes[offset:end]), total
}

// ── POST /navigate ─────────────────────────────────────────

func (b *Bridge) handleNavigate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TabID       string  `json:"tabId"`
		URL         string  `json:"url"`
		Type        string  `json:"type"`        // url (default), back, forward, reload
		WaitUntil   string  `json:"waitUntil"`   // domcontentloaded (default), load, networkidle
		IgnoreCache bool    `json:"ignoreCache"` // reload only
		NewTab      bool    `json:"newTab"`
		WaitTitle   float64 `json:"waitTitle"`   // seconds to wait for title (default 2, max 30)
		Timeout     float64 `json:"timeout"`     // per-request navigate timeout in seconds (default: BRIDGE_NAV_TIMEOUT)
		BlockImages *bool   `json:"blockImages"` // per-request override; nil = use global BRIDGE_BLOCK_IMAGES
		BlockMedia  *bool   `json:"blockMedia"`  // block images + fonts + CSS + video/audio
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}
	if req.Type != "" && req.Type != "url" {
		b.handleHistoryNav(w, r, req.TabID, req.Type, req.IgnoreCache)
		return
	}
	if req.URL == "" {
		jsonErr(w, 400, fmt.Errorf("url required"))
		return
	}

	// Compute title wait duration (default 2s, max 30s).
	titleWait := time.Duration(0)
	if req.WaitTitle > 0 {
		if req.WaitTitle > 30 {
			req.WaitTitle = 30
		}
		titleWait = time.Duration(req.WaitTitle * float64(time.Second))
	}

	// Per-request navigate timeout (default: global navigateTimeout, max 120s).
	navTimeout := navigateTimeout
	if req.Timeout > 0 {
		if req.Timeout > 120 {
			req.Timeout = 120
		}
		navTimeout = time.Duration(req.Timeout * float64(time.Second))
	}

	// Resolve resource blocking: per-request overrides global.
	var blockPatterns []string
	if req.BlockMedia != nil && *req.BlockMedia {
		blockPatterns = mediaBlockPatterns
	} else if req.BlockImages != nil && *req.BlockImages {
		blockPatterns = imageBlockPatterns
	} else if req.BlockImages != nil && !*req.BlockImages {
		blockPatterns = nil // explicitly disabled
	} else if blockMedia {
		blockPatterns = mediaBlockPatterns // global BRIDGE_BLOCK_MEDIA
	} else if blockImages {
		blockPatterns = imageBlockPatterns // global BRIDGE_BLOCK_IMAGES
	}

	if req.NewTab {
		newTargetID, newCtx, _, err := b.CreateTab(req.URL)
		if err != nil {
			jsonErr(w, 500, fmt.Errorf("new tab: %w", err))
			return
		}

		tCtx, tCancel := context.WithTimeout(newCtx, navTimeout)
		defer tCancel()
		go cancelOnClientDone(r.Context(), tCancel)

		if blockPatterns != nil {
			_ = setResourceBlocking(tCtx, blockPatterns)
		}

		b.waitLoad(tCtx, newTargetID, req.WaitUntil)
		var url, title string
		_ = chromedp.Run(tCtx, chromedp.Location(&url))
		title = waitForTitle(tCtx, titleWait)

		jsonResp(w, 200, map[string]any{"tabId": newTargetID, "url": url, "title": title})
		return
	}

	ctx, resolvedTabID, err := b.TabContext(req.TabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}

	tCtx, tCancel := context.WithTimeout(ctx, navTimeout)
	defer tCancel()
	go cancelOnClientDone(r.Context(), tCancel)

	// Apply resource blocking before navigation.
	if blockPatterns != nil {
		_ = setResourceBlocking(tCtx, blockPatterns)
	} else if blockImages {
		// Clear any previous blocking if per-request disabled it.
		_ = setResourceBlocking(tCtx, nil)
	}

	if err := navigatePage(tCtx, req.URL); err != nil {
		jsonErr(w, 500, fmt.Errorf("navigate: %w", err))
		return
	}

	b.DeleteRefCache(resolvedTabID)
	b.waitLoad(tCtx, resolvedTabID, req.WaitUntil)

	var url string
	_ = chromedp.Run(tCtx, chromedp.Location(&url))
	title := waitForTitle(tCtx, titleWait)

	jsonResp(w, 200, map[string]any{"url": url, "title": title})
}

// ── POST /evaluate ─────────────────────────────────────────

func (b *Bridge) handleEvaluate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TabID      string `json:"tabId"`
		Expression string `json:"expression"`
		Ref        string `json:"ref"`      // run expression as function(el) on this element
		Selector   string `json:"selector"` // same, by CSS selector
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}
	if req.Expression == "" {
		jsonErr(w, 400, fmt.Errorf("expression required"))
		return
	}

	ctx, resolvedTabID, err := b.TabContext(req.TabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}

	tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)
	defer tCancel()
	go cancelOnClientDone(r.Context(), tCancel)

	if req.Ref != "" || req.Selector != "" {
		nodeID, _, err := b.resolveRef(tCtx, resolvedTabID, req.Ref)
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		// Arrow functions don't bind `this`, so pass the element as the first argument too.
		fn := "function() { return (" + req.Expression + ")(this); }"
		raw, err := callOnTarget(tCtx, nodeID, req.Selector, fn)
		if err != nil {
			jsonErr(w, 500, fmt.Errorf("evaluate: %w", err))
			return
		}
		jsonResp(w, 200, map[string]any{"result": raw})
		return
	}

	out, err := runWatchingDialogs(b.Monitor(resolvedTabID), func() (map[string]any, error) {
		var result any
		err := chromedp.Run(tCtx, chromedp.Evaluate(req.Expression, &result, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}))
		if errors.Is(err, chromedp.ErrJSUndefined) || errors.Is(err, chromedp.ErrJSNull) {
			err = nil
		}
		return map[string]any{"result": result}, err
	})
	if err != nil {
		jsonErr(w, 500, fmt.Errorf("evaluate: %w", err))
		return
	}

	jsonResp(w, 200, out)
}

// ── POST /tab ──────────────────────────────────────────────

func (b *Bridge) handleTab(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
		TabID  string `json:"tabId"`
		URL    string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}

	switch req.Action {
	case tabActionNew:
		newTargetID, ctx, _, err := b.CreateTab(req.URL)
		if err != nil {
			jsonErr(w, 500, err)
			return
		}

		var curURL, title string
		_ = chromedp.Run(ctx, chromedp.Location(&curURL), chromedp.Title(&title))
		jsonResp(w, 200, map[string]any{"tabId": newTargetID, "url": curURL, "title": title})

	case tabActionClose:
		if req.TabID == "" {
			jsonErr(w, 400, fmt.Errorf("tabId required"))
			return
		}

		if err := b.CloseTab(req.TabID); err != nil {
			jsonErr(w, 500, err)
			return
		}
		jsonResp(w, 200, map[string]any{"closed": true})

	case tabActionFocus:
		ctx, id, err := b.TabContext(req.TabID)
		if err != nil {
			jsonErr(w, 404, err)
			return
		}
		if err := chromedp.Run(ctx, page.BringToFront()); err != nil {
			jsonErr(w, 500, err)
			return
		}
		jsonResp(w, 200, map[string]any{"focused": id})

	default:
		jsonErr(w, 400, fmt.Errorf("action must be 'new', 'close' or 'focus'"))
	}
}

// ── POST /tab/lock ─────────────────────────────────────────

func (b *Bridge) handleTabLock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TabID      string `json:"tabId"`
		Owner      string `json:"owner"`
		TimeoutSec int    `json:"timeoutSec"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}
	if req.TabID == "" || req.Owner == "" {
		jsonErr(w, 400, fmt.Errorf("tabId and owner required"))
		return
	}

	timeout := defaultLockTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
	}

	if err := b.locks.Lock(req.TabID, req.Owner, timeout); err != nil {
		jsonErr(w, 409, err)
		return
	}

	lock := b.locks.Get(req.TabID)
	jsonResp(w, 200, map[string]any{
		"locked":    true,
		"owner":     lock.Owner,
		"expiresAt": lock.ExpiresAt.Format(time.RFC3339),
	})
}

// ── POST /tab/unlock ───────────────────────────────────────

func (b *Bridge) handleTabUnlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TabID string `json:"tabId"`
		Owner string `json:"owner"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}
	if req.TabID == "" || req.Owner == "" {
		jsonErr(w, 400, fmt.Errorf("tabId and owner required"))
		return
	}

	if err := b.locks.Unlock(req.TabID, req.Owner); err != nil {
		jsonErr(w, 409, err)
		return
	}

	jsonResp(w, 200, map[string]any{"unlocked": true})
}

// ── POST /shutdown ─────────────────────────────────────────

func (b *Bridge) handleShutdown(shutdownFn func()) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Info("shutdown requested via API")
		jsonResp(w, 200, map[string]any{"status": "shutting down"})

		// Trigger shutdown in background so the response gets sent first.
		go func() {
			time.Sleep(100 * time.Millisecond)
			shutdownFn()
		}()
	}
}

// Shared helpers (jsonResp, jsonErr, cancelOnClientDone, waitForTitle)
// are defined in middleware.go and cdp.go respectively.

// handleHistoryNav implements back / forward / reload for /navigate.
func (b *Bridge) handleHistoryNav(w http.ResponseWriter, r *http.Request, tabID, kind string, ignoreCache bool) {
	ctx, resolvedTabID, err := b.TabContext(tabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	tCtx, tCancel := context.WithTimeout(ctx, navigateTimeout)
	defer tCancel()
	go cancelOnClientDone(r.Context(), tCancel)

	err = chromedp.Run(tCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		switch kind {
		case "reload":
			return page.Reload().WithIgnoreCache(ignoreCache).Do(ctx)
		case "back", "forward":
			cur, entries, err := page.GetNavigationHistory().Do(ctx)
			if err != nil {
				return err
			}
			idx := cur - 1
			if kind == "forward" {
				idx = cur + 1
			}
			if idx < 0 || idx >= int64(len(entries)) {
				return fmt.Errorf("no %s history entry", kind)
			}
			return page.NavigateToHistoryEntry(entries[idx].ID).Do(ctx)
		}
		return fmt.Errorf("type must be url, back, forward or reload")
	}))
	if err != nil {
		jsonErr(w, 400, fmt.Errorf("navigate %s: %w", kind, err))
		return
	}
	time.Sleep(150 * time.Millisecond) // let the new document replace the old one
	waitReady(tCtx)
	b.DeleteRefCache(resolvedTabID)

	var url string
	_ = chromedp.Run(tCtx, chromedp.Location(&url))
	jsonResp(w, 200, map[string]any{"url": url, "title": waitForTitle(tCtx, 0)})
}

// waitLoad waits beyond "document interactive" when asked: "load" waits for
// readyState complete, "networkidle" additionally for ≤2 requests in flight
// for 500ms. Timeouts are not errors; the page is usually usable anyway.
func (b *Bridge) waitLoad(ctx context.Context, tabID, until string) {
	if until != "load" && until != "networkidle" {
		return
	}
	for ctx.Err() == nil {
		var state string
		if err := chromedp.Run(ctx, chromedp.Evaluate("document.readyState", &state)); err == nil && state == "complete" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if until != "networkidle" {
		return
	}
	m := b.Monitor(tabID)
	for m != nil && ctx.Err() == nil && !m.NetworkIdle(2, 500*time.Millisecond) {
		time.Sleep(100 * time.Millisecond)
	}
}

// crossOriginFrameText appends the text of cross-origin iframes, which the
// page's own JavaScript cannot read (same-origin frames are inlined by text.js).
func (b *Bridge) crossOriginFrameText(ctx context.Context, tabID, mainURL, opts string) string {
	main, err := neturl.Parse(mainURL)
	if err != nil {
		return ""
	}
	frames := b.frames(ctx, tabID)
	if len(frames) < 2 {
		return ""
	}
	fr := b.frameRefsFor(tabID)
	var sb strings.Builder
	for _, f := range frames[1:] {
		u, err := neturl.Parse(f.url)
		if err != nil || u.Scheme == "about" || u.Scheme == "data" || (u.Scheme == main.Scheme && u.Host == main.Host) {
			continue
		}
		wid, err := b.world(ctx, fr, f.id, true)
		if err != nil {
			continue
		}
		var res struct {
			Text string `json:"text"`
		}
		if err := callWorld(ctx, wid, "("+textJS+")("+opts+")", &res); err != nil || strings.TrimSpace(res.Text) == "" {
			continue
		}
		sb.WriteString("\n\n[iframe " + f.url + "]\n")
		sb.WriteString(res.Text)
	}
	return sb.String()
}
