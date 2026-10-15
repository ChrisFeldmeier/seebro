package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
)

func TestMonitor_WebSocketMessages(t *testing.T) {
	m := newTabMonitor()
	ctx := context.TODO()
	m.handleEvent(ctx, &network.EventWebSocketCreated{RequestID: "w1", URL: "wss://live.test/feed"})
	m.handleEvent(ctx, &network.EventWebSocketFrameSent{RequestID: "w1", Response: &network.WebSocketFrame{Opcode: 1, PayloadData: "sub"}})
	m.handleEvent(ctx, &network.EventWebSocketFrameReceived{RequestID: "w1", Response: &network.WebSocketFrame{Opcode: 1, PayloadData: strings.Repeat("x", maxWSData+50)}})
	m.handleEvent(ctx, &network.EventEventSourceMessageReceived{RequestID: "s1", EventName: "tick", Data: "1"})

	all, total := m.WebSocketMessages(false, "", "", 0)
	if total != 3 || all[0].URL != "wss://live.test/feed" || all[0].Dir != "sent" {
		t.Fatalf("messages: %+v", all)
	}
	if all[1].Size != maxWSData+50 || len(all[1].Data) > maxWSData+4 {
		t.Errorf("payload not truncated: size=%d len=%d", all[1].Size, len(all[1].Data))
	}
	if sse, _ := m.WebSocketMessages(false, "", "sse", 0); len(sse) != 1 || sse[0].Event != "tick" {
		t.Errorf("sse filter: %+v", sse)
	}
	if f, _ := m.WebSocketMessages(false, "LIVE.test", "", 0); len(f) != 2 {
		t.Errorf("url filter is case-insensitive: %+v", f)
	}
	for i := 0; i < maxWSMessages+20; i++ {
		m.addWS("w1", "received", 1, "", "x")
	}
	if _, total := m.WebSocketMessages(true, "", "", 0); total != maxWSMessages {
		t.Errorf("ring buffer not capped: %d", total)
	}
	m.Clear(true, false)
	if _, total := m.WebSocketMessages(true, "", "", 0); total != 0 {
		t.Error("network clear must drop websocket messages")
	}
}

func TestDiffSnapshot_StableEngineRefs(t *testing.T) {
	prev := []A11yNode{{Ref: "e1", Role: "textbox", Name: "Email"}, {Ref: "e2", Role: "button", Name: "Send"}}
	curr := []A11yNode{{Ref: "e1", Role: "textbox", Name: "Email", Value: "a@b"}, {Ref: "e3", Role: "link", Name: "Help"}}
	added, changed, removed := diffSnapshot(prev, curr)
	if len(added) != 1 || added[0].Ref != "e3" {
		t.Errorf("added: %+v", added)
	}
	if len(changed) != 1 || changed[0].Ref != "e1" {
		t.Errorf("changed (same ref, new value): %+v", changed)
	}
	if len(removed) != 1 || removed[0].Ref != "e2" {
		t.Errorf("removed: %+v", removed)
	}
}

func TestFormatSnapshotCompact_FlagsAndFrames(t *testing.T) {
	out := formatSnapshotCompact([]A11yNode{
		{Ref: "e1", Role: "combobox", Name: "Plan", Value: "Free", Options: []string{"Free", "Pro"}, Required: true},
		{Ref: "e2", Role: "checkbox", Name: "Agree", Checked: true},
		{Ref: "e3", Role: "button", Name: "Menu", Collapsed: true, Disabled: true},
		{Ref: "f1e4", Role: "textbox", Name: "Card", Frame: "f1 https://pay.test/"},
		{Ref: "f1e5", Role: "link", Name: "Terms", Href: "https://pay.test/terms", Frame: "f1 https://pay.test/"},
	})
	for _, want := range []string{
		`e1:combobox "Plan" val="Free" opts=[Free|Pro] required`,
		`e2:checkbox "Agree" checked`,
		`e3:button "Menu" collapsed -`,
		"# frame f1 https://pay.test/\nf1e4:textbox \"Card\"",
		`f1e5:link "Terms" -> https://pay.test/terms`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "# frame") != 1 {
		t.Errorf("frame header should appear once per frame:\n%s", out)
	}
}

func TestContextHandlers(t *testing.T) {
	b := newTestBridge()
	w := httptest.NewRecorder()
	b.handleContext(w, httptest.NewRequest("POST", "/context", strings.NewReader(`{"action":"close"}`)))
	if w.Code != 400 {
		t.Errorf("missing name: %d", w.Code)
	}
	w = httptest.NewRecorder()
	b.handleContext(w, httptest.NewRequest("POST", "/context", strings.NewReader(`{"action":"close","name":"nope"}`)))
	if w.Code != 404 {
		t.Errorf("unknown context: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	b.handleContext(w, httptest.NewRequest("POST", "/context", strings.NewReader(`{"action":"open","name":"x"}`)))
	if w.Code != 400 {
		t.Errorf("unknown action: %d", w.Code)
	}
}

func TestCheckLock(t *testing.T) {
	b := newTestBridge()
	b.locks = newLockManager()
	if err := b.locks.Lock("T", "agent-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/navigate", nil)
	w := httptest.NewRecorder()
	if b.checkLock(w, req, "T") || w.Code != 423 {
		t.Errorf("anonymous caller must be refused: %d", w.Code)
	}
	req.Header.Set("X-Seebro-Owner", "agent-1")
	if !b.checkLock(httptest.NewRecorder(), req, "T") {
		t.Error("owner must pass")
	}
	if !b.checkLock(httptest.NewRecorder(), httptest.NewRequest("POST", "/navigate?owner=x", nil), "other-tab") {
		t.Error("unlocked tab must pass")
	}
}

func TestHasControlKeys(t *testing.T) {
	if hasControlKeys("hello world, ümlaut 😀") {
		t.Error("plain text has no control keys")
	}
	for _, s := range []string{"a\rb", "tab\there", "x\b"} {
		if !hasControlKeys(s) {
			t.Errorf("%q has control keys", s)
		}
	}
}

func TestTabHealth(t *testing.T) {
	m := newTabMonitor()
	if m.health() != "" {
		t.Error("fresh tab is healthy")
	}
	m.unresponsive = true
	if m.health() != "unresponsive" {
		t.Error("unresponsive")
	}
	m.crashed = true
	if m.health() != "crashed" {
		t.Error("crash wins")
	}
	if !isTimeout(context.DeadlineExceeded) || isTimeout(nil) {
		t.Error("isTimeout")
	}
}
