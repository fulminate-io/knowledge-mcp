// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// truncate mirrors the server truncate helper (helpers.go:133): trims s to n
// runes (bytes) with a trailing "..." when longer.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// jsonResult mirrors the server jsonResult helper (helpers.go:156): marshals
// data as JSON into a text ToolResult, returning an error result on failure.
func jsonResult(data any) kgtools.ToolResult {
	b, err := json.Marshal(data)
	if err != nil {
		return errorResult("json marshal: " + err.Error())
	}
	return kgtools.TextResult(string(b))
}

// render_misc.go mirrors the server-side browse / traversal / bulk-ids /
// mutation renderers (renderGenericBrowse / renderTraversalText+JSON /
// renderGenericNodesByIDs / the mutate success texts in
// cmd/knowledge-server/tools) client-side — those live on the wrong side of the
// import boundary. Field order + markdown are reproduced against the verified
// server functions; the engine returns the DATA (NodeList / merged
// TraversalList / mutation Ids+AffectedCount), this file renders it.

// browseContext carries the render inputs the dispatcher derives from the query
// args for a type-browse / meta-only response.
type browseContext struct {
	Label    string
	NodeType string
	Offset   int
	Format   string
	Fields   []string
	MetaKeys []string // the meta filter keys, surfaced inline per node.
}

// renderBrowseResponse mirrors renderGenericBrowse (tools_query_dispatch.go:343)
// + renderGenericBrowseJSON (419): the numbered list with status + ID +
// truncated description + inline meta values + pagination footer, or the JSON
// {graph, type, results, total} payload.
func renderBrowseResponse(resp *knowledgev1.ExecuteResponse, c browseContext) (kgtools.ToolResult, error) {
	nodes, err := decodeNodes(resp)
	if err != nil {
		return kgtools.ToolResult{}, err
	}
	total := int(resp.GetTotal())

	if c.Format == "json" {
		return renderBrowseJSON(c, nodes, total), nil
	}
	if len(nodes) == 0 {
		if c.NodeType == "" {
			return kgtools.TextResult(fmt.Sprintf("No nodes in %s graph match the requested filters.", c.Label)), nil
		}
		return kgtools.TextResult(fmt.Sprintf("No %s nodes in %s graph.", c.NodeType, c.Label)), nil
	}

	var sb strings.Builder
	if c.NodeType == "" {
		fmt.Fprintf(&sb, "## %s — %d nodes", c.Label, len(nodes))
	} else {
		fmt.Fprintf(&sb, "## %s — %d %s nodes", c.Label, len(nodes), c.NodeType)
	}
	if c.Offset > 0 {
		fmt.Fprintf(&sb, " (offset %d)", c.Offset)
	}
	sb.WriteString("\n\n")
	for i, n := range nodes {
		name := n.SymbolName
		if name == "" {
			name = n.Id
		}
		fmt.Fprintf(&sb, "%d. **%s**", c.Offset+i+1, name)
		if n.Status != "" {
			fmt.Fprintf(&sb, " [%s]", n.Status)
		}
		fmt.Fprintf(&sb, "\n   ID: %s", n.Id)
		if n.Description != "" {
			fmt.Fprintf(&sb, "\n   %s", truncate(n.Description, 120))
		}
		for _, k := range c.MetaKeys {
			if v := kgtypes.Value(n, k); v != "" {
				fmt.Fprintf(&sb, "\n   %s: %s", k, truncate(v, 120))
			}
		}
		sb.WriteString("\n\n")
	}
	if c.Offset+len(nodes) < total {
		fmt.Fprintf(&sb, "_Use offset=%d to see more._\n", c.Offset+len(nodes))
	}
	return kgtools.TextResult(sb.String()), nil
}

// renderBrowseJSON mirrors renderGenericBrowseJSON (tools_query_dispatch.go:419):
// {graph, type, results:[...], total}. Each row is the full-node projection or
// the requested-fields projection.
func renderBrowseJSON(c browseContext, nodes []*knowledgev1.Node, total int) kgtools.ToolResult {
	rows := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		if len(c.Fields) == 0 {
			rows = append(rows, fullNodeJSON(n))
			continue
		}
		rows = append(rows, ProjectNodeJSON(n, c.Fields))
	}
	return jsonResult(map[string]any{
		"graph":   c.Label,
		"type":    c.NodeType,
		"results": rows,
		"total":   total,
	})
}

// BrowseJSONResult builds the {graph, type, results, total} browse-JSON envelope
// — the handleBrowseJSON contract every server-side type-browse emits and the
// agent graph-explorer BrowseResponse consumes — from an already-fetched node
// set. Exported so the client-side type-browse intercepts that render markdown by
// default (decisions, rules) can honor format:"json" with the SAME envelope the
// server browse returns for every other node type, reusing the nodes they already
// fetched (no second wire call). An empty fields list yields the full-node
// projection (fullNodeJSON: id/name/type/status?/metadata?); a non-empty list
// projects through ProjectNodeJSON. tools→engine import is one-way (no cycle).
func BrowseJSONResult(graphLabel, nodeType string, nodes []*knowledgev1.Node, total int, fields []string) kgtools.ToolResult {
	return renderBrowseJSON(browseContext{Label: graphLabel, NodeType: nodeType, Fields: fields}, nodes, total)
}

// fullNodeJSON mirrors the server fullNodeJSON (tools_query_dispatch_project.go):
// id + name + type, plus status + metadata when present.
func fullNodeJSON(n *knowledgev1.Node) map[string]any {
	row := map[string]any{
		"id":   n.Id,
		"name": n.SymbolName,
		"type": n.Type,
	}
	if n.Status != "" {
		row["status"] = n.Status
	}
	if len(n.Metadata) > 0 {
		md := make(map[string]string, len(n.Metadata))
		maps.Copy(md, n.Metadata)
		row["metadata"] = md
	}
	return row
}

// ProjectNodeJSON mirrors the server projectNodeJSON projection grammar:
// top-level id/name/type/status/description + per-metadata-key "metadata.<key>"
// + bare "metadata" (full map). Unknown keys dropped. Exported so the
// cmd/knowledge/internal/tools container-listing intercept reuses the SAME
// grammar (tools→engine import is one-way; no cycle) rather than copy-pasting it.
func ProjectNodeJSON(n *knowledgev1.Node, fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "id":
			out["id"] = n.Id
		case "name":
			out["name"] = n.SymbolName
		case "type":
			out["type"] = n.Type
		case "status":
			out["status"] = n.Status
		case "description":
			out["description"] = n.Description
		case "metadata":
			if len(n.Metadata) > 0 {
				md := make(map[string]string, len(n.Metadata))
				maps.Copy(md, n.Metadata)
				out["metadata"] = md
			}
		default:
			if key, ok := strings.CutPrefix(f, "metadata."); ok {
				if v := kgtypes.Value(n, key); v != "" {
					out[f] = v
				}
			}
		}
	}
	return out
}

// traverseContext carries the render inputs the dispatcher derives from the
// traverse args.
type traverseContext struct {
	Start     string
	GraphName string // "" → "knowledge"
	Direction string
	Format    string
}

// renderTraversalResponse mirrors renderTraversalText (tools_traverse_generic.go:310)
// + renderTraversalJSON (345). The engine returns the both-union ALREADY MERGED
// (T2.4a #6), so the client renders the flat list — it NEVER re-derives the
// union. When include_edge_metadata was set, the engine populates
// resp.TraversalEdges with the per-edge metadata ([]knowledgev1.Edge); the client
// renders it as an edges section (T2.4c). When the carrier is empty the edges
// section is absent (edges:[] in JSON).
//
// Multi-candidate edge groups are reconstructed from those same edges and
// rendered as ONE block per group; their member edges are WITHHELD from the flat
// edges section, so a candidate appears under its group and nowhere else. Groups
// render whenever they exist, independent of the caller's include_edge_metadata
// — code-graph walks request the carrier for themselves (compileTraverse).
func renderTraversalResponse(resp *knowledgev1.ExecuteResponse, c traverseContext) (kgtools.ToolResult, error) {
	results, err := decodeTraversal(resp)
	if err != nil {
		return kgtools.ToolResult{}, err
	}
	edges := decodeTraversalEdges(resp)
	graph := c.GraphName
	if graph == "" {
		graph = "knowledge"
	}

	// Reconstruct groups and apply the frontier short-circuit ONCE, before either
	// arm renders, so text and JSON list exactly the same nodes — a divergence
	// between the arms would be a second, silent semantics.
	g := prepareTraversalGroups(c.Start, results, edges, resp.GetTruncated())
	results, ungrouped := g.results, g.ungrouped

	if c.Format == "json" {
		nodes := make([]map[string]any, 0, len(results))
		for _, r := range results {
			nodes = append(nodes, map[string]any{
				"id":          r.Node.Id,
				"name":        r.Node.SymbolName,
				"type":        r.Node.Type,
				"status":      r.Node.Status,
				"description": r.Node.Description,
			})
		}
		payload := map[string]any{
			"start":     c.Start,
			"graph":     graph,
			"direction": c.Direction,
			"nodes":     nodes,
			// Only the UNGROUPED remainder: a group's members appear under
			// edge_groups and nowhere else, so a JSON consumer never reads N
			// alternatives as N independent facts.
			"edges": edgeMetadataJSON(ungrouped),
		}
		attachCandidateGroupsJSON(payload, g.groups, traversalNodeIndex(results), g.reached, g.incomplete)
		return jsonResult(payload), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Traversal from %s (graph=%s, direction=%s)\n\n", c.Start, graph, c.Direction)
	if len(results) == 0 {
		sb.WriteString("No nodes reached.\n")
		return kgtools.TextResult(sb.String()), nil
	}
	for _, r := range results {
		name := traversalNodeName(r.Node)
		line := fmt.Sprintf("- [%s] %s (%s) at depth %d", r.Node.Type, name, r.Node.Id, r.Distance)
		if proxy := proxyMetadataAnnotation(r.Node); proxy != "" {
			line += " " + proxy
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	// Multi-candidate groups render as ONE block each, and their member edges are
	// withheld from the flat edges section below so a candidate is never restated
	// as an independent fact. With no groups this costs zero bytes of output.
	writeCandidateGroups(&sb, g.groups, traversalNodeIndex(results), g.reached)
	if g.incomplete {
		sb.WriteString("\ngroup reconstruction incomplete - some candidates or reachable nodes are not shown\n")
	}
	writeEdgeMetadataSection(&sb, ungrouped, c.Direction)
	return kgtools.TextResult(sb.String()), nil
}

// traversalNodeName mirrors nodeDisplayName (tools_query_linkage.go:347):
// SymbolName → FilePath → ID.
func traversalNodeName(n *knowledgev1.Node) string {
	if n.SymbolName != "" {
		return n.SymbolName
	}
	if n.FilePath != "" {
		return n.FilePath
	}
	return n.Id
}

// proxyMetadataAnnotation mirrors proxyAnnotation (tools_traverse_proxy.go) —
// the DB-FREE proxy annotation derived from node metadata. The engine resolves
// cross-graph proxies server-side (crossGraphFallback), so a TraversalList node
// is normally a real node; an unresolved proxy gets the metadata annotation,
// matching the server's resolveProxyIfNeeded fallback path. Reads the proxy via
// the shared kgwire reader (the engine-local isProxyWire/proxyInfoWire copies
// were collapsed onto kgwire.IsProxy / kgwire.ProxyInfo per the CEO proxy-family
// correction).
func proxyMetadataAnnotation(n *knowledgev1.Node) string {
	if !kgwire.IsProxy(n) {
		return ""
	}
	info := kgwire.ProxyInfo(n)
	if info == nil {
		return "[proxy]"
	}
	var parts []string
	if info.GetGraphType() != "" {
		parts = append(parts, info.GetGraphType())
	}
	if info.GetName() != "" {
		parts = append(parts, info.GetName())
	}
	if info.GetNodeId() != "" {
		parts = append(parts, info.GetNodeId())
	}
	if len(parts) == 0 {
		return "[proxy]"
	}
	return "[proxy → " + strings.Join(parts, ":") + "]"
}

// renderNodesByIDsResponse mirrors renderGenericNodesByIDs (tools_query_ids.go:24):
// the bulk-hydrate {label, nodes:[]} JSON (default) or the text fallback
// concatenating the node body. The engine's ids[] read returns the NodeList.
// When fields is non-empty, the JSON arm projects each node through the
// ProjectNodeJSON grammar — the bulk-ids shape is a known large-response shape,
// so the tool-wide `fields` projection MUST reach it (the prior raw-node marshal
// ignored fields entirely). An empty fields list preserves the full-node marshal.
func renderNodesByIDsResponse(resp *knowledgev1.ExecuteResponse, label, format string, fields []string) (kgtools.ToolResult, error) {
	nodes, err := decodeNodes(resp)
	if err != nil {
		return kgtools.ToolResult{}, err
	}
	if format == "json" || format == "" {
		if len(fields) > 0 {
			rows := make([]map[string]any, len(nodes))
			for i, n := range nodes {
				rows[i] = ProjectNodeJSON(n, fields)
			}
			return jsonResult(map[string]any{
				"label": label,
				"nodes": rows,
			}), nil
		}
		return jsonResult(map[string]any{
			"label": label,
			"nodes": nodes,
		}), nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s nodes (%d)\n\n", label, len(nodes))
	for _, n := range nodes {
		writeNodeBody(&sb, n)
		sb.WriteString("\n---\n\n")
	}
	return kgtools.TextResult(sb.String()), nil
}

// renderMutationResponse renders a MutationPlan outcome:
// CREATE renders the created Ids (the engine
// threads them into ExecuteResponse.Ids — T2.4a #9); the predicate arms
// (UPDATE/DELETE/LINK/UNLINK) render the AffectedCount. The engine response
// carries only Ids + AffectedCount — node names / changed-field lists / resolved
// endpoints are not on the wire, so this is the IDs+count surface the criterion
// scopes (not the rich legacy strings). A validation error from the engine
// surfaces as an Execute error rendered by the dispatcher, not here.
func renderMutationResponse(resp *knowledgev1.ExecuteResponse, kind knowledgev1.MutationPlan_MutationKind, format string) kgtools.ToolResult {
	if kind == knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
		ids := resp.GetIds()
		if format == "json" {
			return jsonResult(map[string]any{"ids": ids})
		}
		if len(ids) == 1 {
			return kgtools.TextResult(fmt.Sprintf("Created → ID: %s", ids[0]))
		}
		return kgtools.TextResult(fmt.Sprintf("Created %d nodes → IDs: %s", len(ids), strings.Join(ids, ", ")))
	}

	affected := resp.GetAffectedCount()
	verb := mutationVerb(kind)
	if format == "json" {
		return jsonResult(map[string]any{"affected": affected})
	}
	return kgtools.TextResult(fmt.Sprintf("%s %d node(s)", verb, affected))
}

// mutationVerb maps a predicate-arm MutationKind to its past-tense verb for the
// affected-count render line.
func mutationVerb(kind knowledgev1.MutationPlan_MutationKind) string {
	switch kind {
	case knowledgev1.MutationPlan_MUTATION_KIND_UPDATE:
		return "Updated"
	case knowledgev1.MutationPlan_MUTATION_KIND_DELETE:
		return "Deleted"
	case knowledgev1.MutationPlan_MUTATION_KIND_LINK:
		return "Linked"
	case knowledgev1.MutationPlan_MUTATION_KIND_UNLINK:
		return "Unlinked"
	default:
		return "Affected"
	}
}
