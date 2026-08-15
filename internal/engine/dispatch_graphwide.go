// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"

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
// shape, so this enumeration hydrates full nodes rather than bare ids. It reads
// them in BOUNDED keyset pages all the same — the hydration shape is why it
// cannot use the ids-only twin, never a reason to leave the read uncapped.
func dispatchGraphWideJSON(ctx context.Context, exec ExecuteFn, a traverseArgs, target *knowledgev1.GraphSelector) (kgtools.ToolResult, bool) {
	nodes, err := paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		cursor := afterID
		nodesPlan := &knowledgev1.QueryPlan{
			Selection: &knowledgev1.Selection{},
			Limit:     int32(paging.BrowsePageSize),
			// SET on every page including the first, where the value is empty:
			// presence is what selects the keyset browse.
			AfterId:   &cursor,
			SkipTotal: true,
		}
		applyTombstones(nodesPlan, a.IncludeTombstones)
		nodesResp, rerr := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: nodesPlan},
			Target: target,
		})
		if rerr != nil {
			return nil, rerr
		}
		page, derr := DecodeNodes(nodesResp)
		if derr != nil {
			return nil, fmt.Errorf("graph-wide node enumeration decode: %w", derr)
		}
		return page, nil
	}, paging.BrowsePageSize)
	if err != nil {
		return renderEngineError(err), true
	}
	edges, err := graphWideEdgeUnion(ctx, exec, nodeIDsOf(nodes), a.EdgeTypes, a.Graph, target)
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
	ids, err := paging.DrainKeysetIDs(func(afterID string) ([]string, error) {
		cursor := afterID
		plan := &knowledgev1.QueryPlan{
			Selection:  &knowledgev1.Selection{},
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_IDS,
			Limit:      int32(paging.BrowsePageSize),
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
	}, paging.BrowsePageSize)
	if err != nil {
		return renderEngineError(err), true
	}

	edges, uerr := graphWideEdgeUnion(ctx, exec, ids, a.EdgeTypes, a.Graph, target)
	if uerr != nil {
		return renderEngineError(uerr), true
	}
	return renderGraphWideText(a, len(ids), nodeIDSet(ids), edges), true
}

// nodeIDSet indexes the enumerated nodes for the render-side source membership
// check. It was the sole defense against DANGLING edges under the match-all read:
// that read returned every stored edge, including one left behind when a node was
// hard-deleted without its edges — neither endpoint exists, and a vanished node is
// not a tombstoned node, so no tombstone filter catches it (~0.16% of edges on a
// real graph). The read is id-pivoted again, and a dangling endpoint is never in
// the pivot set, so those edges can no longer arrive at all: this filter is now
// belt-and-braces rather than load-bearing. It stays because it costs nothing —
// the ids were already fetched for the response — and keeps the renderers correct
// independent of the read shape.
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

// graphWideEdgeUnion reads every edge incident to the enumerated ids in BOUNDED
// PIVOT PAGES and returns their union. It takes the ids themselves rather than
// only their count because the ids ARE the pivot set now: the match-all read it
// used to issue — one plan with no pivot discriminant, which the engine read as
// "every edge of the graph" — was unbounded by construction, and a user-supplied
// read whose cost scales with the whole edge table is precisely the surface this
// must no longer expose. An empty id set still skips the read entirely, keeping
// the empty-graph shape unchanged. The renderers apply the source-membership
// check (nodeIDSet). edge_types are canonicalized per-graph client-side.
//
// The per-page Limit and the drain's edgeCap are deliberately the same number:
// one is what the server enforces, the other is what the drain uses to notice it.
func graphWideEdgeUnion(ctx context.Context, exec ExecuteFn, ids []string, edgeTypes []string, graph string, target *knowledgev1.GraphSelector) ([]knowledgev1.Edge, error) {
	return paging.DrainPivotEdges(ids, paging.EdgePivotPageSize, CorrelationsEdgeScanCap, func(idPage []string) ([]knowledgev1.Edge, error) {
		sel := &knowledgev1.Selection{}
		if len(edgeTypes) > 0 {
			sel.EdgeTypes = canonicalEdgeCasings(graph, edgeTypes)
		}
		edgesPlan := &knowledgev1.QueryPlan{
			Ids:        idPage,
			Selection:  sel,
			ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES,
			Limit:      int32(CorrelationsEdgeScanCap),
		}
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: edgesPlan},
			Target: target,
		})
		if err != nil {
			return nil, err
		}
		return DecodeEdges(resp)
	})
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

	// Collapse multi-candidate groups; only the ungrouped remainder reaches the
	// per-edge row loop. No frontier filter: this arm is a start-less enumeration
	// of every node and every edge, not a walk, so there is no path to
	// short-circuit and no distance to truncate — the short-circuit is a WALK
	// rule. No enrichment either: this arm has already hydrated every node in the
	// graph, so the candidate facts come from the set already in hand.
	groups, ungrouped := GroupCandidateEdges(edges)
	edges = ungrouped

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
	payload := map[string]any{
		"start":     "",
		"graph":     graphWideLabel(a.Graph),
		"direction": a.Direction,
		"nodes":     nodeRows,
		"edges":     edgeRows,
	}
	// THE DANGLING-SOURCE RULE APPLIES TO GROUPS TOO: a group whose source is not
	// in the enumerated node set is skipped exactly as its edges would have been.
	// Exempting groups would let this arm emit edges the node enumeration says are
	// not there — a rule this arm already decided, which a new emitter must not
	// quietly opt out of.
	rooted := make([]CandidateGroup, 0, len(groups))
	for i := range groups {
		if _, ok := known[groups[i].FromID]; ok {
			rooted = append(rooted, groups[i])
		}
	}
	attachCandidateGroupsJSON(payload, rooted, nodeIndexByID(nodes), nil, false)
	return jsonResult(payload)
}

// nodeIndexByID indexes already-hydrated nodes so a group block can read each
// candidate's file and signature without a second read.
func nodeIndexByID(nodes []*knowledgev1.Node) map[string]*knowledgev1.Node {
	idx := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		if n != nil {
			idx[n.Id] = n
		}
	}
	return idx
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
//
// For a MULTI-CANDIDATE GROUP MEMBER the method and the raw group key are both
// omitted: the group's own edge_groups row already carries the method and the key
// under group_key, so repeating them per edge is redundant and the raw key
// invites parsing an identifier this plan renders opaquely. Every other edge is
// unchanged — a cloud or linkage edge's Evidence is a real citation.
//
// BELT-AND-BRACES BY DESIGN, NOT DEAD CODE: the arm passes only the ungrouped
// remainder, so a member should never arrive. The guards exist because this is a
// render primitive a future surface may call with an unfiltered slice.
func graphWideEdgeMetadata(e *knowledgev1.Edge) map[string]any {
	group := IsCandidateEdge(e)
	m := map[string]any{}
	if e.Weight != 0 {
		m["weight"] = e.Weight
	}
	if e.Confidence != 0 {
		m["confidence"] = e.Confidence
	}
	if e.Method != "" && !group {
		m["method"] = e.Method
	}
	if e.Evidence != "" && !group {
		m["evidence"] = e.Evidence
	}
	if !nanosToTime(e.LastValidated).IsZero() {
		m["last_validated"] = nanosToTime(e.LastValidated).UTC().Format("2006-01-02T15:04:05Z")
	}
	return m
}
