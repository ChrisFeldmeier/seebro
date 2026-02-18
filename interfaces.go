package main

import (
	"context"

	"github.com/chromedp/cdproto/target"
)

// BridgeAPI is the interface handlers use to interact with Chrome.
// Bridge implements this. Tests can mock it.
type BridgeAPI interface {
	// Tab management
	TabContext(tabID string) (ctx context.Context, resolvedID string, err error)
	ListTargets() ([]*target.Info, error)
	CreateTab(url string) (tabID string, ctx context.Context, cancel context.CancelFunc, err error)
	CloseTab(tabID string) error

	// Snapshot cache
	GetRefCache(tabID string) *refCache
	SetRefCache(tabID string, cache *refCache)
	DeleteRefCache(tabID string)
}

// TabInfo is a simplified tab descriptor for JSON responses.
type TabInfo struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Type  string `json:"type"`
}
