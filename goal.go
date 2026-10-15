package main

// Goal-directed navigation: POST /goal runs a whole multi-step browsing task
// ("find the Ristretto article", "open the pricing page") in one call. Every
// step observes the page through Seebro's own snapshot engine, asks a decider
// which action to take and whether the goal is reached or the run is stuck,
// and executes the action with Seebro's actions. The loop design — one
// independent-question call per step, stop gates evaluated before acting,
// next-best recovery when an action has no effect, and an auditable trace
// with confidences — is ported from jev-browser (MIT, © Joey Kudish).
//
// Deciders: "jev" (TypeSafe or OpenRouter, key from the environment) or
// "local" (offline keyword matcher; no key, good for simple link/search hops).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"
)

const (
	goalMaxElements  = 240
	goalExcerptChars = 1500
	goalGateDone     = 0.85
	goalGateStuck    = 0.85
)

type goalRequest struct {
	TabID      string            `json:"tabId"`
	Task       string            `json:"task"`
	StartURL   string            `json:"startUrl"`
	Decider    string            `json:"decider"` // jev (default when a key is set) or local
	Model      string            `json:"model"`   // Jev snapshot, default jev-latest
	MaxSteps   int               `json:"maxSteps"`
	MaxSeconds float64           `json:"maxSeconds"`
	Query      string            `json:"query"`  // text for search boxes (default: task keywords)
	Inputs     map[string]string `json:"inputs"` // field label (substring) → text for type actions
	NoTyping   bool              `json:"noTyping"`
	Record     bool              `json:"record"`     // record the run as MP4
	RecordPath string            `json:"recordPath"` // output path for the recording
}

type goalStep struct {
	Step        int      `json:"step"`
	TMs         int64    `json:"t_ms"`
	URL         string   `json:"url"`
	Proposed    string   `json:"proposed_action"`
	Executed    string   `json:"executed_action,omitempty"`
	Description string   `json:"description,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
	TopProb     *float64 `json:"top_probability,omitempty"`
	GoalDone    float64  `json:"goal_done"`
	Stuck       float64  `json:"stuck"`
	Detail      string   `json:"detail,omitempty"`
	Outcome     string   `json:"outcome"`
	Recovery    string   `json:"recovery,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type goalUsage struct {
	Calls        int     `json:"decider_calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	EstCostUSD   float64 `json:"est_cost_usd"` // billed cost where reported, else input tokens × list price

	billed, estimated float64
}

type goalResult struct {
	Status        string         `json:"status"` // goal_achieved, done, stuck, max_steps, max_seconds, blocked, error
	Task          string         `json:"task"`
	Decider       string         `json:"decider"`
	FinalURL      string         `json:"final_url"`
	FinalTitle    string         `json:"final_title"`
	ElapsedMs     int64          `json:"elapsed_ms"`
	Steps         []goalStep     `json:"steps"`
	Usage         goalUsage      `json:"usage"`
	BotProtection *botProtection `json:"bot_protection,omitempty"`
	Error         string         `json:"error,omitempty"`
	Video         string         `json:"video,omitempty"`
}

// ── State sent to the decider ─────────────────────────────

type goalElement struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	kind        string   // click, type, submit, search, select
	ref         string   // Seebro ref
	name        string   // accessible name
	href        string   // absolute link target
	options     []string // select options
}

type goalHistory struct {
	Step    int    `json:"step"`
	Action  string `json:"action"`
	Outcome string `json:"outcome"`
}

type goalState struct {
	Task                string            `json:"task"`
	CurrentPage         map[string]string `json:"current_page"`
	PageTextExcerpt     string            `json:"page_text_excerpt"`
	InteractiveElements []goalElement     `json:"interactive_elements"`
	Truncated           bool              `json:"element_list_truncated"`
	NoElements          bool              `json:"no_interactive_elements"`
	History             []goalHistory     `json:"history"`
}

// ── Browser access ────────────────────────────────────────

type goalObservation struct {
	URL, Title, Text string
	Nodes            []A11yNode
}

// goalBrowser is what the loop needs from a tab. apiBrowser drives the real
// tab through Seebro's own HTTP handlers in-process; tests use a fake.
type goalBrowser interface {
	Navigate(ctx context.Context, url string) error
	Observe(ctx context.Context) (goalObservation, error)
	Act(ctx context.Context, a map[string]any) error
	Back(ctx context.Context) error
	Submit(ctx context.Context, ref string) (bool, error) // submit the element's form; false when it has none
	Arm(ctx context.Context)                              // before an action: start watching for a navigation
	Settle(ctx context.Context, prevURL string)           // after it: wait for the navigation or in-page update
}

type apiBrowser struct {
	api   http.Handler
	tabID string
	owner string
}

func (a *apiBrowser) call(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rdr = bytes.NewReader(data)
	} else {
		rdr = bytes.NewReader(nil)
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("tabId", a.tabID)
	req := httptest.NewRequest(method, path+"?"+q.Encode(), rdr).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	if a.owner != "" {
		req.Header.Set("X-Seebro-Owner", a.owner)
	}
	rec := httptest.NewRecorder()
	a.api.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(rec.Body.String())
		}
		return fmt.Errorf("%s %s: %s", method, path, e.Error)
	}
	if out != nil {
		return json.Unmarshal(rec.Body.Bytes(), out)
	}
	return nil
}

func (a *apiBrowser) withTab(m map[string]any) map[string]any {
	m["tabId"] = a.tabID
	return m
}

func (a *apiBrowser) Navigate(ctx context.Context, u string) error {
	return a.call(ctx, "POST", "/navigate", nil, a.withTab(map[string]any{"url": u}), nil)
}

func (a *apiBrowser) Observe(ctx context.Context) (goalObservation, error) {
	var snap struct {
		Nodes []A11yNode `json:"nodes"`
	}
	if err := a.call(ctx, "GET", "/snapshot", url.Values{"filter": {"interactive"}, "links": {"true"}}, nil, &snap); err != nil {
		return goalObservation{}, err
	}
	var txt struct {
		Text, URL, Title string
	}
	if err := a.call(ctx, "GET", "/text", url.Values{"maxChars": {fmt.Sprint(goalExcerptChars)}}, nil, &txt); err != nil {
		return goalObservation{}, err
	}
	return goalObservation{URL: txt.URL, Title: txt.Title, Text: txt.Text, Nodes: snap.Nodes}, nil
}

func (a *apiBrowser) Act(ctx context.Context, act map[string]any) error {
	return a.call(ctx, "POST", "/action", nil, a.withTab(act), nil)
}

func (a *apiBrowser) Back(ctx context.Context) error {
	return a.call(ctx, "POST", "/navigate", nil, a.withTab(map[string]any{"type": "back"}), nil)
}

func (a *apiBrowser) eval(ctx context.Context, expr string) (any, error) {
	var out struct {
		Result any `json:"result"`
	}
	err := a.call(ctx, "POST", "/evaluate", nil, a.withTab(map[string]any{"expression": expr}), &out)
	return out.Result, err
}

func (a *apiBrowser) Submit(ctx context.Context, ref string) (bool, error) {
	var out struct {
		Result json.RawMessage `json:"result"`
	}
	err := a.call(ctx, "POST", "/evaluate", nil, a.withTab(map[string]any{"ref": ref,
		"expression": "el => { const f = el.form || el.closest('form'); if (!f) return false; f.requestSubmit ? f.requestSubmit() : f.submit(); return true; }"}), &out)
	return string(out.Result) == "true", err
}

// Arm marks the document so Settle can tell "a navigation is starting"
// (beforeunload fired, the URL only changes once the new page commits) from
// "nothing happened".
func (a *apiBrowser) Arm(ctx context.Context) {
	_, _ = a.eval(ctx, "window.__seebroNav = 0; addEventListener('beforeunload', () => { window.__seebroNav = 1 }, {once: true}); 0")
}

// Settle waits until a navigation the action started has produced a usable
// document, or briefly for in-page updates when the URL stays the same.
func (a *apiBrowser) Settle(ctx context.Context, prevURL string) {
	deadline := time.Now().Add(10 * time.Second)
	quiet := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		r, err := a.eval(ctx, "location.href + ' ' + document.readyState + ' ' + (window.__seebroNav || 0)")
		if err == nil {
			f := strings.Fields(fmt.Sprint(r))
			if len(f) == 3 {
				href, rs, leaving := f[0], f[1], f[2] == "1"
				if href != prevURL && rs != "loading" {
					return
				}
				if href == prevURL && !leaving && time.Now().After(quiet) {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ── Action space ──────────────────────────────────────────

var clickRoles = map[string]bool{
	"link": true, "button": true, "menuitem": true, "menuitemcheckbox": true, "menuitemradio": true, "tab": true,
	"checkbox": true, "radio": true, "switch": true, "option": true, "treeitem": true, "summary": true,
}
var textRoles = map[string]bool{"textbox": true, "searchbox": true, "combobox": true, "spinbutton": true}

// buildActionSpace turns Seebro's interactive snapshot into named options:
// click_eN, type_eN (+ submit_eN = type and press Enter), search_eN (type
// the query and press Enter), select_eN. Noise and duplicate link targets
// are dropped, and the list is capped so one step stays cheap. Unlike a
// plain document-order cut, elements that mention a task keyword survive the
// cap first, so the link a long article holds below its first 240 elements
// is still on offer (document order is kept in the result).
func buildActionSpace(nodes []A11yNode, pageURL string, typing bool, keywords []string) ([]goalElement, bool) {
	base, _ := url.Parse(pageURL)
	seenHref := map[string]bool{}
	out := []goalElement{}
	truncated := false
	for _, n := range nodes {
		if n.Disabled || isNoiseName(n.Name) || isNoiseHref(n.Href) {
			continue
		}
		role := strings.ToLower(n.Role)
		desc := fmt.Sprintf("%s %q", role, n.Name)
		href := n.Href
		if href != "" {
			if seenHref[href] {
				continue
			}
			seenHref[href] = true
			desc += " -> " + shortHref(base, href)
		}
		var st []string
		if n.Checked {
			st = append(st, "checked")
		}
		if n.Expanded {
			st = append(st, "expanded")
		}
		if n.Value != "" && !textRoles[role] {
			st = append(st, "value "+truncateRunes(n.Value, 40))
		}
		if len(st) > 0 {
			desc += " [" + strings.Join(st, ", ") + "]"
		}
		el := goalElement{ID: n.Ref, ref: n.Ref, name: n.Name, href: href, Description: desc}
		var add []goalElement
		switch {
		case len(n.Options) > 0 && (role == "combobox" || role == "listbox"):
			el.kind, el.options = "select", n.Options
			el.Description = desc + " options: " + truncateRunes(strings.Join(n.Options, " | "), 160)
			add = append(add, el)
		case textRoles[role]:
			if !typing {
				continue
			}
			if role == "searchbox" || strings.Contains(strings.ToLower(n.Name), "search") || strings.Contains(strings.ToLower(n.Name), "such") {
				el.kind, el.Description = "search", "search with "+desc
				add = append(add, el)
				break
			}
			if n.Value != "" {
				desc += fmt.Sprintf(" (current text %q)", truncateRunes(n.Value, 40))
			}
			t, s := el, el
			t.kind, t.Description = "type", "type into "+desc
			s.kind, s.Description = "submit", "type into "+desc+" and press Enter"
			add = append(add, t, s)
		case clickRoles[role]:
			el.kind = "click"
			add = append(add, el)
		default:
			continue
		}
		out = append(out, add...)
	}
	if len(out) <= goalMaxElements {
		return out, false
	}
	truncated = true
	keep := make([]bool, len(out))
	n := 0
	relevant := func(e goalElement) bool {
		l := strings.ToLower(e.name + " " + e.href)
		for _, k := range keywords {
			if len(k) > 2 && strings.Contains(l, k) {
				return true
			}
		}
		return e.kind == "search"
	}
	for i, e := range out {
		if n < goalMaxElements && relevant(e) {
			keep[i], n = true, n+1
		}
	}
	for i := range out {
		if n < goalMaxElements && !keep[i] {
			keep[i], n = true, n+1
		}
	}
	capped := make([]goalElement, 0, goalMaxElements)
	for i, e := range out {
		if keep[i] {
			capped = append(capped, e)
		}
	}
	return capped, truncated
}

func shortHref(base *url.URL, href string) string {
	u, err := url.Parse(href)
	if err != nil || base == nil || u.Host != base.Host {
		return truncateRunes(href, 120)
	}
	s := u.EscapedPath()
	if u.RawQuery != "" {
		s += "?" + u.RawQuery
	}
	return truncateRunes(s, 120)
}

func optionKey(e goalElement) string { return e.kind + "_" + e.ID }

func buildCriteria(els []goalElement) map[string]string {
	c := make(map[string]string, len(els)+4)
	for _, e := range els {
		c[optionKey(e)] = e.Description
	}
	c["scroll_down"] = "Scroll down one screen to reveal more of the page"
	c["scroll_up"] = "Scroll up one screen"
	c["back"] = "Go back to the previous page; this branch is wrong"
	c["done"] = "The task is already complete; stop here"
	return c
}

// ── The loop ──────────────────────────────────────────────

type goalRunner struct {
	br  goalBrowser
	dec decider
	req goalRequest
	now func() time.Time
}

func pageFingerprint(o goalObservation) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s|%d|%s", o.URL, len(o.Nodes), o.Text)
	return h.Sum64()
}

func (g *goalRunner) run(ctx context.Context) goalResult {
	now := g.now
	if now == nil {
		now = time.Now
	}
	req := g.req
	start := now()
	res := goalResult{Task: req.Task, Decider: g.dec.Name(), Steps: []goalStep{}}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.MaxSeconds*float64(time.Second)))
	defer cancel()

	if req.StartURL != "" {
		if err := g.br.Navigate(ctx, req.StartURL); err != nil {
			res.Status, res.Error = "error", err.Error()
			return res
		}
	}
	history := []goalHistory{} // the wire format wants [], never null
	keywords := taskKeywords(req.Task)
	var lastExecuted string
	lastNoEffect := false
	var obs goalObservation
	var next *goalObservation // observation taken after the previous action

	for step := 1; ; step++ {
		if step > req.MaxSteps {
			res.Status = "max_steps"
			break
		}
		if ctx.Err() != nil {
			res.Status = "max_seconds"
			break
		}
		if next != nil {
			obs, next = *next, nil
		} else {
			var err error
			if obs, err = g.br.Observe(ctx); err != nil {
				res.Status, res.Error = errorStatus(ctx, err)
				break
			}
		}
		if bp := detectBotProtection(obs.Title, truncateRunes(obs.Text, 4000), ""); bp != nil && bp.FromPage {
			res.Status, res.BotProtection = "blocked", bp
			break
		}
		els, truncated := buildActionSpace(obs.Nodes, obs.URL, !req.NoTyping, keywords)
		state := goalState{
			Task:                req.Task,
			CurrentPage:         map[string]string{"url": obs.URL, "title": obs.Title},
			PageTextExcerpt:     truncateRunes(obs.Text, goalExcerptChars),
			InteractiveElements: els,
			Truncated:           truncated,
			NoElements:          len(els) == 0,
			History:             tail(history, 12),
		}
		answers, usage, err := g.dec.Decide(ctx, state, stepQuestions(buildCriteria(els)))
		if err != nil {
			res.Status, res.Error = errorStatus(ctx, err)
			break
		}
		res.Usage.add(usage)

		act := answers["action"]
		s := goalStep{Step: step, URL: obs.URL, Proposed: act.Choice, Confidence: act.Confidence,
			GoalDone: deref(answers["goal_done"].Noul), Stuck: deref(answers["stuck"].Noul)}
		if p, ok := act.Probabilities[act.Choice]; ok {
			s.TopProb = &p
		}
		// Stop gates run before acting: a watcher that fires on the current
		// page must not be overridden by acting on that page.
		if act.Choice == "done" {
			s.Outcome = "agent declared done"
			res.Steps = append(res.Steps, g.stamp(s, start))
			res.Status = "done"
			break
		}
		if s.GoalDone > goalGateDone {
			s.Outcome = "goal watcher fired; proposed action not executed"
			res.Steps = append(res.Steps, g.stamp(s, start))
			res.Status = "goal_achieved"
			break
		}
		if s.Stuck > goalGateStuck && step > 2 {
			s.Outcome = "stuck watcher fired; proposed action not executed"
			res.Steps = append(res.Steps, g.stamp(s, start))
			res.Status = "stuck"
			break
		}

		chosen := act.Choice
		if chosen == lastExecuted && lastNoEffect {
			if alt := pickAlternate(act.Probabilities, map[string]bool{chosen: true}); alt != "" {
				chosen = alt
				s.Recovery = "repeated action had no effect; switched to the next-best option"
			}
		}
		var el *goalElement
		for i := range els {
			if optionKey(els[i]) == chosen {
				el = &els[i]
				break
			}
		}
		s.Executed = chosen
		if el != nil {
			s.Description = el.Description
		}
		before := pageFingerprint(obs)
		g.br.Arm(ctx)
		detail, actErr := g.execute(ctx, chosen, el, &res.Usage, state)
		s.Detail = detail
		if actErr != nil {
			s.Error = actErr.Error()
			s.Outcome = "action failed"
		} else {
			g.br.Settle(ctx, obs.URL)
		}
		// This observation describes the outcome and is reused as the next
		// step's view of the page, so each step costs one snapshot.
		after, oerr := g.br.Observe(ctx)
		if oerr == nil {
			next = &after
		}
		noEffect := false
		if actErr == nil && oerr == nil {
			switch {
			case after.URL != obs.URL:
				s.Outcome = "navigated to " + after.URL
			case pageFingerprint(after) != before:
				s.Outcome = "page changed"
			default:
				s.Outcome, noEffect = "no visible change", true
			}
		}
		res.Steps = append(res.Steps, g.stamp(s, start))
		histAction := chosen
		if el != nil {
			histAction = el.Description
		}
		if detail != "" && detail != chosen {
			histAction += " (" + detail + ")"
		}
		history = append(history, goalHistory{Step: step, Action: truncateRunes(histAction, 200), Outcome: s.Outcome})
		lastExecuted, lastNoEffect = chosen, noEffect
	}

	if next != nil {
		obs = *next
	} else if o, err := g.br.Observe(context.WithoutCancel(ctx)); err == nil {
		obs = o
	}
	res.FinalURL, res.FinalTitle = obs.URL, obs.Title
	res.ElapsedMs = now().Sub(start).Milliseconds()
	return res
}

func (g *goalRunner) stamp(s goalStep, start time.Time) goalStep {
	now := g.now
	if now == nil {
		now = time.Now
	}
	s.TMs = now().Sub(start).Milliseconds()
	return s
}

func (g *goalRunner) execute(ctx context.Context, chosen string, el *goalElement, usage *goalUsage, state goalState) (string, error) {
	switch chosen {
	case "back":
		return "went back", g.br.Back(ctx)
	case "scroll_down", "scroll_up":
		dy := 600
		if chosen == "scroll_up" {
			dy = -600
		}
		return chosen, g.br.Act(ctx, map[string]any{"kind": "scroll", "scrollY": dy})
	}
	if el == nil {
		return "", fmt.Errorf("unknown action %s", chosen)
	}
	switch el.kind {
	case "click":
		return "clicked " + el.name, g.br.Act(ctx, map[string]any{"kind": "click", "ref": el.ref})
	case "search":
		q := g.req.Query
		if q == "" {
			q = heuristicQuery(g.req.Task)
		}
		// Real key events first: typeahead widgets (Wikipedia, shop search)
		// keep their own model and may ignore a programmatic value. A field a
		// collapsed header hides cannot take focus, so fall back to setting the
		// value and submitting its form, which reads the DOM value.
		_ = g.br.Act(ctx, map[string]any{"kind": "fill", "ref": el.ref, "text": ""})
		if err := g.br.Act(ctx, map[string]any{"kind": "type", "ref": el.ref, "text": q}); err == nil {
			return fmt.Sprintf("searched %q", q), g.br.Act(ctx, map[string]any{"kind": "press", "key": "Enter"})
		}
		if err := g.br.Act(ctx, map[string]any{"kind": "fill", "ref": el.ref, "text": q}); err != nil {
			return "", err
		}
		if ok, err := g.br.Submit(ctx, el.ref); err == nil && ok {
			return fmt.Sprintf("searched %q (form submit)", q), nil
		}
		return fmt.Sprintf("searched %q", q), g.br.Act(ctx, map[string]any{"kind": "press", "key": "Enter"})
	case "type", "submit":
		text, ok := g.inputFor(el.name)
		if !ok {
			// Never type a guess into an ordinary field.
			return "", fmt.Errorf("no input given for field %q (pass inputs: {%q: \"…\"})", el.name, el.name)
		}
		if err := g.br.Act(ctx, map[string]any{"kind": "fill", "ref": el.ref, "text": text}); err != nil {
			return "", err
		}
		if el.kind == "submit" {
			return fmt.Sprintf("typed into %q and pressed Enter", el.name), g.br.Act(ctx, map[string]any{"kind": "press", "key": "Enter"})
		}
		return fmt.Sprintf("typed into %q", el.name), nil
	case "select":
		opt, err := g.chooseOption(ctx, el, usage, state)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("selected %q", opt), g.br.Act(ctx, map[string]any{"kind": "select", "ref": el.ref, "value": opt})
	}
	return "", fmt.Errorf("unsupported element kind %s", el.kind)
}

func (g *goalRunner) inputFor(name string) (string, bool) {
	l := strings.ToLower(name)
	for k, v := range g.req.Inputs {
		if k == "*" || (k != "" && strings.Contains(l, strings.ToLower(k))) {
			return v, true
		}
	}
	return "", false
}

// chooseOption asks the decider a second, dropdown-specific question.
func (g *goalRunner) chooseOption(ctx context.Context, el *goalElement, usage *goalUsage, state goalState) (string, error) {
	if v, ok := g.inputFor(el.name); ok {
		return v, nil
	}
	if len(el.options) == 0 {
		return "", fmt.Errorf("dropdown has no options")
	}
	q := map[string]jevQuestion{"option": selectOptionQuestion(el.name, el.options)}
	ans, u, err := g.dec.Decide(ctx, state, q)
	if err != nil {
		return "", err
	}
	usage.add(u)
	var i int
	if _, err := fmt.Sscanf(ans["option"].Choice, "o%d", &i); err != nil || i < 0 || i >= len(el.options) {
		return "", fmt.Errorf("invalid option answer %q", ans["option"].Choice)
	}
	return el.options[i], nil
}

func (u *goalUsage) add(x jevUsage) {
	u.Calls++
	u.InputTokens += x.InputTokens
	u.OutputTokens += x.OutputTokens
	if x.CostUSD > 0 { // billed by the provider
		u.billed += x.CostUSD
	} else {
		u.estimated += float64(x.InputTokens) / 1e6 * jevPricePerMTokIn
	}
	u.EstCostUSD = u.billed + u.estimated
}

func errorStatus(ctx context.Context, err error) (string, string) {
	if ctx.Err() == context.DeadlineExceeded {
		return "max_seconds", ""
	}
	return "error", err.Error()
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func tail[T any](s []T, n int) []T {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// ── Offline decider ───────────────────────────────────────

// localDecider is a deterministic keyword matcher for simple hops without a
// model: it walks the task's content words in order ("from Coffee to
// Espresso, then Ristretto"), clicks the link named after the next unvisited
// one, falls back to a search box, then to scrolling. Good enough for
// link-to-link navigation and searches; anything needing judgment wants Jev.
type localDecider struct {
	keywords []string
	visited  []string // lowercased titles seen so far
}

func newLocalDecider(task string) *localDecider { return &localDecider{keywords: taskKeywords(task)} }

func (d *localDecider) Name() string { return "local" }

func (d *localDecider) Decide(_ context.Context, st any, qs map[string]jevQuestion) (map[string]jevAnswer, jevUsage, error) {
	state, ok := st.(goalState)
	if !ok {
		return nil, jevUsage{}, fmt.Errorf("local decider needs a goal state")
	}
	if q, ok := qs["option"]; ok { // dropdown: first option mentioning a keyword, else the first
		choice := "o0"
		for k, v := range q.Criteria {
			if d.mentions(strings.ToLower(v)) {
				choice = k
				break
			}
		}
		return map[string]jevAnswer{"option": oneHot(q, choice)}, jevUsage{}, nil
	}
	title := strings.ToLower(state.CurrentPage["title"])
	d.visited = append(d.visited, title)
	one, zero := 1.0, 0.0
	if len(d.keywords) == 0 {
		return nil, jevUsage{}, fmt.Errorf("task has no keywords for the local decider")
	}
	last := d.keywords[len(d.keywords)-1]
	if strings.Contains(title, last) {
		return map[string]jevAnswer{
			"action":    oneHot(qs["action"], "done"),
			"goal_done": {Type: "noul", Noul: &one},
			"stuck":     {Type: "noul", Noul: &zero},
		}, jevUsage{}, nil
	}
	next := last
	for _, k := range d.keywords {
		if !d.seen(k) {
			next = k
			break
		}
	}
	choice := ""
	best := 0
	for _, e := range state.InteractiveElements {
		if e.kind != "click" {
			continue
		}
		n := strings.ToLower(strings.TrimSpace(e.name))
		score := 0
		switch {
		case n == next:
			score = 3
		case strings.HasPrefix(n, next+" ") || strings.HasSuffix(n, " "+next):
			score = 2
		case strings.Contains(n, next):
			score = 1
		}
		if score > best {
			best, choice = score, optionKey(e)
		}
	}
	if choice == "" {
		for _, e := range state.InteractiveElements {
			if e.kind == "search" {
				choice = optionKey(e)
				break
			}
		}
	}
	if choice == "" {
		choice = "scroll_down"
	}
	stuck := 0.0
	if n := len(state.History); n >= 3 && state.History[n-1].Outcome == "no visible change" && state.History[n-2].Outcome == "no visible change" {
		stuck = 1
	}
	return map[string]jevAnswer{
		"action":    oneHot(qs["action"], choice),
		"goal_done": {Type: "noul", Noul: &zero},
		"stuck":     {Type: "noul", Noul: &stuck},
	}, jevUsage{}, nil
}

func (d *localDecider) seen(k string) bool {
	for _, t := range d.visited {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

func (d *localDecider) mentions(s string) bool {
	for _, k := range d.keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

func oneHot(q jevQuestion, choice string) jevAnswer {
	p := map[string]float64{}
	for k := range q.Criteria {
		p[k] = 0
	}
	p[choice] = 1
	c := 1.0
	return jevAnswer{Type: "choice", Choice: choice, Probabilities: p, Confidence: &c}
}

// ── HTTP handler ──────────────────────────────────────────

func newDecider(req goalRequest) (decider, error) {
	switch req.Decider {
	case "local":
		return newLocalDecider(req.Task), nil
	case "", "jev":
		return newJevClient(req.Model)
	}
	return nil, fmt.Errorf("decider must be jev or local")
}

// handleGoal returns POST /goal, dispatching to the given API in-process.
func (b *Bridge) handleGoal(api http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req goalRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
			jsonErr(w, 400, fmt.Errorf("decode: %w", err))
			return
		}
		req.Task = strings.TrimSpace(req.Task)
		if req.Task == "" {
			jsonErr(w, 400, fmt.Errorf("task required"))
			return
		}
		if req.MaxSteps <= 0 {
			req.MaxSteps = 24
		}
		req.MaxSteps = min(req.MaxSteps, 100)
		if req.MaxSeconds <= 0 {
			req.MaxSeconds = 180
		}
		req.MaxSeconds = min(req.MaxSeconds, 900)
		dec, err := newDecider(req)
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		_, tabID, err := b.TabContext(req.TabID)
		if err != nil {
			jsonErr(w, 404, err)
			return
		}
		br := &apiBrowser{api: api, tabID: tabID, owner: r.Header.Get("X-Seebro-Owner")}
		if req.Record {
			if err := br.call(r.Context(), "POST", "/screencast/start", nil, br.withTab(map[string]any{"path": req.RecordPath}), nil); err != nil {
				jsonErr(w, 409, err)
				return
			}
		}
		res := (&goalRunner{br: br, dec: dec, req: req}).run(r.Context())
		if req.Record {
			var out struct {
				Path  string `json:"path"`
				Error string `json:"error"`
			}
			if err := br.call(context.WithoutCancel(r.Context()), "POST", "/screencast/stop", nil, br.withTab(map[string]any{}), &out); err == nil {
				res.Video = out.Path
			}
		}
		jsonResp(w, 200, res)
	}
}
