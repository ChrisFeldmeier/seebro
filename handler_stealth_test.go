package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestHandleStealthStatus_NoTab_ReturnsStatic(t *testing.T) {
	b := &Bridge{}
	b.tabs = make(map[string]*TabEntry)
	req := httptest.NewRequest("GET", "/stealth/status", nil)
	w := httptest.NewRecorder()

	b.handleStealthStatus(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
	// Should contain level and score
	body := w.Body.String()
	if !searchString(body, "level") || !searchString(body, "score") {
		t.Errorf("expected level and score in response, got: %s", body)
	}
}

func TestHandleFingerprintRotate_InvalidJSON(t *testing.T) {
	b := &Bridge{}
	req := httptest.NewRequest("POST", "/fingerprint/rotate", bytes.NewReader([]byte(`not json`)))
	w := httptest.NewRecorder()

	b.handleFingerprintRotate(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ── generateFingerprint ────────────────────────────────────

func TestGenerateFingerprint_Windows(t *testing.T) {
	fp := generateFingerprint(fingerprintRequest{OS: "windows"})
	if fp.Platform != "Win32" {
		t.Errorf("expected Win32, got %q", fp.Platform)
	}
	if fp.UserAgent == "" {
		t.Error("expected non-empty user agent")
	}
	if fp.ScreenWidth == 0 || fp.ScreenHeight == 0 {
		t.Error("expected non-zero screen dimensions")
	}
	if fp.Vendor != "Google Inc." {
		t.Errorf("expected Google Inc., got %q", fp.Vendor)
	}
}

func TestGenerateFingerprint_Mac(t *testing.T) {
	fp := generateFingerprint(fingerprintRequest{OS: "mac"})
	if fp.Platform != "MacIntel" {
		t.Errorf("expected MacIntel, got %q", fp.Platform)
	}
}

func TestGenerateFingerprint_Random(t *testing.T) {
	fp := generateFingerprint(fingerprintRequest{OS: "random"})
	validPlatforms := map[string]bool{"Win32": true, "MacIntel": true}
	if !validPlatforms[fp.Platform] {
		t.Errorf("unexpected platform %q", fp.Platform)
	}
}

func TestGenerateFingerprint_UnknownOS(t *testing.T) {
	// Unknown OS — should return empty fingerprint (no match in map)
	fp := generateFingerprint(fingerprintRequest{OS: "freebsd"})
	// Will have screen/language/timezone from random selection below
	// but platform/UA won't be set from the map
	_ = fp // just ensure no panic
}

func TestGenerateFingerprint_WithBrowser(t *testing.T) {
	fp := generateFingerprint(fingerprintRequest{OS: "windows", Browser: "chrome"})
	if fp.UserAgent == "" {
		t.Error("expected non-empty user agent")
	}
}

// ── getStealthRecommendations ──────────────────────────────

func TestGetStealthRecommendations_AllPassed(t *testing.T) {
	recs := getStealthRecommendations(map[string]bool{"webdriver_false": true, "ua_not_headless": true})
	if len(recs) != 1 || recs[0] != "All checks passed" {
		t.Errorf("expected all-passed message, got %v", recs)
	}
}

func TestGetStealthRecommendations_FailedChecks(t *testing.T) {
	recs := getStealthRecommendations(map[string]bool{
		"webdriver_false": false,
		"ua_not_headless": true,
		"custom_check":    false,
	})
	if len(recs) != 2 {
		t.Fatalf("expected 2 recommendations, got %v", recs)
	}
	if recs[0] != "custom_check failed" || recs[1] != stealthCheckAdvice["webdriver_false"] {
		t.Errorf("unexpected advice (sorted by check name): %v", recs)
	}
}

// ── sendStealthResponse ────────────────────────────────────

func stealthLevelFor(t *testing.T, passed, total int) string {
	t.Helper()
	features := map[string]bool{}
	for i := 0; i < total; i++ {
		features[string(rune('a'+i))] = i < passed
	}
	w := httptest.NewRecorder()
	(&Bridge{}).sendStealthResponse(w, features, "", "", true)
	var out struct {
		Level string `json:"level"`
		Score int    `json:"score"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Score != passed*100/total {
		t.Errorf("score %d for %d/%d", out.Score, passed, total)
	}
	return out.Level
}

func TestSendStealthResponse_Levels(t *testing.T) {
	cases := []struct {
		passed, total int
		level         string
	}{
		{10, 10, "high"}, {8, 10, "medium"}, {5, 10, "basic"}, {1, 10, "minimal"},
	}
	for _, c := range cases {
		if got := stealthLevelFor(t, c.passed, c.total); got != c.level {
			t.Errorf("%d/%d: level %s, want %s", c.passed, c.total, got, c.level)
		}
	}
}

// ── staticStealthFeatures ──────────────────────────────────

func TestStaticStealthFeatures(t *testing.T) {
	if f := staticStealthFeatures(true); len(f) == 0 || !f["ua_not_headless"] {
		t.Errorf("with UA override: %v", f)
	}
	if f := staticStealthFeatures(false); f["ua_not_headless"] {
		t.Errorf("without UA override the UA still says Headless: %v", f)
	}
}
