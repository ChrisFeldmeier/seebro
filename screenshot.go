package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Screenshot capture.
//
// Content beyond the viewport is captured by temporarily enlarging the
// viewport (device metrics override), never with captureBeyondViewport:
// on some pages (e.g. Wikipedia, Chrome 155 headless) that flag wedges the
// renderer at 100% CPU and the tab never answers again.

const defaultMaxShotHeight = 16384

type shotOptions struct {
	format    page.CaptureScreenshotFormat
	quality   int
	fullPage  bool
	maxHeight float64 // CSS px cap for full-page / tall elements
	maxDim    float64 // downscale so the longest image side is at most this (pixels)
	nodeID    int64   // element by backend node ID …
	selector  string  // … or CSS selector
}

// viewportState is a device-metrics override set via /emulate, restored after
// a full-page capture temporarily changes the viewport.
type viewportState struct {
	Width, Height int64
	DPR           float64
	Mobile        bool
}

type pageGeometry struct {
	ScrollX, ScrollY float64
	VW, VH           float64
	DocW, DocH       float64
	DPR              float64
	El               *struct{ X, Y, W, H float64 } `json:"el"`
}

const geometryJS = `function(scroll) {
	const d = document.documentElement, b = document.body || d;
	const vv = window.visualViewport;
	const out = {ScrollX: vv ? vv.pageLeft : scrollX, ScrollY: vv ? vv.pageTop : scrollY,
		VW: vv ? vv.width : innerWidth, VH: vv ? vv.height : innerHeight,
		DocW: Math.max(d.scrollWidth, b.scrollWidth), DocH: Math.max(d.scrollHeight, b.scrollHeight),
		DPR: devicePixelRatio, el: null};
	if (this && this.nodeType === 1) {
		if (scroll) { this.scrollIntoView({block: "center", inline: "center"}); out.ScrollX = scrollX; out.ScrollY = scrollY; }
		const r = this.getBoundingClientRect();
		out.el = {X: r.x + scrollX, Y: r.y + scrollY, W: r.width, H: r.height};
	}
	return out;
}`

func (b *Bridge) geometry(ctx context.Context, o shotOptions, scroll bool) (pageGeometry, error) {
	var g pageGeometry
	var raw json.RawMessage
	var err error
	if o.nodeID > 0 || o.selector != "" {
		raw, err = callOnTarget(ctx, o.nodeID, o.selector, geometryJS, scroll)
	} else {
		var res any
		err = chromedp.Run(ctx, chromedp.Evaluate("("+geometryJS+").call(null, false)", &res))
		raw, _ = json.Marshal(res)
	}
	if err != nil {
		return g, err
	}
	err = json.Unmarshal(raw, &g)
	if g.DPR == 0 {
		g.DPR = 1
	}
	return g, err
}

func (b *Bridge) captureScreenshot(ctx context.Context, tabID string, o shotOptions) ([]byte, error) {
	if o.maxHeight <= 0 {
		o.maxHeight = defaultMaxShotHeight
	}
	element := o.nodeID > 0 || o.selector != ""

	g, err := b.geometry(ctx, o, false)
	if err != nil {
		return nil, fmt.Errorf("element: %w", err)
	}
	if element && (g.El == nil || g.El.W == 0 || g.El.H == 0) {
		return nil, fmt.Errorf("element has no visible box")
	}

	tall := o.fullPage || (element && g.El.H > g.VH)
	if element && !tall {
		// Fits in the viewport: bring it into view and capture in place.
		if g, err = b.geometry(ctx, o, true); err != nil {
			return nil, err
		}
	}

	if tall {
		height := math.Min(g.DocH, o.maxHeight)
		if element {
			height = math.Min(g.El.Y+g.El.H, o.maxHeight)
		}
		vs := b.emulatedViewport(tabID)
		w, dpr, mobile := int64(g.VW), 0.0, false // dpr 0 = keep the browser's
		if vs != nil {
			w, dpr, mobile = vs.Width, vs.DPR, vs.Mobile
		}
		if err := chromedp.Run(ctx, emulation.SetDeviceMetricsOverride(w, int64(math.Ceil(height)), dpr, mobile)); err != nil {
			return nil, fmt.Errorf("enlarge viewport: %w", err)
		}
		defer b.restoreViewport(ctx, vs)
		// Layout may change with the new viewport; measure again (scroll is 0 now).
		if g, err = b.geometry(ctx, o, false); err != nil {
			return nil, err
		}
	}

	clip := &page.Viewport{X: g.ScrollX, Y: g.ScrollY, Width: g.VW, Height: g.VH, Scale: 1}
	switch {
	case element:
		clip.X, clip.Y, clip.Width, clip.Height = g.El.X, g.El.Y, g.El.W, math.Min(g.El.H, o.maxHeight)
	case o.fullPage:
		clip.X, clip.Y, clip.Height = 0, 0, math.Min(g.DocH, o.maxHeight)
	}
	if o.maxDim > 0 {
		longest := math.Max(clip.Width, clip.Height) * g.DPR
		if longest > o.maxDim {
			clip.Scale = o.maxDim / longest
		}
	}

	plainViewport := !element && !o.fullPage && clip.Scale == 1

	var buf []byte
	err = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		p := page.CaptureScreenshot().WithFormat(o.format)
		if !plainViewport {
			p = p.WithClip(clip) // otherwise: exactly what the (emulated) screen shows
		}
		if o.format != page.CaptureScreenshotFormatPng {
			p = p.WithQuality(int64(o.quality))
		}
		var e error
		buf, e = p.Do(ctx)
		return e
	}))
	return buf, err
}

func (b *Bridge) emulatedViewport(tabID string) *viewportState {
	m := b.Monitor(tabID)
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.viewport
}

func (b *Bridge) setEmulatedViewport(tabID string, vs *viewportState) {
	if m := b.Monitor(tabID); m != nil {
		m.mu.Lock()
		m.viewport = vs
		m.mu.Unlock()
	}
}

// restoreViewport puts back the /emulate viewport, or clears the override.
func (b *Bridge) restoreViewport(ctx context.Context, vs *viewportState) {
	var a chromedp.Action = emulation.ClearDeviceMetricsOverride()
	if vs != nil {
		a = emulation.SetDeviceMetricsOverride(vs.Width, vs.Height, vs.DPR, vs.Mobile)
	}
	_ = chromedp.Run(ctx, a)
}
