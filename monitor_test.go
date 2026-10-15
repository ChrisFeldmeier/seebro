package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
)

func reqEvent(id, url string, typ network.ResourceType, frame string) *network.EventRequestWillBeSent {
	return &network.EventRequestWillBeSent{
		RequestID: network.RequestID(id),
		Request:   &network.Request{URL: url, Method: "GET"},
		Type:      typ,
		FrameID:   cdp.FrameID(frame),
	}
}

func TestMonitor_RequestLifecycle(t *testing.T) {
	m := newTabMonitor()
	m.mainFrame = "F"
	m.handleEvent(context.TODO(), reqEvent("1", "https://a.test/", network.ResourceTypeDocument, "F"))
	m.handleEvent(context.TODO(), reqEvent("2", "https://a.test/api", network.ResourceTypeFetch, "F"))
	m.handleEvent(context.TODO(), &network.EventResponseReceived{RequestID: "2", Response: &network.Response{Status: 201, MimeType: "application/json"}})
	m.handleEvent(context.TODO(), &network.EventLoadingFinished{RequestID: "2", EncodedDataLength: 42})
	m.handleEvent(context.TODO(), reqEvent("3", "data:image/png;base64,xx", network.ResourceTypeImage, "F"))

	all, total := m.Requests(netFilter{})
	if total != 2 || len(all) != 2 {
		t.Fatalf("expected 2 requests (data: URLs skipped), got %d", total)
	}
	api := all[1]
	if api.Status != 201 || api.Size != 42 || !api.Done || api.ID != "r2" {
		t.Errorf("unexpected entry %+v", api)
	}
	if m.inflight != 1 {
		t.Errorf("document still in flight, inflight=%d", m.inflight)
	}

	got, _ := m.Requests(netFilter{types: []string{"fetch"}})
	if len(got) != 1 || got[0].URL != "https://a.test/api" {
		t.Errorf("type filter: %+v", got)
	}
	got, _ = m.Requests(netFilter{status: "2xx"})
	if len(got) != 1 {
		t.Errorf("status filter: %+v", got)
	}
	if e, ok := m.Request("r2"); !ok || e.Status != 201 {
		t.Errorf("lookup by id failed")
	}
}

func TestMonitor_NavigationScopesRequests(t *testing.T) {
	m := newTabMonitor()
	m.mainFrame = "F"
	m.handleEvent(context.TODO(), reqEvent("1", "https://a.test/", network.ResourceTypeDocument, "F"))
	m.handleEvent(context.TODO(), reqEvent("2", "https://a.test/x.js", network.ResourceTypeScript, "F"))
	m.handleEvent(context.TODO(), reqEvent("3", "https://iframe.test/", network.ResourceTypeDocument, "CHILD"))
	m.handleEvent(context.TODO(), reqEvent("4", "https://b.test/", network.ResourceTypeDocument, "F"))

	cur, _ := m.Requests(netFilter{})
	if len(cur) != 1 || cur[0].URL != "https://b.test/" {
		t.Errorf("only the latest navigation should be listed, got %+v", cur)
	}
	all, _ := m.Requests(netFilter{all: true})
	if len(all) != 4 {
		t.Errorf("all=true should list 4, got %d", len(all))
	}
}

func TestMonitor_Redirect(t *testing.T) {
	m := newTabMonitor()
	m.handleEvent(context.TODO(), reqEvent("1", "http://a.test/", network.ResourceTypeDocument, ""))
	ev := reqEvent("1", "https://a.test/", network.ResourceTypeDocument, "")
	ev.RedirectResponse = &network.Response{Status: 301}
	m.handleEvent(context.TODO(), ev)
	reqs, _ := m.Requests(netFilter{all: true})
	if len(reqs) != 2 || reqs[0].Status != 301 || !reqs[0].Done || reqs[1].URL != "https://a.test/" {
		t.Errorf("redirect chain: %+v", reqs)
	}
	if m.inflight != 1 {
		t.Errorf("inflight %d", m.inflight)
	}
}

func TestMonitor_RingBuffer(t *testing.T) {
	m := newTabMonitor()
	m.netMax, m.consoleMax = 5, 3
	for i := 0; i < 20; i++ {
		id := itoa(int64(i))
		m.handleEvent(context.TODO(), reqEvent(id, "https://a.test/"+id, network.ResourceTypeFetch, ""))
		m.addConsole("log", "console", "msg "+id, "", 0)
	}
	reqs, total := m.Requests(netFilter{all: true})
	if total != 5 || reqs[0].URL != "https://a.test/15" {
		t.Errorf("expected newest 5 requests, got %d starting %s", total, reqs[0].URL)
	}
	if len(m.netByID) != 5 {
		t.Errorf("index not pruned: %d", len(m.netByID))
	}
	if m.inflight != 5 {
		t.Errorf("evicted requests must leave inflight: %d", m.inflight)
	}
	msgs, _ := m.Console(true, nil, 0)
	if len(msgs) != 3 || msgs[2].Text != "msg 19" {
		t.Errorf("console ring: %+v", msgs)
	}
}

func TestMonitor_Console(t *testing.T) {
	m := newTabMonitor()
	str, _ := json.Marshal("hello")
	m.handleEvent(context.TODO(), &runtime.EventConsoleAPICalled{Type: runtime.APITypeWarning, Args: []*runtime.RemoteObject{
		{Type: runtime.TypeString, Value: str},
		{Type: runtime.TypeNumber, Value: []byte("42")},
		{Type: runtime.TypeObject, Description: "Object"},
	}})
	m.handleEvent(context.TODO(), &runtime.EventExceptionThrown{ExceptionDetails: &runtime.ExceptionDetails{
		Text: "Uncaught", Exception: &runtime.RemoteObject{Description: "Error: boom"}, LineNumber: 9,
	}})
	msgs, _ := m.Console(false, nil, 0)
	if len(msgs) != 2 || msgs[0].Level != "warn" || msgs[0].Text != "hello 42 Object" {
		t.Fatalf("console: %+v", msgs)
	}
	if msgs[1].Source != "exception" || msgs[1].Text != "Error: boom" || msgs[1].Line != 10 {
		t.Errorf("exception: %+v", msgs[1])
	}
	errs, _ := m.Console(false, []string{"error"}, 0)
	if len(errs) != 1 {
		t.Errorf("level filter: %+v", errs)
	}
	m.Clear(false, true)
	if msgs, _ := m.Console(true, nil, 0); len(msgs) != 0 {
		t.Error("clear failed")
	}
}

func TestMonitor_NetworkIdle(t *testing.T) {
	m := newTabMonitor()
	m.lastActivity = time.Now().Add(-time.Second)
	if !m.NetworkIdle(0, 500*time.Millisecond) {
		t.Error("quiet monitor should be idle")
	}
	m.handleEvent(context.TODO(), reqEvent("1", "https://a.test/", network.ResourceTypeFetch, ""))
	if m.NetworkIdle(2, 500*time.Millisecond) {
		t.Error("fresh activity should not be idle")
	}
}

func TestStatusMatches(t *testing.T) {
	cases := []struct {
		want string
		e    netEntry
		ok   bool
	}{
		{"4xx", netEntry{Status: 404}, true},
		{"4xx", netEntry{Status: 200}, false},
		{"failed", netEntry{Failed: "net::ERR"}, true},
		{"failed", netEntry{Status: 500}, true},
		{"204", netEntry{Status: 204}, true},
	}
	for _, c := range cases {
		if got := statusMatches(c.want, &c.e); got != c.ok {
			t.Errorf("%s vs %+v = %v", c.want, c.e, got)
		}
	}
}

func TestResolveKey(t *testing.T) {
	cases := []struct {
		in   string
		key  string
		mods int
		err  bool
	}{
		{"Enter", "\r", 0, false},
		{"enter", "\r", 0, false},
		{"a", "a", 0, false},
		{"Control+A", "A", 1, false},
		{"Meta+Shift+k", "k", 2, false},
		{"Control++", "+", 1, false},
		{"ArrowDown", "́", 0, false},
		{"Tab", "\t", 0, false},
		{"Bogus", "", 0, true},
		{"Hyper+A", "", 0, true},
	}
	for _, c := range cases {
		k, mods, err := resolveKey(c.in)
		if (err != nil) != c.err || k != c.key || len(mods) != c.mods {
			t.Errorf("resolveKey(%q) = %q %v %v", c.in, k, mods, err)
		}
	}
}

func TestIsTextual(t *testing.T) {
	if !isTextual("application/json; charset=utf-8", nil) || !isTextual("text/csv", nil) {
		t.Error("json/csv are text")
	}
	if isTextual("image/png", []byte{0x89, 'P'}) {
		t.Error("png is binary")
	}
	if !isTextual("", []byte(`{"a":1}`)) {
		t.Error("untyped JSON is text")
	}
}

func TestWaitRequest_TextForms(t *testing.T) {
	var a, b waitRequest
	_ = json.Unmarshal([]byte(`{"text":"one","timeout":5}`), &a)
	_ = json.Unmarshal([]byte(`{"text":["x","","y"],"selector":"#s"}`), &b)
	if len(a.Text) != 1 || a.Text[0] != "one" || a.Timeout != 5 {
		t.Errorf("string form: %+v", a)
	}
	if len(b.Text) != 2 || b.Selector != "#s" {
		t.Errorf("list form: %+v", b)
	}
}
