// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptQueryLineage serves query(mode:"lineage")
// client-side. It began as a port of the server-side handleTraceLineage
// + buildLineageJSON + formatLineageNode handlers; all three are gone —
// verified repo-wide by symbol and by path — and no server-side lineage
// arm replaced them, so this intercept is the whole implementation.
//
// Walks the provenance chain upward from a node via three edge
// probes per step (reverse contains, reverse implements, forward
// informed-by). Up to depth 10. Markdown + JSON formats both
// preserved, each with a byte-parity golden (testdata/lineage.golden,
// testdata/lineage.json.golden).

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// InterceptQueryLineage claims query(mode:"lineage"). Returns
// (true, result) on match.
func InterceptQueryLineage(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Mode != "lineage" {
		return false, kgtools.ToolResult{}
	}
	if a.ID == "" {
		return true, errorResult("lineage mode requires 'id' parameter")
	}

	if err := accountQueryParams(armLineage, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("lineage: graph caller unavailable")
	}

	node, err := render.FetchNode(ctx, gc, a.ID)
	if err != nil {
		return true, errorResult(fmt.Sprintf("query failed: %s", err))
	}
	if node == nil {
		return true, errorResult(fmt.Sprintf("node %s not found", a.ID))
	}

	if a.Format == "json" {
		return true, jsonResult(buildLineageJSON(ctx, gc, node))
	}
	return true, kgtools.TextResult(renderLineageMarkdown(ctx, gc, node))
}

// renderLineageMarkdown renders the start node's header, then walks up
// to ten hops upward, emitting one formatLineageNode block per parent,
// and falls through to the root-node hint when the first hop finds
// nothing.
//
// PROVENANCE, NOT A POINTER: this wording was ported byte-for-byte from
// the markdown branch of the server-side handleTraceLineage, which no
// longer exists by symbol or by path. WHAT HOLDS THE FORMAT NOW is
// TestInterceptQueryLineage_TextFormat_DeepChain_ByteIdentical against
// testdata/lineage.golden for the populated chain, and
// TestInterceptQueryLineage_RootNode_ShowsHint for the no-parent
// branch — those assertions, not the vanished port source, are the
// standing contract.
func renderLineageMarkdown(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Lineage for: %s (%s)\n", node.SymbolName, node.Type)
	if node.Summary != "" {
		fmt.Fprintf(&sb, "  %s\n", node.Summary)
	}
	fmt.Fprintf(&sb, "  ID: %s\n", node.Id)

	current := node.Id
	depth := 0
	seen := map[string]bool{current: true}
	for depth < 10 {
		depth++
		parentID, edgeType := nextLineageParent(ctx, gc, current)
		if parentID == "" || seen[parentID] {
			break
		}
		seen[parentID] = true
		parent, ferr := render.FetchNode(ctx, gc, parentID)
		if ferr != nil || parent == nil {
			break
		}
		formatLineageNode(&sb, parent, depth, edgeType)
		current = parentID
	}
	if depth == 1 {
		sb.WriteString("\n  (no lineage found — this is a root node)")
	}
	return sb.String()
}

// nextLineageParent attempts the three edge probes the since-deleted
// server-side handler used (reverse contains → reverse implements →
// forward informed-by) and returns the first hit (parentID, edgeType).
// Empty strings = no parent on this hop.
//
// Only the contains probe is exercised anywhere in this package: the
// shared fixture linker emits contains edges exclusively, and
// kgtypes.EdgeKGImplements appears in the package at this line and
// nowhere else. So the implements and informed-by arms, and the
// precedence between all three, are unpinned.
func nextLineageParent(ctx context.Context, gc GraphCaller, current string) (string, string) {
	if edges, _ := render.IterEdges(ctx, gc, current, kgwire.IncomingEdges, kgtypes.EdgeKGContains); len(edges) > 0 {
		return edges[0].FromId, "contains"
	}
	if edges, _ := render.IterEdges(ctx, gc, current, kgwire.IncomingEdges, kgtypes.EdgeKGImplements); len(edges) > 0 {
		return edges[0].FromId, "implements"
	}
	if edges, _ := render.IterEdges(ctx, gc, current, kgwire.OutgoingEdges, kgtypes.EdgeInformedBy); len(edges) > 0 {
		return edges[0].ToId, "informed-by"
	}
	return "", ""
}

// formatLineageNode emits one parent block: a blank line, the "↑ <edge>"
// arrow, the name/type line with an optional [status] suffix, an optional
// summary line, and the ID line. The block's base indent is two spaces per
// hop; the summary and ID lines sit two further spaces in from that base.
//
// PROVENANCE, NOT A POINTER: this body was ported line-for-line from a
// server-side function of the same name, now gone by symbol and by path;
// only the parent parameter changed (a store node became the wire node,
// so .ID became .Id). WHAT PINS THE LAYOUT NOW is testdata/lineage.golden.
// Note its reach: every node in that fixture carries both a status and a
// summary, so the golden pins the fully-populated block. The two
// conditional branches — empty status, empty summary — have no fixture in
// this package and are therefore unpinned.
func formatLineageNode(sb *strings.Builder, parent *knowledgev1.Node, depth int, edgeType string) {
	indent := strings.Repeat("  ", depth)
	fmt.Fprintf(sb, "\n%s↑ %s\n", indent, edgeType)
	fmt.Fprintf(sb, "%s%s (%s)", indent, parent.SymbolName, parent.Type)
	if parent.Status != "" {
		fmt.Fprintf(sb, " [%s]", parent.Status)
	}
	sb.WriteString("\n")
	if parent.Summary != "" {
		fmt.Fprintf(sb, "%s  %s\n", indent, parent.Summary)
	}
	fmt.Fprintf(sb, "%s  ID: %s\n", indent, parent.Id)
}

// lineageRow is the per-hop JSON row for query(mode:"lineage",
// format:"json"). It began as a named copy of an anonymous struct
// declared inside the since-deleted server-side buildLineageJSON; that
// file is gone by path and the type with it.
//
// WHAT PINS THE SHAPE NOW is testdata/lineage.json.golden, asserted
// byte-for-byte by TestInterceptQueryLineage_JSONFormat_ByteIdentical —
// field set and field order, for every field including the omitempty
// summary, since each node in that fixture carries one.
type lineageRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Summary  string `json:"summary,omitempty"`
	EdgeType string `json:"edge_type"`
	Depth    int    `json:"depth"`
}

// buildLineageJSON assembles the lineage payload: a start header, then
// the same upward walk renderLineageMarkdown performs, as one row per hop.
//
// PROVENANCE, NOT A POINTER: it is a port of a server-side function of
// the same name, gone by symbol and by path along with the file that held
// it. Two things changed in the port. Every direct store query became a
// wire call — render.FetchNode for a node by ID, render.IterEdges for an
// edge walk — because a client-side intercept has no store handle. And
// the three edge probes, which the server duplicated inline in both its
// JSON and its markdown path, were hoisted into the shared
// nextLineageParent above, so the two paths here cannot drift apart.
func buildLineageJSON(ctx context.Context, gc GraphCaller, node *knowledgev1.Node) map[string]any {
	out := map[string]any{
		"start": map[string]any{
			"id":      node.Id,
			"name":    node.SymbolName,
			"type":    node.Type,
			"summary": node.Summary,
		},
	}
	chain := make([]lineageRow, 0, 10)
	current := node.Id
	depth := 0
	seen := map[string]bool{current: true}
	for depth < 10 {
		depth++
		parentID, edgeType := nextLineageParent(ctx, gc, current)
		if parentID == "" || seen[parentID] {
			break
		}
		seen[parentID] = true
		parent, ferr := render.FetchNode(ctx, gc, parentID)
		if ferr != nil || parent == nil {
			break
		}
		chain = append(chain, lineageRow{
			ID: parent.Id, Name: parent.SymbolName, Type: parent.Type,
			Summary: parent.Summary, EdgeType: edgeType, Depth: depth,
		})
		current = parentID
	}
	out["lineage"] = chain
	return out
}
