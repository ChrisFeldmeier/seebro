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
	"strconv"
	"time"

	"github.com/chromedp/cdproto/page"
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
		tabs = append(tabs, entry)
	}
	jsonResp(w, 200, map[string]any{"tabs": tabs})
}

// ── GET /screenshot ────────────────────────────────────────

func (b *Bridge) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	tabID := r.URL.Query().Get("tabId")
	output := r.URL.Query().Get("output")
	reqNoAnim := r.URL.Query().Get("noAnimations") == "true"

	ctx, _, err := b.TabContext(tabID)
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

	if err := chromedp.Run(tCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			buf, err = page.CaptureScreenshot().
				WithFormat(page.CaptureScreenshotFormatJpeg).
				WithQuality(int64(quality)).
				Do(ctx)
			return err
		}),
	); err != nil {
		jsonErr(w, 500, fmt.Errorf("screenshot: %w", err))
		return
	}

	// Handle file output
	if output == "file" {
		screenshotDir := filepath.Join(stateDir, "screenshots")
		if err := os.MkdirAll(screenshotDir, 0755); err != nil {
			jsonErr(w, 500, fmt.Errorf("create screenshot dir: %w", err))
			return
		}

		timestamp := time.Now().Format("20060102-150405")
		filename := fmt.Sprintf("screenshot-%s.jpg", timestamp)
		filePath := filepath.Join(screenshotDir, filename)

		if err := os.WriteFile(filePath, buf, 0644); err != nil {
			jsonErr(w, 500, fmt.Errorf("write screenshot: %w", err))
			return
		}

		jsonResp(w, 200, map[string]any{
			"path":      filePath,
			"size":      len(buf),
			"format":    "jpeg",
			"timestamp": timestamp,
		})
		return
	}

	if r.URL.Query().Get("raw") == "true" {
		w.Header().Set("Content-Type", "image/jpeg")
		if _, err := w.Write(buf); err != nil {
			slog.Error("screenshot write", "err", err)
		}
		return
	}

	jsonResp(w, 200, map[string]any{
		"format": "jpeg",
		"base64": base64.StdEncoding.EncodeToString(buf),
	})
}

// ── GET /text ──────────────────────────────────────────────

func (b *Bridge) handleText(w http.ResponseWriter, r *http.Request) {
	tabID := r.URL.Query().Get("tabId")
	mode := r.URL.Query().Get("mode")

	ctx, _, err := b.TabContext(tabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}

	tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)
	defer tCancel()
	go cancelOnClientDone(r.Context(), tCancel)

	var text string
	if mode == "raw" {
		if err := chromedp.Run(tCtx,
			chromedp.Evaluate(`document.body.innerText`, &text),
		); err != nil {
			jsonErr(w, 500, fmt.Errorf("text extract: %w", err))
			return
		}
	} else {
		if err := chromedp.Run(tCtx,
			chromedp.Evaluate(readabilityJS, &text),
		); err != nil {
			jsonErr(w, 500, fmt.Errorf("text extract: %w", err))
			return
		}
	}

	var url, title string
	_ = chromedp.Run(tCtx,
		chromedp.Location(&url),
		chromedp.Title(&title),
	)

	jsonResp(w, 200, map[string]any{
		"url":   url,
		"title": title,
		"text":  text,
	})
}

// ── POST /navigate ─────────────────────────────────────────

func (b *Bridge) handleNavigate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TabID       string  `json:"tabId"`
		URL         string  `json:"url"`
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
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}
	if req.Expression == "" {
		jsonErr(w, 400, fmt.Errorf("expression required"))
		return
	}

	ctx, _, err := b.TabContext(req.TabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}

	tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)
	defer tCancel()
	go cancelOnClientDone(r.Context(), tCancel)

	var result any
	if err := chromedp.Run(tCtx, chromedp.Evaluate(req.Expression, &result)); err != nil {
		jsonErr(w, 500, fmt.Errorf("evaluate: %w", err))
		return
	}

	jsonResp(w, 200, map[string]any{"result": result})
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

	default:
		jsonErr(w, 400, fmt.Errorf("action must be 'new' or 'close'"))
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
