package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForwardMCP(t *testing.T) {
	status, reply := 200, `{"jsonrpc":"2.0","id":1,"result":{}}`
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()

	oldToken := token
	token = "secret"
	defer func() { token = oldToken }()
	t.Setenv("SEEBRO_NO_AUTOSTART", "true")
	logf := func(string, ...any) {}

	out := forwardMCP(srv.Client(), srv.URL, []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`), logf)
	if string(out) != reply || gotAuth != "Bearer secret" {
		t.Errorf("passthrough: %s auth=%q", out, gotAuth)
	}

	status, reply = http.StatusAccepted, ""
	if out := forwardMCP(srv.Client(), srv.URL, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), logf); out != nil {
		t.Errorf("notification must not produce output: %s", out)
	}

	for code, wantCode := range map[int]float64{404: -32002, 401: -32001, 500: -32000} {
		status, reply = code, "plain text"
		out := forwardMCP(srv.Client(), srv.URL, []byte(`{"jsonrpc":"2.0","id":"a","method":"tools/list"}`), logf)
		var resp map[string]any
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("HTTP %d: not JSON: %s", code, out)
		}
		if resp["id"] != "a" || resp["error"].(map[string]any)["code"] != wantCode {
			t.Errorf("HTTP %d: %v", code, resp)
		}
	}
}

func TestForwardMCP_Unreachable(t *testing.T) {
	t.Setenv("SEEBRO_NO_AUTOSTART", "true")
	out := forwardMCP(http.DefaultClient, "http://127.0.0.1:1", []byte(`{"jsonrpc":"2.0","id":5,"method":"ping"}`), func(string, ...any) {})
	if !strings.Contains(string(out), "unreachable") || !strings.Contains(string(out), `"id":5`) {
		t.Errorf("got %s", out)
	}
}
