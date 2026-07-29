// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// dispatch_graphwide.go holds the client-side composition of the
// graph-wide-edges traverse (a start-less traverse). A single compile→one-Execute
// cannot express the two-step "enumerate every node, then read every edge", so
// — like dispatchQueryByID composes include_edges — this routes through a
// pre-Compile composer in the Dispatch seam: a Match-all node enumeration + a
// match-all RETURN_MODE_EDGES read (NO new server arm — an edges plan with no
// pivot discriminant already means "all edges"). Split into a sibling file so
// dispatch.go stays under the 500-line cap.

// dispatchGraphWideEdges intercepts a start-less traverse (traverse with no
// Start) — the graph-wide-edges fast path the legacy handleTraverseGraphWideEdges
// served. It composes the result client-side and returns handled=true. For a
// from_id walk (Start set) or a logs traverse (rendered by the client intercept),
// it returns handled=false so Dispatch proceeds to the generic Compile/exec flow.
//
// BOUNDEDNESS: the edge read is ONE Execute regardless of edge cardinality. The
// node enumeration is split by output format — the JSON arm hydrates every node
// because it renders five fields per node, while the TEXT arm needs only a count
// and a membership set, so it drains ids in bounded keyset pages instead of
// hydrating the whole graph.
func dispatchGraphWideEdges(ctx context.Context, exec ExecuteFn, args json.RawMessage) (kgtools.ToolResult, bool) {
	var a traverseArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return kgtools.ToolResult{}, false // malformed → let the generic flow surface it.
	}
	if a.Start != "" || a.Graph == "logs" {
		return kgtools.ToolResult{}, false // from_id walk / logs intercept → not graph-wide.
	}
	// Re-stamp over the tool-level traverse term: this is the start-less
	// two-step (enumerate every node, then union their edges), a far heavier
	// shape than a bounded from_id walk and the one worth spotting in the
	// per-tag metrics.
	ctx = graphclient.WithOperation(ctx, graphclient.OpTraverseGraphWide)
	target := buildTarget(a.Graph, a.Repo, a.Account, a.Name, a.Language, a.Branch)

	if a.Format == "json" {
		return dispatchGraphWideJSON(ctx, exec, a, target)
	}
	return dispatchGraphWideText(ctx, exec, a, target)
}

// dispatchGraphWideJSON serves the hydrated arm: renderGraphWideJSON emits
// id/name/type/status/description per node and no wire projection carries that
// shape, so this enumeration stays a full match-all node read. It is a NAMED
// SURVIVOR of the bounded-reads census (bootstrap/bounded_reads_census_test.go)
// for exactly that reason — not an oversight.
func dispatchGraphWideJSON(ctx context.Context, exec ExecuteFn, a traverseArgs, target *knowledgev1.GraphSelector) (kgtools.ToolResult, bool) {
	nodesPlan := &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{}}
	applyTombstones(nodesPlan, a.IncludeTombstones)
	nodesResp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: nodesPlan},
		Target: target,
	})
	if err != nil {
		return renderEngineError(err), true
	}
	nodes, derr := DecodeNodes(nodesResp)
	if derr != nil {
		return errorResult("graph-wide node enumeration decode: " + derr.Error()), true
	}
	edges, err := graphWideEdgeUnion(ctx, exec, len(nodes), a.EdgeTypes, a.Graph, target)
	if err != nil {
		return renderEngineError(err), true
	}
	return renderGraphWideJSON(a, nodes, edges), true
}

// dispatchGraphWideText serves the text arm with an IDS-ONLY enumeration. The
// text render needs exactly three things from the node fetch — the emptiness
// gate, the node count, and the source-membership set for the dangling-edge
// filter — and bare ids supply all three, so hydrating 21 columns of every node
// to print two numbers was pure waste.
func dispatchGraphWideText(ctx context.Context, exec ExecuteFn, a traverseArgs, target *knowledgev1.GraphSelector) (kgtools.ToolResult, bool) {
	ids, err := DrainKeysetIDs(func(afterID string) ([]string, error) {
		cursor := afterID
		plan := &knowledgev1.QueryPlan{
			Selection:  &knowledgev1.Selection{},
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_IDS,
			Limit:      int32(BrowsePageSize),
			// SET on every page including the first, where the value is empty:
			// presence is what selects the keyset browse.
			AfterId:   &cursor,
			SkipTotal: true,
		}
		applyTombstones(plan, a.IncludeTombstones)
		resp, rerr := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
			Target: target,
		})
		if rerr != nil {
			return nil, rerr
		}
		return resp.GetIds(), nil
	}, BrowsePageSize)
	if err != nil {
		return renderEngineError(err), true
	}

	edges, uerr := graphWideEdgeUnion(ctx, exec, len(ids), a.EdgeTypes, a.Graph, target)
	if uerr != nil {
		return renderEngineError(uerr), true
	}
	return renderGraphWideText(a, len(ids), nodeIDSet(ids), edges), true
}

// nodeIDSet indexes the enumerated nodes for the render-side source membership
// check. The match-all read returns every stored edge, including a dangling one
// left behind when a node was hard-deleted without its edges: neither endpoint
// exists, and a vanished node is not a tombstoned node, so no tombstone filter
// catches it (~0.16% of edges on a real graph). The id-pivoted read this replaced
// could not return those, because a dangling endpoint is never in the pivot set.
// The renderers therefore skip an edge whose source is not in the enumerated set,
// keeping the rendered list exactly what it was — using the ids already fetched
// for the response, so no extra round trip.
func nodeIDSet(ids []string) map[string]struct{} {
	known := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		known[id] = struct{}{}
	}
	return known
}

// nodeIDsOf projects the hydrated JSON arm's nodes down to the id slice nodeIDSet
// now takes.
func nodeIDsOf(nodes []*knowledgev1.Node) []string {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.GetId())
	}
	return ids
}

// graphWideEdgeUnion issues ONE match-all RETURN_MODE_EDGES read — a plan with no
// pivot discriminant, which the engine reads as "every edge of the graph" — and
// returns those edges. nodeCount is the enumerated node count and gates the call:
// a graph with no nodes has no edges to render, and skipping the Execute keeps
// the empty-graph shape identical to the id-pivoted read this replaced. A count
// is all the gate ever needed, which is what lets the text arm enumerate ids
// only. The renderers apply the source-membership check (nodeIDSet). edge_types
// are canonicalized per-graph client-side.
func graphWideEdgeUnion(ctx context.Context, exec ExecuteFn, nodeCount int, edgeTypes []string, graph string, target *knowledgev1.GraphSelector) ([]knowledgev1.Edge, error) {
	if nodeCount == 0 {
		return nil, nil
	}
	sel := &knowledgev1.Selection{}
	if len(edgeTypes) > 0 {
		sel.EdgeTypes = canonicalEdgeCasings(graph, edgeTypes)
	}
	edgesPlan := &knowledgev1.QueryPlan{
		Selection:  sel,
		ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES,
	}
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: edgesPlan},
		Target: target,
	})
	if err != nil {
		return nil, err
	}
	return DecodeEdges(resp)
}

// renderGraphWideJSON emits the structured {start,graph,direction,nodes,edges}
// payload (mirrors the server renderGraphWideJSON). Every node carries
// id/name/type/status/description; every edge carries source/target/relationship
// and, when include_edge_metadata is set, an edge_metadata sub-object.
func renderGraphWideJSON(a traverseArgs, nodes []*knowledgev1.Node, edges []knowledgev1.Edge) kgtools.ToolResult {
	nodeRows := make([]graphWideNode, 0, len(nodes))
	for _, n := range nodes {
		nodeRows = append(nodeRows, graphWideNode{
			ID: n.Id, Name: n.SymbolName,
			Type: n.Type, Status: n.Status,
			Description: n.Description,
		})
	}
	known := nodeIDSet(nodeIDsOf(nodes))
	edgeRows := make([]map[string]any, 0, len(edges))
	for i := range edges {
		e := &edges[i]
		if _, ok := known[e.FromId]; !ok {
			continue // dangling source — see nodeIDSet.
		}
		row := map[string]any{
			"source":       e.FromId,
			"target":       e.ToId,
			"relationship": e.Type,
		}
		if a.IncludeEdgeMetadata {
			if meta := graphWideEdgeMetadata(e); len(meta) > 0 {
				row["edge_metadata"] = meta
			}
		}
		edgeRows = append(edgeRows, row)
	}
	return jsonResult(map[string]any{
		"start":     "",
		"graph":     graphWideLabel(a.Graph),
		"direction": a.Direction,
		"nodes":     nodeRows,
		"edges":     edgeRows,
	})
}

// renderGraphWideText is the human-readable fallback (mirrors the server
// renderGraphWideText): a tiny node/edge count summary. It takes the node COUNT
// and the id membership set rather than hydrated nodes — the only two things it
// ever read from them — so the text arm can enumerate ids only.
func renderGraphWideText(a traverseArgs, nodeCount int, known map[string]struct{}, edges []knowledgev1.Edge) kgtools.ToolResult {
	edgeCount := 0
	for i := range edges {
		if _, ok := known[edges[i].FromId]; ok {
			edgeCount++ // dangling sources are not counted — see nodeIDSet.
		}
	}
	body := fmt.Sprintf("## Graph-wide traversal (graph=%s, name=%s)\n\n", graphWideLabel(a.Graph), a.Name)
	body += fmt.Sprintf("- nodes: %d\n- edges: %d\n", nodeCount, edgeCount)
	if len(a.EdgeTypes) > 0 {
		body += fmt.Sprintf("- edge_types: %v\n", a.EdgeTypes)
	}
	return kgtools.TextResult(body)
}

// graphWideNode is the JSON node row (mirrors the server traverseNode,
// tools_traverse_generic.go:28).
type graphWideNode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
}

// graphWideLabel is the render-header graph label (mirrors the server graphLabel,
// tools_traverse_generic.go:250): empty graph → "knowledge".
func graphWideLabel(graph string) string {
	if graph == "" {
		return "knowledge"
	}
	return graph
}

// graphWideEdgeMetadata builds the {weight,confidence,method,evidence,
// last_validated} map for the JSON formatter (mirrors the server edgeMetadataMap,
// tools_traverse_edge_metadata.go:202). Zero/empty fields are elided.
func graphWideEdgeMetadata(e *knowledgev1.Edge) map[string]any {
	m := map[string]any{}
	if e.Weight != 0 {
		m["weight"] = e.Weight
	}
	if e.Confidence != 0 {
		m["confidence"] = e.Confidence
	}
	if e.Method != "" {
		m["method"] = e.Method
	}
	if e.Evidence != "" {
		m["evidence"] = e.Evidence
	}
	if !nanosToTime(e.LastValidated).IsZero() {
		m["last_validated"] = nanosToTime(e.LastValidated).UTC().Format("2006-01-02T15:04:05Z")
	}
	return m
}
