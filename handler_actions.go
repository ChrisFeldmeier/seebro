package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"math/rand"

	"github.com/chromedp/chromedp"
)

// ── POST /action ───────────────────────────────────────────

// actionRequest is the parsed JSON body for /action.
type actionRequest struct {
	TabID    string `json:"tabId"`
	Kind     string `json:"kind"`
	Ref      string `json:"ref"`
	Selector string `json:"selector"`
	Text     string `json:"text"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	NodeID   int64  `json:"nodeId"`
	ScrollX  int    `json:"scrollX"`
	ScrollY  int    `json:"scrollY"`
	WaitNav  bool   `json:"waitNav"`
	Fast     bool   `json:"fast"`

	X          float64  `json:"x"` // clickAt / drag target
	Y          float64  `json:"y"`
	Count      int      `json:"count"`      // clickAt click count
	ToRef      string   `json:"toRef"`      // drag target
	ToSelector string   `json:"toSelector"` // drag target
	ToNodeID   int64    `json:"toNodeId"`
	Files      []string `json:"files"` // upload
}

// ActionFunc handles a single action kind.
type ActionFunc func(ctx context.Context, req actionRequest) (map[string]any, error)

// initActionRegistry builds the action registry once. Call from main() after
// Bridge is initialised; the result is stored in b.actions.
func (b *Bridge) initActionRegistry() {
	b.actions = map[string]ActionFunc{
		actionClick: func(ctx context.Context, req actionRequest) (map[string]any, error) {
			if engineFromCtx(ctx) == engineObscura {
				if handled, res, err := obscuraDownload(ctx, req.NodeID, req.Selector); handled || err != nil {
					return res, err
				}
			}
			var err error
			if req.Selector != "" {
				_, err = callOnTarget(ctx, 0, req.Selector, clickJS)
			} else if req.NodeID > 0 {
				err = clickByNodeID(ctx, req.NodeID)
			} else {
				return nil, fmt.Errorf("need selector, ref, or nodeId")
			}
			if err != nil {
				return nil, err
			}
			if req.WaitNav {
				_ = chromedp.Run(ctx, chromedp.Sleep(waitNavDelay))
			}
			return map[string]any{"clicked": true}, nil
		},
		actionType: func(ctx context.Context, req actionRequest) (map[string]any, error) {
			if req.Text == "" {
				return nil, fmt.Errorf("text required for type")
			}
			if req.Selector != "" {
				if _, err := callOnTarget(ctx, 0, req.Selector, focusJS); err != nil {
					return nil, err
				}
				return map[string]any{"typed": req.Text}, typeText(ctx, req.Text)
			}
			if req.NodeID > 0 {
				return map[string]any{"typed": req.Text}, typeByNodeID(ctx, req.NodeID, req.Text)
			}
			return nil, fmt.Errorf("need selector or ref")
		},
		actionFill:  fillAction,
		actionPress: pressAction,
		actionFocus: func(ctx context.Context, req actionRequest) (map[string]any, error) {
			if req.Selector != "" {
				_, err := callOnTarget(ctx, 0, req.Selector, focusJS)
				return map[string]any{"focused": true}, err
			}
			if req.NodeID > 0 {
				return map[string]any{"focused": true}, chromedp.Run(ctx,
					chromedp.ActionFunc(func(ctx context.Context) error {
						p := map[string]any{"backendNodeId": req.NodeID}
						return chromedp.FromContext(ctx).Target.Execute(ctx, "DOM.focus", p, nil)
					}),
				)
			}
			return map[string]any{"focused": true}, nil
		},
		actionHover: func(ctx context.Context, req actionRequest) (map[string]any, error) {
			if req.NodeID > 0 {
				return map[string]any{"hovered": true}, hoverByNodeID(ctx, req.NodeID)
			}
			if req.Selector != "" {
				return map[string]any{"hovered": true}, chromedp.Run(ctx,
					chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q)?.dispatchEvent(new MouseEvent('mouseover', {bubbles:true}))`, req.Selector), nil),
				)
			}
			return nil, fmt.Errorf("need selector or ref")
		},
		actionSelect: func(ctx context.Context, req actionRequest) (map[string]any, error) {
			val := req.Value
			if val == "" {
				val = req.Text
			}
			if val == "" {
				return nil, fmt.Errorf("value required for select")
			}
			if req.NodeID > 0 {
				return map[string]any{"selected": val}, selectByNodeID(ctx, req.NodeID, val)
			}
			if req.Selector != "" {
				_, err := callOnTarget(ctx, 0, req.Selector, selectJS, val)
				return map[string]any{"selected": val}, err
			}
			return nil, fmt.Errorf("need selector or ref")
		},
		actionScroll: func(ctx context.Context, req actionRequest) (map[string]any, error) {
			if req.NodeID > 0 {
				return map[string]any{"scrolled": true}, scrollByNodeID(ctx, req.NodeID)
			}
			if req.ScrollX != 0 || req.ScrollY != 0 {
				js := fmt.Sprintf("window.scrollBy(%d, %d)", req.ScrollX, req.ScrollY)
				return map[string]any{"scrolled": true, "x": req.ScrollX, "y": req.ScrollY},
					chromedp.Run(ctx, chromedp.Evaluate(js, nil))
			}
			return map[string]any{"scrolled": true, "y": 800},
				chromedp.Run(ctx, chromedp.Evaluate("window.scrollBy(0, 800)", nil))
		},
		actionHumanClick: func(ctx context.Context, req actionRequest) (map[string]any, error) {
			if req.NodeID == 0 && req.Selector == "" {
				return nil, fmt.Errorf("need selector, ref, or nodeId")
			}
			x, y, err := elementCenter(ctx, req.NodeID, req.Selector)
			if err != nil {
				return nil, err
			}
			// Humans don't hit the exact center.
			x += (rand.Float64() - 0.5) * 6
			y += (rand.Float64() - 0.5) * 6
			if err := humanClick(ctx, x, y); err != nil {
				return nil, err
			}
			return map[string]any{"clicked": true, "human": true}, nil
		},
		actionHumanType: func(ctx context.Context, req actionRequest) (map[string]any, error) {
			if req.Text == "" {
				return nil, fmt.Errorf("text required for humanType")
			}

			if req.NodeID == 0 && req.Selector == "" {
				return nil, fmt.Errorf("need selector, ref, or nodeId")
			}
			if _, err := callOnTarget(ctx, req.NodeID, req.Selector, focusJS); err != nil {
				return nil, err
			}

			actions := humanType(req.Text, req.Fast)
			if err := chromedp.Run(ctx, actions...); err != nil {
				return nil, err
			}

			return map[string]any{"typed": req.Text, "human": true}, nil
		},
	}
	b.registerExtraActions()
}

// resolveActionRefs turns ref/toRef into backend node IDs. When the element
// lives in a child frame, the returned context carries that frame's offset so
// coordinate-based input lands in the right place.
func (b *Bridge) resolveActionRefs(ctx context.Context, tabID string, req *actionRequest) (context.Context, error) {
	if req.Ref != "" && req.NodeID == 0 && req.Selector == "" {
		nid, frameID, err := b.resolveRef(ctx, tabID, req.Ref)
		if err != nil {
			return ctx, err
		}
		req.NodeID = nid
		ctx = withFrameOffset(ctx, tabID, frameID)
	}
	if req.ToRef != "" && req.ToNodeID == 0 && req.ToSelector == "" {
		nid, _, err := b.resolveRef(ctx, tabID, req.ToRef)
		if err != nil {
			return ctx, err
		}
		req.ToNodeID = nid
	}
	return ctx, nil
}

func (b *Bridge) handleAction(w http.ResponseWriter, r *http.Request) {
	var req actionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}

	ctx, resolvedTabID, err := b.TabContext(req.TabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	if !b.checkLock(w, r, resolvedTabID) {
		return
	}

	tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)
	defer tCancel()
	go cancelOnClientDone(r.Context(), tCancel)

	// Resolve refs to backend node IDs (and the frame they live in).
	actCtx, err := b.resolveActionRefs(tCtx, resolvedTabID, &req)
	if err != nil {
		jsonResp(w, 400, map[string]string{"error": err.Error()})
		return
	}

	registry := b.actions
	if req.Kind == "" {
		kinds := make([]string, 0, len(registry))
		for k := range registry {
			kinds = append(kinds, k)
		}
		jsonResp(w, 400, map[string]string{
			"error": fmt.Sprintf("missing required field 'kind' - valid values: %s", strings.Join(kinds, ", ")),
		})
		return
	}
	fn, ok := registry[req.Kind]
	if !ok {
		kinds := make([]string, 0, len(registry))
		for k := range registry {
			kinds = append(kinds, k)
		}
		jsonResp(w, 400, map[string]string{
			"error": fmt.Sprintf("unknown action: %s - valid values: %s", req.Kind, strings.Join(kinds, ", ")),
		})
		return
	}

	result, err := runWatchingDialogs(b.Monitor(resolvedTabID), func() (map[string]any, error) { return fn(actCtx, req) })
	if err != nil {
		b.failTab(w, resolvedTabID, fmt.Errorf("action %s: %w", req.Kind, err))
		return
	}

	jsonResp(w, 200, result)
}

// ── POST /actions (batch) ──────────────────────────────────

type actionsRequest struct {
	TabID       string          `json:"tabId"`
	Actions     []actionRequest `json:"actions"`
	StopOnError bool            `json:"stopOnError"`
}

type actionResult struct {
	Index   int            `json:"index"`
	Success bool           `json:"success"`
	Result  map[string]any `json:"result,omitempty"`
	Error   string         `json:"error,omitempty"`
}

func (b *Bridge) handleActions(w http.ResponseWriter, r *http.Request) {
	var req actionsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize)).Decode(&req); err != nil {
		jsonErr(w, 400, fmt.Errorf("decode: %w", err))
		return
	}

	if len(req.Actions) == 0 {
		jsonErr(w, 400, fmt.Errorf("actions array is empty"))
		return
	}

	ctx, resolvedTabID, err := b.TabContext(req.TabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	if !b.checkLock(w, r, resolvedTabID) {
		return
	}

	results := make([]actionResult, 0, len(req.Actions))
	registry := b.actions

	for i, action := range req.Actions {
		if action.TabID == "" {
			action.TabID = resolvedTabID
		} else if action.TabID != resolvedTabID {
			ctx, resolvedTabID, err = b.TabContext(action.TabID)
			if err == nil && b.locks != nil {
				err = b.locks.CheckAccess(resolvedTabID, ownerOf(r))
			}
			if err != nil {
				results = append(results, actionResult{
					Index: i, Success: false,
					Error: fmt.Sprintf("tab not found: %v", err),
				})
				if req.StopOnError {
					break
				}
				continue
			}
		}

		tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)

		actCtx, err := b.resolveActionRefs(tCtx, resolvedTabID, &action)
		if err != nil {
			tCancel()
			results = append(results, actionResult{Index: i, Success: false, Error: err.Error()})
			if req.StopOnError {
				break
			}
			continue
		}

		if action.Kind == "" {
			tCancel()
			results = append(results, actionResult{
				Index: i, Success: false, Error: "missing required field 'kind'",
			})
			if req.StopOnError {
				break
			}
			continue
		}

		fn, ok := registry[action.Kind]
		if !ok {
			tCancel()
			kinds := make([]string, 0, len(registry))
			for k := range registry {
				kinds = append(kinds, k)
			}
			results = append(results, actionResult{
				Index: i, Success: false,
				Error: fmt.Sprintf("unknown action: %s - valid values: %s", action.Kind, strings.Join(kinds, ", ")),
			})
			if req.StopOnError {
				break
			}
			continue
		}

		actionRes, err := runWatchingDialogs(b.Monitor(resolvedTabID), func() (map[string]any, error) { return fn(actCtx, action) })
		tCancel()

		if err != nil {
			results = append(results, actionResult{
				Index: i, Success: false,
				Error: fmt.Sprintf("action %s: %v", action.Kind, err),
			})
			if req.StopOnError {
				break
			}
		} else {
			results = append(results, actionResult{
				Index: i, Success: true, Result: actionRes,
			})
		}

		if i < len(req.Actions)-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	jsonResp(w, 200, map[string]any{
		"results":    results,
		"total":      len(req.Actions),
		"successful": countSuccessful(results),
		"failed":     len(req.Actions) - countSuccessful(results),
	})
}

func countSuccessful(results []actionResult) int {
	count := 0
	for _, r := range results {
		if r.Success {
			count++
		}
	}
	return count
}
