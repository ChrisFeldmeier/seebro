package main

import (
	"context"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// disableAnimationsCSS is injected to force-disable all CSS animations and transitions.
const disableAnimationsCSS = `
(function() {
  const style = document.createElement('style');
  style.setAttribute('data-seebro', 'no-animations');
  style.textContent = '*, *::before, *::after { animation: none !important; animation-duration: 0s !important; transition: none !important; transition-duration: 0s !important; scroll-behavior: auto !important; }';
  (document.head || document.documentElement).appendChild(style);
})();
`

// injectNoAnimations adds a persistent script (via CDP) that disables CSS
// animations on every document load. Used when BRIDGE_NO_ANIMATIONS=true.
func (b *Bridge) injectNoAnimations(ctx context.Context) {
	_ = chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(disableAnimationsCSS).Do(ctx)
			return err
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetEmulatedMedia().
				WithFeatures([]*emulation.MediaFeature{
					{Name: "prefers-reduced-motion", Value: "reduce"},
				}).Do(ctx)
		}),
	)
}

// disableAnimationsOnce runs the animation-disabling CSS on the current page
// (one-shot, for per-request ?noAnimations=true).
func disableAnimationsOnce(ctx context.Context) {
	_ = chromedp.Run(ctx,
		chromedp.Evaluate(disableAnimationsCSS, nil),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetEmulatedMedia().
				WithFeatures([]*emulation.MediaFeature{
					{Name: "prefers-reduced-motion", Value: "reduce"},
				}).Do(ctx)
		}),
	)
}
