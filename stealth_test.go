package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStealthJavaScript(t *testing.T) {
	content := stealthScript
	for _, feature := range []string{
		"sessionSeed", "seededRandom", "toDataURL", "measureText",
		"hardwareConcurrency", "deviceMemory", "nativeNames", "replaceMethod",
	} {
		if !strings.Contains(content, feature) {
			t.Errorf("stealth script missing feature: %s", feature)
		}
	}
	// These overrides are themselves detectable and must stay out.
	for _, bad := range []string{"navigator, 'webdriver'", "navigator, 'plugins'", "navigator, 'platform'", "chrome.runtime"} {
		if strings.Contains(content, bad) {
			t.Errorf("stealth script contains detectable override: %s", bad)
		}
	}
	if count := strings.Count(content, "const sessionSeed"); count != 1 {
		t.Errorf("expected 1 sessionSeed definition, found %d", count)
	}
}

func TestBuildStealthScript_NoGlobals(t *testing.T) {
	s := buildStealthScript(7, "full")
	if !strings.HasPrefix(s, "(function () {") || !strings.HasSuffix(s, "})();") {
		t.Error("stealth script must be wrapped in a closure")
	}
	if !strings.Contains(s, "var __seebro_seed = 7;") || !strings.Contains(s, `var __seebro_stealth_level = "full";`) {
		t.Error("settings not injected")
	}
}

func TestGenerateFingerprint(t *testing.T) {
	tests := []struct {
		name     string
		req      fingerprintRequest
		wantUA   string
		wantPlat string
	}{
		{
			name: "windows_chrome",
			req: fingerprintRequest{
				OS:      "windows",
				Browser: "chrome",
			},
			wantUA:   "Chrome/" + chromeVersion,
			wantPlat: "Win32",
		},
		{
			name: "mac_chrome",
			req: fingerprintRequest{
				OS:      "mac",
				Browser: "chrome",
			},
			wantUA:   "Chrome/" + chromeVersion,
			wantPlat: "MacIntel",
		},
		{
			name: "random_chrome",
			req: fingerprintRequest{
				OS:      "random",
				Browser: "chrome",
			},
			wantUA:   "Chrome/" + chromeVersion, // Should use configured chromeVersion
			wantPlat: "",                        // Could be Win32, MacIntel, or Linux
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fp := generateFingerprint(tt.req)

			if tt.wantUA != "" && !strings.Contains(fp.UserAgent, tt.wantUA) {
				t.Errorf("expected UA to contain %s, got %s", tt.wantUA, fp.UserAgent)
			}

			if tt.wantPlat != "" && fp.Platform != tt.wantPlat {
				t.Errorf("expected platform %s, got %s", tt.wantPlat, fp.Platform)
			}

			// Check all fields are populated
			if fp.UserAgent == "" {
				t.Error("UserAgent is empty")
			}
			if fp.Platform == "" && tt.req.OS != "random" {
				t.Error("Platform is empty")
			}
		})
	}
}

func TestHandleStealthStatus(t *testing.T) {
	b := &Bridge{
		tabs: make(map[string]*TabEntry),
	}

	req := httptest.NewRequest("GET", "/stealth/status", nil)
	w := httptest.NewRecorder()

	b.handleStealthStatus(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Level       string          `json:"level"`
		Score       int             `json:"score"`
		Features    map[string]bool `json:"features"`
		ChromeFlags []string        `json:"chrome_flags"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// Check response structure
	if resp.Level == "" {
		t.Error("level is empty")
	}
	if resp.Score < 0 || resp.Score > 100 {
		t.Errorf("invalid score: %d", resp.Score)
	}
	if len(resp.Features) == 0 {
		t.Error("features is empty")
	}
	if len(resp.ChromeFlags) == 0 {
		t.Error("chrome_flags is empty")
	}

	// Verify score calculation
	enabled := 0
	for _, v := range resp.Features {
		if v {
			enabled++
		}
	}
	expectedScore := (enabled * 100) / len(resp.Features)
	if resp.Score != expectedScore {
		t.Errorf("score mismatch: got %d, expected %d", resp.Score, expectedScore)
	}
}
