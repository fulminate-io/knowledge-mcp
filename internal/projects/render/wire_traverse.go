// SPDX-License-Identifier: Apache-2.0

// Package render — wire_traverse.go holds the single-RPC subtree traversal
// helpers. They live beside BuildChildIndex (child_index.go) and
// FetchDependsOnEdges (wire_fetch.go) because those three together are the
// whole batched-render prologue: one traversal supplies the nodes and
// structure edges, the index turns them into a parent→children map, and the
// depends-on read orders siblings. Callers in cmd/knowledge/internal/tools
// reach them through the render. qualifier; render/ must not import tools/,
// because tools/intercept_assemble.go imports render/.
package render

import (
	"context"
	"fmt"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TraverseDescendants walks the EdgeKGContains tree (or any edge type)
// forward from rootID up to depth and returns the hydrated descendant
// nodes in BFS-discovery order. The root id itself is NOT included —
// callers that need it prepend it themselves.
//
// Single gc.Call — the server's traverse handler issues one store-side
// bounded-depth query and returns the full BFS in one response.
func TraverseDescendants(ctx context.Context, gc GraphCaller, rootID string, edgeType kgtypes.EdgeType, depth int) ([]*knowledgev1.Node, error) {
	if gc == nil {
		return nil, fmt.Errorf("TraverseDescendants: graph caller unavailable")
	}
	ex, err := asExecutor(gc)
	if err != nil {
		return nil, fmt.Errorf("TraverseDescendants: %w", err)
	}
	// A forward (out) traversal over the edge type, returning the
	// raw traversal_results_json carrier ([]store.TraversalResult — each carries a
	// full store.Node), then the skip-root / skip-empty-ID filter client-side.
	fwd := true
	plan := &knowledgev1.QueryPlan{
		Selection:  &knowledgev1.Selection{FromId: []string{rootID}, EdgeTypes: []string{string(edgeType)}},
		Forward:    &fwd,
		ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL,
	}
	if depth > 0 {
		plan.MaxHops = int32(depth)
	}
	resp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: plan},
	})
	if err != nil {
		return nil, fmt.Errorf("TraverseDescendants: %w", err)
	}
	results, derr := engine.DecodeTraversal(resp)
	if derr != nil {
		return nil, fmt.Errorf("TraverseDescendants: decode: %w", derr)
	}
	return filterTraversalDescendants(results, rootID), nil
}

// TraverseDescendantsWithEdges is the edge-aware sibling of
// TraverseDescendants: one forward traversal that returns BOTH the
// hydrated descendant nodes (root filtered, BFS-discovery order) AND
// the subtree's structure edges, in a single Execute. It carries the
// same QueryPlan shape as TraverseDescendants and adds one field:
// IncludeEdgeMetadata, which is what makes the server populate the
// traversal_edges carrier (the edges walked during the traversal).
// The whole flat (nodes, edges) result is the complete source a
// client-side tree builder needs without any per-node fetch.
//
// It deliberately does NOT set IncludeTombstones. The structure-edge
// carrier unconditionally drops any edge whose peer is tombstoned, so a
// tombstoned child never reaches the index regardless of the flag; the
// per-node walk this replaces dropped the same edges, so behavior is
// preserved by the edge being absent. Setting the flag would only add
// tombstoned nodes whose inbound edge is still dropped — orphans that
// render in neither tree.
//
// A separate function (rather than extending TraverseDescendants) keeps
// the remaining nodes-only caller of TraverseDescendants unchanged — one
// production site, lastSessionThoughtID in
// intercept_thoughts_think_session.go, since the rollup moved to this
// sibling.
//
// The third return is the response's truncated flag, carried out rather than
// dropped: a server ceiling engaging mid-walk (the traversal hop clamp, the
// edge-row cap) yields a PARTIAL subtree that is otherwise indistinguishable
// from a complete one. Callers render their own output and so cannot route
// through engine.Render's notice append — they must surface the clamp
// themselves, or a caller reads a tree with silently missing branches.
func TraverseDescendantsWithEdges(
	ctx context.Context,
	gc GraphCaller,
	rootID string,
	edgeType kgtypes.EdgeType,
	depth int,
) ([]*knowledgev1.Node, []*knowledgev1.Edge, bool, error) {
	if gc == nil {
		return nil, nil, false, fmt.Errorf("TraverseDescendantsWithEdges: graph caller unavailable")
	}
	ex, err := asExecutor(gc)
	if err != nil {
		return nil, nil, false, fmt.Errorf("TraverseDescendantsWithEdges: %w", err)
	}
	fwd := true
	plan := &knowledgev1.QueryPlan{
		Selection:           &knowledgev1.Selection{FromId: []string{rootID}, EdgeTypes: []string{string(edgeType)}},
		Forward:             &fwd,
		ReturnMode:          knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL,
		IncludeEdgeMetadata: true,
	}
	if depth > 0 {
		plan.MaxHops = int32(depth)
	}
	resp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: plan},
	})
	if err != nil {
		return nil, nil, false, fmt.Errorf("TraverseDescendantsWithEdges: %w", err)
	}
	results, derr := engine.DecodeTraversal(resp)
	if derr != nil {
		return nil, nil, false, fmt.Errorf("TraverseDescendantsWithEdges: decode: %w", derr)
	}
	nodes := filterTraversalDescendants(results, rootID)
	// engine.EdgesFromProto returns a []knowledgev1.Edge value slice;
	// take addresses into a pointer slice so the index builder consumes
	// the same *knowledgev1.Edge shape the wire carriers use elsewhere.
	edgeVals := engine.EdgesFromProto(resp.GetTraversalEdges())
	edges := make([]*knowledgev1.Edge, len(edgeVals))
	for i := range edgeVals {
		edges[i] = &edgeVals[i]
	}
	return nodes, edges, resp.GetTruncated(), nil
}

// filterTraversalDescendants applies the skip-root / skip-empty-ID filter over a
// decoded []engine.TraversalResult and returns the surviving nodes in order. The
// carrier already carries full typed nodes, so there is no wire→store conversion
// (this is the filter half of the removed collectTraverseNodes).
func filterTraversalDescendants(results []engine.TraversalResult, rootID string) []*knowledgev1.Node {
	out := make([]*knowledgev1.Node, 0, len(results))
	for _, r := range results {
		if r.Node.Id == "" || r.Node.Id == rootID {
			continue
		}
		out = append(out, r.Node)
	}
	return out
}

// AssembleSubtree is the prologue every batched tree-rendering arm shares: one
// subtree traversal and one batched depends-on read, two Execute calls total
// regardless of how many nodes the subtree holds. It returns the parent→child
// index and id→node lookup RenderTreeFromIndex renders from, the sibling
// ordering map, and the traversal's truncation verdict.
//
// byID is returned rather than discarded because the arms read their
// contains-side section data — a ticket's child plans, a test plan's steps, a
// research node's questions, a project's tickets — out of it instead of
// re-fetching those nodes one at a time.
//
// depth is the caller's own maxDepth, unchanged. RenderTreeFromIndex stops at
// depth >= maxDepth and the traversal sets MaxHops to the same number, so the
// fetched subtree is exactly the rendered subtree. Passing a larger depth "to be
// safe" costs more rows and brings the server ceiling closer, turning a complete
// render into a truncated one.
//
// BOTH WIRE CALLS DEGRADE RATHER THAN ERROR, mirroring what the per-node walk
// this replaces did. A failed traversal yields empty maps, so the arm renders a
// root-only tree exactly as RenderTree did when its IterEdges call failed; a
// failed depends-on read yields no ordering, so children render in
// structure-edge order.
//
// THE TRUNCATION VERDICT IS THE FOURTH RETURN, AND EVERY CALLER OWES IT THREE
// THINGS. (1) Bind it as `truncated`, never `_`: the disclosure census enrolls a
// site by seeing a truncation-named value bound off a call, so `_` produces a
// site the census cannot see and whose silence is therefore unnoticeable. (2) OR
// together every verdict the arm receives — a subtree that was complete while a
// linked-nodes hydrate was clamped is still an incomplete render. (3) Append the
// notice in the arm immediately before returning, via AppendTruncationNotice.
//
// The notice belongs in the ARM rather than at the dispatch choke point because
// that is the only place the verdict and the finished result are in scope
// together: Handle dispatches on node type and gets back a ToolResult alone, so
// it cannot know whether the render was clamped.
func AssembleSubtree(
	ctx context.Context,
	gc GraphCaller,
	rootID string,
	depth int,
) (childIndex map[string][]*knowledgev1.Node, byID map[string]*knowledgev1.Node, dependsOn map[string]string, truncated bool) {
	nodes, structureEdges, truncated, err := TraverseDescendantsWithEdges(ctx, gc, rootID, kgtypes.EdgeKGContains, depth)
	if err != nil {
		slog.Warn("assemble subtree traversal failed; rendering root only", "id", rootID, "error", err)
		return map[string][]*knowledgev1.Node{}, map[string]*knowledgev1.Node{}, map[string]string{}, false
	}

	childIndex, byID = BuildChildIndex(rootID, nodes, structureEdges)

	allIDs := make([]string, 0, len(nodes)+1)
	allIDs = append(allIDs, rootID)
	for _, n := range nodes {
		allIDs = append(allIDs, n.Id)
	}
	dependsOn, derr := FetchDependsOnEdges(ctx, gc, allIDs)
	if derr != nil {
		dependsOn = map[string]string{}
	}

	return childIndex, byID, dependsOn, truncated
}

// AppendTruncationNotice carries the traversal's truncated flag onto the rendered
// result. plan_tree assembles its own output and returns it directly, so it
// never passes through engine.Render — the single place every other tool's
// response picks up the notice. Without this the subtree a ceiling clamped
// renders as a complete-looking tree with branches silently missing.
//
// The notice is a SEPARATE trailing block, never concatenated into the tree
// text: blocks are delivered as an array, so a format=json payload stays in its
// own block and remains independently parseable — the same reason
// engine.Render appends rather than concatenates.
//
// Copy tracks engine's truncationNotice — the row count, the "server row
// ceiling" phrasing, and `limit` named verbatim so a reader maps the advice
// onto the actual parameter. The action clause deliberately differs: a tree has
// no pages to walk, and plan_tree's `limit` IS the subtree depth (see the depth
// default above), so the re-run that yields a complete result is a smaller one.
// rows is the descendant count, mirroring engine's traversal-results row count
// (which likewise excludes nothing but the filtered root).
func AppendTruncationNotice(res kgtools.ToolResult, truncated bool, rows int) kgtools.ToolResult {
	if !truncated {
		return res
	}
	res.Content = append(res.Content, kgtools.ContentBlock{
		Type: "text",
		Text: fmt.Sprintf(
			"Showing %d rows — the server row ceiling engaged, so this subtree may be incomplete. "+
				"Re-run with a smaller `limit` (the subtree depth) for a complete tree at that depth.",
			rows),
	})
	return res
}
