package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Additional /action kinds: dblclick, clickAt, drag, upload, plus the
// event-firing fill and the key-name-aware press used by the base registry.

func (b *Bridge) registerExtraActions() {
	b.actions[actionDblClick] = func(ctx context.Context, req actionRequest) (map[string]any, error) {
		x, y, err := elementCenter(ctx, req.NodeID, req.Selector)
		if err != nil {
			return nil, err
		}
		return map[string]any{"clicked": true, "count": 2}, chromedp.Run(ctx, chromedp.MouseClickXY(x, y, chromedp.ClickCount(2)))
	}

	b.actions[actionClickAt] = func(ctx context.Context, req actionRequest) (map[string]any, error) {
		if req.X <= 0 && req.Y <= 0 {
			return nil, fmt.Errorf("x and y required for clickAt")
		}
		n := req.Count
		if n <= 0 {
			n = 1
		}
		return map[string]any{"clicked": true, "x": req.X, "y": req.Y},
			chromedp.Run(ctx, chromedp.MouseClickXY(req.X, req.Y, chromedp.ClickCount(n)))
	}

	b.actions[actionDrag] = func(ctx context.Context, req actionRequest) (map[string]any, error) {
		if req.NodeID == 0 && req.Selector == "" {
			return nil, fmt.Errorf("need ref or selector for the drag source")
		}
		if req.ToNodeID == 0 && req.ToSelector == "" && req.X <= 0 && req.Y <= 0 {
			return nil, fmt.Errorf("need toRef, toSelector or x/y for the drop target")
		}
		// HTML5 drag-and-drop (draggable=true) ignores synthetic mouse input,
		// so dispatch DragEvents directly for those.
		if req.ToNodeID != 0 || req.ToSelector != "" {
			raw, err := callOnTarget(ctx, req.NodeID, req.Selector, `function() { return this.draggable === true; }`)
			if err == nil && string(raw) == "true" {
				if err := html5Drag(ctx, req); err != nil {
					return nil, err
				}
				return map[string]any{"dragged": true, "mode": "html5"}, nil
			}
		}
		fx, fy, err := elementCenter(ctx, req.NodeID, req.Selector)
		if err != nil {
			return nil, err
		}
		tx, ty := req.X, req.Y
		if req.ToNodeID != 0 || req.ToSelector != "" {
			if tx, ty, err = elementCenterNoScroll(ctx, req.ToNodeID, req.ToSelector); err != nil {
				return nil, fmt.Errorf("drop target: %w", err)
			}
		}
		return map[string]any{"dragged": true, "mode": "mouse"}, mouseDrag(ctx, fx, fy, tx, ty)
	}

	b.actions[actionUpload] = func(ctx context.Context, req actionRequest) (map[string]any, error) {
		if len(req.Files) == 0 {
			return nil, fmt.Errorf("files required for upload")
		}
		files := make([]string, 0, len(req.Files))
		for _, f := range req.Files {
			abs, err := filepath.Abs(f)
			if err != nil {
				return nil, err
			}
			if _, err := os.Stat(abs); err != nil {
				return nil, fmt.Errorf("file %s: %w", f, err)
			}
			files = append(files, abs)
		}
		if req.NodeID > 0 {
			err := chromedp.Run(ctx, dom.SetFileInputFiles(files).WithBackendNodeID(cdp.BackendNodeID(req.NodeID)))
			return map[string]any{"uploaded": files}, err
		}
		if req.Selector != "" {
			err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
				objectID, err := resolveTargetObject(ctx, 0, req.Selector)
				if err != nil {
					return err
				}
				node, err := dom.DescribeNode().WithObjectID(runtime.RemoteObjectID(objectID)).Do(ctx)
				if err != nil {
					return err
				}
				return dom.SetFileInputFiles(files).WithBackendNodeID(node.BackendNodeID).Do(ctx)
			}))
			return map[string]any{"uploaded": files}, err
		}
		return nil, fmt.Errorf("need ref or selector of an <input type=file>")
	}
}

// fillJS sets a form control's value the way a user would, so frameworks
// (React, Vue, Angular) see the change: native setter + input/change events.
const fillJS = `function(v) {
	if (this.scrollIntoViewIfNeeded) this.scrollIntoViewIfNeeded();
	this.focus && this.focus();
	if (this.type === "checkbox" || this.type === "radio") {
		const want = ["", "true", "on", "1", "checked", "yes"].includes(String(v).toLowerCase());
		if (this.checked !== want) this.click();
		return this.checked;
	}
	if (this.tagName === "SELECT") {
		const o = Array.from(this.options).find(o => o.value === v || o.textContent.trim() === v);
		this.value = o ? o.value : v;
	} else if (this.isContentEditable) {
		this.textContent = v;
	} else {
		const proto = this.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
		const desc = Object.getOwnPropertyDescriptor(proto, "value");
		desc && desc.set ? desc.set.call(this, v) : (this.value = v);
	}
	this.dispatchEvent(new Event("input", {bubbles: true}));
	this.dispatchEvent(new Event("change", {bubbles: true}));
	return this.value ?? this.textContent;
}`

func fillAction(ctx context.Context, req actionRequest) (map[string]any, error) {
	val := req.Text
	if val == "" {
		val = req.Value
	}
	if req.NodeID == 0 && req.Selector == "" {
		return nil, fmt.Errorf("need selector or ref")
	}
	raw, err := callOnTarget(ctx, req.NodeID, req.Selector, fillJS, val)
	if err != nil {
		return nil, err
	}
	var now any
	_ = json.Unmarshal(raw, &now)
	return map[string]any{"filled": val, "value": now}, nil
}

var (
	keyNamesOnce sync.Once
	keyNames     map[string]string // lower-case DOM key name → kb rune string
)

// resolveKey turns "Enter", "ArrowDown", "Control+A", "Meta+Shift+K" or a
// single character into the key string and modifiers chromedp expects.
func resolveKey(spec string) (string, []input.Modifier, error) {
	keyNamesOnce.Do(func() {
		keyNames = map[string]string{"space": " ", "esc": kb.Escape, "return": kb.Enter, "del": kb.Delete}
		for r, k := range kb.Keys {
			// Named keys only ("Enter", "ArrowDown"); single characters are typed as-is.
			if len([]rune(k.Key)) > 1 {
				if _, dup := keyNames[strings.ToLower(k.Key)]; !dup || r < 0x80 {
					keyNames[strings.ToLower(k.Key)] = string(r)
				}
			}
		}
	})
	if len([]rune(spec)) == 1 {
		return spec, nil, nil
	}
	parts := strings.Split(spec, "+")
	// "Control++" → last part is "+"
	if strings.HasSuffix(spec, "++") {
		parts = append(strings.Split(strings.TrimSuffix(spec, "++"), "+"), "+")
	}
	var mods []input.Modifier
	for _, p := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "control", "ctrl":
			mods = append(mods, input.ModifierCtrl)
		case "shift":
			mods = append(mods, input.ModifierShift)
		case "alt", "option":
			mods = append(mods, input.ModifierAlt)
		case "meta", "cmd", "command":
			mods = append(mods, input.ModifierMeta)
		default:
			return "", nil, fmt.Errorf("unknown modifier %q", p)
		}
	}
	key := parts[len(parts)-1]
	if len([]rune(key)) == 1 {
		return key, mods, nil
	}
	if k, ok := keyNames[strings.ToLower(key)]; ok {
		return k, mods, nil
	}
	return "", nil, fmt.Errorf("unknown key %q (use DOM key names like Enter, Tab, Escape, ArrowDown, Backspace, F5)", key)
}

func pressAction(ctx context.Context, req actionRequest) (map[string]any, error) {
	if req.Key == "" {
		return nil, fmt.Errorf("key required for press")
	}
	if req.NodeID > 0 || req.Selector != "" {
		if _, err := callOnTarget(ctx, req.NodeID, req.Selector, `function() { this.focus(); }`); err != nil {
			return nil, err
		}
	}
	key, mods, err := resolveKey(req.Key)
	if err != nil {
		return nil, err
	}
	var opts []chromedp.KeyOption
	if len(mods) > 0 {
		opts = append(opts, chromedp.KeyModifiers(mods...))
	}
	return map[string]any{"pressed": req.Key}, chromedp.Run(ctx, chromedp.KeyEvent(key, opts...))
}

const centerJS = `function(scroll) {
	if (scroll) this.scrollIntoView({block: "center", inline: "center"});
	const r = this.getBoundingClientRect();
	return [r.x + r.width / 2, r.y + r.height / 2];
}`

// elementCenter scrolls the element into view and returns its viewport center.
func elementCenter(ctx context.Context, nodeID int64, selector string) (float64, float64, error) {
	return center(ctx, nodeID, selector, true)
}

func elementCenterNoScroll(ctx context.Context, nodeID int64, selector string) (float64, float64, error) {
	return center(ctx, nodeID, selector, false)
}

func center(ctx context.Context, nodeID int64, selector string, scroll bool) (float64, float64, error) {
	if nodeID == 0 && selector == "" {
		return 0, 0, fmt.Errorf("need selector or ref")
	}
	raw, err := callOnTarget(ctx, nodeID, selector, centerJS, scroll)
	if err != nil {
		return 0, 0, err
	}
	var xy [2]float64
	if err := json.Unmarshal(raw, &xy); err != nil {
		return 0, 0, err
	}
	dx, dy := offsetFrom(ctx) // element inside a child frame
	return xy[0] + dx, xy[1] + dy, nil
}

func mouseDrag(ctx context.Context, fx, fy, tx, ty float64) error {
	const steps = 12
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := input.DispatchMouseEvent(input.MouseMoved, fx, fy).Do(ctx); err != nil {
			return err
		}
		if err := input.DispatchMouseEvent(input.MousePressed, fx, fy).
			WithButton(input.Left).WithButtons(1).WithClickCount(1).Do(ctx); err != nil {
			return err
		}
		for i := 1; i <= steps; i++ {
			x := fx + (tx-fx)*float64(i)/steps
			y := fy + (ty-fy)*float64(i)/steps
			if err := input.DispatchMouseEvent(input.MouseMoved, x, y).
				WithButton(input.Left).WithButtons(1).Do(ctx); err != nil {
				return err
			}
			time.Sleep(15 * time.Millisecond)
		}
		return input.DispatchMouseEvent(input.MouseReleased, tx, ty).
			WithButton(input.Left).WithButtons(0).WithClickCount(1).Do(ctx)
	}))
}

// html5Drag synthesizes the HTML5 drag-and-drop event sequence with a shared DataTransfer.
func html5Drag(ctx context.Context, req actionRequest) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		src, err := resolveTargetObject(ctx, req.NodeID, req.Selector)
		if err != nil {
			return err
		}
		dst, err := resolveTargetObject(ctx, req.ToNodeID, req.ToSelector)
		if err != nil {
			return err
		}
		p := map[string]any{
			"objectId": src,
			"functionDeclaration": `function(dst) {
				const dt = new DataTransfer();
				const fire = (el, type) => el.dispatchEvent(new DragEvent(type, {bubbles: true, cancelable: true, dataTransfer: dt}));
				fire(this, "dragstart"); fire(dst, "dragenter"); fire(dst, "dragover"); fire(dst, "drop"); fire(this, "dragend");
			}`,
			"arguments": []any{map[string]any{"objectId": dst}},
		}
		return chromedp.FromContext(ctx).Target.Execute(ctx, "Runtime.callFunctionOn", p, nil)
	}))
}
