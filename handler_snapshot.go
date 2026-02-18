package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/chromedp/chromedp"
	"gopkg.in/yaml.v3"
)

// ── GET /snapshot ──────────────────────────────────────────

func (b *Bridge) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	tabID := r.URL.Query().Get("tabId")
	filter := r.URL.Query().Get("filter")
	doDiff := r.URL.Query().Get("diff") == "true"
	format := r.URL.Query().Get("format")          // "text", "yaml", "compact"
	output := r.URL.Query().Get("output")          // "file" to save to disk
	outputPath := r.URL.Query().Get("path")        // custom file path for output=file
	selector := r.URL.Query().Get("selector")      // CSS selector to scope snapshot
	maxTokensStr := r.URL.Query().Get("maxTokens") // truncate output to ~N tokens
	reqNoAnim := r.URL.Query().Get("noAnimations") == "true"
	maxDepthStr := r.URL.Query().Get("depth")
	maxDepth := -1
	if maxDepthStr != "" {
		if d, err := strconv.Atoi(maxDepthStr); err == nil {
			maxDepth = d
		}
	}
	maxTokens := -1
	if maxTokensStr != "" {
		if t, err := strconv.Atoi(maxTokensStr); err == nil && t > 0 {
			maxTokens = t
		}
	}

	ctx, resolvedTabID, err := b.TabContext(tabID)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}

	tCtx, tCancel := context.WithTimeout(ctx, actionTimeout)
	defer tCancel()
	go cancelOnClientDone(r.Context(), tCancel)

	if reqNoAnim && !noAnimations {
		disableAnimationsOnce(tCtx)
	}

	var rawResult json.RawMessage
	if err := chromedp.Run(tCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			return chromedp.FromContext(ctx).Target.Execute(ctx,
				"Accessibility.getFullAXTree", nil, &rawResult)
		}),
	); err != nil {
		jsonErr(w, 500, fmt.Errorf("a11y tree: %w", err))
		return
	}

	var treeResp struct {
		Nodes []rawAXNode `json:"nodes"`
	}
	if err := json.Unmarshal(rawResult, &treeResp); err != nil {
		jsonErr(w, 500, fmt.Errorf("parse a11y tree: %w", err))
		return
	}

	// If a CSS selector is specified, scope the tree to that element's subtree.
	if selector != "" {
		var scopeNodeID int64
		if err := chromedp.Run(tCtx,
			chromedp.ActionFunc(func(ctx context.Context) error {
				// Use DOM.querySelector to find the element
				p := map[string]any{"nodeId": 0, "selector": selector}
				// First get the document node
				var docResult json.RawMessage
				if err := chromedp.FromContext(ctx).Target.Execute(ctx, "DOM.getDocument", map[string]any{"depth": 0}, &docResult); err != nil {
					return fmt.Errorf("get document: %w", err)
				}
				var doc struct {
					Root struct {
						NodeID int64 `json:"nodeId"`
					} `json:"root"`
				}
				if err := json.Unmarshal(docResult, &doc); err != nil {
					return err
				}
				p["nodeId"] = doc.Root.NodeID
				var qResult json.RawMessage
				if err := chromedp.FromContext(ctx).Target.Execute(ctx, "DOM.querySelector", p, &qResult); err != nil {
					return fmt.Errorf("querySelector: %w", err)
				}
				var qr struct {
					NodeID int64 `json:"nodeId"`
				}
				if err := json.Unmarshal(qResult, &qr); err != nil {
					return err
				}
				if qr.NodeID == 0 {
					return fmt.Errorf("selector %q not found", selector)
				}
				// Resolve DOM nodeId to backendNodeId
				var descResult json.RawMessage
				if err := chromedp.FromContext(ctx).Target.Execute(ctx, "DOM.describeNode", map[string]any{"nodeId": qr.NodeID}, &descResult); err != nil {
					return fmt.Errorf("describe node: %w", err)
				}
				var desc struct {
					Node struct {
						BackendNodeID int64 `json:"backendNodeId"`
					} `json:"node"`
				}
				if err := json.Unmarshal(descResult, &desc); err != nil {
					return err
				}
				scopeNodeID = desc.Node.BackendNodeID
				return nil
			}),
		); err != nil {
			jsonErr(w, 400, fmt.Errorf("selector: %w", err))
			return
		}

		treeResp.Nodes = filterSubtree(treeResp.Nodes, scopeNodeID)
	}

	flat, refs := buildSnapshot(treeResp.Nodes, filter, maxDepth)

	// Truncate to maxTokens if specified (rough: 1 token ≈ 4 bytes)
	truncated := false
	if maxTokens > 0 {
		flat, truncated = truncateToTokens(flat, maxTokens, format)
	}

	// Get previous snapshot for diff before overwriting cache
	var prevNodes []A11yNode
	if doDiff {
		if prev := b.GetRefCache(resolvedTabID); prev != nil {
			prevNodes = prev.nodes
		}
	}

	// Cache ref→nodeID mapping and nodes for this tab
	b.SetRefCache(resolvedTabID, &refCache{refs: refs, nodes: flat})

	var url, title string
	_ = chromedp.Run(tCtx,
		chromedp.Location(&url),
		chromedp.Title(&title),
	)

	// Handle file output
	if output == "file" {
		// Create snapshots directory if it doesn't exist
		snapshotDir := filepath.Join(stateDir, "snapshots")
		if err := os.MkdirAll(snapshotDir, 0755); err != nil {
			jsonErr(w, 500, fmt.Errorf("create snapshot dir: %w", err))
			return
		}

		// Generate filename with timestamp
		timestamp := time.Now().Format("20060102-150405")
		var filename string
		var content []byte

		switch format {
		case "text":
			filename = fmt.Sprintf("snapshot-%s.txt", timestamp)
			textContent := fmt.Sprintf("# %s\n# %s\n# %d nodes\n# %s\n\n%s",
				title, url, len(flat), time.Now().Format(time.RFC3339),
				formatSnapshotText(flat))
			content = []byte(textContent)
		case "yaml":
			filename = fmt.Sprintf("snapshot-%s.yaml", timestamp)
			data := map[string]any{
				"url":       url,
				"title":     title,
				"timestamp": time.Now().Format(time.RFC3339),
				"nodes":     flat,
				"count":     len(flat),
			}
			if doDiff && prevNodes != nil {
				added, changed, removed := diffSnapshot(prevNodes, flat)
				data["diff"] = true
				data["added"] = added
				data["changed"] = changed
				data["removed"] = removed
				data["counts"] = map[string]int{
					"added":   len(added),
					"changed": len(changed),
					"removed": len(removed),
					"total":   len(flat),
				}
			}
			var err error
			content, err = yaml.Marshal(data)
			if err != nil {
				jsonErr(w, 500, fmt.Errorf("marshal yaml: %w", err))
				return
			}
		default:
			filename = fmt.Sprintf("snapshot-%s.json", timestamp)
			data := map[string]any{
				"url":       url,
				"title":     title,
				"timestamp": time.Now().Format(time.RFC3339),
				"nodes":     flat,
				"count":     len(flat),
			}
			if doDiff && prevNodes != nil {
				added, changed, removed := diffSnapshot(prevNodes, flat)
				data["diff"] = true
				data["added"] = added
				data["changed"] = changed
				data["removed"] = removed
				data["counts"] = map[string]int{
					"added":   len(added),
					"changed": len(changed),
					"removed": len(removed),
					"total":   len(flat),
				}
			}
			var err error
			content, err = json.MarshalIndent(data, "", "  ")
			if err != nil {
				jsonErr(w, 500, fmt.Errorf("marshal snapshot: %w", err))
				return
			}
		}

		// Write to file — use custom path if provided, otherwise default
		filePath := filepath.Join(snapshotDir, filename)
		if outputPath != "" {
			filePath = outputPath
			// Ensure parent directory exists for custom paths
			if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
				jsonErr(w, 500, fmt.Errorf("create output dir: %w", err))
				return
			}
		}
		if err := os.WriteFile(filePath, content, 0644); err != nil {
			jsonErr(w, 500, fmt.Errorf("write snapshot: %w", err))
			return
		}

		// Return path instead of data
		jsonResp(w, 200, map[string]any{
			"path":      filePath,
			"size":      len(content),
			"format":    format,
			"timestamp": timestamp,
		})
		return
	}

	if doDiff && prevNodes != nil {
		added, changed, removed := diffSnapshot(prevNodes, flat)
		jsonResp(w, 200, map[string]any{
			"url":     url,
			"title":   title,
			"diff":    true,
			"added":   added,
			"changed": changed,
			"removed": removed,
			"counts": map[string]int{
				"added":   len(added),
				"changed": len(changed),
				"removed": len(removed),
				"total":   len(flat),
			},
		})
		return
	}

	switch format {
	case "compact":
		// One-line-per-node format, minimal tokens. Similar to OpenClaw aria refs.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(200)
		_, _ = fmt.Fprintf(w, "# %s | %s | %d nodes", title, url, len(flat))
		if truncated {
			_, _ = fmt.Fprintf(w, " (truncated to ~%d tokens)", maxTokens)
		}
		_, _ = w.Write([]byte("\n"))
		_, _ = w.Write([]byte(formatSnapshotCompact(flat)))
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(200)
		_, _ = fmt.Fprintf(w, "# %s\n# %s\n# %d nodes\n\n", title, url, len(flat))
		_, _ = w.Write([]byte(formatSnapshotText(flat)))
	case "yaml":
		data := map[string]any{
			"url":   url,
			"title": title,
			"nodes": flat,
			"count": len(flat),
		}
		yamlContent, err := yaml.Marshal(data)
		if err != nil {
			jsonErr(w, 500, fmt.Errorf("marshal yaml: %w", err))
			return
		}
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.WriteHeader(200)
		_, _ = w.Write(yamlContent)
	default:
		resp := map[string]any{
			"url":   url,
			"title": title,
			"nodes": flat,
			"count": len(flat),
		}
		if truncated {
			resp["truncated"] = true
			resp["maxTokens"] = maxTokens
		}
		jsonResp(w, 200, resp)
	}
}
