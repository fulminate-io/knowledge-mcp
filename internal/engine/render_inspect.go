// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// render_inspect.go holds the client-side render shape for query(mode:examine).
// The InterceptQueryExamine composer (cmd/knowledge/internal/tools) walks the
// subject ByID + edge-neighborhood + CONTAINS-backward ancestry over generic
// Execute primitives and fills InspectData; renderInspectNode (added in the
// chain-wiring step) ports the server's since-removed
// renderInspectHeader/Ancestry/Edges markdown over it.
//
// The types are exported so the tools-package composer can fill them while the
// renderer stays in the engine package alongside the other render_*.go bodies.

// InspectAncestor is one resolved ancestry hop (a CONTAINS-parent), depth-tagged
// from the subject (DepthAbove 1 = direct parent). Name/Type/Status are resolved
// from the single bulk hydrate over the combined peer+ancestor id set.
type InspectAncestor struct {
	ID         string
	Name       string
	Type       string
	Status     string
	DepthAbove int
}

// InspectEdge is one edge in the subject's neighborhood. Direction is "out" or
// "in"; Peer is the peer node ID; PeerType/PeerName are resolved from the bulk
// hydrate (empty when the edge dangles to a missing node).
type InspectEdge struct {
	Direction string
	Type      string
	Peer      string
	PeerType  string
	PeerName  string
}

// InspectData is the fully-composed examine subject: the subject node, its
// ordered ancestry chain, and its edge neighborhood. The composer fills it; the
// renderer (markdown) and buildInspectJSON (json) consume it.
type InspectData struct {
	Node     *knowledgev1.Node
	Ancestry []InspectAncestor
	Edges    []InspectEdge
	// Truncated is the SERVER's verdict for this examine read. Today exactly one
	// of the composition's reads can set it: the bulk peer+ancestor hydrate, an
	// unbounded QueryPlan{Ids} the server clamps above 10,000 ids. The subject read
	// is by-id (one row), and the edge neighborhood rides a drain that never
	// returns a short union — it completes or errors by name.
	Truncated bool
}

// inspectIDTrunc truncates an ID to its first 12 chars (or fewer) — the
// load-bearing readability truncation the server applied on ancestry/edge
// lines, carried forward by this port.
func inspectIDTrunc(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// RenderInspectNode renders the markdown examine view from composed InspectData,
// a port of the server's since-removed renderInspectHeader +
// renderInspectAncestry + renderInspectEdges. Three
// sections: Composite View, Ancestry (back-arrow chain, id truncated to 12,
// orphan empty case), Edges (arrow lines with peer type+name, no-edges and
// dangling-edge cases).
func RenderInspectNode(data InspectData) string {
	var sb strings.Builder
	renderInspectHeader(&sb, data.Node)
	renderInspectAncestry(&sb, data.Ancestry)
	renderInspectEdges(&sb, data.Edges)
	return sb.String()
}

// renderInspectHeader ports the server renderInspectHeader: the Inspect title +
// Composite View bullet block.
func renderInspectHeader(sb *strings.Builder, node *knowledgev1.Node) {
	name := node.SymbolName
	if name == "" {
		name = node.Id
	}
	fmt.Fprintf(sb, "# Inspect: %s\n\n", name)
	fmt.Fprintf(sb, "## Composite View\n")
	fmt.Fprintf(sb, "- **ID:** %s\n", node.Id)
	fmt.Fprintf(sb, "- **Type:** %s\n", node.Type)
	fmt.Fprintf(sb, "- **Status:** %s\n", node.Status)
	if node.Source != "" {
		fmt.Fprintf(sb, "- **Source:** %s\n", node.Source)
	}
	if !nanosToTime(node.CreatedAt).IsZero() {
		fmt.Fprintf(sb, "- **Created:** %s\n", nanosToTime(node.CreatedAt).Format("2006-01-02 15:04"))
	}
	if !nanosToTime(node.UpdatedAt).IsZero() {
		fmt.Fprintf(sb, "- **Updated:** %s\n", nanosToTime(node.UpdatedAt).Format("2006-01-02 15:04"))
	}
	sb.WriteString("\n")
}

// renderInspectAncestry ports the server renderInspectAncestry: the back-arrow
// chain with the id truncated to 12 chars and the orphan-node empty case. The
// composer already resolved each ancestor's name/type/status.
func renderInspectAncestry(sb *strings.Builder, ancestry []InspectAncestor) {
	fmt.Fprintf(sb, "## Ancestry\n")
	for _, a := range ancestry {
		pName := a.Name
		if pName == "" {
			pName = a.ID
		}
		indent := strings.Repeat("  ", a.DepthAbove-1)
		fmt.Fprintf(sb, "%s← [%s] %s (status: %s, id: %s)\n", indent, a.Type, pName, a.Status, inspectIDTrunc(a.ID))
	}
	if len(ancestry) == 0 {
		sb.WriteString("(no parent — orphan node)\n")
	}
	sb.WriteString("\n")
}

// renderInspectEdges ports the server renderInspectEdges: outgoing then incoming
// arrow lines with peer type+name, the no-edges empty case, and the
// dangling-edge case (a peer that did not resolve in the bulk hydrate).
func renderInspectEdges(sb *strings.Builder, edges []InspectEdge) {
	fmt.Fprintf(sb, "## Edges\n")
	if len(edges) == 0 {
		sb.WriteString("(no edges)\n")
	}
	for _, e := range edges {
		arrow := "→"
		if e.Direction == "in" {
			arrow = "←"
		}
		// A peer that did not resolve (no type+name) is a dangling edge.
		if e.PeerType == "" && e.PeerName == "" {
			fmt.Fprintf(sb, "  %s [%s] [missing] %s (dangling edge)\n", arrow, e.Type, inspectIDTrunc(e.Peer))
			continue
		}
		name := e.PeerName
		if name == "" {
			name = inspectIDTrunc(e.Peer)
		}
		fmt.Fprintf(sb, "  %s [%s] [%s] %s\n", arrow, e.Type, e.PeerType, name)
	}
	sb.WriteString("\n")
}
