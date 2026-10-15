package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Tab health: detect hung or crashed renderers, reset tabs, and close tabs
// nobody uses any more. Every Chrome tab costs tens of MB, and agents rarely
// close what they open.

var errTabUnresponsive = errors.New("tab unresponsive")

// tabUnresponsiveHint is returned to callers so agents know how to recover.
const tabUnresponsiveHint = "the tab's page is not responding (hung or crashed renderer). Reset it with POST /tab {\"action\":\"reset\",\"tabId\":…} (MCP: tab_reset) or close it"

// isTimeout reports whether err is a timeout of a CDP call.
func isTimeout(err error) bool {
	return err != nil && (errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "deadline exceeded"))
}

// probe checks whether the tab's renderer still answers within 1.5s.
func (b *Bridge) probe(tabID string) bool {
	b.mu.RLock()
	entry := b.tabs[tabID]
	b.mu.RUnlock()
	if entry == nil || entry.ctx == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(entry.ctx, 1500*time.Millisecond)
	defer cancel()
	var one int
	return chromedp.Run(ctx, chromedp.Evaluate("1", &one)) == nil
}

// failTab writes err; when it was a timeout and the renderer no longer
// answers, it marks the tab and says how to recover instead of a bare
// "context deadline exceeded".
func (b *Bridge) failTab(w http.ResponseWriter, tabID string, err error) {
	if isTimeout(err) && !b.probe(tabID) {
		if m := b.Monitor(tabID); m != nil {
			m.mu.Lock()
			m.unresponsive = true
			m.mu.Unlock()
		}
		jsonErr(w, http.StatusGatewayTimeout, fmt.Errorf("%w: %s", errTabUnresponsive, tabUnresponsiveHint))
		return
	}
	jsonErr(w, 500, err)
}

// tabHealth is reported in /tabs: "crashed", "unresponsive" or "".
func (m *tabMonitor) health() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.crashed:
		return "crashed"
	case m.unresponsive:
		return "unresponsive"
	}
	return ""
}

// ResetTab replaces a (hung) tab with a fresh one at the same URL.
func (b *Bridge) ResetTab(tabID string) (string, string, error) {
	url := ""
	if targets, err := b.ListTargets(); err == nil {
		for _, t := range targets {
			if string(t.TargetID) == tabID {
				url = t.URL
			}
		}
	}
	if err := b.CloseTab(tabID); err != nil {
		return "", "", err
	}
	if url == "" || strings.HasPrefix(url, "chrome-error://") {
		url = "about:blank"
	}
	newID, _, _, err := b.CreateTab(url)
	return newID, url, err
}

// markUsed records activity for idle-tab cleanup.
func (b *Bridge) markUsed(tabID string) {
	b.mu.Lock()
	if b.lastUsed == nil {
		b.lastUsed = make(map[string]time.Time)
	}
	b.lastUsed[tabID] = time.Now()
	b.mu.Unlock()
}

// CloseIdleTabs closes tabs untouched for longer than idle. The initial tab
// (it owns the browser connection) and locked tabs are kept.
func (b *Bridge) CloseIdleTabs(ctx context.Context, idle time.Duration) {
	if idle <= 0 {
		return
	}
	tick := time.NewTicker(min(idle/2, time.Minute))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		targets, err := b.ListTargets()
		if err != nil {
			continue
		}
		now := time.Now()
		var stale []*target.Info
		b.mu.Lock()
		if b.lastUsed == nil {
			b.lastUsed = make(map[string]time.Time)
		}
		for _, t := range targets {
			id := string(t.TargetID)
			seen, ok := b.lastUsed[id]
			if !ok {
				b.lastUsed[id] = now // first seen (e.g. popups): start the clock
				continue
			}
			if id != b.initialTab && now.Sub(seen) > idle && b.locks.Get(id) == nil {
				stale = append(stale, t)
			}
		}
		b.mu.Unlock()
		for _, t := range stale {
			id := string(t.TargetID)
			if err := b.CloseTab(id); err == nil {
				b.mu.Lock()
				delete(b.lastUsed, id)
				b.mu.Unlock()
				slog.Info("closed idle tab", "id", id, "url", t.URL, "idle", idle)
			}
		}
	}
}
