package main

import (
	"context"
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
	ctx := context.Background()
	b.snapshots["T"] = &refCache{refs: map[string]int64{"a1": 99}}
	if id, _, err := b.resolveRef(ctx, "T", "a1"); err != nil || id != 99 {
		t.Errorf("accessibility ref: got %d %v", id, err)
	}
	if id, _, err := b.resolveRef(ctx, "T", ""); err != nil || id != 0 {
		t.Errorf("empty ref: %d %v", id, err)
	}
	if _, _, err := b.resolveRef(ctx, "T", "a2"); err == nil {
		t.Error("missing ref should error")
	}
	// Engine refs need a snapshot of that frame first.
	if _, _, err := b.resolveRef(ctx, "T", "f3e7"); err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("unknown frame ref: %v", err)
	}
	if _, _, err := b.resolveRef(ctx, "T", "e7"); err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("engine ref without world: %v", err)
	}
}

func TestRefNameAndPattern(t *testing.T) {
	cases := map[string][2]int64{"e5": {0, 5}, "f2e17": {2, 17}}
	for ref, want := range cases {
		m := jsRefPattern.FindStringSubmatch(ref)
		if m == nil {
			t.Fatalf("%s did not match", ref)
		}
		if got := refName(int(want[0]), want[1]); got != ref {
			t.Errorf("refName(%d,%d) = %s, want %s", want[0], want[1], got, ref)
		}
	}
	for _, bad := range []string{"a5", "e", "f1", "ex5", "e5x"} {
		if jsRefPattern.MatchString(bad) {
			t.Errorf("%s should not be an engine ref", bad)
		}
	}
}

func TestPageText(t *testing.T) {
	text, total := pageText("hello wörld", 6, 3)
	if text != "wör" || total != 11 {
		t.Errorf("got %q %d", text, total)
	}
	if text, _ := pageText("abc", 10, 5); text != "" {
		t.Errorf("offset past end: %q", text)
	}
	if text, _ := pageText("abc", -1, 0); text != "abc" {
		t.Errorf("no limit: %q", text)
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
