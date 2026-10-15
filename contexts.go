package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Isolated browser contexts and tab-lock enforcement.
//
// A named context is an incognito-like profile: its tabs share cookies and
// storage with each other but not with the persistent profile or other
// contexts. Useful for parallel agents or logging into two accounts at once.
// Contexts live in memory and disappear on close or restart.

type browserContexts struct {
	mu     sync.Mutex
	byName map[string]cdp.BrowserContextID
}

func (b *Bridge) contextID(name string, create bool) (cdp.BrowserContextID, error) {
	b.contexts.mu.Lock()
	defer b.contexts.mu.Unlock()
	if b.contexts.byName == nil {
		b.contexts.byName = map[string]cdp.BrowserContextID{}
	}
	if id, ok := b.contexts.byName[name]; ok {
		return id, nil
	}
	if !create {
		return "", fmt.Errorf("context %q not found", name)
	}
	var id cdp.BrowserContextID
	err := chromedp.Run(b.browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		exec := cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser)
		var e error
		id, e = target.CreateBrowserContext().WithDisposeOnDetach(false).Do(exec)
		if e != nil {
			return e
		}
		// Downloads of isolated contexts land in the same folder.
		_ = browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllow).
			WithBrowserContextID(id).WithDownloadPath(downloadDir).WithEventsEnabled(true).Do(exec)
		return nil
	}))
	if err != nil {
		return "", fmt.Errorf("create context: %w", err)
	}
	b.contexts.byName[name] = id
	return id, nil
}

// contextName maps a browser context ID back to its name ("" = default profile).
func (b *Bridge) contextName(id cdp.BrowserContextID) string {
	b.contexts.mu.Lock()
	defer b.contexts.mu.Unlock()
	for n, cid := range b.contexts.byName {
		if cid == id {
			return n
		}
	}
	return ""
}

// CreateTabInContext opens a tab in a named isolated context ("" = default).
func (b *Bridge) CreateTabInContext(name, url string) (string, context.Context, context.CancelFunc, error) {
	if name == "" {
		return b.CreateTab(url)
	}
	id, err := b.contextID(name, true)
	if err != nil {
		return "", nil, nil, err
	}
	// Create the target ourselves: Chrome refuses a plain createTarget in a
	// fresh context ("no browser is open") until it gets its own window.
	var tid target.ID
	err = chromedp.Run(b.browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		exec := cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser)
		var e error
		tid, e = target.CreateTarget("about:blank").WithBrowserContextID(id).Do(exec)
		if e != nil {
			tid, e = target.CreateTarget("about:blank").WithBrowserContextID(id).WithNewWindow(true).Do(exec)
		}
		return e
	}))
	if err != nil {
		return "", nil, nil, fmt.Errorf("open tab in context %q: %w", name, err)
	}
	return b.createTab(url, chromedp.WithTargetID(tid))
}

// CloseContext closes all tabs of a context and disposes it.
func (b *Bridge) CloseContext(name string) (int, error) {
	id, err := b.contextID(name, false)
	if err != nil {
		return 0, err
	}
	closed := 0
	if targets, err := b.ListTargets(); err == nil {
		for _, t := range targets {
			if t.BrowserContextID == id {
				if b.CloseTab(string(t.TargetID)) == nil {
					closed++
				}
			}
		}
	}
	err = chromedp.Run(b.browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		return target.DisposeBrowserContext(id).Do(cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser))
	}))
	b.contexts.mu.Lock()
	delete(b.contexts.byName, name)
	b.contexts.mu.Unlock()
	return closed, err
}

// ── GET /contexts, POST /context ───────────────────────────

func (b *Bridge) handleContexts(w http.ResponseWriter, r *http.Request) {
	counts := map[cdp.BrowserContextID]int{}
	if targets, err := b.ListTargets(); err == nil {
		for _, t := range targets {
			counts[t.BrowserContextID]++
		}
	}
	b.contexts.mu.Lock()
	out := make([]map[string]any, 0, len(b.contexts.byName))
	for name, id := range b.contexts.byName {
		out = append(out, map[string]any{"name": name, "tabs": counts[id]})
	}
	b.contexts.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	jsonResp(w, 200, map[string]any{"contexts": out})
}

func (b *Bridge) handleContext(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}
	if req.Name == "" {
		jsonErr(w, 400, fmt.Errorf("name required"))
		return
	}
	switch req.Action {
	case "close":
		n, err := b.CloseContext(req.Name)
		if err != nil && n == 0 && strings.Contains(err.Error(), "not found") {
			jsonErr(w, 404, err)
			return
		}
		jsonResp(w, 200, map[string]any{"closed": req.Name, "tabsClosed": n})
	default:
		jsonErr(w, 400, fmt.Errorf("action must be 'close' (contexts are created by opening a tab with \"context\")"))
	}
}

// ── Tab locks ──────────────────────────────────────────────

// ownerOf identifies the caller for tab locks: the X-Seebro-Owner header
// (sent automatically by `seebro mcp`) or ?owner=.
func ownerOf(r *http.Request) string {
	if o := r.Header.Get("X-Seebro-Owner"); o != "" {
		return o
	}
	return r.URL.Query().Get("owner")
}

// checkLock rejects changes to a tab locked by someone else (423 Locked).
func (b *Bridge) checkLock(w http.ResponseWriter, r *http.Request, tabID string) bool {
	if b.locks == nil {
		return true
	}
	if err := b.locks.CheckAccess(tabID, ownerOf(r)); err != nil {
		jsonErr(w, http.StatusLocked, err)
		return false
	}
	return true
}
