// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_correlations_pivot.go is the client-side claim for the
// composite query modes correlations + pivot on NON-logs graphs (logs is owned
// by InterceptLogsQuery earlier in the chain). Ports the server
// handleGenericCorrelations (tools_query_correlations.go) and handleGenericPivot
// (tools_query_pivot.go) recipes over generic Execute primitives.
//
// BOUNDED PAYLOAD (correlations). The earlier contract here was a CALL-COUNT
// one — "exactly two Executes" — which was true while the payload was unbounded,
// and the payload is what killed the session: a Limit-0 whole-graph node hydrate
// measured at 175.1 MB plus every one of the graph's 216,109 edges at 20.4 MB.
// The contract is now measured in bytes, not calls:
//
//   - ONE match-all RETURN_MODE_EDGES read bounded by engine.CorrelationsEdgeScanCap
//     (~5 MB at the measured ~99 bytes/edge), with edge_type pushed server-side.
//   - The whole-graph node hydrate is GONE. Names come from ONE bulk ids[] hydrate
//     over only the endpoints of the rows that survive the cap.
//   - Rows rendered are capped (correlationRowCap), and a capped scan is reported
//     as a SAMPLE — the server's match-all walk is unordered, so a truncated read
//     is an arbitrary subset rather than a prefix.
//
// OLD-CLIENT CONTRACT, the non-obvious half of the design: the server caps the
// edges arm only on a POSITIVE plan Limit — limit=0 still means UNLIMITED
// (cmd/knowledge-server/internal/bootstrap/engine_edges.go
// collectEdgesForReturnMode), which is what keeps every pre-existing whole-graph
// edge reader — topology's full-graph load, tools_logs_wire_fetch_edges.go
// fetchAllLogEdges — working across a rolling deploy.
//
// PER-MODE BOUNDEDNESS VERDICTS for the whole non-logs composite family, recorded
// here so the sibling sweep is complete in one place:
//
//   - correlations — BOUNDED on every axis, as above: one capped match-all edge
//     read, an endpoint-only hydrate bounded by 2x the row cap, and a render
//     capped at engine.CorrelationsRowCapDefault/Max with a SAMPLE notice when the
//     scan cap engaged (the walk ranges an unordered *xsync.Map,
//     cmd/knowledge-server/internal/store/graph_graph.go:58).
//   - pivot — BOUNDED. Keyset pages of engine.BrowsePageSize streamed into
//     engine.PivotAccumulator (nodeSetPage below); the render was already capped
//     at 20x20 by capPivotKeys (engine/render_correlations_pivot.go).
//   - pivot text-seed arm — ALREADY BOUNDED before this ticket, by
//     knowledgeSearchDefaultLimit (pivotSeedSearchClient below).
//   - timeline — BOUNDED. The same keyset pages plus a top-K retention of
//     engine.TimelineRowCapDefault/Max, render capped with a truncation notice
//     (intercept_query_explain_timeline.go).
//   - explain — BOUNDED, UNCHANGED BY THIS TICKET. Both forms pivot on a single
//     node id (fetchNodeEdges sends ById) and hydrate only that node's edge
//     endpoints: O(degree of one node), never graph cardinality. The census
//     classifier exempts by-id plans, which is why explain never appeared on the
//     survivor list.
//   - metadata_stats — BOUNDED, and out of this file: a server-side aggregate over
//     the MetadataStats RPC with no node enumeration at all
//     (intercept_query_metadata_stats_topology.go).

// InterceptQueryCorrelationsPivot claims query(mode in {correlations,pivot}) for
// non-logs graphs.
func InterceptQueryCorrelationsPivot(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
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
	if a.Mode != "correlations" && a.Mode != "pivot" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult(a.Mode + ": graph client unavailable")
	}

	if a.Mode == "correlations" {
		if err := accountQueryParams(armCorrelations, params.Arguments); err != nil {
			return true, errorResult(err.Error())
		}
		return true, composeCorrelations(ctx, gc.Execute, a)
	}
	if err := accountQueryParams(armPivot, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	return true, composePivot(ctx, deps, a)
}

// composeCorrelations issues the bounded recipe — one capped match-all edge read
// plus one endpoint-only hydrate — and renders.
func composeCorrelations(ctx context.Context, exec engine.ExecuteFn, a queryArgs) kgtools.ToolResult {
	label := domainGraphLabel(a)
	target := domainTarget(a)

	rows, total, scanCapped, err := fetchCorrelationRows(ctx, exec, target, a)
	if err != nil {
		return errorResult(fmt.Sprintf("correlations fetch failed: %v", err))
	}
	if len(rows) == 0 {
		filterMsg := "any edge type"
		if len(a.EdgeType) > 0 {
			filterMsg = strings.Join(a.EdgeType, ", ")
		}
		return textResult(engine.RenderCorrelationsEmpty(label, filterMsg))
	}
	return textResult(engine.RenderCorrelations(label, rows, total, scanCapped))
}

// fetchCorrelationRows issues ONE match-all RETURN_MODE_EDGES Execute capped at
// engine.CorrelationsEdgeScanCap, shapes the surviving edges into rows, sorts by
// confidence desc, truncates to the row cap, and hydrates only the endpoints of
// the rows that survive. Returns the capped rows, the PRE-CAP row total, and
// whether the server truncated the scan.
func fetchCorrelationRows(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, a queryArgs) ([]engine.CorrelationEdgeRow, int, bool, error) {
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: matchAllEdgesPlan(a.EdgeType, a.IncludeTombstones)},
		Target: target,
	})
	if err != nil {
		return nil, 0, false, err
	}
	edges, derr := engine.DecodeEdges(resp)
	if derr != nil {
		return nil, 0, false, derr
	}
	// Measured on the DECODED slice BEFORE the client-side type filter: that is
	// the count the server actually returned, and it truncates at exactly the cap.
	// `>=` rather than `==` so a future server that over-delivers still reports
	// honestly. A graph holding exactly the cap's worth of edges reports capped
	// when nothing was dropped — the SAFE direction (an over-cautious sample
	// warning, never a silently wrong ranking).
	scanCapped := len(edges) >= engine.CorrelationsEdgeScanCap

	// The type filter is pushed server-side via the plan's Selection; this
	// client-side pass is belt-and-braces over the same as-given comparison.
	typeSet := correlationEdgeTypeSet(a.EdgeType)
	rows := make([]engine.CorrelationEdgeRow, 0, len(edges))
	for i := range edges {
		e := &edges[i]
		if typeSet != nil && !typeSet[e.Type] {
			continue
		}
		// Construct the row as a fresh composite literal directly into append,
		// building the embedded Edge field-by-field. A `buildRow(...)` helper that
		// returned the row by value would copylocks the proto MessageState on the
		// return + append; an in-place fresh literal is lock-clean. Names are
		// filled after the cap, by hydrateCorrelationEndpoints.
		rows = append(rows, engine.CorrelationEdgeRow{
			Edge: knowledgev1.Edge{
				FromId:        e.FromId,
				ToId:          e.ToId,
				Type:          e.Type,
				Weight:        e.Weight,
				Confidence:    e.Confidence,
				Method:        e.Method,
				Evidence:      e.Evidence,
				LastValidated: e.LastValidated,
			},
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Edge.Confidence != rows[j].Edge.Confidence {
			return rows[i].Edge.Confidence > rows[j].Edge.Confidence
		}
		if rows[i].Edge.FromId != rows[j].Edge.FromId {
			return rows[i].Edge.FromId < rows[j].Edge.FromId
		}
		return rows[i].Edge.ToId < rows[j].Edge.ToId
	})
	total := len(rows)
	if rowCap := correlationRowCap(a); len(rows) > rowCap {
		rows = rows[:rowCap]
	}
	hydrateCorrelationEndpoints(ctx, exec, target, rows)
	return rows, total, scanCapped, nil
}

// matchAllEdgesPlan builds the capped match-all edge read. Carrying NO
// Ids/ById/FromId is what selects EdgeIterRequest.AllEdges server-side; Forward
// is left UNSET because Direction is inert under AllEdges, so setting it would
// be a false signal. edge_types ride the plan so the filter is applied at the
// source (as-given, byte-identical to the client's own comparison).
func matchAllEdgesPlan(edgeTypes []string, includeTombstones bool) *knowledgev1.QueryPlan {
	plan := &knowledgev1.QueryPlan{
		ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_EDGES,
		IncludeTombstones: includeTombstones,
		Limit:             int32(engine.CorrelationsEdgeScanCap),
	}
	if len(edgeTypes) > 0 {
		plan.Selection = &knowledgev1.Selection{EdgeTypes: edgeTypes}
	}
	return plan
}

// correlationRowCap resolves how many rows to RENDER. Distinct from
// engine.CorrelationsEdgeScanCap, which caps how many edges are SCANNED and is
// what the plan's Limit field carries — the two must not be conflated.
func correlationRowCap(a queryArgs) int {
	if l := int(a.Limit); l > 0 {
		return min(l, engine.CorrelationsRowCapMax)
	}
	return engine.CorrelationsRowCapDefault
}

// hydrateCorrelationEndpoints fills the from/to names and types of the capped
// rows via ONE bulk ids[] Execute over their distinct endpoints — at most
// 2*rowCap ids, never the whole graph. Same idiom as renderExplainWithNames
// (intercept_query_explain_timeline.go). A failed hydrate is not fatal: the rows
// keep their raw ids, matching correlationEndpoint's own fallback.
func hydrateCorrelationEndpoints(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, rows []engine.CorrelationEdgeRow) {
	idSet := make(map[string]struct{}, len(rows)*2)
	for i := range rows {
		idSet[rows[i].Edge.FromId] = struct{}{}
		idSet[rows[i].Edge.ToId] = struct{}{}
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	nameByID := map[string]*knowledgev1.Node{}
	if len(ids) > 0 {
		resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: ids}},
			Target: target,
		})
		if err == nil {
			if nodes, derr := engine.DecodeNodes(resp); derr == nil {
				for _, n := range nodes {
					nameByID[n.Id] = n
				}
			}
		}
	}
	for i := range rows {
		rows[i].FromName, rows[i].FromType = correlationEndpoint(rows[i].Edge.FromId, nameByID)
		rows[i].ToName, rows[i].ToType = correlationEndpoint(rows[i].Edge.ToId, nameByID)
	}
}

func correlationEdgeTypeSet(edgeTypes []string) map[string]bool {
	if len(edgeTypes) == 0 {
		return nil
	}
	out := make(map[string]bool, len(edgeTypes))
	for _, t := range edgeTypes {
		out[t] = true
	}
	return out
}

// correlationEndpoint resolves an edge endpoint id to its display name + node
// type via the hydrated name map, falling back to the raw id when absent.
func correlationEndpoint(id string, nameByID map[string]*knowledgev1.Node) (string, kgtypes.NodeType) {
	if n, ok := nameByID[id]; ok {
		return engine.CorrelationNodeName(n), kgtypes.NodeType(n.Type)
	}
	return id, ""
}

// composePivot validates rows/cols, folds the candidate node set into the matrix
// one page at a time, and renders. Port of handleGenericPivot.
func composePivot(ctx context.Context, deps ClientDeps, a queryArgs) kgtools.ToolResult {
	if a.Rows == "" || a.Cols == "" {
		return errorResult("pivot requires rows and cols when graph is not logs")
	}
	if a.Rows == a.Cols {
		return errorResult(fmt.Sprintf("rows and cols must differ (both were %q)", a.Rows))
	}
	// Built AFTER validation so an invalid request still costs zero Executes.
	acc := engine.NewPivotAccumulator(a.Rows, a.Cols)
	if err := pivotFetchNodesClient(ctx, deps, a, acc.Add); err != nil {
		return errorResult(fmt.Sprintf("pivot fetch failed: %v", err))
	}
	return textResult(engine.RenderPivotMatrix(domainGraphLabel(a), acc.Finish()))
}

// pivotFetchNodesClient streams the candidate node set to onPage: by type or
// every node (ONE bounded keyset drain, differing only in the node-type string —
// an empty type matches every node), or by text (the CLIENT segment engine —
// mgr.Search + bulk hydrate, NOT a server RETURN_MODE_SEARCH), which is already
// bounded by knowledgeSearchDefaultLimit and arrives as a single batch.
//
// Streaming rather than returning a slice is the bound: neither the pivot matrix
// nor the timeline top-K needs the whole node set at once, so no caller holds it.
func pivotFetchNodesClient(ctx context.Context, deps ClientDeps, a queryArgs, onPage func([]*knowledgev1.Node)) error {
	gc := deps.GraphCaller()
	target := domainTarget(a)
	if a.Text != "" {
		nodes, err := pivotSeedSearchClient(ctx, deps, a)
		if err != nil {
			return err
		}
		onPage(nodes)
		return nil
	}
	return engine.DrainKeysetPagesFunc(func(afterID string) ([]*knowledgev1.Node, error) {
		cursor := afterID
		resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: nodeSetPage(a.Type, &cursor, a.IncludeTombstones)},
			Target: target,
		})
		if err != nil {
			return nil, err
		}
		return engine.DecodeNodes(resp)
	}, engine.BrowsePageSize, func(page []*knowledgev1.Node) error {
		onPage(page)
		return nil
	})
}

// nodeSetPage builds one bounded keyset page of the pivot/timeline node set. An
// EMPTY nodeType means "match every node" — what store.Match(NodeType("")) does
// server-side — so the by-type and match-all arms are one drain. AfterId is SET
// on every page including the first, where the value is empty: its PRESENCE is
// what selects the keyset browse, and an omitted cursor would page in the
// backend's default order so the cursor taken from page 1 skips every lower id.
// Modeled on the in-package typeBrowsePage (intercept_query_modules_codestats.go).
func nodeSetPage(nodeType string, cursor *string, includeTombstones bool) *knowledgev1.QueryPlan {
	return &knowledgev1.QueryPlan{
		Selection:         &knowledgev1.Selection{NodeType: nodeType},
		Limit:             int32(engine.BrowsePageSize),
		AfterId:           cursor,
		SkipTotal:         true, // the drain consumes only the payload, never Total
		IncludeTombstones: includeTombstones,
	}
}

// pivotSeedSearchClient gathers the pivot text-seed candidate node set from the
// CLIENT segment engine: embed the seed query client-side, mgr.Search over the
// target graph's segments, then ONE bulk RETURN_MODE_NODES hydrate. No server
// RETURN_MODE_SEARCH. An un-collected graph (no segments) yields no candidates.
func pivotSeedSearchClient(ctx context.Context, deps ClientDeps, a queryArgs) ([]*knowledgev1.Node, error) {
	mgr := deps.SegmentManager()
	if mgr == nil {
		return nil, nil
	}
	gt, name := pivotEngineKey(a)
	var queryVec []byte
	if emb := deps.Embedder(); emb != nil {
		if vec, err := emb.EmbedBinary(ctx, a.Text); err == nil && len(vec) > 0 {
			queryVec = vec
		}
	}
	hits, err := mgr.Search(ctx, gt, name, a.Text, queryVec, knowledgeSearchDefaultLimit)
	if err != nil {
		return nil, err
	}
	results, err := hydrateEngineHits(ctx, deps.GraphCaller(), pivotHydrateSelector(a), hits)
	if err != nil {
		return nil, err
	}
	out := make([]*knowledgev1.Node, 0, len(results))
	for _, r := range results {
		out = append(out, r.Node)
	}
	return out, nil
}

// pivotEngineKey resolves the (graph type, instance name) the segment engine keys
// the pivot seed search on: knowledge→default, code→repo, cloud/cicd→account,
// practice→language, everything else→name.
func pivotEngineKey(a queryArgs) (kgtypes.GraphType, string) {
	gt := kgtypes.GraphType(a.Graph)
	if a.Graph == "" {
		gt = kgtypes.GraphKnowledge
	}
	switch graphsel.InstanceField(gt) {
	case graphsel.FieldRepo:
		return gt, a.Repo
	case graphsel.FieldAccount:
		return gt, a.Account
	default:
		if gt == kgtypes.GraphKnowledge {
			return gt, knowledgeDefaultName
		}
		if a.Language != "" {
			return gt, a.Language
		}
		return gt, a.Name
	}
}

// pivotHydrateSelector builds the hydrate routing envelope for the pivot seed
// search, mirroring pivotEngineKey's per-graph instance key.
func pivotHydrateSelector(a queryArgs) hydrateSelector {
	graph := a.Graph
	if graph == "" {
		graph = string(kgtypes.GraphKnowledge)
	}
	return hydrateSelector{
		Graph:    graph,
		Repo:     a.Repo,
		Account:  a.Account,
		Name:     a.Name,
		Language: a.Language,
	}
}

// domainTarget builds the GraphSelector for a composite-mode query from the
// generic args (graph + per-graph instance key).
func domainTarget(a queryArgs) *knowledgev1.GraphSelector {
	return &knowledgev1.GraphSelector{
		Graph:    a.Graph,
		Repo:     a.Repo,
		Account:  a.Account,
		Name:     a.Name,
		Language: a.Language,
	}
}

// domainGraphLabel returns a human label for the target graph used in the
// composite-mode headers, mirroring the server's per-graph label.
func domainGraphLabel(a queryArgs) string {
	switch a.Graph {
	case "", "knowledge":
		return "knowledge"
	case "cloud", "cicd", "practice", "linkage", "code":
		if a.Account != "" {
			return a.Graph + ":" + a.Account
		}
		if a.Repo != "" {
			return a.Graph + ":" + a.Repo
		}
		if a.Language != "" {
			return a.Graph + ":" + a.Language
		}
		return a.Graph
	default:
		return a.Graph
	}
}
