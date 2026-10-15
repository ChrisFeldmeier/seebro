package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindObscura(t *testing.T) {
	old, oldState := obscuraBinary, stateDir
	defer func() { obscuraBinary, stateDir = old, oldState }()

	obscuraBinary = filepath.Join(t.TempDir(), "missing")
	if _, err := findObscura(); err == nil {
		t.Error("missing OBSCURA_BINARY must error")
	}

	obscuraBinary = ""
	stateDir = t.TempDir()
	t.Setenv("PATH", t.TempDir()) // nothing on PATH
	bin := filepath.Join(stateDir, "bin", "obscura")
	if err := os.MkdirAll(filepath.Dir(bin), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if got, err := findObscura(); err != nil || got != bin {
		t.Errorf("expected %s from ~/.seebro/bin, got %q %v", bin, got, err)
	}
}

func TestPrimaryEngine(t *testing.T) {
	old := engineMode
	defer func() { engineMode = old }()
	for mode, want := range map[string]string{"chrome": engineChrome, "obscura": engineObscura, "nonsense": engineChrome} {
		engineMode = mode
		if got := primaryEngine(); got != want {
			t.Errorf("%s → %s, want %s", mode, got, want)
		}
	}
	engineMode = "auto"
	if got := primaryEngine(); got != engineChrome && got != engineObscura {
		t.Errorf("auto resolved to %q", got)
	}
}

func TestEngineRouting(t *testing.T) {
	b := newTestBridge()
	b.primary = engineChrome
	b.tabEngine = map[string]string{"page-2": engineObscura}
	if b.engineOf("page-2") != engineObscura || b.engineOf("ABC") != engineChrome {
		t.Error("engineOf")
	}
	if err := b.engineUnsupported("ABC", "x"); err != nil {
		t.Error("chrome tab must support everything")
	}
	if err := b.engineUnsupported("page-2", "isolated contexts"); err == nil || !strings.Contains(err.Error(), "obscura") {
		t.Errorf("obscura tab: %v", err)
	}
	if _, _, _, err := b.openTab(engineObscura, "ctx", "about:blank"); err == nil || !strings.Contains(err.Error(), "chrome") {
		t.Errorf("contexts on obscura must be refused: %v", err)
	}
	if _, _, _, err := b.CreateTabOnEngine("firefox", ""); err == nil {
		t.Error("unknown engine must error")
	}
	ctx := withEngine(context.Background(), engineObscura)
	if engineFromCtx(ctx) != engineObscura || engineFromCtx(context.Background()) != "" {
		t.Error("engine context tagging")
	}
}
