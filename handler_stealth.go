package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sort"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// ── GET /stealth/status ────────────────────────────────────

// launchStealthFlags are the Chrome switches set in main.go that matter for detection.
var launchStealthFlags = []string{
	"--disable-blink-features=AutomationControlled",
	"--exclude-switches=enable-automation",
	"--disable-infobars",
}

// handleStealthStatus runs the checks bot detectors use inside a live tab and
// scores the result. Without a browser it reports the static configuration.
func (b *Bridge) handleStealthStatus(w http.ResponseWriter, r *http.Request) {
	features := map[string]bool{}
	userAgent, renderer := "", ""
	measured := false

	if ctx, _, err := b.TabContext(r.URL.Query().Get("tabId")); err == nil {
		tCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var res struct {
			Checks        map[string]bool `json:"checks"`
			UserAgent     string          `json:"userAgent"`
			WebglRenderer string          `json:"webglRenderer"`
		}
		err := chromedp.Run(tCtx, chromedp.Evaluate(stealthChecksJS, &res, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}))
		if err == nil && len(res.Checks) > 0 {
			features, userAgent, renderer, measured = res.Checks, res.UserAgent, res.WebglRenderer, true
		}
	}
	if !measured {
		features = staticStealthFeatures(b.userAgent != "" || !headless)
	}
	b.sendStealthResponse(w, features, userAgent, renderer, measured)
}

// staticStealthFeatures describes what the configuration provides when no
// tab is available to measure.
func staticStealthFeatures(uaOverride bool) map[string]bool {
	return map[string]bool{
		"webdriver_false":     true, // launch flag
		"ua_not_headless":     uaOverride,
		"client_hints_clean":  uaOverride,
		"functions_native":    true,
		"no_global_leaks":     true,
		"device_memory_valid": true,
	}
}

func (b *Bridge) sendStealthResponse(w http.ResponseWriter, features map[string]bool, userAgent, renderer string, measured bool) {
	passed := 0
	for _, ok := range features {
		if ok {
			passed++
		}
	}
	score := 0
	if len(features) > 0 {
		score = passed * 100 / len(features)
	}

	var level string
	switch {
	case score >= 90:
		level = "high"
	case score >= 70:
		level = "medium"
	case score >= 40:
		level = "basic"
	default:
		level = "minimal"
	}

	jsonResp(w, 200, map[string]any{
		"level":           level,
		"score":           score,
		"measured":        measured,
		"features":        features,
		"chrome_flags":    launchStealthFlags,
		"headless_mode":   headless,
		"stealth_level":   stealthLevel,
		"user_agent":      userAgent,
		"webgl_renderer":  renderer,
		"profile_path":    profileDir,
		"recommendations": getStealthRecommendations(features),
	})
}

func getStealthRecommendations(features map[string]bool) []string {
	recs := []string{}
	keys := make([]string, 0, len(features))
	for k := range features {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !features[k] {
			if advice, ok := stealthCheckAdvice[k]; ok {
				recs = append(recs, advice)
			} else {
				recs = append(recs, k+" failed")
			}
		}
	}
	if len(recs) == 0 {
		recs = append(recs, "All checks passed")
	}
	return recs
}

// ── POST /fingerprint/rotate ───────────────────────────────

type fingerprintRequest struct {
	TabID    string `json:"tabId"`
	OS       string `json:"os"`
	Browser  string `json:"browser"`
	Screen   string `json:"screen"`
	Language string `json:"language"`
	Timezone int    `json:"timezone"`
	WebGL    bool   `json:"webgl"`
	Canvas   bool   `json:"canvas"`
	Fonts    bool   `json:"fonts"`
	Audio    bool   `json:"audio"`
}

func (b *Bridge) handleFingerprintRotate(w http.ResponseWriter, r *http.Request) {
	var req fingerprintRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}

	ctx, tabID, err := b.TabContext(req.TabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}

	fp := generateFingerprint(req)

	tCtx, tCancel := context.WithTimeout(ctx, 5*time.Second)
	defer tCancel()

	// CDP-level overrides (undetectable — no JS property redefinition)
	if err := chromedp.Run(tCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			// UA, platform, accept-language at network level
			err := emulation.SetUserAgentOverride(fp.UserAgent).
				WithPlatform(fp.Platform).
				WithAcceptLanguage(fp.Language).
				Do(ctx)
			if err != nil {
				return fmt.Errorf("setUserAgentOverride: %w", err)
			}
			return nil
		}),
	); err != nil {
		jsonErr(w, 500, fmt.Errorf("CDP UA override: %w", err))
		return
	}

	// Hardware and screen via CDP as well: redefining navigator/screen
	// properties in JS is visible to detectors (descriptors, toString).
	vs := b.emulatedViewport(tabID)
	metrics := emulation.SetDeviceMetricsOverride(0, 0, 0, false)
	if vs != nil {
		metrics = emulation.SetDeviceMetricsOverride(vs.Width, vs.Height, vs.DPR, vs.Mobile)
	}
	if err := chromedp.Run(tCtx,
		emulation.SetHardwareConcurrencyOverride(int64(fp.CPUCores)),
		metrics.WithScreenWidth(int64(fp.ScreenWidth)).WithScreenHeight(int64(fp.ScreenHeight)),
	); err != nil {
		// Non-fatal: the UA override above is the important part.
		slog.Warn("fingerprint hardware/screen override failed", "err", err)
	}

	jsonResp(w, 200, map[string]any{
		"fingerprint": fp,
		"status":      "rotated",
	})
}

type fingerprint struct {
	UserAgent      string `json:"userAgent"`
	Platform       string `json:"platform"`
	Vendor         string `json:"vendor"`
	ScreenWidth    int    `json:"screenWidth"`
	ScreenHeight   int    `json:"screenHeight"`
	Language       string `json:"language"`
	TimezoneOffset int    `json:"timezoneOffset"`
	CPUCores       int    `json:"cpuCores"`
	Memory         int    `json:"memory"`
}

func generateFingerprint(req fingerprintRequest) fingerprint {
	fp := fingerprint{}

	osConfigs := map[string]map[string]fingerprint{
		"windows": {
			"chrome": {
				UserAgent: fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36", chromeVersion),
				Platform:  "Win32",
				Vendor:    "Google Inc.",
			},
			"edge": {
				UserAgent: fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36 Edg/%s", chromeVersion, chromeVersion),
				Platform:  "Win32",
				Vendor:    "Google Inc.",
			},
		},
		"mac": {
			"chrome": {
				UserAgent: fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36", chromeVersion),
				Platform:  "MacIntel",
				Vendor:    "Google Inc.",
			},
			"safari": {
				UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
				Platform:  "MacIntel",
				Vendor:    "Apple Computer, Inc.",
			},
		},
	}

	os := req.OS
	if os == "random" {
		if rand.Float64() < 0.7 {
			os = "windows"
		} else {
			os = "mac"
		}
	}

	browser := req.Browser
	if browser == "" {
		browser = "chrome"
	}

	if osConfig, ok := osConfigs[os]; ok {
		if browserConfig, ok := osConfig[browser]; ok {
			fp.UserAgent = browserConfig.UserAgent
			fp.Platform = browserConfig.Platform
			fp.Vendor = browserConfig.Vendor
		}
	}

	screens := [][]int{
		{1920, 1080}, {1366, 768}, {1536, 864}, {1440, 900},
		{1280, 720}, {1600, 900}, {2560, 1440},
	}
	if req.Screen == "random" {
		screen := screens[rand.Intn(len(screens))]
		fp.ScreenWidth = screen[0]
		fp.ScreenHeight = screen[1]
	} else if req.Screen != "" {
		_, _ = fmt.Sscanf(req.Screen, "%dx%d", &fp.ScreenWidth, &fp.ScreenHeight)
	} else {
		fp.ScreenWidth = 1920
		fp.ScreenHeight = 1080
	}

	if req.Language != "" {
		fp.Language = req.Language
	} else {
		fp.Language = "en-US"
	}

	if req.Timezone != 0 {
		fp.TimezoneOffset = req.Timezone
	} else {
		fp.TimezoneOffset = -300
	}

	fp.CPUCores = []int{4, 8, 8, 12, 16}[rand.Intn(5)]
	fp.Memory = []int{8, 16, 32}[rand.Intn(3)] // deviceMemory is always a power of two

	return fp
}
