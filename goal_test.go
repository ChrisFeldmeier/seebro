package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// ── Fake site ─────────────────────────────────────────────

type fakePage struct {
	title, text string
	nodes       []A11yNode
	links       map[string]string // ref → url
	search      string            // ref of a search box; Enter goes to "/search?q=<text>"
}

type fakeBrowser struct {
	pages       map[string]*fakePage
	url         string
	back        []string
	typed       string
	actions     []string
	observe     int
	unfocusable bool // type fails like a field hidden in a collapsed header
}

func (f *fakeBrowser) Navigate(_ context.Context, u string) error {
	if _, ok := f.pages[u]; !ok {
		return fmt.Errorf("404 %s", u)
	}
	f.back = append(f.back, f.url)
	f.url = u
	return nil
}

func (f *fakeBrowser) Observe(context.Context) (goalObservation, error) {
	f.observe++
	p := f.pages[f.url]
	return goalObservation{URL: f.url, Title: p.title, Text: p.text, Nodes: p.nodes}, nil
}

func (f *fakeBrowser) Act(ctx context.Context, a map[string]any) error {
	kind, _ := a["kind"].(string)
	ref, _ := a["ref"].(string)
	f.actions = append(f.actions, kind+" "+ref)
	p := f.pages[f.url]
	switch kind {
	case "click":
		if to, ok := p.links[ref]; ok {
			return f.Navigate(ctx, to)
		}
		return nil // a button that does nothing
	case "type":
		if f.unfocusable {
			return fmt.Errorf("Element is not focusable")
		}
		f.typed, _ = a["text"].(string)
	case "fill":
		f.typed, _ = a["text"].(string)
	case "press":
		if p.search != "" {
			return f.Navigate(ctx, "/search?q="+f.typed)
		}
	}
	return nil
}

func (f *fakeBrowser) Back(context.Context) error {
	if len(f.back) == 0 {
		return fmt.Errorf("no history")
	}
	f.url, f.back = f.back[len(f.back)-1], f.back[:len(f.back)-1]
	return nil
}

func (f *fakeBrowser) Settle(context.Context, string) {}
func (f *fakeBrowser) Arm(context.Context)            {}
func (f *fakeBrowser) Submit(ctx context.Context, ref string) (bool, error) {
	f.actions = append(f.actions, "submit "+ref)
	if f.pages[f.url].search != ref {
		return false, nil
	}
	return true, f.Navigate(ctx, "/search?q="+f.typed)
}

func wiki() *fakeBrowser {
	link := func(ref, name, href string) A11yNode {
		return A11yNode{Ref: ref, Role: "link", Name: name, Href: "https://w.org" + href}
	}
	return &fakeBrowser{pages: map[string]*fakePage{
		"/Coffee": {title: "Coffee - Wikipedia", text: "Coffee is a beverage.",
			nodes: []A11yNode{
				{Ref: "e1", Role: "searchbox", Name: "Search Wikipedia"},
				link("e2", "Edit", "/w/index.php?action=edit"), // noise
				link("e3", "[1]", "#cite"),                     // noise
				link("e4", "Espresso", "/Espresso"),
				link("e5", "Espresso", "/Espresso"), // duplicate target
				{Ref: "e6", Role: "button", Name: "Dead button"},
			},
			links: map[string]string{"e4": "/Espresso", "e5": "/Espresso"}, search: "e1"},
		"/Espresso": {title: "Espresso - Wikipedia", text: "Espresso is concentrated coffee.",
			nodes: []A11yNode{link("e1", "Ristretto", "/Ristretto"), link("e2", "Coffee", "/Coffee")},
			links: map[string]string{"e1": "/Ristretto", "e2": "/Coffee"}},
		"/Ristretto":          {title: "Ristretto - Wikipedia", text: "Ristretto is a short shot."},
		"/search?q=ristretto": {title: "Ristretto - Wikipedia", text: "Ristretto is a short shot."},
	}}
}

func runGoal(t *testing.T, br goalBrowser, dec decider, req goalRequest) goalResult {
	t.Helper()
	if req.MaxSteps == 0 {
		req.MaxSteps = 10
	}
	if req.MaxSeconds == 0 {
		req.MaxSeconds = 10
	}
	return (&goalRunner{br: br, dec: dec, req: req}).run(context.Background())
}

// ── Action space ──────────────────────────────────────────

func TestBuildActionSpace(t *testing.T) {
	br := wiki()
	p := br.pages["/Coffee"]
	els, trunc := buildActionSpace(p.nodes, "https://w.org/Coffee", true, nil)
	if trunc {
		t.Error("not truncated")
	}
	keys := map[string]string{}
	for _, e := range els {
		keys[optionKey(e)] = e.Description
	}
	for _, want := range []string{"search_e1", "click_e4", "click_e6"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("missing %s in %v", want, keys)
		}
	}
	for _, bad := range []string{"click_e2", "click_e3", "click_e5", "type_e1"} {
		if _, ok := keys[bad]; ok {
			t.Errorf("%s should be filtered", bad)
		}
	}
	if d := keys["click_e4"]; d != `link "Espresso" -> /Espresso` {
		t.Errorf("description %q", d)
	}
	if els, _ := buildActionSpace(p.nodes, "https://w.org/Coffee", false, nil); len(els) != 2 {
		t.Errorf("noTyping must drop text fields: %+v", els)
	}

	form := []A11yNode{
		{Ref: "e1", Role: "textbox", Name: "Email"},
		{Ref: "e2", Role: "combobox", Name: "Plan", Options: []string{"Free", "Pro"}},
		{Ref: "e3", Role: "checkbox", Name: "Agree", Checked: true},
		{Ref: "e4", Role: "button", Name: "Off", Disabled: true},
	}
	els, _ = buildActionSpace(form, "https://x.org/", true, nil)
	got := []string{}
	for _, e := range els {
		got = append(got, optionKey(e))
	}
	if strings.Join(got, ",") != "type_e1,submit_e1,select_e2,click_e3" {
		t.Errorf("form option keys %v", got)
	}
	if !strings.Contains(els[3].Description, "[checked]") {
		t.Errorf("state suffix missing: %q", els[3].Description)
	}

	var many []A11yNode
	for i := range 300 {
		many = append(many, A11yNode{Ref: fmt.Sprintf("e%d", i), Role: "button", Name: fmt.Sprintf("b%d", i)})
	}
	many = append(many, A11yNode{Ref: "e999", Role: "link", Name: "Ristretto", Href: "https://w.org/Ristretto"})
	els, trunc = buildActionSpace(many, "https://w.org/", true, []string{"ristretto"})
	if len(els) != goalMaxElements || !trunc {
		t.Errorf("cap: %d %v", len(els), trunc)
	}
	if last := els[len(els)-1]; last.ID != "e999" {
		t.Errorf("relevant element beyond the cap must survive, in document order: last is %s", last.ID)
	}
}

func TestGoalStateNeverNull(t *testing.T) {
	br := &fakeBrowser{url: "/empty", pages: map[string]*fakePage{"/empty": {title: "Empty"}}}
	d := &scripted{steps: []map[string]jevAnswer{ans("done", nil, 0, 0)}}
	runGoal(t, br, d, goalRequest{Task: "t"})
	data, _ := json.Marshal(d.seen[0])
	if strings.Contains(string(data), "null") {
		t.Errorf("state must use [] not null: %s", data)
	}
}

func TestBuildCriteriaControls(t *testing.T) {
	c := buildCriteria(nil)
	for _, k := range []string{"scroll_down", "scroll_up", "back", "done"} {
		if c[k] == "" {
			t.Errorf("missing control %s", k)
		}
	}
}

// ── Loop with the local decider ───────────────────────────

func TestGoalLocalLinkHops(t *testing.T) {
	br := wiki()
	res := runGoal(t, br, newLocalDecider("Go from Coffee to Espresso, then to the Ristretto article"), goalRequest{
		Task: "Go from Coffee to Espresso, then to the Ristretto article", StartURL: "/Coffee"})
	if res.Status != "done" || res.FinalURL != "/Ristretto" {
		t.Fatalf("status %s at %s: %+v", res.Status, res.FinalURL, res.Steps)
	}
	if len(res.Steps) != 3 || res.Steps[0].Executed != "click_e4" || res.Steps[1].Executed != "click_e1" {
		t.Errorf("steps %+v", res.Steps)
	}
	if res.Steps[0].Outcome != "navigated to /Espresso" {
		t.Errorf("outcome %q", res.Steps[0].Outcome)
	}
	// one observation per step plus the start, not two per step
	if br.observe > len(res.Steps)+1 {
		t.Errorf("observed %d times for %d steps", br.observe, len(res.Steps))
	}
}

func TestGoalLocalSearch(t *testing.T) {
	br := wiki()
	br.pages["/Coffee"].nodes = br.pages["/Coffee"].nodes[:1] // only the search box
	res := runGoal(t, br, newLocalDecider("Search Wikipedia for Ristretto"), goalRequest{Task: "Search Wikipedia for Ristretto", StartURL: "/Coffee"})
	if res.Status != "done" || br.typed != "ristretto" {
		t.Fatalf("status %s typed %q steps %+v", res.Status, br.typed, res.Steps)
	}
	if !strings.Contains(res.Steps[0].Detail, `searched "ristretto"`) {
		t.Errorf("detail %q", res.Steps[0].Detail)
	}

	br = wiki()
	br.unfocusable = true
	res = runGoal(t, br, newLocalDecider("Search Wikipedia for Ristretto"), goalRequest{Task: "Search Wikipedia for Ristretto", StartURL: "/Coffee"})
	if res.Status != "done" || res.Steps[0].Detail != `searched "ristretto" (form submit)` {
		t.Errorf("unfocusable search must fall back to form submit: %s %+v", res.Status, res.Steps)
	}
}

func TestGoalBudgets(t *testing.T) {
	br := wiki()
	res := runGoal(t, br, newLocalDecider("find the Mocha page"), goalRequest{Task: "find the Mocha page", StartURL: "/Espresso", MaxSteps: 2})
	if res.Status != "max_steps" || len(res.Steps) != 2 {
		t.Errorf("status %s, %d steps", res.Status, len(res.Steps))
	}
	res = runGoal(t, wiki(), newLocalDecider("x"), goalRequest{Task: "x", StartURL: "/nowhere"})
	if res.Status != "error" || !strings.Contains(res.Error, "404") {
		t.Errorf("bad start url: %+v", res)
	}
}

// ── Scripted decider: gates, recovery, typing rules ───────

type scripted struct {
	steps []map[string]jevAnswer
	i     int
	seen  []goalState
}

func (s *scripted) Name() string { return "scripted" }
func (s *scripted) Decide(_ context.Context, st any, qs map[string]jevQuestion) (map[string]jevAnswer, jevUsage, error) {
	if q, ok := qs["option"]; ok {
		return map[string]jevAnswer{"option": oneHot(q, "o1")}, jevUsage{InputTokens: 10}, nil
	}
	s.seen = append(s.seen, st.(goalState))
	a := s.steps[min(s.i, len(s.steps)-1)]
	s.i++
	if err := validateAnswers(qs, a); err != nil {
		return nil, jevUsage{}, err
	}
	return a, jevUsage{InputTokens: 1000, OutputTokens: 5}, nil
}

func ans(choice string, probs map[string]float64, goal, stuck float64) map[string]jevAnswer {
	return map[string]jevAnswer{
		"action":    {Type: "choice", Choice: choice, Probabilities: probs},
		"goal_done": {Type: "noul", Noul: &goal},
		"stuck":     {Type: "noul", Noul: &stuck},
	}
}

func TestGoalGatesBeforeActing(t *testing.T) {
	br := wiki()
	d := &scripted{steps: []map[string]jevAnswer{ans("click_e4", nil, 0.9, 0)}}
	res := runGoal(t, br, d, goalRequest{Task: "t", StartURL: "/Coffee"})
	if res.Status != "goal_achieved" || len(br.actions) != 0 {
		t.Errorf("goal gate must stop before acting: %s %v", res.Status, br.actions)
	}
	if res.Usage.Calls != 1 || res.Usage.InputTokens != 1000 || res.Usage.EstCostUSD <= 0 {
		t.Errorf("usage %+v", res.Usage)
	}

	// stuck only counts after step 2
	br = wiki()
	d = &scripted{steps: []map[string]jevAnswer{ans("click_e6", nil, 0, 0.99)}}
	res = runGoal(t, br, d, goalRequest{Task: "t", StartURL: "/Coffee"})
	if res.Status != "stuck" || len(res.Steps) != 3 {
		t.Errorf("stuck: %s after %d steps", res.Status, len(res.Steps))
	}
}

func TestGoalRepeatRecovery(t *testing.T) {
	br := wiki()
	p := map[string]float64{"click_e6": 0.7, "click_e4": 0.2, "back": 0.1}
	d := &scripted{steps: []map[string]jevAnswer{
		ans("click_e6", p, 0, 0), // dead button: no effect
		ans("click_e6", p, 0, 0), // same again → switch to next best (not back)
		ans("done", nil, 0, 0),
	}}
	res := runGoal(t, br, d, goalRequest{Task: "t", StartURL: "/Coffee"})
	if res.Steps[0].Outcome != "no visible change" {
		t.Fatalf("first outcome %q", res.Steps[0].Outcome)
	}
	if res.Steps[1].Executed != "click_e4" || res.Steps[1].Recovery == "" {
		t.Errorf("recovery: %+v", res.Steps[1])
	}
	if res.Status != "done" || res.FinalURL != "/Espresso" {
		t.Errorf("status %s at %s", res.Status, res.FinalURL)
	}
	// history reaches the decider with outcomes
	if h := d.seen[2].History; len(h) != 2 || !strings.Contains(h[1].Outcome, "navigated") {
		t.Errorf("history %+v", h)
	}
}

func TestGoalTypingNeedsInputs(t *testing.T) {
	br := &fakeBrowser{url: "/f", pages: map[string]*fakePage{"/f": {title: "Form",
		nodes: []A11yNode{{Ref: "e1", Role: "textbox", Name: "Email address"}, {Ref: "e2", Role: "combobox", Name: "Plan", Options: []string{"Free", "Pro"}}}}}}
	d := &scripted{steps: []map[string]jevAnswer{ans("type_e1", nil, 0, 0), ans("done", nil, 0, 0)}}
	res := runGoal(t, br, d, goalRequest{Task: "sign up"})
	if res.Steps[0].Error == "" || len(br.actions) != 0 {
		t.Errorf("must not type a guess: %+v %v", res.Steps[0], br.actions)
	}
	br.actions = nil
	d = &scripted{steps: []map[string]jevAnswer{ans("type_e1", nil, 0, 0), ans("select_e2", nil, 0, 0), ans("done", nil, 0, 0)}}
	res = runGoal(t, br, d, goalRequest{Task: "sign up", Inputs: map[string]string{"email": "a@b.c"}})
	if br.typed != "a@b.c" || res.Steps[1].Detail != `selected "Pro"` {
		t.Errorf("typed %q, select %q", br.typed, res.Steps[1].Detail)
	}
	if res.Usage.Calls != 4 { // three steps + one dropdown question
		t.Errorf("calls %d", res.Usage.Calls)
	}
}

func TestGoalStopsOnBotWall(t *testing.T) {
	br := &fakeBrowser{url: "/cf", pages: map[string]*fakePage{"/cf": {title: "Just a moment...", text: "Performance & security by Cloudflare"}}}
	res := runGoal(t, br, &scripted{steps: []map[string]jevAnswer{ans("done", nil, 0, 0)}}, goalRequest{Task: "t"})
	if res.Status != "blocked" || res.BotProtection == nil || res.Usage.Calls != 0 {
		t.Errorf("bot wall: %+v", res)
	}
}

// ── Jev wire client against a fake TypeSafe server ────────

func TestJevClientWire(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503) // retried
			return
		}
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(401)
			return
		}
		var body struct {
			Model     string                 `json:"model"`
			Questions map[string]jevQuestion `json:"questions"`
			State     map[string]any         `json:"state"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "jev-latest" || body.State["task"] != "find it" || body.Questions["action"].Type != "choice" {
			w.WriteHeader(400)
			return
		}
		g, s := 0.1, 0.0
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": map[string]jevAnswer{
				"action":    {Type: "choice", Choice: "done", Probabilities: map[string]float64{"done": 0.6, "back": 0.4}},
				"goal_done": {Type: "noul", Noul: &g},
				"stuck":     {Type: "noul", Noul: &s},
			},
			"usage": map[string]int{"input_tokens": 812, "output_tokens": 3},
			"model": "jev-1.12",
		})
	}))
	defer srv.Close()
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	t.Setenv("TYPESAFE_BASE_URL", srv.URL)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("JEV_PROVIDER", "")
	c, err := newJevClient("")
	if err != nil {
		t.Fatal(err)
	}
	qs := stepQuestions(map[string]string{"done": "stop", "back": "go back"})
	a, u, err := c.Decide(context.Background(), map[string]any{"task": "find it"}, qs)
	if err != nil {
		t.Fatal(err)
	}
	if a["action"].Choice != "done" || u.InputTokens != 812 || calls.Load() != 2 {
		t.Errorf("answers %+v usage %+v calls %d", a, u, calls.Load())
	}
}

func TestJevClientVercel(t *testing.T) {
	var gotBool, gotHeaders bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Get("ai-model-id") == "typesafe-ai/jev" && r.Header.Get("ai-evaluation-model-specification-version") == "4" &&
			r.Header.Get("Authorization") == "Bearer vck_test"
		var body struct {
			Questions map[string]jevQuestion `json:"questions"`
			Model     *string                `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotBool = body.Questions["goal_done"].Type == "boolean" && body.Model == nil
		if r.URL.Query().Get("fail") != "" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":{"message":"Free tier users do not have access to this model."}}`))
			return
		}
		_, _ = w.Write([]byte(`{"answers":{
			"action":{"type":"choice","choice":"done","probabilities":{"done":0.7,"back":0.3}},
			"goal_done":{"type":"boolean","probability":0.9},
			"stuck":{"type":"boolean","probability":0.05}},
			"usage":{"inputTokens":640,"outputTokens":0},
			"providerMetadata":{"typesafe":{"confidence":{"action":0.8}},"gateway":{"cost":"0.000031"}}}`))
	}))
	defer srv.Close()
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("JEV_PROVIDER", "")
	t.Setenv("AI_GATEWAY_API_KEY", "vck_test")
	t.Setenv("AI_GATEWAY_EVALUATION_URL", srv.URL)
	c, err := newJevClient("")
	if err != nil || c.provider != "vercel" || c.model != "typesafe-ai/jev" {
		t.Fatalf("client %+v %v", c, err)
	}
	a, u, err := c.Decide(context.Background(), map[string]any{"task": "x"}, stepQuestions(map[string]string{"done": "stop", "back": "back"}))
	if err != nil {
		t.Fatal(err)
	}
	if !gotHeaders || !gotBool {
		t.Errorf("wire format: headers %v boolean questions %v", gotHeaders, gotBool)
	}
	if a["action"].Choice != "done" || *a["action"].Confidence != 0.8 || *a["goal_done"].Noul != 0.9 || a["goal_done"].Type != "noul" || u.InputTokens != 640 {
		t.Errorf("answers %+v usage %+v", a, u)
	}
	if u.CostUSD != 0.000031 {
		t.Errorf("billed cost %v", u.CostUSD)
	}
	var gu goalUsage
	gu.add(u)
	gu.add(jevUsage{InputTokens: 1e6})
	if d := gu.EstCostUSD - (0.000031 + jevPricePerMTokIn); d > 1e-12 || d < -1e-12 {
		t.Errorf("billed + estimated cost %v", gu.EstCostUSD)
	}
	c.url = srv.URL + "?fail=1"
	if _, _, err := c.Decide(context.Background(), map[string]any{}, stepQuestions(map[string]string{"done": "stop"})); err == nil || !strings.Contains(err.Error(), "Free tier") {
		t.Errorf("gateway error message should be surfaced: %v", err)
	}
}

func TestJevClientConfig(t *testing.T) {
	t.Setenv("AI_GATEWAY_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("JEV_PROVIDER", "")
	if _, err := newJevClient(""); err == nil || !strings.Contains(err.Error(), "decider=local") {
		t.Errorf("missing key: %v", err)
	}
	t.Setenv("OPENROUTER_API_KEY", "sk-or-x")
	c, err := newJevClient("jev-1.12")
	if err != nil || c.provider != "openrouter" || c.model != "typesafe/jev-1.12" {
		t.Errorf("openrouter: %+v %v", c, err)
	}
	if openrouterJevModel("jev-latest") != "~typesafe/jev-latest" {
		t.Error("latest alias")
	}
}

func TestValidateAnswers(t *testing.T) {
	qs := stepQuestions(map[string]string{"a": "A", "b": "B"})
	half, bad := 0.5, 1.5
	ok := map[string]jevAnswer{
		"action":    {Type: "choice", Choice: "a", Probabilities: map[string]float64{"a": 0.6, "b": 0.4}},
		"goal_done": {Type: "noul", Noul: &half}, "stuck": {Type: "noul", Noul: &half},
	}
	if err := validateAnswers(qs, ok); err != nil {
		t.Error(err)
	}
	cases := map[string]func(m map[string]jevAnswer){
		"unknown choice": func(m map[string]jevAnswer) { a := m["action"]; a.Choice = "z"; m["action"] = a },
		"sum": func(m map[string]jevAnswer) {
			a := m["action"]
			a.Probabilities = map[string]float64{"a": 0.9, "b": 0.9}
			m["action"] = a
		},
		"noul range": func(m map[string]jevAnswer) { m["stuck"] = jevAnswer{Type: "noul", Noul: &bad} },
		"missing":    func(m map[string]jevAnswer) { delete(m, "goal_done") },
		"type":       func(m map[string]jevAnswer) { m["stuck"] = jevAnswer{Type: "choice", Choice: "a"} },
	}
	for name, mut := range cases {
		m := map[string]jevAnswer{}
		for k, v := range ok {
			m[k] = v
		}
		mut(m)
		if validateAnswers(qs, m) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// ── Ported helpers ────────────────────────────────────────

func TestBotProtection(t *testing.T) {
	if bp := detectBotProtection("Just a moment...", "", ""); bp == nil || !bp.FromPage || bp.Kind != "challenge" {
		t.Errorf("cf title: %+v", bp)
	}
	if bp := detectBotProtection("Attention Required! | Cloudflare", "", ""); bp == nil || bp.Kind != "block" {
		t.Errorf("block title: %+v", bp)
	}
	if bp := detectBotProtection("Verify you are human", "", ""); bp != nil {
		t.Errorf("generic title alone must not count: %+v", bp)
	}
	if bp := detectBotProtection("Verify you are human", "Ray ID: 123", ""); bp == nil || !bp.FromPage {
		t.Errorf("generic title + brand: %+v", bp)
	}
	article := "An article quoting: verify you are human. This process is automatic."
	if bp := detectBotProtection("Bots on the web", article, ""); bp != nil {
		t.Errorf("quotes without brand must not count: %+v", bp)
	}
	if bp := detectBotProtection("Shop", "", "challenge"); bp == nil || bp.FromPage {
		t.Errorf("header alone annotates but never stops: %+v", bp)
	}
	body := "Sorry, you have been blocked. Ray ID: 1. Performance & security by Cloudflare"
	if bp := detectBotProtection("x", body, ""); bp == nil || bp.Kind != "block" || len(bp.Evidence) != 3 {
		t.Errorf("body block (substring counted once): %+v", bp)
	}
}

func TestNoiseAndQuery(t *testing.T) {
	for _, n := range []string{"", "Edit", "[12]", "1.2", strings.Repeat("x", 81)} {
		if !isNoiseName(n) {
			t.Errorf("%q should be noise", n)
		}
	}
	if isNoiseName("Espresso") || isNoiseHref("https://x/y") || !isNoiseHref("javascript:void(0)") || !isNoiseHref("/w?action=edit") {
		t.Error("noise filters")
	}
	if q := heuristicQuery("Search Wikipedia for the Ristretto article and stop"); q != "ristretto" {
		t.Errorf("query %q", q)
	}
	if a := pickAlternate(map[string]float64{"back": 0.5, "x": 0.3, "y": 0.2, "done": 0.9}, map[string]bool{"x": true}); a != "y" {
		t.Errorf("alternate %q", a)
	}
}

func TestHandleGoalValidation(t *testing.T) {
	b := newTestBridge()
	h := b.handleGoal(http.NewServeMux())
	for body, code := range map[string]int{
		`{`:                            400,
		`{"task":""}`:                  400,
		`{"task":"x","decider":"gpt"}`: 400,
		`{"task":"x","decider":"local","tabId":"nope"}`: 404,
	} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("POST", "/goal", strings.NewReader(body)))
		if w.Code != code {
			t.Errorf("%s → %d, want %d (%s)", body, w.Code, code, w.Body.String())
		}
	}
}
