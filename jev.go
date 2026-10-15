package main

// Jev decision model client, step questions, action-space filters and the
// Cloudflare bot-protection detector, ported from jev-browser
// (https://github.com/jkudish/jev-browser, MIT, © Joey Kudish) and its
// @jkudish/jev-agent-tools transports. See THIRD_PARTY_LICENSES.md.
//
// Jev is a small decision model: instead of generating text it answers typed
// questions about a JSON state — "choice" (a probability over named options)
// and "noul" (a probability that a statement is true). One call per step
// answers "which action?", "is the goal reached?" and "are we stuck?" in a
// few hundred milliseconds for a fraction of a cent.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ── Questions and answers (TypeSafe wire format) ───────────

type jevQuestion struct {
	Type         string            `json:"type"` // "choice" or "noul"
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type jevAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
}

type jevUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// decider answers a set of questions about a state. The Jev HTTP client is
// the real one; tests and the offline "local" mode plug in their own.
type decider interface {
	Name() string
	Decide(ctx context.Context, state any, questions map[string]jevQuestion) (map[string]jevAnswer, jevUsage, error)
}

func choiceQ(instructions string, criteria map[string]string) jevQuestion {
	return jevQuestion{Type: "choice", Instructions: instructions, Criteria: criteria}
}

func noulQ(instructions, yes, no string) jevQuestion {
	return jevQuestion{Type: "noul", Instructions: instructions, Criteria: map[string]string{"true": yes, "false": no}}
}

// stepQuestions are asked together every step. They are independent: the
// model answers each without seeing the others, which keeps goal_done an
// honest watcher instead of an echo of the chosen action.
func stepQuestions(criteria map[string]string) map[string]jevQuestion {
	return map[string]jevQuestion{
		"action": choiceQ("Which single action best advances the task on the current page?", criteria),
		"goal_done": noulQ("The task's goal has been achieved: the current page and history show the sought outcome",
			"The page being viewed is the sought destination or shows the sought information",
			"The goal is not yet achieved"),
		"stuck": noulQ("The actions so far are not making progress toward the task (repeats, loops, or no change)",
			"Recent actions repeat or nothing changes; a different strategy is needed",
			"Progress is visible or the first steps are still reasonable"),
	}
}

func selectOptionQuestion(element string, options []string) jevQuestion {
	criteria := make(map[string]string, len(options))
	for i, o := range options {
		criteria["o"+strconv.Itoa(i)] = truncateRunes(o, 80)
	}
	return choiceQ(fmt.Sprintf("Which option should be selected in the dropdown %q, given the task and the page?", element), criteria)
}

// validateAnswers rejects malformed model output instead of acting on it:
// every question answered with its own type, choices among the criteria,
// probabilities over exactly the criteria and summing to ~1.
func validateAnswers(questions map[string]jevQuestion, answers map[string]jevAnswer) error {
	for name, q := range questions {
		a, ok := answers[name]
		if !ok {
			return fmt.Errorf("answer %q missing", name)
		}
		if a.Type != q.Type {
			return fmt.Errorf("answer %q has type %q, want %q", name, a.Type, q.Type)
		}
		switch q.Type {
		case "noul":
			if a.Noul == nil || *a.Noul < 0 || *a.Noul > 1 || math.IsNaN(*a.Noul) {
				return fmt.Errorf("answer %q: noul must be in [0,1]", name)
			}
		case "choice":
			if _, ok := q.Criteria[a.Choice]; !ok {
				return fmt.Errorf("answer %q: choice %q is not an option", name, a.Choice)
			}
			if len(a.Probabilities) > 0 {
				sum := 0.0
				for k, p := range a.Probabilities {
					if _, ok := q.Criteria[k]; !ok || p < 0 || p > 1 {
						return fmt.Errorf("answer %q: bad probability for %q", name, k)
					}
					sum += p
				}
				if math.Abs(sum-1) > 0.02 {
					return fmt.Errorf("answer %q: probabilities sum to %.3f", name, sum)
				}
			}
		}
	}
	return nil
}

// ── HTTP client (TypeSafe / OpenRouter) ────────────────────

type jevClient struct {
	provider string // "typesafe" or "openrouter"
	url      string
	key      string
	model    string
	http     *http.Client
}

// newJevClient picks the provider from the environment: TYPESAFE_API_KEY
// (TYPESAFE_BASE_URL overrides the endpoint) or OPENROUTER_API_KEY.
// JEV_PROVIDER forces one when both are set; JEV_MODEL picks the snapshot.
func newJevClient(model string) (*jevClient, error) {
	if model == "" {
		model = envOr("JEV_MODEL", "jev-latest")
	}
	c := &jevClient{model: model, http: &http.Client{Timeout: 30 * time.Second}}
	ts, or := os.Getenv("TYPESAFE_API_KEY"), os.Getenv("OPENROUTER_API_KEY")
	provider := os.Getenv("JEV_PROVIDER")
	if provider == "" {
		switch {
		case ts != "":
			provider = "typesafe"
		case strings.HasPrefix(or, "sk-or-"):
			provider = "openrouter"
		default:
			return nil, fmt.Errorf("no Jev key: set TYPESAFE_API_KEY (typesafe.ai) or OPENROUTER_API_KEY, or use decider=local")
		}
	}
	switch provider {
	case "typesafe":
		if ts == "" {
			return nil, fmt.Errorf("TYPESAFE_API_KEY is not set")
		}
		c.provider, c.key = provider, ts
		c.url = strings.TrimRight(envOr("TYPESAFE_BASE_URL", "https://api.typesafe.ai"), "/") + "/v1/systemone"
	case "openrouter":
		if !strings.HasPrefix(or, "sk-or-") {
			return nil, fmt.Errorf("OPENROUTER_API_KEY is not set or not an sk-or- key")
		}
		c.provider, c.key = provider, or
		c.url = envOr("OPENROUTER_DECISIONS_URL", "https://openrouter.ai/api/alpha/decisions")
		c.model = openrouterJevModel(c.model)
	default:
		return nil, fmt.Errorf("JEV_PROVIDER must be typesafe or openrouter")
	}
	return c, nil
}

func openrouterJevModel(m string) string {
	switch {
	case m == "jev-latest" || m == "typesafe/jev-latest" || m == "~typesafe/jev-latest":
		return "~typesafe/jev-latest"
	case strings.HasPrefix(m, "typesafe/"):
		return m
	}
	return "typesafe/" + m
}

func (c *jevClient) Name() string { return c.provider + ":" + c.model }

func (c *jevClient) Decide(ctx context.Context, state any, questions map[string]jevQuestion) (map[string]jevAnswer, jevUsage, error) {
	body, _ := json.Marshal(map[string]any{"state": state, "questions": questions, "model": c.model})
	var reply struct {
		Answers map[string]jevAnswer `json:"answers"`
		Usage   *jevUsage            `json:"usage"`
		Model   string               `json:"model"`
	}
	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
		if err != nil {
			return nil, jevUsage{}, err
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if c.provider == "openrouter" {
			req.Header.Set("HTTP-Referer", "https://github.com/ChrisFeldmeier/seebro")
			req.Header.Set("X-Title", "seebro")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, jevUsage{}, ctx.Err()
			}
			return nil, jevUsage{}, fmt.Errorf("%s request failed", c.provider) // never echo the key-bearing request
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if retryableStatus(resp.StatusCode) && attempt < maxAttempts {
			select {
			case <-time.After(backoff(resp.Header.Get("Retry-After"), attempt)):
				continue
			case <-ctx.Done():
				return nil, jevUsage{}, ctx.Err()
			}
		}
		if resp.StatusCode >= 300 {
			return nil, jevUsage{}, fmt.Errorf("%s HTTP %d (response omitted)", c.provider, resp.StatusCode)
		}
		if err := json.Unmarshal(data, &reply); err != nil {
			return nil, jevUsage{}, fmt.Errorf("%s: invalid JSON (%d bytes)", c.provider, len(data))
		}
		break
	}
	if err := validateAnswers(questions, reply.Answers); err != nil {
		return nil, jevUsage{}, fmt.Errorf("invalid Jev answer: %w", err)
	}
	var u jevUsage
	if reply.Usage != nil {
		u = *reply.Usage
	}
	return reply.Answers, u, nil
}

func retryableStatus(s int) bool { return s == 408 || s == 409 || s == 429 || s >= 500 }

// backoff honours Retry-After (seconds or HTTP date) and otherwise uses a
// small jittered exponential delay, never more than 5s.
func backoff(retryAfter string, attempt int) time.Duration {
	d := time.Duration(250*(1<<(attempt-1))+rand.IntN(100)) * time.Millisecond
	if s, err := strconv.ParseFloat(strings.TrimSpace(retryAfter), 64); err == nil {
		d = time.Duration(s * float64(time.Second))
	} else if t, err := http.ParseTime(retryAfter); err == nil {
		d = time.Until(t)
	}
	return min(max(d, 0), 5*time.Second)
}

// jevPricePerMTokIn is jev-1.12's published input price (output is free).
const jevPricePerMTokIn = 0.042

// ── Action-space filters ───────────────────────────────────

var junkNames = map[string]bool{
	"jump up": true, "jump up to": true, "jump up to:": true, "jump to content": true, "edit": true, "permalink": true,
	"permanent link": true, "cite this page": true, "donate": true, "create account": true, "log in": true, "talk": true,
	"contributions": true, "view history": true, "read": true, "source": true, "hide": true, "show": true, "skip to content": true,
}

var citationRe = regexp.MustCompile(`^[\d\s.,:;()\[\]-]+$`)

// isNoiseName drops elements an agent should never spend a step on:
// empty or essay-length names, site chrome, citation markers.
func isNoiseName(name string) bool {
	l := strings.ToLower(strings.TrimSpace(name))
	return l == "" || len([]rune(l)) > 80 || junkNames[l] || citationRe.MatchString(l)
}

func isNoiseHref(href string) bool {
	if href == "" {
		return false // buttons and inputs legitimately have no href
	}
	for _, p := range []string{"#", "javascript:", "mailto:", "tel:"} {
		if strings.HasPrefix(href, p) {
			return true
		}
	}
	return strings.Contains(href, "action=edit")
}

// pickAlternate returns the most probable option that is neither excluded
// nor a judgment the recovery should not make for the agent (back, done).
func pickAlternate(probs map[string]float64, exclude map[string]bool) string {
	best, bestP := "", 0.0
	for o, p := range probs {
		if o == "back" || o == "done" || exclude[o] || p <= 0 {
			continue
		}
		if p > bestP || (p == bestP && o < best) {
			best, bestP = o, p
		}
	}
	return best
}

var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields("search wikipedia the a an article about for find stop when you are on it and to of called page site website navigate go open that this then from") {
		stopWords[w] = true
	}
}

var nonWordRe = regexp.MustCompile(`[^\p{L}\p{N}\s-]+`)

// taskKeywords are the content words of a task, in order.
func taskKeywords(task string) []string {
	var out []string
	for _, w := range strings.Fields(nonWordRe.ReplaceAllString(strings.ToLower(task), " ")) {
		if !stopWords[w] && w != "-" {
			out = append(out, w)
		}
	}
	return out
}

// heuristicQuery is the deterministic search text when the caller gave none.
func heuristicQuery(task string) string {
	w := taskKeywords(task)
	return strings.Join(w[:min(len(w), 6)], " ")
}

// ── Bot-protection detection ───────────────────────────────

type botProtection struct {
	Provider string   `json:"provider"`
	Kind     string   `json:"kind"` // challenge or block
	Evidence []string `json:"evidence"`
	Guidance string   `json:"guidance"`
	FromPage bool     `json:"from_page"`
}

var botTitles = []struct {
	text           string
	block, generic bool
}{
	{text: "just a moment..."},
	{text: "attention required! | cloudflare", block: true},
	{text: "please wait... | cloudflare"},
	{text: "verify you are human", generic: true},
	{text: "verifying you are human", generic: true},
	{text: "checking your browser before accessing", generic: true},
}

// Longest first within overlapping pairs so one visual phrase counts once.
var botBodyMarkers = []struct {
	text         string
	block, brand bool
}{
	{text: "performing security verification"},
	{text: "verifying you are human"},
	{text: "verify you are human"},
	{text: "checking if the site connection is secure"},
	{text: "needs to review the security of your connection"},
	{text: "uses a security service to protect against malicious bots"},
	{text: "enable javascript and cookies to continue"},
	{text: "this process is automatic"},
	{text: "ray id:", brand: true},
	{text: "performance and security by cloudflare", brand: true},
	{text: "performance & security by cloudflare", brand: true},
	{text: "error 1020", block: true, brand: true},
	{text: "sorry, you have been blocked", block: true},
	{text: "you have been blocked", block: true},
}

const (
	botGuidanceChallenge = "Cloudflare served a bot challenge this client cannot pass. Clearance cookies are bound to the browser and IP that earned them, so seeded cookies do not help. Run the task in a headed Seebro profile where you passed the challenge yourself, or use the site's API."
	botGuidanceBlock     = "Cloudflare or the site blocked this client outright; cookies cannot clear it. Retry from a different network, or use the site's API."
)

// detectBotProtection decides from the title, a visible-text excerpt and the
// cf-mitigated response header whether the page is a CDN interstitial. Only
// page evidence (a decisive title, or three body markers including a
// Cloudflare-brand phrase) sets FromPage, which is what may stop a run: a
// header alone can belong to a challenge that already auto-passed.
func detectBotProtection(title, excerpt, cfMitigated string) *botProtection {
	title, excerpt = strings.ToLower(strings.TrimSpace(title)), strings.ToLower(excerpt)
	header := strings.ToLower(strings.TrimSpace(cfMitigated))
	headerCf := header == "challenge" || header == "blocked"
	block := header == "blocked"

	titleHit := -1
	for i, t := range botTitles {
		if strings.Contains(title, t.text) {
			titleHit = i
			break
		}
	}
	points, brand, blockBody := 0, false, false
	var matched []string
	for _, m := range botBodyMarkers {
		if !strings.Contains(excerpt, m.text) {
			continue
		}
		dup := false
		for _, t := range matched {
			if strings.Contains(t, m.text) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		matched = append(matched, m.text)
		points++
		brand = brand || m.brand
		blockBody = blockBody || m.block
	}
	bodyDecisive := points >= 3 && brand
	titleDecisive := titleHit >= 0 && (!botTitles[titleHit].generic || brand)
	pageDecisive := titleDecisive || bodyDecisive
	if !pageDecisive && !headerCf {
		return nil
	}
	var ev []string
	if headerCf {
		ev = append(ev, "header cf-mitigated: "+header)
	}
	if titleDecisive {
		ev = append(ev, fmt.Sprintf("title %q", botTitles[titleHit].text))
	}
	for _, m := range matched {
		if len(ev) >= 4 {
			break
		}
		ev = append(ev, fmt.Sprintf("body %q", m))
	}
	block = block || (titleDecisive && botTitles[titleHit].block) || (bodyDecisive && blockBody)
	bp := &botProtection{Provider: "cloudflare", Kind: "challenge", Evidence: ev, Guidance: botGuidanceChallenge, FromPage: pageDecisive}
	if block {
		bp.Kind, bp.Guidance = "block", botGuidanceBlock
	}
	return bp
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
