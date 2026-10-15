package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/inspector"
	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Per-tab recording of network traffic, console output and JS dialogs.
//
// Everything lives in fixed-size buffers, so a tab that stays open for days
// costs the same as one opened a minute ago. Response bodies and POST data
// are never copied here: Chrome already buffers them and we fetch them on
// demand (see /network/request).

const (
	maxURLLen         = 2048
	maxConsoleTextLen = 2000
	// Chrome-side body buffers (per tab). Bodies beyond this are evicted by
	// Chrome and can no longer be fetched.
	netResourceBuffer = 5 << 20
	netTotalBuffer    = 25 << 20
)

type netEntry struct {
	ID          string         `json:"id"`
	URL         string         `json:"url"`
	Method      string         `json:"method"`
	Type        string         `json:"type,omitempty"`
	Status      int64          `json:"status,omitempty"`
	StatusText  string         `json:"statusText,omitempty"`
	MimeType    string         `json:"mimeType,omitempty"`
	Size        int64          `json:"size,omitempty"`
	DurationMs  int64          `json:"durationMs,omitempty"`
	Failed      string         `json:"failed,omitempty"`
	FromCache   bool           `json:"fromCache,omitempty"`
	HasPostData bool           `json:"hasPostData,omitempty"`
	Done        bool           `json:"done"`
	Nav         int            `json:"nav"`
	ReqHeaders  map[string]any `json:"requestHeaders,omitempty"`
	RespHeaders map[string]any `json:"responseHeaders,omitempty"`
	RemoteAddr  string         `json:"remoteAddress,omitempty"`

	reqID network.RequestID
	start time.Time
}

type consoleEntry struct {
	ID     int64  `json:"id"`
	Level  string `json:"level"`
	Source string `json:"source"` // console, exception, browser
	Text   string `json:"text"`
	URL    string `json:"url,omitempty"`
	Line   int64  `json:"line,omitempty"`
	Time   string `json:"time"`
	Nav    int    `json:"nav"`
}

// wsMessage is a WebSocket frame or Server-Sent Event.
type wsMessage struct {
	ID     int64  `json:"id"`
	URL    string `json:"url"`
	Dir    string `json:"dir"` // sent, received, sse, error, closed
	Opcode int    `json:"opcode,omitempty"`
	Event  string `json:"event,omitempty"` // SSE event name
	Data   string `json:"data"`
	Size   int    `json:"size"`
	Time   string `json:"time"`
	Nav    int    `json:"nav"`
}

const (
	maxWSMessages = 200
	maxWSData     = 4000
)

type dialogInfo struct {
	Type          string `json:"type"`
	Message       string `json:"message"`
	DefaultPrompt string `json:"defaultPrompt,omitempty"`
	URL           string `json:"url,omitempty"`
	OpenedAt      string `json:"openedAt"`
}

type tabMonitor struct {
	mu        sync.Mutex
	mainFrame string // equals the target ID in Chrome
	nav       int    // incremented on every main-frame navigation

	net     []*netEntry
	netByID map[network.RequestID]*netEntry
	netSeq  int64
	netMax  int

	console    []*consoleEntry
	consoleSeq int64
	consoleMax int

	ws     []*wsMessage
	wsSeq  int64
	wsURLs map[network.RequestID]string

	dialog       *dialogInfo
	viewport     *viewportState // device metrics set via /emulate
	crashed      bool           // renderer crashed (Inspector.targetCrashed)
	unresponsive bool           // a call timed out and the renderer didn't answer a probe

	inflight     int
	lastActivity time.Time
}

func newTabMonitor() *tabMonitor {
	return &tabMonitor{
		netByID:      make(map[network.RequestID]*netEntry),
		netMax:       networkBufferSize,
		consoleMax:   consoleBufferSize,
		lastActivity: time.Now(),
	}
}

// attachMonitor registers the event listener for a tab context. It can be
// called before the target exists (chromedp queues the listener). Call
// m.setMainFrame once the target ID is known.
func (b *Bridge) attachMonitor(ctx context.Context, tabID string) *tabMonitor {
	m := newTabMonitor()
	m.mainFrame = tabID
	chromedp.ListenTarget(ctx, func(ev any) { m.handleEvent(ctx, ev) })
	if tabID != "" {
		b.storeMonitor(tabID, m)
	}
	return m
}

// attachMonitorLocked is attachMonitor for callers that already hold b.mu
// and whose target already exists.
func (b *Bridge) attachMonitorLocked(ctx context.Context, tabID string) {
	m := newTabMonitor()
	m.mainFrame = tabID
	chromedp.ListenTarget(ctx, func(ev any) { m.handleEvent(ctx, ev) })
	if b.monitors == nil {
		b.monitors = make(map[string]*tabMonitor)
	}
	b.monitors[tabID] = m
	go tuneNetworkBuffers(ctx)
}

func (b *Bridge) storeMonitor(tabID string, m *tabMonitor) {
	m.mu.Lock()
	m.mainFrame = tabID
	m.mu.Unlock()
	b.mu.Lock()
	if b.monitors == nil {
		b.monitors = make(map[string]*tabMonitor)
	}
	b.monitors[tabID] = m
	b.mu.Unlock()
}

// Monitor returns the recorder for a tab, or nil if none is attached.
func (b *Bridge) Monitor(tabID string) *tabMonitor {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.monitors[tabID]
}

// PendingDialogs returns tabs that currently have a JS dialog open.
func (b *Bridge) PendingDialogs() map[string]dialogInfo {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := map[string]dialogInfo{}
	for id, m := range b.monitors {
		if d := m.Dialog(); d != nil {
			out[id] = *d
		}
	}
	return out
}

// tuneNetworkBuffers caps how much response data Chrome keeps per tab.
// chromedp already enables the Network domain; re-enabling with limits is idempotent.
func tuneNetworkBuffers(ctx context.Context) {
	if err := chromedp.Run(ctx, network.Enable().
		WithMaxResourceBufferSize(netResourceBuffer).
		WithMaxTotalBufferSize(netTotalBuffer)); err != nil {
		slog.Debug("network buffer tuning failed", "err", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (m *tabMonitor) handleEvent(ctx context.Context, ev any) {
	switch e := ev.(type) {
	case *network.EventRequestWillBeSent:
		m.onRequest(e)
	case *network.EventResponseReceived:
		m.mu.Lock()
		if n := m.netByID[e.RequestID]; n != nil && e.Response != nil {
			n.Status = e.Response.Status
			n.StatusText = e.Response.StatusText
			n.MimeType = e.Response.MimeType
			n.RespHeaders = e.Response.Headers
			n.FromCache = e.Response.FromDiskCache || e.Response.FromServiceWorker || e.Response.FromPrefetchCache
			if e.Response.RemoteIPAddress != "" {
				n.RemoteAddr = e.Response.RemoteIPAddress
			}
		}
		m.mu.Unlock()
	case *network.EventLoadingFinished:
		m.finish(e.RequestID, e.EncodedDataLength, "")
	case *network.EventLoadingFailed:
		reason := e.ErrorText
		if e.Canceled {
			reason = "canceled"
		}
		if e.BlockedReason != "" {
			reason = "blocked:" + e.BlockedReason.String()
		}
		m.finish(e.RequestID, 0, reason)
	case *network.EventRequestServedFromCache:
		m.mu.Lock()
		if n := m.netByID[e.RequestID]; n != nil {
			n.FromCache = true
		}
		m.mu.Unlock()

	case *network.EventWebSocketCreated:
		m.mu.Lock()
		if m.wsURLs == nil || len(m.wsURLs) > 100 {
			m.wsURLs = map[network.RequestID]string{}
		}
		m.wsURLs[e.RequestID] = truncate(e.URL, maxURLLen)
		m.mu.Unlock()
	case *network.EventWebSocketFrameSent:
		if e.Response != nil {
			m.addWS(e.RequestID, "sent", int(e.Response.Opcode), "", e.Response.PayloadData)
		}
	case *network.EventWebSocketFrameReceived:
		if e.Response != nil {
			m.addWS(e.RequestID, "received", int(e.Response.Opcode), "", e.Response.PayloadData)
		}
	case *network.EventWebSocketFrameError:
		m.addWS(e.RequestID, "error", 0, "", e.ErrorMessage)
	case *network.EventWebSocketClosed:
		m.addWS(e.RequestID, "closed", 0, "", "")
	case *network.EventEventSourceMessageReceived:
		m.addWS(e.RequestID, "sse", 0, e.EventName, e.Data)

	case *runtime.EventConsoleAPICalled:
		parts := make([]string, 0, len(e.Args))
		for _, a := range e.Args {
			parts = append(parts, remoteObjectString(a))
		}
		url, line := "", int64(0)
		if e.StackTrace != nil && len(e.StackTrace.CallFrames) > 0 {
			url, line = e.StackTrace.CallFrames[0].URL, e.StackTrace.CallFrames[0].LineNumber+1
		}
		level := string(e.Type)
		if level == "warning" {
			level = "warn"
		}
		m.addConsole(level, "console", strings.Join(parts, " "), url, line)
	case *runtime.EventExceptionThrown:
		d := e.ExceptionDetails
		if d == nil {
			return
		}
		text := d.Text
		if d.Exception != nil && d.Exception.Description != "" {
			text = d.Exception.Description
		}
		m.addConsole("error", "exception", text, d.URL, d.LineNumber+1)
	case *cdplog.EventEntryAdded:
		if e.Entry != nil {
			m.addConsole(string(e.Entry.Level), "browser", e.Entry.Text, e.Entry.URL, e.Entry.LineNumber)
		}

	case *page.EventJavascriptDialogOpening:
		auto := dialogPolicy
		if e.Type == page.DialogTypeBeforeunload {
			auto = "accept" // never let a beforeunload prompt block navigation
		}
		if auto == "accept" || auto == "dismiss" {
			// Event callbacks must not block, so answer from a goroutine.
			go func() {
				c := chromedp.FromContext(ctx)
				if c == nil || c.Target == nil {
					return
				}
				_ = page.HandleJavaScriptDialog(auto == "accept").Do(cdp.WithExecutor(ctx, c.Target))
			}()
			m.addConsole("info", "dialog", "auto-"+auto+"ed "+string(e.Type)+": "+e.Message, e.URL, 0)
			return
		}
		m.mu.Lock()
		m.dialog = &dialogInfo{
			Type: string(e.Type), Message: truncate(e.Message, maxConsoleTextLen),
			DefaultPrompt: e.DefaultPrompt, URL: e.URL, OpenedAt: time.Now().Format(time.RFC3339),
		}
		m.mu.Unlock()
	case *inspector.EventTargetCrashed:
		m.mu.Lock()
		m.crashed = true
		m.mu.Unlock()
		m.addConsole("error", "browser", "renderer crashed — reset the tab", "", 0)
	case *page.EventJavascriptDialogClosed:
		m.mu.Lock()
		m.dialog = nil
		m.mu.Unlock()
	}
}

func (m *tabMonitor) onRequest(e *network.EventRequestWillBeSent) {
	if e.Request == nil || strings.HasPrefix(e.Request.URL, "data:") {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// A redirect reuses the request ID: close out the previous hop.
	if prev := m.netByID[e.RequestID]; prev != nil && e.RedirectResponse != nil {
		prev.Status = e.RedirectResponse.Status
		prev.StatusText = e.RedirectResponse.StatusText
		prev.RespHeaders = e.RedirectResponse.Headers
		prev.Done = true
		prev.ID += ":redirect"
		m.inflight--
		delete(m.netByID, e.RequestID)
	}

	if e.Type == network.ResourceTypeDocument && string(e.FrameID) == m.mainFrame && e.RedirectResponse == nil {
		m.nav++
	}

	m.netSeq++
	n := &netEntry{
		ID:          "r" + itoa(m.netSeq),
		reqID:       e.RequestID,
		URL:         truncate(e.Request.URL, maxURLLen),
		Method:      e.Request.Method,
		Type:        string(e.Type),
		HasPostData: e.Request.HasPostData,
		ReqHeaders:  e.Request.Headers,
		Nav:         m.nav,
		start:       time.Now(),
	}
	m.net = append(m.net, n)
	m.netByID[e.RequestID] = n
	m.inflight++
	m.lastActivity = time.Now()

	if len(m.net) > m.netMax {
		old := m.net[0]
		copy(m.net, m.net[1:])
		m.net[len(m.net)-1] = nil
		m.net = m.net[:len(m.net)-1]
		if m.netByID[old.reqID] == old {
			delete(m.netByID, old.reqID)
		}
		if !old.Done {
			m.inflight--
		}
	}
}

func (m *tabMonitor) finish(id network.RequestID, size float64, failed string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.netByID[id]
	if n == nil || n.Done {
		return
	}
	n.Done = true
	n.Size = int64(size)
	n.Failed = failed
	n.DurationMs = time.Since(n.start).Milliseconds()
	m.inflight--
	m.lastActivity = time.Now()
}

func (m *tabMonitor) addConsole(level, source, text, url string, line int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.consoleSeq++
	m.console = append(m.console, &consoleEntry{
		ID: m.consoleSeq, Level: level, Source: source,
		Text: truncate(text, maxConsoleTextLen), URL: truncate(url, 300), Line: line,
		Time: time.Now().Format("15:04:05.000"), Nav: m.nav,
	})
	if len(m.console) > m.consoleMax {
		copy(m.console, m.console[1:])
		m.console[len(m.console)-1] = nil
		m.console = m.console[:len(m.console)-1]
	}
}

func (m *tabMonitor) addWS(id network.RequestID, dir string, opcode int, event, data string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	url := m.wsURLs[id]
	if url == "" {
		if n := m.netByID[id]; n != nil {
			url = n.URL // EventSource connections are normal requests
		}
	}
	m.wsSeq++
	m.ws = append(m.ws, &wsMessage{
		ID: m.wsSeq, URL: url, Dir: dir, Opcode: opcode, Event: event,
		Data: truncate(data, maxWSData), Size: len(data),
		Time: time.Now().Format("15:04:05.000"), Nav: m.nav,
	})
	if len(m.ws) > maxWSMessages {
		copy(m.ws, m.ws[1:])
		m.ws[len(m.ws)-1] = nil
		m.ws = m.ws[:len(m.ws)-1]
	}
}

// WebSocketMessages lists WebSocket frames and SSE events, newest last.
func (m *tabMonitor) WebSocketMessages(all bool, urlSub, dir string, limit int) ([]wsMessage, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]wsMessage, 0)
	for _, w := range m.ws {
		if !all && w.Nav != m.nav {
			continue
		}
		if urlSub != "" && !strings.Contains(strings.ToLower(w.URL), strings.ToLower(urlSub)) {
			continue
		}
		if dir != "" && dir != w.Dir {
			continue
		}
		out = append(out, *w)
	}
	total := len(out)
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, total
}

// netFilter selects entries for listing.
type netFilter struct {
	all     bool     // include previous navigations
	types   []string // resource types (case-insensitive)
	urlSub  string
	method  string
	status  string // "2xx", "4xx", "failed", or exact code
	limit   int
	offset  int
	headers bool
}

func (m *tabMonitor) Requests(f netFilter) ([]netEntry, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]netEntry, 0)
	matched := 0
	for _, n := range m.net {
		if !f.all && n.Nav != m.nav {
			continue
		}
		if len(f.types) > 0 && !containsFold(f.types, n.Type) {
			continue
		}
		if f.urlSub != "" && !strings.Contains(strings.ToLower(n.URL), strings.ToLower(f.urlSub)) {
			continue
		}
		if f.method != "" && !strings.EqualFold(f.method, n.Method) {
			continue
		}
		if f.status != "" && !statusMatches(f.status, n) {
			continue
		}
		matched++
		if matched <= f.offset || (f.limit > 0 && len(out) >= f.limit) {
			continue
		}
		c := *n
		if !f.headers {
			c.ReqHeaders, c.RespHeaders = nil, nil
		}
		out = append(out, c)
	}
	return out, matched
}

func (m *tabMonitor) Request(id string) (netEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.net {
		if n.ID == id || string(n.reqID) == id {
			return *n, true
		}
	}
	return netEntry{}, false
}

func (m *tabMonitor) Console(all bool, levels []string, limit int) ([]consoleEntry, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]consoleEntry, 0)
	matched := 0
	for _, c := range m.console {
		if !all && c.Nav != m.nav {
			continue
		}
		if len(levels) > 0 && !containsFold(levels, c.Level) {
			continue
		}
		matched++
		out = append(out, *c)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:] // newest wins
	}
	return out, matched
}

func (m *tabMonitor) Clear(clearNet, clearConsole bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if clearNet {
		m.net = nil
		m.netByID = make(map[network.RequestID]*netEntry)
		m.inflight = 0
		m.ws = nil
	}
	if clearConsole {
		m.console = nil
	}
}

func (m *tabMonitor) Dialog() *dialogInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dialog
}

func (m *tabMonitor) clearDialog() {
	m.mu.Lock()
	m.dialog = nil
	m.mu.Unlock()
}

// NetworkIdle reports whether at most maxInflight requests are pending and
// nothing started or finished for quiet.
func (m *tabMonitor) NetworkIdle(maxInflight int, quiet time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inflight <= maxInflight && time.Since(m.lastActivity) >= quiet
}

func statusMatches(want string, n *netEntry) bool {
	switch {
	case want == "failed":
		return n.Failed != "" || n.Status >= 400
	case len(want) == 3 && strings.HasSuffix(want, "xx"):
		return n.Status/100 == int64(want[0]-'0')
	default:
		return itoa(n.Status) == want
	}
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// remoteObjectString renders a console argument the way DevTools shows it.
func remoteObjectString(o *runtime.RemoteObject) string {
	if o == nil {
		return ""
	}
	if o.Type == runtime.TypeString && len(o.Value) > 0 {
		var s string
		if json.Unmarshal(o.Value, &s) == nil {
			return s
		}
	}
	if len(o.Value) > 0 {
		return string(o.Value)
	}
	if o.UnserializableValue != "" {
		return string(o.UnserializableValue)
	}
	if o.Description != "" {
		return o.Description
	}
	return string(o.Type)
}

// runWatchingDialogs runs fn but returns early if a JS dialog opens while it
// runs: alert()/confirm() block the page, so the CDP call would otherwise hang
// until its timeout. The caller cancels ctx, which releases fn.
func runWatchingDialogs(m *tabMonitor, fn func() (map[string]any, error)) (map[string]any, error) {
	if m == nil {
		return fn()
	}
	type outcome struct {
		res map[string]any
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := fn()
		done <- outcome{res, err}
	}()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case o := <-done:
			return o.res, o.err
		case <-tick.C:
			if d := m.Dialog(); d != nil {
				return map[string]any{"dialog": d, "note": "a JavaScript dialog is open and blocks the page - handle it via POST /dialog"}, nil
			}
		}
	}
}
