package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Headless Chrome announces itself as "HeadlessChrome" in the user agent.
// We derive the real UA and client-hint metadata from the running browser,
// strip "Headless" and apply it per tab at the protocol level, so headers,
// navigator.userAgent and navigator.userAgentData all agree.

// buildStealthScript wraps stealth.js in a closure so neither its helpers nor
// the injected settings become page-visible globals.
func buildStealthScript(seed int, level string) string {
	return fmt.Sprintf("(function () {\nvar __seebro_seed = %d;\nvar __seebro_stealth_level = %q;\n%s\n})();", seed, level, stealthScript)
}

// initUserAgent computes the UA override once at startup (headless only).
func (b *Bridge) initUserAgent(ctx context.Context) {
	var ua string
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		c := chromedp.FromContext(ctx)
		var e error
		_, _, _, ua, _, e = browser.GetVersion().Do(cdp.WithExecutor(ctx, c.Browser))
		return e
	}))
	if err != nil || !strings.Contains(ua, "HeadlessChrome") {
		return
	}
	b.userAgent = strings.Replace(ua, "HeadlessChrome", "Chrome", 1)

	var raw json.RawMessage
	err = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		res, exc, e := runtime.Evaluate(`navigator.userAgentData ? navigator.userAgentData.getHighEntropyValues(
			["architecture", "bitness", "model", "platformVersion", "fullVersionList", "wow64"]) : null`).
			WithAwaitPromise(true).WithReturnByValue(true).Do(ctx)
		if e != nil {
			return e
		}
		if exc != nil {
			return fmt.Errorf("%s", exceptionText(exc))
		}
		raw = json.RawMessage(res.Value)
		return nil
	}))
	var meta emulation.UserAgentMetadata
	if err == nil && len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &meta) == nil {
		clean := func(list []*emulation.UserAgentBrandVersion) {
			for _, bv := range list {
				bv.Brand = strings.Replace(bv.Brand, "HeadlessChrome", "Google Chrome", 1)
			}
		}
		clean(meta.Brands)
		clean(meta.FullVersionList)
		b.uaMeta = &meta
	}
	slog.Info("user agent override", "ua", b.userAgent)
}

// userAgentAction applies the stealth UA to a tab (no-op when headed).
func (b *Bridge) userAgentAction() chromedp.Action {
	if b.userAgent == "" {
		return chromedp.ActionFunc(func(context.Context) error { return nil })
	}
	p := emulation.SetUserAgentOverride(b.userAgent)
	if b.uaMeta != nil {
		p = p.WithUserAgentMetadata(b.uaMeta)
	}
	return p
}

// stealthChecksJS evaluates what bot detectors actually look at.
const stealthChecksJS = `(async () => {
	const r = {};
	r.webdriver_false = navigator.webdriver === false;
	r.ua_not_headless = !/Headless/i.test(navigator.userAgent);
	r.client_hints_clean = !navigator.userAgentData || !navigator.userAgentData.brands.some(b => /Headless/i.test(b.brand));
	r.plugins_native = navigator.plugins instanceof PluginArray && navigator.plugins.length > 0;
	r.mime_types_native = navigator.mimeTypes instanceof MimeTypeArray && navigator.mimeTypes.length > 0;
	r.window_chrome = typeof window.chrome === "object" && window.chrome !== null;
	r.languages = Array.isArray(navigator.languages) && navigator.languages.length > 0;
	try {
		const p = await navigator.permissions.query({name: "notifications"});
		r.permissions_consistent = (Notification.permission === "default" ? "prompt" : Notification.permission) === p.state;
	} catch (e) { r.permissions_consistent = false; }
	const ts = Function.prototype.toString;
	r.functions_native = /native code/.test(ts.call(navigator.permissions.query)) && /native code/.test(ts.call(ts));
	r.outer_dimensions = outerWidth > 0 && outerHeight > 0;
	let renderer = "";
	try {
		const gl = document.createElement("canvas").getContext("webgl");
		const ext = gl && gl.getExtension("WEBGL_debug_renderer_info");
		renderer = ext ? gl.getParameter(ext.UNMASKED_RENDERER_WEBGL) : "";
	} catch (e) {}
	r.webgl_hardware = !!renderer && !/SwiftShader|llvmpipe|software/i.test(renderer);
	r.device_memory_valid = !("deviceMemory" in navigator) || [0.25, 0.5, 1, 2, 4, 8, 16, 32].includes(navigator.deviceMemory);
	r.no_global_leaks = !Object.keys(window).some(k => /^(__seebro|cdc_|\$cdc_|__webdriver|__selenium|_phantom|callPhantom)/.test(k));
	return {checks: r, userAgent: navigator.userAgent, webglRenderer: renderer};
})()`

var stealthCheckAdvice = map[string]string{
	"webdriver_false":        "navigator.webdriver is not false: launch Chrome with --disable-blink-features=AutomationControlled",
	"ua_not_headless":        "user agent contains Headless: the UA override was not applied to this tab",
	"client_hints_clean":     "client hints mention Headless",
	"plugins_native":         "navigator.plugins is missing or not a real PluginArray",
	"mime_types_native":      "navigator.mimeTypes is missing or not a real MimeTypeArray",
	"window_chrome":          "window.chrome is missing",
	"languages":              "navigator.languages is empty",
	"permissions_consistent": "Notification.permission and permissions.query disagree",
	"functions_native":       "patched functions reveal their source via toString()",
	"outer_dimensions":       "window.outerWidth/outerHeight are 0",
	"webgl_hardware":         "WebGL uses a software renderer (SwiftShader/llvmpipe), typical for servers without GPU",
	"device_memory_valid":    "navigator.deviceMemory is not a power of two between 0.25 and 32, which real Chrome never reports",
	"no_global_leaks":        "automation-related globals are visible on window",
}
