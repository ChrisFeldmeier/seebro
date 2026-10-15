package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingAPI captures the last request a tool made and answers with a canned response.
type recordingAPI struct {
	method, path string
	query        map[string]string
	body         map[string]any
	ctype        string
	respBody     string
	status       int
}

func (a *recordingAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.method, a.path = r.Method, r.URL.Path
	a.query = map[string]string{}
	for k := range r.URL.Query() {
		a.query[k] = r.URL.Query().Get(k)
	}
	a.body = nil
	if data, _ := io.ReadAll(r.Body); len(data) > 0 {
		_ = json.Unmarshal(data, &a.body)
	}
	ctype := a.ctype
	if ctype == "" {
		ctype = "application/json"
	}
	w.Header().Set("Content-Type", ctype)
	status := a.status
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	resp := a.respBody
	if resp == "" {
		resp = `{"ok": true}`
	}
	_, _ = w.Write([]byte(resp))
}

func newTestMCP() (*mcpServer, *recordingAPI) {
	api := &recordingAPI{}
	return newMCPServer(newTestBridge(), api), api
}

func rpcCall(t *testing.T, s *mcpServer, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleHTTP(w, req)
	var out map[string]any
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func callTool(t *testing.T, s *mcpServer, name string, args map[string]any) mcpResult {
	t.Helper()
	a, _ := json.Marshal(args)
	_, out := rpcCall(t, s, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"`+name+`","arguments":`+string(a)+`}}`)
	raw, _ := json.Marshal(out["result"])
	var res mcpResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode result: %v (%v)", err, out)
	}
	return res
}

func TestMCP_Initialize(t *testing.T) {
	s, _ := newTestMCP()
	code, out := rpcCall(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	res := out["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" {
		t.Errorf("expected negotiated 2025-03-26, got %v", res["protocolVersion"])
	}
	if res["serverInfo"].(map[string]any)["name"] != "seebro" {
		t.Errorf("serverInfo: %v", res["serverInfo"])
	}

	_, out = rpcCall(t, s, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if v := out["result"].(map[string]any)["protocolVersion"]; v != mcpProtocolVersions[0] {
		t.Errorf("unknown version should fall back to latest, got %v", v)
	}
}

func TestMCP_NotificationAccepted(t *testing.T) {
	s, _ := newTestMCP()
	code, out := rpcCall(t, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if code != http.StatusAccepted || out != nil {
		t.Errorf("expected 202 without body, got %d %v", code, out)
	}
}

func TestMCP_Errors(t *testing.T) {
	s, _ := newTestMCP()
	_, out := rpcCall(t, s, `{"jsonrpc":"2.0","id":1,"method":"nope"}`)
	if out["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Errorf("unknown method: %v", out)
	}
	_, out = rpcCall(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"missing"}}`)
	if out["error"].(map[string]any)["code"].(float64) != -32602 {
		t.Errorf("unknown tool: %v", out)
	}
	code, _ := rpcCall(t, s, `{bad json`)
	if code != 400 {
		t.Errorf("parse error should be 400, got %d", code)
	}
}

func TestMCP_Batch(t *testing.T) {
	s, _ := newTestMCP()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/x"}]`))
	w := httptest.NewRecorder()
	s.handleHTTP(w, req)
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out) != 1 {
		t.Fatalf("expected one response, got %s", w.Body.String())
	}
}

func TestMCP_ToolCatalogue(t *testing.T) {
	s, _ := newTestMCP()
	_, out := rpcCall(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) < 35 {
		t.Errorf("expected the full API surface, got %d tools", len(tools))
	}
	seen := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		if seen[name] {
			t.Errorf("duplicate tool %s", name)
		}
		seen[name] = true
		if tool["description"] == "" {
			t.Errorf("%s: missing description", name)
		}
		schema := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Errorf("%s: schema type %v", name, schema["type"])
		}
		props := schema["properties"].(map[string]any)
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if _, ok := props[r.(string)]; !ok {
					t.Errorf("%s: required %v not in properties", name, r)
				}
			}
		}
	}
	if seen["shutdown"] {
		t.Error("shutdown must not be exposed as a tool")
	}
}

func TestMCP_ToolRouting(t *testing.T) {
	cases := []struct {
		tool   string
		args   map[string]any
		method string
		path   string
		check  func(t *testing.T, api *recordingAPI)
	}{
		{"tab_new", map[string]any{"url": "https://x.test"}, "POST", "/tab", func(t *testing.T, a *recordingAPI) {
			if a.body["action"] != "new" || a.body["url"] != "https://x.test" {
				t.Errorf("body %v", a.body)
			}
		}},
		{"snapshot", map[string]any{"tabId": "T"}, "GET", "/snapshot", func(t *testing.T, a *recordingAPI) {
			if a.query["filter"] != "interactive" || a.query["format"] != "compact" || a.query["tabId"] != "T" {
				t.Errorf("defaults not applied: %v", a.query)
			}
		}},
		{"snapshot", map[string]any{"filter": "all", "format": "json"}, "GET", "/snapshot", func(t *testing.T, a *recordingAPI) {
			if _, ok := a.query["filter"]; ok {
				t.Errorf("filter=all should drop the filter: %v", a.query)
			}
			if _, ok := a.query["format"]; ok {
				t.Errorf("format=json should use the default JSON output: %v", a.query)
			}
		}},
		{"click", map[string]any{"ref": "e1", "double": true}, "POST", "/action", func(t *testing.T, a *recordingAPI) {
			if a.body["kind"] != "dblclick" || a.body["ref"] != "e1" {
				t.Errorf("body %v", a.body)
			}
		}},
		{"click", map[string]any{"ref": "e1", "human": true}, "POST", "/action", func(t *testing.T, a *recordingAPI) {
			if a.body["kind"] != "humanClick" {
				t.Errorf("body %v", a.body)
			}
		}},
		{"fill", map[string]any{"ref": "e2", "value": "x"}, "POST", "/action", func(t *testing.T, a *recordingAPI) {
			if a.body["kind"] != "fill" || a.body["text"] != "x" {
				t.Errorf("body %v", a.body)
			}
		}},
		{"type_text", map[string]any{"ref": "e3", "text": "q", "submitKey": "Enter"}, "POST", "/actions", func(t *testing.T, a *recordingAPI) {
			acts := a.body["actions"].([]any)
			if len(acts) != 2 || acts[1].(map[string]any)["key"] != "Enter" || a.body["stopOnError"] != true {
				t.Errorf("body %v", a.body)
			}
		}},
		{"fill_form", map[string]any{"fields": []any{map[string]any{"ref": "e1", "value": "a"}, map[string]any{"selector": "#b", "value": true}}}, "POST", "/actions", func(t *testing.T, a *recordingAPI) {
			acts := a.body["actions"].([]any)
			if len(acts) != 2 || acts[1].(map[string]any)["text"] != "true" {
				t.Errorf("body %v", a.body)
			}
		}},
		{"scroll", map[string]any{"y": 500}, "POST", "/action", func(t *testing.T, a *recordingAPI) {
			if a.body["scrollY"] != 500.0 || a.body["y"] != nil {
				t.Errorf("body %v", a.body)
			}
		}},
		{"network_requests", map[string]any{"type": []any{"XHR", "Fetch"}, "headers": true}, "GET", "/network", func(t *testing.T, a *recordingAPI) {
			if a.query["type"] != "XHR,Fetch" || a.query["headers"] != "true" {
				t.Errorf("query %v", a.query)
			}
		}},
		{"screenshot", map[string]any{}, "GET", "/screenshot", func(t *testing.T, a *recordingAPI) {
			if a.query["raw"] != "true" {
				t.Errorf("query %v", a.query)
			}
		}},
		{"screenshot", map[string]any{"path": "/tmp/x.png"}, "GET", "/screenshot", func(t *testing.T, a *recordingAPI) {
			if a.query["output"] != "file" || a.query["raw"] != "" {
				t.Errorf("query %v", a.query)
			}
		}},
		{"navigate", map[string]any{"type": "back"}, "POST", "/navigate", nil},
		{"handle_dialog", map[string]any{"action": "accept"}, "POST", "/dialog", nil},
		{"wait_for", map[string]any{"text": []any{"a"}}, "POST", "/wait", nil},
		{"emulate", map[string]any{"viewport": "reset"}, "POST", "/emulate", nil},
		{"downloads", map[string]any{}, "GET", "/downloads", nil},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			s, api := newTestMCP()
			res := callTool(t, s, c.tool, c.args)
			if res.IsError {
				t.Fatalf("unexpected error: %v", res.Content)
			}
			if api.method != c.method || api.path != c.path {
				t.Errorf("got %s %s, want %s %s", api.method, api.path, c.method, c.path)
			}
			if c.check != nil {
				c.check(t, api)
			}
		})
	}
}

func TestMCP_ClearLogsCallsBoth(t *testing.T) {
	s, api := newTestMCP()
	res := callTool(t, s, "clear_logs", map[string]any{"tabId": "T", "network": false})
	if res.IsError || api.path != "/console/clear" {
		t.Errorf("expected only console clear, last path %s", api.path)
	}
}

func TestMCP_ResultConversion(t *testing.T) {
	s, api := newTestMCP()

	api.ctype, api.respBody = "image/jpeg", "\xff\xd8jpeg"
	res := callTool(t, s, "screenshot", map[string]any{})
	if res.Content[0].Type != "image" || res.Content[0].MimeType != "image/jpeg" || res.Content[0].Data == "" {
		t.Errorf("image content: %+v", res.Content[0])
	}

	api.ctype, api.respBody, api.status = "application/json", "{\n  \"error\": \"nope\"\n}", 404
	res = callTool(t, s, "tabs_list", map[string]any{})
	if !res.IsError || res.Content[0].Text != `{"error":"nope"}` {
		t.Errorf("error result: %+v", res)
	}

	api.ctype, api.respBody, api.status = "text/plain", strings.Repeat("x", mcpMaxText+10), 200
	res = callTool(t, s, "snapshot", map[string]any{})
	if len(res.Content[0].Text) > mcpMaxText+200 || !strings.Contains(res.Content[0].Text, "truncated") {
		t.Errorf("text should be truncated, len %d", len(res.Content[0].Text))
	}
}

func TestMCP_DialogNotice(t *testing.T) {
	s, _ := newTestMCP()
	m := newTabMonitor()
	m.dialog = &dialogInfo{Type: "confirm", Message: "Sure?"}
	s.b.monitors = map[string]*tabMonitor{"T1": m}
	res := callTool(t, s, "health", map[string]any{})
	if len(res.Content) != 2 || !strings.Contains(res.Content[1].Text, "handle_dialog") {
		t.Errorf("expected dialog notice, got %+v", res.Content)
	}
}

func TestToQuery(t *testing.T) {
	q := toQuery(map[string]any{"a": "x", "b": true, "c": 2.5, "d": []any{"p", "q"}, "e": nil, "f": ""})
	if q.Get("a") != "x" || q.Get("b") != "true" || q.Get("c") != "2.5" || q.Get("d") != "p,q" {
		t.Errorf("query %v", q)
	}
	if q.Has("e") || q.Has("f") {
		t.Errorf("empty values should be dropped: %v", q)
	}
}

func TestMCP_CallUsesRequestContext(t *testing.T) {
	s, _ := newTestMCP()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A cancelled context must not panic or hang the in-process dispatch.
	res := s.call(ctx, "GET", "/health", nil, nil)
	if len(res.Content) == 0 {
		t.Error("expected content")
	}
}
