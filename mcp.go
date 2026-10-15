package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// MCP (Model Context Protocol) server.
//
// Served at POST /mcp (Streamable HTTP, JSON responses) on the normal Seebro
// port, and over stdio by `seebro mcp`, which proxies to that endpoint.
// Every tool is a thin mapping onto the HTTP API, dispatched in-process, so
// the MCP surface and the REST surface can never drift apart.

const mcpMaxText = 200_000 // bytes of text per tool result

var mcpProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const mcpInstructions = `Seebro is a real Chrome browser (with a persistent, possibly logged-in profile).
Workflow: tab_new → pass its tabId to every call → get_text to read (mode=markdown keeps links/tables) → snapshot for element refs (e12, f1e3 inside iframes) → click/fill/type_text/press_key with ref → wait_for after actions that load content.
Refs stay valid while the element exists; after navigation take a new snapshot. snapshot viewport=true lists only what is on screen (far fewer tokens). Prefer get_text/snapshot over screenshots.
To pull data out of web apps: network_requests (type XHR,Fetch) then network_request for the JSON body; or evaluate with fetch() inside the page (uses its cookies); websocket_messages for live data.
If a call says the tab is unresponsive, use tab_reset. Close tabs you opened. Never submit purchases, payments or messages without the user's explicit OK.`

type mcpTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`

	run func(ctx context.Context, s *mcpServer, args map[string]any) mcpResult
}

type mcpContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type mcpResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type mcpServer struct {
	b      *Bridge
	api    http.Handler
	tools  []mcpTool
	byName map[string]*mcpTool
}

func newMCPServer(b *Bridge, api http.Handler) *mcpServer {
	s := &mcpServer{b: b, api: api, tools: mcpTools()}
	s.byName = make(map[string]*mcpTool, len(s.tools))
	for i := range s.tools {
		s.byName[s.tools[i].Name] = &s.tools[i]
	}
	return s
}

// ── JSON-RPC ───────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// handleHTTP serves POST /mcp. Notifications get 202 with no body.
type ownerKey struct{}

func (s *mcpServer) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if owner := r.Header.Get("X-Seebro-Owner"); owner != "" {
		r = r.WithContext(context.WithValue(r.Context(), ownerKey{}, owner))
	}
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*maxBodySize)).Decode(&raw); err != nil {
		jsonResp(w, 400, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
		return
	}
	raw = bytes.TrimSpace(raw)

	if len(raw) > 0 && raw[0] == '[' { // legacy batch
		var reqs []rpcRequest
		if err := json.Unmarshal(raw, &reqs); err != nil {
			jsonResp(w, 400, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			return
		}
		var out []rpcResponse
		for _, req := range reqs {
			if resp := s.dispatch(r.Context(), req); resp != nil {
				out = append(out, *resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		jsonResp(w, 200, out)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		jsonResp(w, 400, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32600, "invalid request"}})
		return
	}
	resp := s.dispatch(r.Context(), req)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	jsonResp(w, 200, resp)
}

func (s *mcpServer) dispatch(ctx context.Context, req rpcRequest) *rpcResponse {
	if len(req.ID) == 0 { // notification (initialized, cancelled, …)
		return nil
	}
	resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		proto := mcpProtocolVersions[0]
		for _, v := range mcpProtocolVersions {
			if v == p.ProtocolVersion {
				proto = v
			}
		}
		resp.Result = map[string]any{
			"protocolVersion": proto,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "seebro", "title": "Seebro browser", "version": version},
			"instructions":    mcpInstructions,
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": s.tools}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{-32602, "invalid params"}
			break
		}
		tool := s.byName[p.Name]
		if tool == nil {
			resp.Error = &rpcError{-32602, "unknown tool: " + p.Name}
			break
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		res := tool.run(ctx, s, p.Arguments)
		if notice := s.dialogNotice(); notice != "" {
			res.Content = append(res.Content, mcpContent{Type: "text", Text: notice})
		}
		resp.Result = res
	case "resources/list":
		resp.Result = map[string]any{"resources": []any{}}
	case "prompts/list":
		resp.Result = map[string]any{"prompts": []any{}}
	default:
		resp.Error = &rpcError{-32601, "method not found: " + req.Method}
	}
	return resp
}

// dialogNotice tells the agent about blocking JS dialogs, which otherwise
// look like hung tool calls.
func (s *mcpServer) dialogNotice() string {
	if s.b == nil {
		return ""
	}
	pending := s.b.PendingDialogs()
	if len(pending) == 0 {
		return ""
	}
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var sb strings.Builder
	for _, id := range ids {
		d := pending[id]
		fmt.Fprintf(&sb, "⚠ JavaScript %s dialog open in tab %s: %q — the page is blocked until you call handle_dialog.\n", d.Type, id, d.Message)
	}
	return strings.TrimSpace(sb.String())
}

// ── In-process API calls ───────────────────────────────────

func (s *mcpServer) call(ctx context.Context, method, path string, query url.Values, body any) mcpResult {
	var rdr *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rdr = bytes.NewReader(data)
	} else {
		rdr = bytes.NewReader(nil)
	}
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req := httptest.NewRequest(method, target, rdr).WithContext(ctx)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if owner, ok := ctx.Value(ownerKey{}).(string); ok {
		req.Header.Set("X-Seebro-Owner", owner) // tab locks know which agent this is
	}
	rec := httptest.NewRecorder()
	s.api.ServeHTTP(rec, req)
	return toResult(rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
}

func toResult(status int, ctype string, data []byte) mcpResult {
	res := mcpResult{IsError: status >= 400}
	if strings.HasPrefix(ctype, "image/") {
		res.Content = []mcpContent{{Type: "image", Data: base64.StdEncoding.EncodeToString(data), MimeType: strings.Split(ctype, ";")[0]}}
		return res
	}
	if strings.HasPrefix(ctype, "application/json") {
		var buf bytes.Buffer
		if json.Compact(&buf, data) == nil {
			data = buf.Bytes()
		}
	}
	text := strings.TrimSpace(string(data))
	if len(text) > mcpMaxText {
		text = text[:mcpMaxText] + fmt.Sprintf("\n…(truncated, %d bytes total — narrow the request, e.g. maxTokens, selector, filter or limit)", len(data))
	}
	if text == "" {
		text = "ok"
	}
	res.Content = []mcpContent{{Type: "text", Text: text}}
	return res
}

func errResult(format string, a ...any) mcpResult {
	return mcpResult{IsError: true, Content: []mcpContent{{Type: "text", Text: fmt.Sprintf(format, a...)}}}
}

// toQuery flattens tool arguments into URL query parameters.
func toQuery(args map[string]any) url.Values {
	q := url.Values{}
	for k, v := range args {
		switch t := v.(type) {
		case nil:
		case string:
			if t != "" {
				q.Set(k, t)
			}
		case bool:
			q.Set(k, strconv.FormatBool(t))
		case float64:
			q.Set(k, strconv.FormatFloat(t, 'f', -1, 64))
		case []any:
			parts := make([]string, 0, len(t))
			for _, p := range t {
				parts = append(parts, fmt.Sprint(p))
			}
			q.Set(k, strings.Join(parts, ","))
		default:
			b, _ := json.Marshal(t)
			q.Set(k, string(b))
		}
	}
	return q
}

func getTool(path string, defaults map[string]any) func(context.Context, *mcpServer, map[string]any) mcpResult {
	return func(ctx context.Context, s *mcpServer, args map[string]any) mcpResult {
		for k, v := range defaults {
			if _, ok := args[k]; !ok {
				args[k] = v
			}
		}
		return s.call(ctx, "GET", path, toQuery(args), nil)
	}
}

func postTool(path string, fixed map[string]any) func(context.Context, *mcpServer, map[string]any) mcpResult {
	return func(ctx context.Context, s *mcpServer, args map[string]any) mcpResult {
		for k, v := range fixed {
			args[k] = v
		}
		return s.call(ctx, "POST", path, nil, args)
	}
}

// actionTool posts a single /action of the given kind.
func actionTool(kind string) func(context.Context, *mcpServer, map[string]any) mcpResult {
	return postTool("/action", map[string]any{"kind": kind})
}

func strArg(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return s
}

func boolArg(args map[string]any, k string) bool {
	b, _ := args[k].(bool)
	return b
}

// ── Schema helpers ─────────────────────────────────────────

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func pStr(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func pNum(desc string) map[string]any  { return map[string]any{"type": "number", "description": desc} }
func pBool(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
func pEnum(desc string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": vals}
}
func pArr(items map[string]any, desc string) map[string]any {
	return map[string]any{"type": "array", "items": items, "description": desc}
}

// withTab adds the common tabId parameter.
func withTab(props map[string]any) map[string]any {
	props["tabId"] = pStr("Tab ID from tab_new (default: last used tab)")
	return props
}

// withTarget adds ref/selector element addressing.
func withTarget(props map[string]any) map[string]any {
	props["ref"] = pStr("Element ref from snapshot, e.g. e12")
	props["selector"] = pStr("CSS selector instead of ref")
	return withTab(props)
}

var (
	readOnly    = map[string]any{"readOnlyHint": true}
	destructive = map[string]any{"readOnlyHint": false, "destructiveHint": true}
	sideEffects = map[string]any{"readOnlyHint": false, "destructiveHint": false}
)

// ── Tool catalogue ─────────────────────────────────────────

func mcpTools() []mcpTool {
	return []mcpTool{
		// Tabs
		{Name: "tabs_list", Description: "List open browser tabs with id, url, title, lock owner and any open JS dialog.",
			InputSchema: obj(map[string]any{}), Annotations: readOnly, run: getTool("/tabs", nil)},
		{Name: "tab_new", Description: "Open a new tab (optionally at a URL). Returns its tabId — use it for all following calls. context=\"name\" opens it in an isolated, incognito-like session (own cookies/storage, shared by tabs with the same name).",
			InputSchema: obj(map[string]any{"url": pStr("URL to open (default about:blank)"), "context": pStr("Isolated context name (optional)"),
				"engine": pEnum("chrome: full browser (default). obscura: lightweight Chrome-free engine, fast and lean for simple pages; no iframes, scrolling, downloads or dialogs", "chrome", "obscura")}), Annotations: sideEffects,
			run: postTool("/tab", map[string]any{"action": "new"})},
		{Name: "tab_reset", Description: "Replace a hung or crashed tab with a fresh one at the same URL. Returns the new tabId.",
			InputSchema: obj(withTab(map[string]any{}), "tabId"), Annotations: destructive,
			run: postTool("/tab", map[string]any{"action": "reset"})},
		{Name: "contexts", Description: "List isolated contexts, or close one with close=\"name\" (closes its tabs, discards its cookies).",
			InputSchema: obj(map[string]any{"close": pStr("Context name to close")}), Annotations: sideEffects, run: contextsTool},
		{Name: "tab_close", Description: "Close a tab. Close every tab you opened when done.",
			InputSchema: obj(withTab(map[string]any{}), "tabId"), Annotations: destructive,
			run: postTool("/tab", map[string]any{"action": "close"})},
		{Name: "tab_focus", Description: "Bring a tab to the front (matters for headed mode and some focus-dependent pages).",
			InputSchema: obj(withTab(map[string]any{}), "tabId"), Annotations: sideEffects,
			run: postTool("/tab", map[string]any{"action": "focus"})},
		{Name: "tab_lock", Description: "Reserve a tab so other agents can't change it (they get an error until it is unlocked or expires).",
			InputSchema: obj(withTab(map[string]any{"owner": pStr("Owner name (default: this MCP session)"), "timeoutSec": pNum("Lock lifetime in seconds (max 300)")}), "tabId"),
			Annotations: sideEffects, run: postTool("/tab/lock", nil)},
		{Name: "tab_unlock", Description: "Release a tab lock.",
			InputSchema: obj(withTab(map[string]any{"owner": pStr("Owner name (default: this MCP session)")}), "tabId"),
			Annotations: sideEffects, run: postTool("/tab/unlock", nil)},

		// Navigation & reading
		{Name: "navigate", Description: "Load a URL, or go back/forward/reload. Returns url and title.",
			InputSchema: obj(withTab(map[string]any{
				"url":         pStr("URL to load (type=url)"),
				"type":        pEnum("Navigation type (default url)", "url", "back", "forward", "reload"),
				"waitUntil":   pEnum("domcontentloaded (default), load, or networkidle", "domcontentloaded", "load", "networkidle"),
				"ignoreCache": pBool("Reload bypassing the cache"),
				"newTab":      pBool("Open in a new tab (returns tabId)"),
				"context":     pStr("With newTab: isolated context name"),
				"engine":      pEnum("With newTab: chrome (default) or obscura", "chrome", "obscura"),
				"timeout":     pNum("Seconds (default 30, max 120)"),
				"waitTitle":   pNum("Seconds to wait for a title set by JS"),
				"blockImages": pBool("Skip images"),
				"blockMedia":  pBool("Skip images, fonts, CSS, media"),
			})), Annotations: sideEffects, run: postTool("/navigate", nil)},
		{Name: "get_text", Description: "Page text, including iframes. readability (default): main content without navigation/ads. markdown: headings, lists, links with URLs, tables. raw: all visible text. Long pages are paged: pass offset=nextOffset to continue.",
			InputSchema: obj(withTab(map[string]any{
				"mode":     pEnum("readability (default), markdown or raw", "readability", "markdown", "raw"),
				"maxChars": pNum("Characters per call (default 30000)"),
				"offset":   pNum("Start at this character (from nextOffset)"),
			})),
			Annotations: readOnly, run: getTool("/text", map[string]any{"maxChars": 30000.0})},
		{Name: "snapshot", Description: "Interactive elements with refs for click/fill/etc. (e12; f1e3 = inside iframe f1). Refs stay valid while the element exists. viewport=true: only what is on screen. diff=true: only changes since the last snapshot. filter=all: full accessibility tree (main frame).",
			InputSchema: obj(withTab(map[string]any{
				"viewport":  pBool("Only elements currently visible on screen"),
				"links":     pBool("Include link URLs"),
				"filter":    pEnum("interactive (default) or all", "interactive", "all"),
				"format":    pEnum("Output format (default compact)", "compact", "text", "json", "yaml"),
				"diff":      pBool("Only changes since the last snapshot"),
				"depth":     pNum("Max tree depth"),
				"selector":  pStr("Only inside this CSS selector"),
				"maxTokens": pNum("Truncate to ~N tokens"),
				"maxNodes":  pNum("Stop after this many elements (default 5000)"),
			})), Annotations: readOnly, run: snapshotTool},
		{Name: "screenshot", Description: "Screenshot of the viewport, the full page or one element. Returned as an image (scaled to ≤1568px) unless path is given (full resolution). Use sparingly: get_text/snapshot are far cheaper.",
			InputSchema: obj(withTarget(map[string]any{
				"format":   pEnum("jpeg (default), png or webp", "jpeg", "png", "webp"),
				"quality":  pNum("JPEG/WebP quality 0-100 (default 80)"),
				"fullPage": pBool("Capture the whole scrollable page (tall pages get scaled down; use path for full size)"),
				"maxDim":   pNum("Longest image side in pixels (default 1568 when returned inline)"),
				"path":     pStr("Save to this file path instead of returning the image"),
			})), Annotations: readOnly, run: screenshotTool},
		{Name: "inspect_element", Description: "Details of one element: tag, attributes, text, value, bounding box, visibility; optionally outerHTML and computed styles.",
			InputSchema: obj(withTarget(map[string]any{
				"html":   pBool("Include outerHTML (max 20KB)"),
				"styles": pEnum("Computed styles: true = common subset, all = every property", "true", "all"),
			})), Annotations: readOnly, run: getTool("/element", nil)},
		{Name: "wait_for", Description: "Wait until any of the texts appears, a selector exists (or is gone), the URL matches and/or the network is idle. Reacts instantly to DOM changes.",
			InputSchema: obj(withTab(map[string]any{
				"text":        pArr(map[string]any{"type": "string"}, "Resolve when any of these strings is on the page"),
				"selector":    pStr("CSS selector that must exist"),
				"gone":        pBool("Invert: wait until text/selector is absent"),
				"url":         pStr("location.href must contain this"),
				"networkIdle": pBool("Wait for ≤2 requests in flight for 500ms"),
				"timeout":     pNum("Milliseconds (default 10000, max 60000)"),
			})), Annotations: readOnly, run: postTool("/wait", nil)},

		// Input
		{Name: "click", Description: "Click an element by ref or selector. double=true for double click, human=true for a natural mouse path (anti-bot).",
			InputSchema: obj(withTarget(map[string]any{
				"double":  pBool("Double click"),
				"human":   pBool("Move the mouse along a human-like curve before clicking"),
				"waitNav": pBool("Wait ~1s afterwards for a navigation to start"),
			})), Annotations: sideEffects, run: clickTool},
		{Name: "click_at", Description: "Click at viewport coordinates (CSS pixels), e.g. for canvas apps or elements without a ref.",
			InputSchema: obj(withTab(map[string]any{"x": pNum("X"), "y": pNum("Y"), "count": pNum("Click count (2 = double click)")}), "x", "y"),
			Annotations: sideEffects, run: actionTool(actionClickAt)},
		{Name: "type_text", Description: "Focus an element and type text with real key events. Optional submitKey (e.g. Enter) is pressed afterwards.",
			InputSchema: obj(withTarget(map[string]any{
				"text":      pStr("Text to type"),
				"submitKey": pStr("Key to press after typing, e.g. Enter or Tab"),
				"human":     pBool("Human-like typing rhythm"),
			}), "text"), Annotations: sideEffects, run: typeTool},
		{Name: "fill", Description: "Set the value of an input, textarea, select, checkbox/radio (value true/false) or contenteditable, firing input/change events so frameworks notice.",
			InputSchema: obj(withTarget(map[string]any{"value": pStr("Value to set")}), "value"), Annotations: sideEffects, run: fillTool},
		{Name: "fill_form", Description: "Fill several form fields in one call (preferred over many fill calls).",
			InputSchema: obj(withTab(map[string]any{
				"fields": pArr(obj(map[string]any{"ref": pStr("Element ref"), "selector": pStr("CSS selector"), "value": pStr("Value")}, "value"), "Fields to fill"),
			}), "fields"), Annotations: sideEffects, run: fillFormTool},
		{Name: "press_key", Description: "Press a key or combination: Enter, Tab, Escape, ArrowDown, Backspace, Control+A, Meta+Shift+K … Optionally focus ref/selector first.",
			InputSchema: obj(withTarget(map[string]any{"key": pStr("Key name or combo")}), "key"), Annotations: sideEffects, run: actionTool(actionPress)},
		{Name: "hover", Description: "Hover over an element (mouseover/mouseenter).",
			InputSchema: obj(withTarget(map[string]any{})), Annotations: sideEffects, run: actionTool(actionHover)},
		{Name: "select_option", Description: "Choose an option of a <select> by value or visible text.",
			InputSchema: obj(withTarget(map[string]any{"value": pStr("Option value or label")}), "value"), Annotations: sideEffects, run: actionTool(actionSelect)},
		{Name: "scroll", Description: "Scroll an element into view (ref/selector) or the page by x/y pixels (default 800 down).",
			InputSchema: obj(withTarget(map[string]any{"x": pNum("Horizontal pixels"), "y": pNum("Vertical pixels")})), Annotations: sideEffects, run: scrollTool},
		{Name: "drag", Description: "Drag an element onto another (toRef/toSelector) or to x/y. Works for HTML5 and mouse-based drag & drop.",
			InputSchema: obj(withTarget(map[string]any{
				"toRef": pStr("Drop target ref"), "toSelector": pStr("Drop target CSS selector"),
				"x": pNum("Drop X (if no target element)"), "y": pNum("Drop Y"),
			})), Annotations: sideEffects, run: actionTool(actionDrag)},
		{Name: "upload_file", Description: "Set files on an <input type=file> (ref/selector). Paths must exist on the machine running Seebro.",
			InputSchema: obj(withTarget(map[string]any{"files": pArr(map[string]any{"type": "string"}, "Absolute file paths")}), "files"),
			Annotations: sideEffects, run: actionTool(actionUpload)},
		{Name: "batch_actions", Description: "Run several /action steps in one call. Each step: {kind, ref|selector, text, key, value, …}. kinds: click, dblclick, clickAt, type, fill, press, focus, hover, select, scroll, drag, upload, humanClick, humanType.",
			InputSchema: obj(withTab(map[string]any{
				"actions":     pArr(map[string]any{"type": "object"}, "Action objects"),
				"stopOnError": pBool("Stop at the first failing step"),
			}), "actions"), Annotations: sideEffects, run: postTool("/actions", nil)},

		// Scripting
		{Name: "evaluate", Description: "Run JavaScript in the page, return the JSON result. Promises are awaited: fetch('/api/x').then(r => r.json()) runs with the page's cookies. With ref/selector pass a function, e.g. el => el.innerText.",
			InputSchema: obj(withTarget(map[string]any{"expression": pStr("JavaScript expression or function")}), "expression"),
			Annotations: sideEffects, run: postTool("/evaluate", nil)},

		// DevTools: network & console
		{Name: "network_requests", Description: "Network requests since the last navigation. Use network_request for headers and body.",
			InputSchema: obj(withTab(map[string]any{
				"type":    pArr(map[string]any{"type": "string"}, "e.g. [\"XHR\",\"Fetch\",\"Document\"]"),
				"filter":  pStr("URL contains (case-insensitive)"),
				"method":  pStr("HTTP method"),
				"status":  pStr("2xx, 4xx, failed or a code"),
				"all":     pBool("Include previous navigations"),
				"headers": pBool("Include request/response headers"),
				"limit":   pNum("Max entries (default 100)"),
				"offset":  pNum("Skip this many matches"),
			})), Annotations: readOnly, run: getTool("/network", nil)},
		{Name: "network_request", Description: "One request in detail: headers, POST data and the response body (text/JSON inline up to maxBytes; binary via saveTo).",
			InputSchema: obj(withTab(map[string]any{
				"id":       pStr("Request id from network_requests, e.g. r12"),
				"body":     pBool("Fetch the response body (default true)"),
				"maxBytes": pNum("Max body bytes inline (default 100000)"),
				"saveTo":   pStr("Write the full body to this file path"),
				"base64":   pBool("Return binary bodies as base64"),
			}), "id"), Annotations: readOnly, run: getTool("/network/request", nil)},
		{Name: "websocket_messages", Description: "WebSocket frames (sent/received) and Server-Sent Events of the tab since the last navigation — live data of chat, trading or dashboard apps.",
			InputSchema: obj(withTab(map[string]any{
				"filter": pStr("Connection URL contains"),
				"dir":    pEnum("Only this direction", "sent", "received", "sse"),
				"all":    pBool("Include previous navigations"),
				"limit":  pNum("Newest N messages (default 50)"),
			})), Annotations: readOnly, run: getTool("/network/websocket", nil)},
		{Name: "export_har", Description: "Save the tab's network log as a HAR file (opens in Chrome DevTools, Charles, Postman). bodies=true includes response bodies.",
			InputSchema: obj(withTab(map[string]any{
				"path":   pStr("Output .har path (required)"),
				"bodies": pBool("Include request/response bodies"),
				"all":    pBool("Include previous navigations"),
			}), "path"), Annotations: readOnly, run: getTool("/network/har", nil)},
		{Name: "performance_metrics", Description: "Page timing and Web Vitals (TTFB, FCP, LCP, CLS, load) plus Chrome runtime metrics (JS heap, DOM nodes, layout counts).",
			InputSchema: obj(withTab(map[string]any{})), Annotations: readOnly, run: getTool("/performance", nil)},
		{Name: "console_messages", Description: "Console output, uncaught exceptions and browser log entries since the last navigation.",
			InputSchema: obj(withTab(map[string]any{
				"level": pArr(map[string]any{"type": "string"}, "Levels to keep: log, info, warn, error, debug, verbose"),
				"all":   pBool("Include messages from previous navigations"),
				"limit": pNum("Newest N messages (default 100)"),
			})), Annotations: readOnly, run: getTool("/console", nil)},
		{Name: "clear_logs", Description: "Clear the recorded network requests and/or console messages of a tab.",
			InputSchema: obj(withTab(map[string]any{"network": pBool("Clear network (default true)"), "console": pBool("Clear console (default true)")})),
			Annotations: sideEffects, run: clearLogsTool},
		{Name: "handle_dialog", Description: "Accept or dismiss an open alert/confirm/prompt dialog (the page is frozen until you do).",
			InputSchema: obj(withTab(map[string]any{
				"action":     pEnum("accept or dismiss", "accept", "dismiss"),
				"promptText": pStr("Text to enter for prompt() dialogs"),
			}), "action"), Annotations: sideEffects, run: postTool("/dialog", nil)},

		// Emulation & output
		{Name: "emulate", Description: "Emulate device/environment for a tab. Each field is optional; \"reset\", \"auto\" or \"\" undo it.",
			InputSchema: obj(withTab(map[string]any{
				"viewport":      pStr("e.g. 390x844x3,mobile,touch or reset"),
				"userAgent":     pStr("User agent; \"\" restores the default"),
				"colorScheme":   pEnum("prefers-color-scheme", "dark", "light", "auto"),
				"reducedMotion": pEnum("prefers-reduced-motion", "reduce", "no-preference", "auto"),
				"geolocation":   pStr("\"lat,lng\" or reset"),
				"network":       pEnum("Network throttling", "Offline", "Slow 3G", "Fast 3G", "Slow 4G", "Fast 4G", "none"),
				"cpuThrottling": pNum("CPU slowdown factor (1 = off)"),
				"extraHeaders":  map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Headers for every request"},
				"timezone":      pStr("IANA timezone, e.g. Europe/Berlin"),
				"locale":        pStr("ICU locale, e.g. de-DE"),
			})), Annotations: sideEffects, run: postTool("/emulate", nil)},
		{Name: "save_pdf", Description: "Print the page to PDF (headless mode). Returns the file path.",
			InputSchema: obj(withTab(map[string]any{
				"path": pStr("Output path (default ~/.seebro/pdfs/…)"), "landscape": pBool("Landscape"), "printBackground": pBool("Print backgrounds (default true)"),
			})), Annotations: readOnly, run: getTool("/pdf", nil)},
		{Name: "downloads", Description: "Files the browser downloaded (CSV/PDF exports etc.) with progress and paths in the download directory.",
			InputSchema: obj(map[string]any{}), Annotations: readOnly, run: getTool("/downloads", nil)},

		// Session
		{Name: "cookies_get", Description: "Cookies for the tab's current URL (or url), optionally one by name.",
			InputSchema: obj(withTab(map[string]any{"url": pStr("URL to get cookies for"), "name": pStr("Cookie name")})),
			Annotations: readOnly, run: getTool("/cookies", nil)},
		{Name: "cookies_set", Description: "Set cookies: [{name, value, domain, path, secure, httpOnly, sameSite, expires}].",
			InputSchema: obj(withTab(map[string]any{"url": pStr("URL the cookies belong to"), "cookies": pArr(map[string]any{"type": "object"}, "Cookies")}), "cookies"),
			Annotations: sideEffects, run: postTool("/cookies", nil)},

		// Status & stealth
		{Name: "health", Description: "Browser connection status and tab count.",
			InputSchema: obj(map[string]any{}), Annotations: readOnly, run: getTool("/health", nil)},
		{Name: "stealth_status", Description: "Which anti-bot-detection patches are active, with a score.",
			InputSchema: obj(withTab(map[string]any{})), Annotations: readOnly, run: getTool("/stealth/status", nil)},
		{Name: "fingerprint_rotate", Description: "Apply a new random browser fingerprint (user agent, platform, screen, languages) to a tab.",
			InputSchema: obj(withTab(map[string]any{
				"os": pEnum("Target OS", "windows", "mac", "random"), "browser": pStr("Browser family: chrome (default), edge or safari (mac)"),
				"screen": pStr("Screen like 1920x1080 or random"), "language": pStr("e.g. de-DE"),
			})), Annotations: sideEffects, run: postTool("/fingerprint/rotate", nil)},
	}
}

func snapshotTool(ctx context.Context, s *mcpServer, args map[string]any) mcpResult {
	switch strArg(args, "filter") {
	case "", "interactive":
		args["filter"] = filterInteractive
	case "all":
		delete(args, "filter")
	}
	switch strArg(args, "format") {
	case "":
		args["format"] = "compact"
	case "json":
		delete(args, "format")
	}
	return s.call(ctx, "GET", "/snapshot", toQuery(args), nil)
}

func screenshotTool(ctx context.Context, s *mcpServer, args map[string]any) mcpResult {
	if strArg(args, "path") != "" {
		args["output"] = "file"
	} else {
		args["raw"] = true
		// Claude downsizes anything beyond ~1568px anyway; sending less is faster.
		if _, ok := args["maxDim"]; !ok {
			args["maxDim"] = 1568.0
		}
		if _, ok := args["quality"]; !ok {
			args["quality"] = 75.0
		}
	}
	return s.call(ctx, "GET", "/screenshot", toQuery(args), nil)
}

func clickTool(ctx context.Context, s *mcpServer, args map[string]any) mcpResult {
	kind := actionClick
	switch {
	case boolArg(args, "double"):
		kind = actionDblClick
	case boolArg(args, "human"):
		kind = actionHumanClick
	}
	delete(args, "double")
	delete(args, "human")
	args["kind"] = kind
	return s.call(ctx, "POST", "/action", nil, args)
}

func typeTool(ctx context.Context, s *mcpServer, args map[string]any) mcpResult {
	kind := actionType
	if boolArg(args, "human") {
		kind = actionHumanType
	}
	step := map[string]any{"kind": kind, "text": args["text"], "ref": args["ref"], "selector": args["selector"]}
	submit := strArg(args, "submitKey")
	if submit == "" {
		step["tabId"] = args["tabId"]
		return s.call(ctx, "POST", "/action", nil, step)
	}
	return s.call(ctx, "POST", "/actions", nil, map[string]any{
		"tabId":       args["tabId"],
		"stopOnError": true,
		"actions":     []any{step, map[string]any{"kind": actionPress, "key": submit}},
	})
}

func fillTool(ctx context.Context, s *mcpServer, args map[string]any) mcpResult {
	args["kind"] = actionFill
	args["text"] = args["value"]
	delete(args, "value")
	return s.call(ctx, "POST", "/action", nil, args)
}

func fillFormTool(ctx context.Context, s *mcpServer, args map[string]any) mcpResult {
	fields, _ := args["fields"].([]any)
	if len(fields) == 0 {
		return errResult("fields must be a non-empty list")
	}
	steps := make([]any, 0, len(fields))
	for _, f := range fields {
		m, ok := f.(map[string]any)
		if !ok {
			return errResult("each field must be an object {ref|selector, value}")
		}
		steps = append(steps, map[string]any{"kind": actionFill, "ref": m["ref"], "selector": m["selector"], "text": fmt.Sprint(m["value"])})
	}
	return s.call(ctx, "POST", "/actions", nil, map[string]any{"tabId": args["tabId"], "actions": steps})
}

func scrollTool(ctx context.Context, s *mcpServer, args map[string]any) mcpResult {
	if x, ok := args["x"]; ok {
		args["scrollX"] = x
		delete(args, "x")
	}
	if y, ok := args["y"]; ok {
		args["scrollY"] = y
		delete(args, "y")
	}
	args["kind"] = actionScroll
	return s.call(ctx, "POST", "/action", nil, args)
}

func clearLogsTool(ctx context.Context, s *mcpServer, args map[string]any) mcpResult {
	clearNet, clearCon := true, true
	if v, ok := args["network"].(bool); ok {
		clearNet = v
	}
	if v, ok := args["console"].(bool); ok {
		clearCon = v
	}
	body := map[string]any{"tabId": args["tabId"]}
	if clearNet {
		if r := s.call(ctx, "POST", "/network/clear", nil, body); r.IsError {
			return r
		}
	}
	if clearCon {
		if r := s.call(ctx, "POST", "/console/clear", nil, body); r.IsError {
			return r
		}
	}
	return mcpResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf(`{"clearedNetwork":%t,"clearedConsole":%t}`, clearNet, clearCon)}}}
}

func contextsTool(ctx context.Context, s *mcpServer, args map[string]any) mcpResult {
	if name := strArg(args, "close"); name != "" {
		return s.call(ctx, "POST", "/context", nil, map[string]any{"action": "close", "name": name})
	}
	return s.call(ctx, "GET", "/contexts", nil, nil)
}
