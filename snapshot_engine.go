package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// In-page snapshot engine (snapshot.js) driven per frame.
//
// Refs: "e<id>" for the main frame, "f<n>e<id>" for child frames. Ids come
// from a per-document WeakMap in an isolated world, so a ref keeps pointing
// at the same element across snapshots, and ids keep increasing within a tab
// so a ref from a previous page can never hit an element on the next one.

//go:embed snapshot.js
var snapshotJS string

var jsRefPattern = regexp.MustCompile(`^(?:f(\d+))?e(\d+)$`)

type jsNode struct {
	ID        int64    `json:"id"`
	Role      string   `json:"role"`
	Name      string   `json:"name"`
	Value     string   `json:"value"`
	X         float64  `json:"x"`
	Y         float64  `json:"y"`
	W         float64  `json:"w"`
	H         float64  `json:"h"`
	Disabled  bool     `json:"disabled"`
	Checked   bool     `json:"checked"`
	Expanded  bool     `json:"expanded"`
	Collapsed bool     `json:"collapsed"`
	Focused   bool     `json:"focused"`
	Required  bool     `json:"required"`
	Options   []string `json:"options"`
	Href      string   `json:"href"`
}

type jsSnapshot struct {
	Nodes     []jsNode `json:"nodes"`
	Next      int64    `json:"next"`
	URL       string   `json:"url"`
	Title     string   `json:"title"`
	VW        float64  `json:"vw"`
	VH        float64  `json:"vh"`
	Truncated bool     `json:"truncated"`
}

// frameRefs is the per-tab bookkeeping for isolated worlds and frame indexes.
type frameRefs struct {
	mu        sync.Mutex
	worlds    map[string]worldEntry // frameID → isolated world
	index     map[string]int        // frameID → n in "f<n>"
	frames    map[int]string        // n → frameID
	nextIdx   int
	nextID    int64
	snapped   bool // a snapshot was taken at least once
	mainWorld bool // run the engine in the page's main world (obscura: isolated worlds are slow there)
}

func (b *Bridge) frameRefsFor(tabID string) *frameRefs {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refs == nil {
		b.refs = make(map[string]*frameRefs)
	}
	fr := b.refs[tabID]
	if fr == nil {
		fr = &frameRefs{
			worlds:  map[string]worldEntry{},
			index:   map[string]int{tabID: 0},
			frames:  map[int]string{0: tabID},
			nextIdx: 1, nextID: 1,
			mainWorld: b.tabEngine[tabID] == engineObscura || (b.primary == engineObscura && b.tabEngine[tabID] == ""),
		}
		b.refs[tabID] = fr
	}
	return fr
}

func (fr *frameRefs) frameIndex(frameID string) int {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if n, ok := fr.index[frameID]; ok {
		return n
	}
	n := fr.nextIdx
	fr.nextIdx++
	fr.index[frameID] = n
	fr.frames[n] = frameID
	return n
}

func refName(frameIdx int, id int64) string {
	if frameIdx == 0 {
		return "e" + strconv.FormatInt(id, 10)
	}
	return "f" + strconv.Itoa(frameIdx) + "e" + strconv.FormatInt(id, 10)
}

// worldEntry is an isolated world bound to one document (loaderID).
// Execution context ids are only unique per renderer process: after a
// cross-site navigation a cached id may point at an unrelated context.
type worldEntry struct {
	id     runtime.ExecutionContextID
	loader string
}

func isContextGone(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "Cannot find context") || strings.Contains(s, "context was destroyed") ||
		strings.Contains(s, "No frame") || strings.Contains(s, "Frame with the given id") ||
		strings.Contains(s, "__sb is not defined")
}

// loaderOf returns the current document (loader) id of a frame, "" if unknown.
func loaderOf(ctx context.Context, frameID string) string {
	var tree *page.FrameTree
	if chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var e error
		tree, e = page.GetFrameTree().Do(ctx)
		return e
	})) != nil || tree == nil {
		return ""
	}
	var find func(t *page.FrameTree) string
	find = func(t *page.FrameTree) string {
		if t.Frame != nil && string(t.Frame.ID) == frameID {
			return string(t.Frame.LoaderID)
		}
		for _, c := range t.ChildFrames {
			if l := find(c); l != "" {
				return l
			}
		}
		return ""
	}
	return find(tree)
}

// world returns the frame's isolated world with the engine loaded, creating it
// when missing or bound to an older document (create=true). loader "" skips
// the document check (ref lookups, where a mismatch surfaces as an error).
func (b *Bridge) world(ctx context.Context, fr *frameRefs, frameID string, create bool, loader ...string) (runtime.ExecutionContextID, error) {
	want := ""
	if len(loader) > 0 {
		want = loader[0]
	}
	fr.mu.Lock()
	entry, ok := fr.worlds[frameID]
	fr.mu.Unlock()
	if ok && (want == "" || entry.loader == want) {
		return entry.id, nil
	}
	var id runtime.ExecutionContextID
	if !create {
		return 0, fmt.Errorf("page changed since the last snapshot")
	}
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		if fr.mainWorld {
			_, exc, e := runtime.Evaluate(snapshotJS).Do(ctx) // id 0 = main world
			if e == nil && exc != nil {
				e = fmt.Errorf("snapshot engine: %s", exceptionText(exc))
			}
			return e
		}
		var e error
		id, e = page.CreateIsolatedWorld(cdp.FrameID(frameID)).WithWorldName("seebro").Do(ctx)
		if e != nil {
			return e
		}
		_, exc, e := runtime.Evaluate(snapshotJS).WithContextID(id).Do(ctx)
		if e == nil && exc != nil {
			e = fmt.Errorf("snapshot engine: %s", exceptionText(exc))
		}
		return e
	}))
	if err != nil {
		return 0, err
	}
	fr.mu.Lock()
	fr.worlds[frameID] = worldEntry{id: id, loader: want}
	fr.mu.Unlock()
	return id, nil
}

func (fr *frameRefs) forget(frameID string) {
	fr.mu.Lock()
	delete(fr.worlds, frameID)
	fr.mu.Unlock()
}

// callWorld runs expr (an expression) in the frame's world and decodes the JSON result.
func callWorld(ctx context.Context, worldID runtime.ExecutionContextID, expr string, out any) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		p := runtime.Evaluate(expr).WithReturnByValue(true).WithAwaitPromise(true)
		if worldID != 0 {
			p = p.WithContextID(worldID)
		}
		res, exc, err := p.Do(ctx)
		if err != nil {
			return err
		}
		if exc != nil {
			return fmt.Errorf("%s", exceptionText(exc))
		}
		if out == nil || len(res.Value) == 0 {
			return nil
		}
		return json.Unmarshal(res.Value, out)
	}))
}

type frameInfo struct {
	id     string
	url    string
	loader string
	dx, dy float64 // offset of the frame's viewport in the main viewport
}

// frames lists the tab's frames that live in this session (main first) with
// their offsets. Frames in other processes (site isolation) are skipped.
func (b *Bridge) frames(ctx context.Context, tabID string) []frameInfo {
	out := []frameInfo{{id: tabID}}
	if b.engineOf(tabID) == engineObscura {
		// obscura doesn't load iframes, and box models force a full layout pass.
		out[0].loader = loaderOf(ctx, tabID)
		return out
	}
	var tree *page.FrameTree
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var e error
		tree, e = page.GetFrameTree().Do(ctx)
		return e
	})); err != nil || tree == nil {
		return out
	}
	out[0].id = string(tree.Frame.ID)
	out[0].url = tree.Frame.URL
	out[0].loader = string(tree.Frame.LoaderID)
	var walk func(t *page.FrameTree)
	walk = func(t *page.FrameTree) {
		for _, c := range t.ChildFrames {
			if c.Frame == nil {
				continue
			}
			fi := frameInfo{id: string(c.Frame.ID), url: c.Frame.URL, loader: string(c.Frame.LoaderID)}
			ok := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
				backend, _, err := dom.GetFrameOwner(c.Frame.ID).Do(ctx)
				if err != nil {
					return err
				}
				box, err := dom.GetBoxModel().WithBackendNodeID(backend).Do(ctx)
				if err != nil {
					return err
				}
				if len(box.Content) >= 2 {
					fi.dx, fi.dy = box.Content[0], box.Content[1]
				}
				return nil
			})) == nil
			if ok {
				out = append(out, fi)
			}
			walk(c)
		}
	}
	walk(tree)
	return out
}

type jsSnapshotOptions struct {
	Viewport bool   `json:"-"`
	Links    bool   `json:"links,omitempty"`
	MaxNodes int    `json:"maxNodes,omitempty"`
	Selector string `json:"selector,omitempty"`
	StartID  int64  `json:"startId"`
	Lite     bool   `json:"lite,omitempty"` // engines with expensive layout queries (obscura)
}

// jsSnapshot collects interactive elements from all frames of a tab.
func (b *Bridge) jsSnapshot(ctx context.Context, tabID string, o jsSnapshotOptions) ([]A11yNode, string, string, int, error) {
	fr := b.frameRefsFor(tabID)
	o.Lite = b.engineOf(tabID) == engineObscura
	if fr.mainWorld {
		return b.mainWorldSnapshot(ctx, fr, tabID, o)
	}
	var all []A11yNode
	url, title := "", ""
	hidden := 0
	var vw, vh float64

	for i, f := range b.frames(ctx, tabID) {
		idx := fr.frameIndex(f.id)
		if i == 0 && f.id != tabID {
			// Main frame id differs from the target id: alias it.
			fr.mu.Lock()
			fr.index[f.id], fr.frames[0] = 0, f.id
			fr.mu.Unlock()
			idx = 0
		}
		fr.mu.Lock()
		o.StartID = fr.nextID
		fr.mu.Unlock()
		opts, _ := json.Marshal(o)

		var snap jsSnapshot
		var err error
		for attempt := 0; attempt < 2; attempt++ {
			var wid runtime.ExecutionContextID
			if wid, err = b.world(ctx, fr, f.id, true, f.loader); err != nil {
				break
			}
			expr := "__sb.snapshot(" + string(opts) + ")"
			if o.Selector != "" && i == 0 {
				sel, _ := json.Marshal(o.Selector)
				expr = "(() => { const r = document.querySelector(" + string(sel) + "); if (!r) throw new Error('selector not found'); return __sb.snapshot(Object.assign(" + string(opts) + ", {root: r})); })()"
			}
			if err = callWorld(ctx, wid, expr, &snap); isContextGone(err) {
				fr.forget(f.id) // document replaced: build a fresh world and retry
				continue
			}
			break
		}
		if err != nil {
			if i == 0 {
				return nil, "", "", 0, err
			}
			continue // a broken child frame shouldn't fail the snapshot
		}
		fr.mu.Lock()
		if snap.Next > fr.nextID {
			fr.nextID = snap.Next
		}
		fr.snapped = true
		fr.mu.Unlock()

		if i == 0 {
			url, title, vw, vh = snap.URL, snap.Title, snap.VW, snap.VH
		}
		frameLabel := ""
		if i > 0 {
			frameLabel = "f" + strconv.Itoa(idx) + " " + f.url
		}
		for _, n := range snap.Nodes {
			x, y := n.X+f.dx, n.Y+f.dy
			if o.Viewport && (x+n.W < 0 || y+n.H < 0 || x > vw || y > vh) {
				hidden++
				continue
			}
			all = append(all, A11yNode{
				Ref: refName(idx, n.ID), Role: n.Role, Name: n.Name, Value: n.Value,
				Disabled: n.Disabled, Focused: n.Focused, Checked: n.Checked,
				Expanded: n.Expanded, Collapsed: n.Collapsed, Required: n.Required,
				Options: n.Options, Href: n.Href, Frame: frameLabel,
			})
		}
	}
	return all, url, title, hidden, nil
}

// resolveRef maps a ref to a backend node ID and the frame it lives in.
// Engine refs (e12, f1e7) are looked up in their frame's isolated world; "a"
// refs come from the accessibility-tree snapshot cache.
func (b *Bridge) resolveRef(ctx context.Context, tabID, ref string) (int64, string, error) {
	if ref == "" {
		return 0, "", nil
	}
	if m := jsRefPattern.FindStringSubmatch(ref); m != nil {
		frameIdx := 0
		if m[1] != "" {
			frameIdx, _ = strconv.Atoi(m[1])
		}
		id, _ := strconv.ParseInt(m[2], 10, 64)
		fr := b.frameRefsFor(tabID)
		fr.mu.Lock()
		frameID, ok := fr.frames[frameIdx]
		snapped := fr.snapped
		fr.mu.Unlock()
		if !ok || !snapped {
			return 0, "", fmt.Errorf("ref %s not found - take a /snapshot first", ref)
		}
		wid, err := b.world(ctx, fr, frameID, false)
		if err != nil {
			return 0, "", fmt.Errorf("ref %s is stale (%v) - take a new /snapshot", ref, err)
		}
		var backend int64
		err = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			p := runtime.Evaluate("__sb.el(" + m[2] + ")")
			if wid != 0 {
				p = p.WithContextID(wid)
			}
			res, exc, err := p.Do(ctx)
			if err != nil {
				return err
			}
			if exc != nil {
				return fmt.Errorf("%s", exceptionText(exc))
			}
			if res.ObjectID == "" {
				return fmt.Errorf("element no longer exists")
			}
			defer func() { _ = runtime.ReleaseObject(res.ObjectID).Do(ctx) }()
			node, err := dom.DescribeNode().WithObjectID(res.ObjectID).Do(ctx)
			if err != nil {
				return err
			}
			backend = int64(node.BackendNodeID)
			return nil
		}))
		if isContextGone(err) {
			fr.forget(frameID)
		}
		if err != nil {
			return 0, "", fmt.Errorf("ref %s is stale (%v) - take a new /snapshot", ref, err)
		}
		_ = id
		return backend, frameID, nil
	}
	if cache := b.GetRefCache(tabID); cache != nil {
		if nid, ok := cache.refs[ref]; ok {
			return nid, "", nil
		}
	}
	return 0, "", fmt.Errorf("ref %s not found - take a /snapshot first", ref)
}

// frameOffset returns where a child frame's viewport sits in the main viewport.
func frameOffset(ctx context.Context, frameID string) (float64, float64) {
	var dx, dy float64
	_ = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		backend, _, err := dom.GetFrameOwner(cdp.FrameID(frameID)).Do(ctx)
		if err != nil {
			return err
		}
		box, err := dom.GetBoxModel().WithBackendNodeID(backend).Do(ctx)
		if err == nil && len(box.Content) >= 2 {
			dx, dy = box.Content[0], box.Content[1]
		}
		return err
	}))
	return dx, dy
}

type frameOffsetKey struct{}

// withFrameOffset tags ctx so coordinate-based input (dblclick, drag, human
// clicks) lands inside a child frame.
func withFrameOffset(ctx context.Context, tabID, frameID string) context.Context {
	if frameID == "" || frameID == tabID {
		return ctx
	}
	dx, dy := frameOffset(ctx, frameID)
	return context.WithValue(ctx, frameOffsetKey{}, [2]float64{dx, dy})
}

func offsetFrom(ctx context.Context) (float64, float64) {
	if v, ok := ctx.Value(frameOffsetKey{}).([2]float64); ok {
		return v[0], v[1]
	}
	return 0, 0
}

// mainWorldSnapshot is the single-round-trip path for engines whose CDP calls
// queue behind page JavaScript (obscura runs all pages on one V8 thread):
// install the engine if missing and take the snapshot in one evaluate.
func (b *Bridge) mainWorldSnapshot(ctx context.Context, fr *frameRefs, tabID string, o jsSnapshotOptions) ([]A11yNode, string, string, int, error) {
	fr.mu.Lock()
	o.StartID = fr.nextID
	fr.mu.Unlock()
	opts, _ := json.Marshal(o)
	root := "null"
	if o.Selector != "" {
		sel, _ := json.Marshal(o.Selector)
		root = "document.querySelector(" + string(sel) + ")"
	}
	expr := "(() => { if (!globalThis.__sb) {" + snapshotJS + "}\n const r = " + root +
		"; return __sb.snapshot(Object.assign(" + string(opts) + ", r ? {root: r} : {})); })()"
	var snap jsSnapshot
	if err := callWorld(ctx, 0, expr, &snap); err != nil {
		return nil, "", "", 0, err
	}
	fr.mu.Lock()
	if snap.Next > fr.nextID {
		fr.nextID = snap.Next
	}
	fr.snapped = true
	fr.worlds[tabID] = worldEntry{id: 0} // refs resolve in the main world
	fr.mu.Unlock()

	all := make([]A11yNode, 0, len(snap.Nodes))
	for _, n := range snap.Nodes {
		all = append(all, A11yNode{
			Ref: refName(0, n.ID), Role: n.Role, Name: n.Name, Value: n.Value,
			Disabled: n.Disabled, Focused: n.Focused, Checked: n.Checked,
			Expanded: n.Expanded, Collapsed: n.Collapsed, Required: n.Required,
			Options: n.Options, Href: n.Href,
		})
	}
	return all, snap.URL, snap.Title, 0, nil
}
