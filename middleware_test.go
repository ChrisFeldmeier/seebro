package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestJsonResp(t *testing.T) {
	w := httptest.NewRecorder()
	jsonResp(w, 200, map[string]string{"status": "ok"})

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}
	var body map[string]string
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("expected ok, got %s", body["status"])
	}
}

func TestJsonErr(t *testing.T) {
	w := httptest.NewRecorder()
	jsonErr(w, 500, fmt.Errorf("something broke"))

	if w.Code != 500 {
		t.Errorf("expected 500, got %d", w.Code)
	}
	var body map[string]string
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body["error"] != "something broke" {
		t.Errorf("expected 'something broke', got %s", body["error"])
	}
}

func TestAuthMiddleware_NoToken(t *testing.T) {
	// When token is empty, all requests pass through
	origToken := token
	token = ""
	defer func() { token = origToken }()

	called := false
	handler := authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("handler should have been called")
	}
}

func TestAuthMiddleware_ValidToken(t *testing.T) {
	origToken := token
	token = "secret123"
	defer func() { token = origToken }()

	called := false
	handler := authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer secret123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("handler should have been called with valid token")
	}
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	origToken := token
	token = "secret123"
	defer func() { token = origToken }()

	called := false
	handler := authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if called {
		t.Error("handler should NOT have been called with invalid token")
	}
	if w.Code != 401 {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestCorsMiddleware(t *testing.T) {
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	// Unknown origins get no CORS headers (browsers then refuse to share responses).
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unexpected CORS header for foreign origin: %q", got)
	}

	// Allowed origins are echoed back, never "*".
	old := allowedOrigins
	allowedOrigins = []string{"https://app.example"}
	defer func() { allowedOrigins = old }()
	req = httptest.NewRequest("OPTIONS", "/test", nil)
	req.Header.Set("Origin", "https://app.example")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Errorf("preflight: %d %q", w.Code, w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestGuardMiddleware(t *testing.T) {
	called := 0
	handler := guardMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(200)
	}))
	cases := []struct {
		name    string
		host    string
		headers map[string]string
		want    int
	}{
		{"curl", "localhost:9867", nil, 200},
		{"same-origin page", "localhost:9867", map[string]string{"Origin": "http://localhost:" + port, "Sec-Fetch-Site": "same-origin"}, 200},
		{"typed URL", "127.0.0.1:9867", map[string]string{"Sec-Fetch-Site": "none"}, 200},
		{"cross-origin fetch", "localhost:9867", map[string]string{"Origin": "https://evil.example"}, 403},
		{"img tag", "localhost:9867", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"same-site other port", "localhost:9867", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"DNS rebinding", "evil.example:9867", nil, 403},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "/evaluate", nil)
		req.Host = c.host
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, w.Code, c.want)
		}
	}
	if called != 3 {
		t.Errorf("handler called %d times, want 3", called)
	}
}

func TestOutputPath(t *testing.T) {
	old := stateDir
	stateDir = t.TempDir()
	defer func() { stateDir = old }()
	dir := t.TempDir()

	if p, err := outputPath(dir+"/shot.png", ".png"); err != nil || p != dir+"/shot.png" {
		t.Errorf("plain path: %q %v", p, err)
	}
	if _, err := outputPath(dir+"/shot.sh", ".png"); err == nil {
		t.Error("wrong extension accepted")
	}
	if _, err := outputPath(dir + "/.ssh/authorized_keys"); err == nil {
		t.Error("hidden directory accepted")
	}
	if _, err := outputPath(stateDir+"/screens/a.png", ".png"); err != nil {
		t.Errorf("state dir must always be allowed: %v", err)
	}
	if p, _ := outputPath(""); p != "" {
		t.Error("empty path should stay empty")
	}
}

func TestEnsureToken(t *testing.T) {
	oldTok, oldBind := token, bindAddr
	defer func() { token, bindAddr = oldTok, oldBind }()
	t.Setenv("BRIDGE_CONFIG", t.TempDir()+"/config.json")

	token, bindAddr = "", "127.0.0.1"
	ensureToken()
	if token != "" {
		t.Error("loopback must not force a token")
	}
	bindAddr = "0.0.0.0"
	ensureToken()
	if len(token) != 48 {
		t.Fatalf("expected generated token, got %q", token)
	}
	data, _ := os.ReadFile(os.Getenv("BRIDGE_CONFIG"))
	if !strings.Contains(string(data), token) {
		t.Error("generated token not persisted")
	}
}

func TestLoggingMiddleware(t *testing.T) {
	handler := loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 201 {
		t.Errorf("expected 201, got %d", w.Code)
	}
}

func TestStatusWriter(t *testing.T) {
	w := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: w, code: 200}

	sw.WriteHeader(404)
	if sw.code != 404 {
		t.Errorf("expected 404, got %d", sw.code)
	}
}
