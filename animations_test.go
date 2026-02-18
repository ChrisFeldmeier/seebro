package main

import (
	"strings"
	"testing"
)

func TestDisableAnimationsCSSContent(t *testing.T) {
	// Verify the injected CSS contains the critical rules
	if !strings.Contains(disableAnimationsCSS, "animation: none !important") {
		t.Error("missing animation: none rule")
	}
	if !strings.Contains(disableAnimationsCSS, "transition: none !important") {
		t.Error("missing transition: none rule")
	}
	if !strings.Contains(disableAnimationsCSS, "animation-duration: 0s !important") {
		t.Error("missing animation-duration: 0s rule")
	}
	if !strings.Contains(disableAnimationsCSS, "transition-duration: 0s !important") {
		t.Error("missing transition-duration: 0s rule")
	}
	if !strings.Contains(disableAnimationsCSS, "scroll-behavior: auto !important") {
		t.Error("missing scroll-behavior: auto rule")
	}
	if !strings.Contains(disableAnimationsCSS, "data-seebro") {
		t.Error("missing data-seebro attribute for identification")
	}
}

func TestDisableAnimationsCSSIsIIFE(t *testing.T) {
	trimmed := strings.TrimSpace(disableAnimationsCSS)
	if !strings.HasPrefix(trimmed, "(function()") {
		t.Error("CSS injection should be wrapped in IIFE")
	}
	if !strings.HasSuffix(trimmed, "();") {
		t.Error("CSS injection should end with IIFE invocation")
	}
}

func TestNoAnimationsConfigDefault(t *testing.T) {
	// Default should be false (env not set in tests)
	if noAnimations {
		t.Error("noAnimations should default to false")
	}
}
