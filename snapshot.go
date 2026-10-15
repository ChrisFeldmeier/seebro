package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// A11yNode is a flattened accessibility tree node returned by /snapshot.
// Refs (e0, e1, ...) are stable within a snapshot and cached for use by /action.
type A11yNode struct {
	Ref       string   `json:"ref"`
	Role      string   `json:"role"`
	Name      string   `json:"name"`
	Depth     int      `json:"depth"`
	Value     string   `json:"value,omitempty"`
	Disabled  bool     `json:"disabled,omitempty"`
	Focused   bool     `json:"focused,omitempty"`
	Checked   bool     `json:"checked,omitempty"`
	Expanded  bool     `json:"expanded,omitempty"`
	Collapsed bool     `json:"collapsed,omitempty"`
	Required  bool     `json:"required,omitempty"`
	Options   []string `json:"options,omitempty"`
	Href      string   `json:"href,omitempty"`
	Frame     string   `json:"frame,omitempty"` // child frame ("f1 https://…"); empty for the main frame
	NodeID    int64    `json:"nodeId,omitempty"`
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

		ref := "a" + strconv.Itoa(refID) // "a" = accessibility tree; "e" refs come from the engine
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

// nodeFlags renders state markers shared by the text formats.
func nodeFlags(b *strings.Builder, n A11yNode, compact bool) {
	if len(n.Options) > 0 {
		b.WriteString(` opts=[`)
		b.WriteString(strings.Join(n.Options, "|"))
		b.WriteByte(']')
	}
	if n.Checked {
		b.WriteString(" checked")
	}
	if n.Expanded {
		b.WriteString(" expanded")
	}
	if n.Collapsed {
		b.WriteString(" collapsed")
	}
	if n.Required {
		b.WriteString(" required")
	}
	if compact {
		if n.Focused {
			b.WriteString(" *")
		}
		if n.Disabled {
			b.WriteString(" -")
		}
	} else {
		if n.Focused {
			b.WriteString(" [focused]")
		}
		if n.Disabled {
			b.WriteString(" [disabled]")
		}
	}
	if n.Href != "" {
		b.WriteString(" -> ")
		b.WriteString(n.Href)
	}
}

// frameHeader prints a "# frame …" line whenever the frame changes.
func frameHeader(b *strings.Builder, n A11yNode, last *string) {
	if n.Frame != *last {
		*last = n.Frame
		if n.Frame != "" {
			b.WriteString("# frame ")
			b.WriteString(n.Frame)
			b.WriteByte('\n')
		}
	}
}

// formatSnapshotText renders nodes as an indented text tree.
// Much cheaper on tokens than JSON (~40-60% reduction).
func formatSnapshotText(nodes []A11yNode) string {
	var b strings.Builder
	frame := ""
	for _, n := range nodes {
		frameHeader(&b, n, &frame)
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
		nodeFlags(&b, n, false)
		b.WriteByte('\n')
	}
	return b.String()
}

// formatSnapshotCompact renders nodes in a minimal one-line-per-node format.
// ~60-70% fewer tokens than JSON. Format: ref:role "name" [val="v"] [flags]
func formatSnapshotCompact(nodes []A11yNode) string {
	var b strings.Builder
	frame := ""
	for _, n := range nodes {
		frameHeader(&b, n, &frame)
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
		nodeFlags(&b, n, true)
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

// diffKey identifies a node across snapshots. Engine refs are stable per
// element, so they are the key; accessibility-tree refs are positional, so
// those nodes are keyed by role, name and DOM node.
func diffKey(n A11yNode) string {
	if n.NodeID == 0 {
		return n.Ref
	}
	return fmt.Sprintf("%s:%s:%d", n.Role, n.Name, n.NodeID)
}

func nodeChanged(a, b A11yNode) bool {
	return a.Name != b.Name || a.Value != b.Value || a.Focused != b.Focused || a.Disabled != b.Disabled ||
		a.Checked != b.Checked || a.Expanded != b.Expanded || a.Collapsed != b.Collapsed ||
		strings.Join(a.Options, "|") != strings.Join(b.Options, "|")
}

// diffSnapshot returns only nodes that changed between prev and current snapshots.
// A node is "changed" if it's new, removed, or has different name/value/state.
// Returns added, changed, and removed nodes.
func diffSnapshot(prev, curr []A11yNode) (added, changed, removed []A11yNode) {
	prevMap := make(map[string]A11yNode, len(prev))
	for _, n := range prev {
		prevMap[diffKey(n)] = n
	}

	currMap := make(map[string]bool, len(curr))
	for _, n := range curr {
		key := diffKey(n)
		currMap[key] = true
		old, existed := prevMap[key]
		if !existed {
			added = append(added, n)
		} else if nodeChanged(old, n) {
			changed = append(changed, n)
		}
	}

	for _, n := range prev {
		if !currMap[diffKey(n)] {
			removed = append(removed, n)
		}
	}

	return
}
