package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Validation paths that don't need Chrome.

func TestDevtoolsHandlers_Validation(t *testing.T) {
	b := newTestBridge()
	cases := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		target  string
		body    string
		want    int
	}{
		{"wait without condition", b.handleWait, "POST", "/wait", `{}`, 400},
		{"wait bad json", b.handleWait, "POST", "/wait", `{`, 400},
		{"wait no tab", b.handleWait, "POST", "/wait", `{"text":"x"}`, 404},
		{"dialog bad action", b.handleDialog, "POST", "/dialog", `{"action":"maybe"}`, 400},
		{"dialog no tab", b.handleDialog, "POST", "/dialog", `{"action":"accept"}`, 404},
		{"emulate bad json", b.handleEmulate, "POST", "/emulate", `[`, 400},
		{"emulate no tab", b.handleEmulate, "POST", "/emulate", `{"viewport":"1x1"}`, 404},
		{"network no tab", b.handleNetwork, "GET", "/network", ``, 404},
		{"network request no tab", b.handleNetworkRequest, "GET", "/network/request?id=r1", ``, 404},
		{"console no tab", b.handleConsole, "GET", "/console", ``, 404},
		{"element no tab", b.handleElement, "GET", "/element?selector=a", ``, 404},
		{"pdf no tab", b.handlePDF, "GET", "/pdf", ``, 404},
		{"clear no tab", b.handleMonitorClear(true, true), "POST", "/network/clear", `{}`, 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
			w := httptest.NewRecorder()
			c.handler(w, req)
			if w.Code != c.want {
				t.Errorf("got %d want %d: %s", w.Code, c.want, w.Body.String())
			}
		})
	}
}

func TestHandleNavigate_HistoryNoTab(t *testing.T) {
	b := newTestBridge()
	req := httptest.NewRequest("POST", "/navigate", strings.NewReader(`{"type":"back"}`))
	w := httptest.NewRecorder()
	b.handleNavigate(w, req)
	if w.Code != 404 {
		t.Errorf("back without tab should be 404, got %d", w.Code)
	}
}

func TestResolveRef(t *testing.T) {
	b := newTestBridge()
	b.snapshots["T"] = &refCache{refs: map[string]int64{"e1": 99}}
	if id, err := b.resolveRef("T", "e1"); err != nil || id != 99 {
		t.Errorf("got %d %v", id, err)
	}
	if id, err := b.resolveRef("T", ""); err != nil || id != 0 {
		t.Errorf("empty ref: %d %v", id, err)
	}
	if _, err := b.resolveRef("T", "e2"); err == nil {
		t.Error("missing ref should error")
	}
}

func TestDownloadsEndpoint(t *testing.T) {
	old := downloadDir
	downloadDir = t.TempDir()
	defer func() { downloadDir = old }()
	b := newTestBridge()
	w := httptest.NewRecorder()
	b.handleDownloads(w, httptest.NewRequest("GET", "/downloads", nil))
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out["dir"] != downloadDir {
		t.Errorf("unexpected %s", w.Body.String())
	}
}
