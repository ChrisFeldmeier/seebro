package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// A11yNode is a flattened accessibility tree node returned by /snapshot.
// Refs (e0, e1, ...) are stable within a snapshot and cached for use by /action.
type A11yNode struct {
	Ref      string `json:"ref"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Depth    int    `json:"depth"`
	Value    string `json:"value,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	Focused  bool   `json:"focused,omitempty"`
	NodeID   int64  `json:"nodeId,omitempty"`
}

// Raw a11y tree types — we parse CDP responses manually because the typed
// cdproto library crashes on the "uninteresting" PropertyName value.

type rawAXNode struct {
	NodeID           string      `json:"nodeId"`
	Ignored          bool        `json:"ignored"`
	Role             *rawAXValue `json:"role"`
	Name             *rawAXValue `json:"name"`
	Value            *rawAXValue `json:"value"`
	Properties       []rawAXProp `json:"properties"`
	ChildIDs         []string    `json:"childIds"`
	BackendDOMNodeID int64       `json:"backendDOMNodeId"`
}

type rawAXValue struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type rawAXProp struct {
	Name  string      `json:"name"`
	Value *rawAXValue `json:"value"`
}

func (v *rawAXValue) String() string {
	if v == nil || v.Value == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(v.Value, &s); err == nil {
		return s
	}
	return strings.Trim(string(v.Value), `"`)
}

// interactiveRoles is the set of ARIA roles considered interactive
// for the ?filter=interactive snapshot parameter.
var interactiveRoles = map[string]bool{
	"button": true, "link": true, "textbox": true, "searchbox": true,
	"combobox": true, "listbox": true, "option": true, "checkbox": true,
	"radio": true, "switch": true, "slider": true, "spinbutton": true,
	"menuitem": true, "menuitemcheckbox": true, "menuitemradio": true,
	"tab": true, "treeitem": true,
}

// buildSnapshot converts raw a11y nodes into a flat list of A11yNode entries
// and a ref→backendNodeID map. filter and maxDepth control output.
func buildSnapshot(nodes []rawAXNode, filter string, maxDepth int) ([]A11yNode, map[string]int64) {
	// Build parent map for depth calculation
	parentMap := make(map[string]string)
	for _, n := range nodes {
		for _, childID := range n.ChildIDs {
			parentMap[childID] = n.NodeID
		}
	}
	depthOf := func(nodeID string) int {
		d := 0
		cur := nodeID
		for {
			p, ok := parentMap[cur]
			if !ok {
				break
			}
			d++
			cur = p
		}
		return d
	}

	flat := make([]A11yNode, 0)
	refs := make(map[string]int64)
	refID := 0

	for _, n := range nodes {
		if n.Ignored {
			continue
		}

		role := n.Role.String()
		name := n.Name.String()

		if role == "none" || role == "generic" || role == "InlineTextBox" {
			continue
		}
		if name == "" && role == "StaticText" {
			continue
		}

		depth := depthOf(n.NodeID)
		if maxDepth >= 0 && depth > maxDepth {
			continue
		}
		if filter == filterInteractive && !interactiveRoles[role] {
			continue
		}

		ref := fmt.Sprintf("e%d", refID)
		entry := A11yNode{
			Ref:   ref,
			Role:  role,
			Name:  name,
			Depth: depth,
		}

		if v := n.Value.String(); v != "" {
			entry.Value = v
		}
		if n.BackendDOMNodeID != 0 {
			entry.NodeID = n.BackendDOMNodeID
			refs[ref] = n.BackendDOMNodeID
		}

		for _, prop := range n.Properties {
			if prop.Name == "disabled" && prop.Value.String() == "true" {
				entry.Disabled = true
			}
			if prop.Name == "focused" && prop.Value.String() == "true" {
				entry.Focused = true
			}
		}

		flat = append(flat, entry)
		refID++
	}

	return flat, refs
}

// formatSnapshotText renders nodes as an indented text tree.
// Much cheaper on tokens than JSON (~40-60% reduction).
func formatSnapshotText(nodes []A11yNode) string {
	var b strings.Builder
	for _, n := range nodes {
		for i := 0; i < n.Depth; i++ {
			b.WriteString("  ")
		}
		b.WriteString(n.Ref)
		b.WriteByte(' ')
		b.WriteString(n.Role)
		if n.Name != "" {
			b.WriteString(` "`)
			b.WriteString(n.Name)
			b.WriteByte('"')
		}
		if n.Value != "" {
			b.WriteString(` val="`)
			b.WriteString(n.Value)
			b.WriteByte('"')
		}
		if n.Focused {
			b.WriteString(" [focused]")
		}
		if n.Disabled {
			b.WriteString(" [disabled]")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// formatSnapshotCompact renders nodes in a minimal one-line-per-node format.
// ~60-70% fewer tokens than JSON. Format: ref:role "name" [val="v"] [flags]
func formatSnapshotCompact(nodes []A11yNode) string {
	var b strings.Builder
	for _, n := range nodes {
		b.WriteString(n.Ref)
		b.WriteByte(':')
		b.WriteString(n.Role)
		if n.Name != "" {
			b.WriteString(` "`)
			b.WriteString(n.Name)
			b.WriteByte('"')
		}
		if n.Value != "" {
			b.WriteString(` val="`)
			b.WriteString(n.Value)
			b.WriteByte('"')
		}
		if n.Focused {
			b.WriteString(" *")
		}
		if n.Disabled {
			b.WriteString(" -")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// truncateToTokens trims nodes to stay under a rough token budget.
// Returns the truncated slice and whether truncation occurred.
// Token estimate: compact/text ~= len/4, json ~= len/3.
func truncateToTokens(nodes []A11yNode, maxTokens int, format string) ([]A11yNode, bool) {
	tokensUsed := 0
	for i, n := range nodes {
		// Estimate tokens for this node based on format
		var nodeTokens int
		switch format {
		case "compact":
			// ref:role "name" ≈ compact
			size := len(n.Ref) + 1 + len(n.Role) + len(n.Name) + len(n.Value) + 8
			nodeTokens = size / 4
		case "text":
			size := n.Depth*2 + len(n.Ref) + 1 + len(n.Role) + len(n.Name) + len(n.Value) + 8
			nodeTokens = size / 4
		default:
			// JSON is more verbose
			size := len(n.Ref) + len(n.Role) + len(n.Name) + len(n.Value) + 60
			nodeTokens = size / 3
		}
		if nodeTokens < 1 {
			nodeTokens = 1
		}
		tokensUsed += nodeTokens
		if tokensUsed > maxTokens {
			return nodes[:i], true
		}
	}
	return nodes, false
}

// filterSubtree returns only nodes that are descendants of the node with
// the given backendDOMNodeID. Uses the a11y tree's childIds to walk the subtree.
func filterSubtree(nodes []rawAXNode, scopeBackendID int64) []rawAXNode {
	// Find the a11y node whose backendDOMNodeId matches
	scopeAXID := ""
	for _, n := range nodes {
		if n.BackendDOMNodeID == scopeBackendID {
			scopeAXID = n.NodeID
			break
		}
	}
	if scopeAXID == "" {
		return nodes // scope not found, return all
	}

	// BFS to find all descendant node IDs
	childMap := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		childMap[n.NodeID] = append(childMap[n.NodeID], n.ChildIDs...)
	}

	include := make(map[string]bool)
	include[scopeAXID] = true
	queue := []string{scopeAXID}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, cid := range childMap[cur] {
			if !include[cid] {
				include[cid] = true
				queue = append(queue, cid)
			}
		}
	}

	result := make([]rawAXNode, 0, len(include))
	for _, n := range nodes {
		if include[n.NodeID] {
			result = append(result, n)
		}
	}
	return result
}

// diffSnapshot returns only nodes that changed between prev and current snapshots.
// A node is "changed" if it's new, removed, or has different name/value/focused/disabled.
// Returns added, changed, and removed nodes.
func diffSnapshot(prev, curr []A11yNode) (added, changed, removed []A11yNode) {
	prevMap := make(map[string]A11yNode, len(prev))
	for _, n := range prev {
		key := fmt.Sprintf("%s:%s:%d", n.Role, n.Name, n.NodeID)
		prevMap[key] = n
	}

	currMap := make(map[string]bool, len(curr))
	for _, n := range curr {
		key := fmt.Sprintf("%s:%s:%d", n.Role, n.Name, n.NodeID)
		currMap[key] = true
		old, existed := prevMap[key]
		if !existed {
			added = append(added, n)
		} else if old.Value != n.Value || old.Focused != n.Focused || old.Disabled != n.Disabled {
			changed = append(changed, n)
		}
	}

	for _, n := range prev {
		key := fmt.Sprintf("%s:%s:%d", n.Role, n.Name, n.NodeID)
		if !currMap[key] {
			removed = append(removed, n)
		}
	}

	return
}
