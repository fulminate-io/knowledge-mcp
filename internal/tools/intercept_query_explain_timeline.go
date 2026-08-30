// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// copyEdge returns a fresh knowledgev1.Edge with the same field values as e. The
// proto knowledgev1.Edge carries a noCopy MessageState, so `dst = *e` / appending
// an existing Edge by value is a copylocks violation; a field-by-field literal is
// the lock-clean way to materialize a copy.
func copyEdge(e *knowledgev1.Edge) knowledgev1.Edge {
	return knowledgev1.Edge{
		FromId:        e.FromId,
		ToId:          e.ToId,
		Type:          e.Type,
		Weight:        e.Weight,
		Confidence:    e.Confidence,
		Method:        e.Method,
		Evidence:      e.Evidence,
		LastValidated: e.LastValidated,
	}
}

// intercept_query_explain_timeline.go is the client-side claim for the composite
// query modes explain + timeline on NON-logs graphs. Ports the server
// handleGenericExplain (tools_query_explain.go) and handleGenericTimeline
// (tools_query_timeline.go) recipes over generic Execute primitives.
//
// BOUNDED: explain fetches the target id(s)' edges through the bounded pivot
// drain (both directions) + ONE bulk endpoint-name hydrate; the pair form
// filters to the b-peer client-side over that same fetch. Its cost is O(degree
// of one node), never graph cardinality.
//
// timeline reads the target node set in BOUNDED KEYSET PAGES and folds each page
// into an engine.TimelineTopK, which retains only the cap earliest entries — so
// neither the node set nor the entry slice is ever whole-corpus sized (a
// TimelineEntry pins its *knowledgev1.Node, so an uncapped entry slice pinned the
// entire fetch). The cap now applies BY DEFAULT rather than only when the caller
// supplies a limit. The surviving rationale from the old post-sort limit still
// holds and is why the cap is on RETENTION, not on the fetch: truncating the
// fetch would bias which entries were ever considered, whereas the top-K always
// keeps the earliest entries seen and therefore preserves T0.

// InterceptQueryExplainTimeline claims query(mode in {explain,timeline}) for
// non-logs graphs.
func InterceptQueryExplainTimeline(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a queryArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	if a.Graph == "logs" {
		return false, kgtools.ToolResult{} // logs owned by InterceptLogsQuery.
	}
	if a.Mode != "explain" && a.Mode != "timeline" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult(a.Mode + ": graph client unavailable")
	}

	if a.Mode == "explain" {
		if err := accountQueryParams(armExplain, params.Arguments); err != nil {
			return true, errorResult(err.Error())
		}
		return true, composeExplain(ctx, gc.Execute, a)
	}
	if err := accountQueryParams(armTimeline, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	return true, composeTimeline(ctx, deps, a)
}

// composeExplain dispatches the explain forms (pair / single-node) — port of
// handleGenericExplain.
func composeExplain(ctx context.Context, exec engine.ExecuteFn, a queryArgs) kgtools.ToolResult {
	label := domainGraphLabel(a)
	extraA := strings.TrimSpace(a.Extra["a"])
	extraB := strings.TrimSpace(a.Extra["b"])
	edgeFilter := correlationEdgeTypeSet(a.EdgeType)

	switch {
	case extraA != "" && extraB != "":
		return explainPairClient(ctx, exec, a, label, extraA, extraB, edgeFilter)
	case a.ID != "":
		return explainNodeClient(ctx, exec, a, label, a.ID, edgeFilter)
	case extraA != "":
		return explainNodeClient(ctx, exec, a, label, extraA, edgeFilter)
	default:
		return errorResult("explain mode requires id=<node> OR extra={a:<nodeA>, b:<nodeB>}")
	}
}

// fetchNodeEdges reads the node id's edges (both directions — Forward unset)
// through the bounded pivot drain and returns the raw edges.
//
// The plan Limit and the drain's edgeCap are the same number twice on purpose:
// the Limit is what the server enforces, the cap is what the drain uses to
// notice it was enforced. One without the other yields a drain that never
// detects truncation, or one that splits on a threshold nobody applies.
func fetchNodeEdges(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, nodeID string) ([]knowledgev1.Edge, error) {
	return paging.DrainPivotEdges([]string{nodeID}, paging.EdgePivotPageSize, engine.CorrelationsEdgeScanCap,
		func(idPage []string, fromIDGte, fromIDLt string) ([]knowledgev1.Edge, bool, error) {
			resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
				Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
					Ids:          idPage,
					ReturnMode:   knowledgev1.ReturnMode_RETURN_MODE_EDGES,
					Limit:        int32(engine.CorrelationsEdgeScanCap),
					EdgeFromBand: paging.EdgeFromBandOrNil(fromIDGte, fromIDLt),
				}},
				Target: target,
			})
			if err != nil {
				return nil, false, err
			}
			edges, derr := engine.DecodeEdges(resp)
			return edges, resp.GetTruncated(), derr
		})
}

// explainPairClient fetches a's edges (both directions) and filters to the
// b-peer + edge_type client-side over that single fetch. Port of explainPair.
func explainPairClient(ctx context.Context, exec engine.ExecuteFn, a queryArgs, label, nodeA, nodeB string, edgeFilter map[string]bool) kgtools.ToolResult {
	target := domainTarget(a)
	rawEdges, err := fetchNodeEdges(ctx, exec, target, nodeA)
	if err != nil {
		return errorResult(fmt.Sprintf("explain fetch failed: %v", err))
	}
	var edges []knowledgev1.Edge
	for i := range rawEdges {
		e := &rawEdges[i]
		peer := e.ToId
		if e.FromId != nodeA {
			peer = e.FromId
		}
		if peer != nodeB {
			continue
		}
		if edgeFilter != nil && !edgeFilter[e.Type] {
			continue
		}
		edges = append(edges, copyEdge(e))
	}
	if len(edges) == 0 {
		return errorResult(fmt.Sprintf("no edges between %s and %s", nodeA, nodeB))
	}
	return renderExplainWithNames(ctx, exec, target, label, edges)
}

// explainNodeClient fetches the node's edges (both directions) filtered by
// edge_type. Port of explainNode incl. the no-edges empty branch.
func explainNodeClient(ctx context.Context, exec engine.ExecuteFn, a queryArgs, label, nodeID string, edgeFilter map[string]bool) kgtools.ToolResult {
	target := domainTarget(a)
	rawEdges, err := fetchNodeEdges(ctx, exec, target, nodeID)
	if err != nil {
		return errorResult(fmt.Sprintf("explain fetch failed: %v", err))
	}
	var edges []knowledgev1.Edge
	for i := range rawEdges {
		e := &rawEdges[i]
		if edgeFilter != nil && !edgeFilter[e.Type] {
			continue
		}
		edges = append(edges, copyEdge(e))
	}
	if len(edges) == 0 {
		filterMsg := "any edge type"
		if edgeFilter != nil {
			filterMsg = fmt.Sprintf("%v", edgeFilter)
		}
		return textResult(engine.RenderExplainEmpty(label, nodeID, filterMsg))
	}
	return renderExplainWithNames(ctx, exec, target, label, edges)
}

// renderExplainWithNames resolves every endpoint name via ONE bulk ids[] hydrate
// then renders. This replaces the server's per-endpoint ByID resolveEdgeEndpoint
// with a single bulk fetch (no N+1).
func renderExplainWithNames(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, label string, edges []knowledgev1.Edge) kgtools.ToolResult {
	idSet := make(map[string]struct{}, len(edges)*2)
	for i := range edges {
		idSet[edges[i].FromId] = struct{}{}
		idSet[edges[i].ToId] = struct{}{}
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	nameByID := map[string]*knowledgev1.Node{}
	// hydrateClamped is captured OUT of the branch: the id set is both endpoints
	// of every incident edge, drained up to the 50,000-row edge cap, so it can
	// exceed the 10,000-id bound the server flags on the request alone. This arm
	// bypasses engine.Render, so an undisclosed clamp renders unresolved endpoint
	// names as though the peers simply had none.
	hydrateClamped := false
	if len(ids) > 0 {
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: ids}},
			Target: target,
		})
		// A FAILED HYDRATE IS AN ERROR, not a nameless render. Both failures used to
		// be swallowed — `if err == nil { if ..., derr := ...; derr == nil {...} }` —
		// so a broken read rendered EVERY endpoint under the truncated-id fallback
		// name RenderExplainEdges falls back to, indistinguishable from peers that
		// genuinely have no SymbolName. That is the silent-narrowing class the sibling
		// fetchTypeSamples fix (intercept_query_cloud_cicd.go) split apart: a fault
		// the reader must know about is never reported as an ordinary answer.
		//
		// THE GENUINELY-ABSENT CASE IS UNTOUCHED and still renders its fallback name
		// (explainEndpointName's truncated id): an id the hydrate simply did not
		// return is an ordinary answer, and only the READ's own failure is raised
		// here. The two causes are named separately because they fail for different
		// reasons and a reader retries them differently.
		//
		// THE DECODE LEG IS DECLARED BUT NOT REACHABLE TODAY, stated so no reader
		// mistakes it for a tested path: engine.DecodeNodes delegates to decodeNodes,
		// which is `return resp.GetNodes(), nil` over the typed carrier
		// (engine_decode.go) and cannot fail. It is handled rather than discarded
		// because the signature declares an error and a discarded error return is the
		// exact shape this fix removes — the day that carrier decodes again, the
		// failure is already loud. The exec leg is the one with a fixture.
		if err != nil {
			return errorResult(fmt.Sprintf("explain endpoint hydrate failed: %v", err))
		}
		hydrateClamped = resp.GetTruncated()
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil {
			return errorResult(fmt.Sprintf("explain endpoint decode failed: %v", derr))
		}
		for _, n := range nodes {
			nameByID[n.Id] = n
		}
	}
	// Collapse multi-candidate groups: only the UNGROUPED remainder gets a
	// per-edge "### Edge #n" block, so N alternatives are never restated as N
	// independent facts. No enrichment here — explain reads ALL edges incident to
	// the node in both directions, so a group whose source is that node arrives
	// whole; a partial group means the reader asked about a candidate rather than
	// the source, and the honest render is the partial block plus its declared
	// count.
	groups, ungrouped := engine.GroupCandidateEdges(edges)
	return engine.WithTruncationNoticeFor(
		textResult(engine.RenderExplainEdges(label, ungrouped, nameByID, groups)),
		hydrateClamped, len(ids))
}

// composeTimeline ports handleGenericTimeline: require time_field (non-logs),
// drain the node set in bounded keyset pages into a bounded top-K, render flat
// or bucketed. The top-K owns the cap — there is no post-sort truncation here.
func composeTimeline(ctx context.Context, deps ClientDeps, a queryArgs) kgtools.ToolResult {
	field := a.TimeField
	if field == "" {
		return errorResult("timeline requires time_field when graph is not logs")
	}
	label := domainGraphLabel(a)
	tk := engine.NewTimelineTopK(field, timelineRowCap(a))
	if err := pivotFetchNodesClient(ctx, deps, a, tk.Add); err != nil { // same fetch shape (type/text/all).
		return errorResult(fmt.Sprintf("timeline fetch failed: %v", err))
	}
	if tk.Total() == 0 {
		return textResult(engine.RenderTimelineEmpty(label, field))
	}
	entries := tk.Entries()
	bucket := strings.TrimSpace(a.Extra["bucket"])
	if bucket != "" {
		body, errMsg := engine.RenderTimelineBucketed(label, field, entries, bucket, tk.Total())
		if errMsg != "" {
			return errorResult(errMsg)
		}
		return textResult(body)
	}
	return textResult(engine.RenderTimelineFlat(label, field, entries, tk.Total()))
}

// timelineRowCap resolves how many entries the top-K RETAINS and the render
// shows: the default when the caller passes no limit, otherwise the caller's
// limit under the hard ceiling.
func timelineRowCap(a queryArgs) int {
	if l := int(a.Limit); l > 0 {
		return min(l, engine.TimelineRowCapMax)
	}
	return engine.TimelineRowCapDefault
}
