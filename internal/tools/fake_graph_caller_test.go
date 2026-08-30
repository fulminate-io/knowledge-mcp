// SPDX-License-Identifier: Apache-2.0

package tools

// fake_graph_caller_test.go holds the scripted GraphCaller the intercept tests
// drive, plus its per-call failure knobs. Split out of backend_lookup_test.go
// (which keeps the backend-lookup and batch-guard tests) to keep both files
// inside the repo's file-length gate; the RETURN_MODE_GRAPH_NAMES serving
// helpers live in the fake_graph_caller_graphnames_test.go sibling, and the
// call-log carrier plus its guarded appenders in fake_graph_caller_record_test.go.
// The ordinal knob's own self-test lives in fake_graph_caller_ordinal_test.go,
// moved there so this file stays inside that gate.

import (
	"context"
	"encoding/json"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

type fakeGraphCaller struct {
	// recordMu guards the CALL LOGS below (calls, execRequests, execMutations,
	// statsReqs) and nothing else — this fake is driven by composers that fan their
	// reads out concurrently. See fake_graph_caller_record_test.go, which holds the
	// appenders every seam method records through.
	recordMu sync.Mutex

	queryResponses map[string]kgtools.ToolResult
	queryErrors    map[string]error
	mutateResult   kgtools.ToolResult
	mutateError    error

	// queryResponsesByGraph: graphType → id → result. An Execute ByID query
	// consults it FIRST keyed on the request's Target graph (empty → "knowledge"),
	// so a node can resolve in one graph but not another. Falls back to the flat
	// queryResponses when the (graph,id) pair is absent. Purely additive.
	queryResponsesByGraph map[string]map[string]kgtools.ToolResult

	// queryResponsesByGraphName: (graphType,graphName) → id → result, a FLAT map
	// keyed by graphKey. Consulted BEFORE queryResponsesByGraph so a test can
	// distinguish two graphs of the SAME type by name; falls back to the type-only
	// path when the (type,name,id) triple is absent. Purely additive.
	queryResponsesByGraphName map[graphKey]map[string]kgtools.ToolResult

	// nodeMatchResults answers a Match(NodeType) scan Execute (q.GetById()=="" and
	// a Selection.NodeType set) keyed by (graphType,graphName) → seeded node set,
	// carried in the typed Nodes field. Used by scanSlugLessPracticeProxies's
	// NodeProxy scan (the ByID-only fake path returns nothing for a Match scan).
	nodeMatchResults map[graphKey][]*knowledgev1.Node

	// nodeMatchErr forces a Match(NodeType) scan to ERROR, keyed by the scanned
	// node type (e.g. thought_session). Used by the context-linking tests to
	// drive a getOrCreateThoughtSessionClient resolve failure (session browse errors)
	// WITHOUT the blanket execErr that would also fail the node create. Consulted
	// before nodeMatchResults. Purely additive.
	nodeMatchErr map[string]error

	// edgesByID answers a RETURN_MODE_EDGES read (render.IterEdges) keyed by the
	// probed node id → its incident edges, encoded into the typed edges carrier.
	// The seeded edges carry the metadata fields the migration re-point must preserve.
	edgesByID map[string][]*knowledgev1.Edge

	// traversalByRoot answers a RETURN_MODE_TRAVERSAL read (TraverseDescendants,
	// e.g. the think composer's session-thoughts lineage read) keyed by the
	// from-root id → the descendant nodes, encoded into the typed traversal_results
	// carrier. Purely additive; absent root → empty traversal.
	traversalByRoot map[string][]*knowledgev1.Node

	// traversalEdgesByRoot answers the traversal_edges carrier a
	// RETURN_MODE_TRAVERSAL read populates when IncludeEdgeMetadata is set,
	// keyed by the from-root id. Absent root or an edge-blind read leaves the
	// carrier empty, which is the shape the nodes-only traversal already sees.
	traversalEdgesByRoot map[string][]knowledgev1.Edge

	// traversalErr forces a RETURN_MODE_TRAVERSAL read to ERROR, leaving every
	// other Execute successful. The blanket execErr cannot express this: a caller
	// that writes BEFORE it traverses needs the write to land and only the
	// traversal to fail. Purely additive; consulted before traversalByRoot.
	traversalErr error

	// listGraphsResult, when set, is returned for a pipeline_list_graphs Call
	// (the client graph-overview source used by listForeignGraphs). Purely
	// additive; the generic-forward arm answers it otherwise.
	listGraphsResult *kgtools.ToolResult

	// overlayKeysByBase answers a RETURN_MODE_GRAPH_NAMES read whose QueryPlan
	// carries overlay_of (the clear_llm_failures overlay fan-out): base name → the
	// full "base@overlay" keys for that base. Absent → the base-name enumeration
	// (execGraphNames) answers.
	overlayKeysByBase map[string][]string

	// mutateErrByTargetName forces a Mutation Execute to ERROR keyed by the
	// resolved target name discriminant (Repo/Account/Language/Name). Used by the
	// per-graph-error-surfacing test. Consulted before mutateError.
	mutateErrByTargetName map[string]error

	// mutateIDs is the carrier-path response for a Mutation Execute (the
	// created-node IDs PersistBatch reads from resp.GetIds()). execMutations
	// records every Mutation ExecuteRequest the carrier path issues.
	mutateIDs     []string
	execMutations []*knowledgev1.MutationPlan
	execErr       error

	// mutateErrOnNth fails ONLY the Nth Mutation Execute (1-based, counted
	// across the whole fake's lifetime), leaving the others successful. Needed
	// where two writes of the SAME MutationKind must be failed independently.
	mutateErrOnNth map[int]error

	// mutateErrByKind is the per-MutationKind error knob: keyed on
	// MutationPlan.GetKind(), it fails ONLY mutations of the named kind while
	// letting the others succeed. This is finer-grained than the blanket
	// mutateError (which fails every mutation incl. the create). Seed
	// {MUTATION_KIND_LINK: err} to prove a post-create LinkOne fails while the
	// CREATE still lands (node ID returned, result non-error). Consulted
	// BEFORE the blanket mutateError. Purely additive.
	mutateErrByKind map[knowledgev1.MutationPlan_MutationKind]error

	// execRequests records every full ExecuteRequest (Target + Plan) the carrier
	// path issues — used by composers that assert the GraphSelector envelope
	// (e.g. the intra-practice link's practice Target, clear_llm_failures
	// per-graph fan-out). Purely additive; existing tests ignore it.
	execRequests []*knowledgev1.ExecuteRequest

	// mutateAffected, when non-zero, is returned as the Mutation Execute
	// affected_count (clear_llm_failures reads it to tally cleared markers).
	// mutateSkipped is the skipped_count (not_found markers the engine
	// tolerated-and-skipped) — both zero by default.
	mutateAffected int64
	mutateSkipped  int64

	// metadataStatsResp, when set, is returned for a MetadataStats RPC (the
	// promote_metadata composer's stats+override read). metadataStatsErr forces a
	// load failure. Purely additive — the seam the metadataStatsCaller type-assert
	// upgrades to.
	metadataStatsResp *knowledgev1.MetadataStatsResponse
	metadataStatsErr  error

	// statsResp / statsErr back the statsRPC seam, and statsReqs records every
	// StatsRequest issued. Having Stats here is what makes the stats-bearing query
	// arms drivable at all: InterceptQueryStats and InterceptQueryCloudCICD's stats
	// shape type-assert gc.(statsRPC) and refuse before reading a param when it
	// fails, and InterceptQueryPracticeLinkage resolves the seam FIRST (statsSeamFor)
	// so ALL NINE of its arms are unreachable without it — not just the two stats
	// ones. A nil statsResp serves an empty GraphStats, which every reader handles.
	statsResp *knowledgev1.GraphStats
	statsErr  error
	statsReqs []*knowledgev1.StatsRequest

	calls []recordedCall
}

// graphKey is the (graphType, graphName) lookup key for the name-aware fake
// maps. graphName is the Target's Language (practice), Repo (code), Account
// (cloud/cicd) or Name (everything else, including the checks singleton, whose
// name is empty); an empty Target → ("knowledge", "").
type graphKey struct {
	Type string
	Name string
}

// targetGraphKey extracts the (type,name) key from an Execute Target, mirroring
// the SERVER's selector contract (not the client helper): practice carries its
// name in Language, code in Repo, cloud and cicd in Account, every other type in
// Name; an empty Target defaults to knowledge. checks is a SINGLETON whose
// selector policy rejects a set name, so it lands in the Name arm with an empty
// name — mirroring the server, which would refuse anything else. The code and cloud/cicd arms
// exist because the server's resolvers reject a name-keyed selector for those
// families before any lookup (resolveCode requires Repo, resolveAccountGraph
// errors with "graph=cloud requires account"), so a Target with only Name set
// must deliberately MISS here too — otherwise the fake would keep agreeing with
// a client that builds selectors the real server refuses.
func targetGraphKey(target *knowledgev1.GraphSelector) graphKey {
	gt := target.GetGraph()
	switch gt {
	case "":
		return graphKey{Type: "knowledge"}
	case "practice":
		return graphKey{Type: gt, Name: target.GetLanguage()}
	case "code":
		return graphKey{Type: gt, Name: target.GetRepo()}
	case "cloud", "cicd":
		return graphKey{Type: gt, Name: target.GetAccount()}
	default:
		return graphKey{Type: gt, Name: target.GetName()}
	}
}

func (f *fakeGraphCaller) Call(_ context.Context, tool string, args json.RawMessage) (kgtools.ToolResult, error) {
	f.recordCall(recordedCall{tool: tool, args: append(json.RawMessage(nil), args...)})
	if tool == "pipeline_list_graphs" && f.listGraphsResult != nil {
		return *f.listGraphsResult, nil
	}
	if tool == "query" {
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(args, &a)
		if err, ok := f.queryErrors[a.ID]; ok {
			return kgtools.ToolResult{}, err
		}
		if res, ok := f.queryResponses[a.ID]; ok {
			return res, nil
		}
		// Default: error result so callers treat as not-found.
		return kgtools.ToolResult{IsError: true, Content: []kgtools.ContentBlock{{Type: "text", Text: "not found"}}}, nil
	}
	// Generic forwarded-tool path: all non-query intercept forwards
	// (create_project / create_ticket / mutate / ...) route through the
	// same scripted mutate{Result, Error}. We don't differentiate
	// because intercept tests typically assert on the captured (tool,
	// args) shape rather than the return value.
	return f.mutateResult, f.mutateError
}

// Execute satisfies render.Executor: render.FetchNode /
// IterEdges now ride Execute. Records a "query" call (preserving the call-shape
// assertions) and answers a ByID from queryResponses by re-decoding the seeded
// node body into a nodes_json carrier; RETURN_MODE_EDGES returns no edges.
func (f *fakeGraphCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if f.execErr != nil {
		return nil, f.execErr
	}
	f.recordExec(req)
	// Mutation carrier path (PersistBatch / LinkOne / UpdateBatchStatus): record
	// the MutationPlan and return the seeded Ids + an affected_count. When
	// mutateIDs is unset, fall back to the ids embedded in the seeded
	// mutateResult {ids:[...]} body (the shape most create-* tests seed) so the
	// carrier path returns the same IDs the legacy Call path used to.
	if m := req.GetMutation(); m != nil {
		mutationOrdinal := f.recordMutation(m)
		// Ordinal error: fail ONLY the Nth Mutation Execute. Consulted BEFORE the
		// coarser knobs below — an explicitly ordinal-targeted failure should win.
		// recordMutation returned this call's 1-based ordinal from inside the same
		// critical section as the append, so it names this call and no other.
		if nthErr, ok := f.mutateErrOnNth[mutationOrdinal]; ok && nthErr != nil {
			return nil, nthErr
		}
		// Per-target error: fail ONLY the named graph (by its resolved name
		// discriminant) so the clear sweep's per-graph-error surfacing can be
		// exercised while other graphs succeed.
		if tname := targetNameDiscriminant(req.GetTarget()); tname != "" {
			if terr, ok := f.mutateErrByTargetName[tname]; ok && terr != nil {
				return nil, terr
			}
		}
		// Per-kind error: fail ONLY the named MutationKind so a
		// post-create LINK can fail while the CREATE succeeds.
		if kindErr, ok := f.mutateErrByKind[m.GetKind()]; ok && kindErr != nil {
			return nil, kindErr
		}
		if f.mutateError != nil {
			return nil, f.mutateError
		}
		ids := f.mutateIDs
		if len(ids) == 0 && len(f.mutateResult.Content) > 0 {
			var parsed struct {
				IDs []string `json:"ids"`
			}
			_ = json.Unmarshal([]byte(f.mutateResult.Content[0].Text), &parsed)
			ids = parsed.IDs
		}
		affected := f.mutateAffected
		if affected == 0 {
			affected = int64(len(ids))
			// AN UPDATE OVER A NAMED ID-SET REPORTS THAT SET'S SIZE, mirroring the
			// server rather than this fake's created-id carrier. `ids` above is the
			// CREATED-node list a PersistBatch returns, which is empty for an UPDATE —
			// so without this arm every UPDATE answered affected_count 0 while the real
			// server answers len(Selection.Ids) (asserted server-side by
			// TestExecuteMutation_ByIDUpdate). A caller that reads the count to confirm
			// its write would have been testing the fake's omission, not its own logic.
			// mutateAffected stays the override, which is how a test drives a shortfall.
			if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_UPDATE {
				affected = int64(len(m.GetSelection().GetIds()))
			}
		}
		return &knowledgev1.ExecuteResponse{Ids: ids, AffectedCount: affected, SkippedCount: f.mutateSkipped}, nil
	}
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		// Per-type graph-catalog enumeration (fetchGraphNamesOfType): derive the
		// names from the SAME listGraphsResult body the legacy pipeline_list_graphs
		// Call served, filtered to the requested Target graph type and projected to
		// the graph_names_json []store.GraphInfo carrier. Lets every existing
		// listGraphsResult seeding keep working across the Call→Execute repoint.
		// Record a "query" call so call-shape assertions (the linker's graph-list
		// read used to ride a query) still observe it.
		f.recordCall(recordedCall{tool: "query"})
		// overlay_of read: serve the seeded overlay keys for that base when
		// configured (the clear_llm_failures overlay fan-out path).
		if base := q.GetOverlayOf(); base != "" {
			return f.execOverlayKeys(base)
		}
		return f.execGraphNames(req.GetTarget().GetGraph())
	}
	id := q.GetById()
	f.recordCall(recordedCall{tool: "query", args: json.RawMessage(`{"id":"` + id + `"}`)})
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL {
		if f.traversalErr != nil {
			return nil, f.traversalErr
		}
		// Serve seeded descendants (TraverseDescendants) keyed by the from-root id,
		// encoded into the typed traversal_results carrier.
		roots := q.GetSelection().GetFromId()
		resp := &knowledgev1.ExecuteResponse{}
		if len(roots) > 0 {
			if nodes, ok := f.traversalByRoot[roots[0]]; ok {
				results := make([]engine.TraversalResult, len(nodes))
				for i, n := range nodes {
					results[i] = engine.TraversalResult{Distance: 1, Node: n}
				}
				resp.TraversalResults = traversalResultsToProtoForTest(results)
			}
			// The structure-edge carrier fills ONLY for an edge-aware read, mirroring
			// the server: an edge-blind traversal leaves TraversalEdges nil, which is
			// byte-for-byte what every existing traversal test already observes.
			if q.GetIncludeEdgeMetadata() {
				if edges, ok := f.traversalEdgesByRoot[roots[0]]; ok {
					resp.TraversalEdges = edgePtrsForTest(edges)
				}
			}
		}
		return resp, nil
	}
	// PLURAL-Ids arms. Both sit behind this guard so every existing single-id
	// path is untouched. Without them the charge readout is unobservable in
	// tools-package unit tests: FetchChargesFor issues two plural-Ids Executes
	// (an EDGES read for the charged-by edges, then a bulk hydrate of the charge
	// nodes), and a fake with no plural arm answers both with an empty response —
	// which renders identically to the pre-fix bug.
	if id == "" && len(q.GetIds()) > 0 {
		if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
			return f.execBulkEdges(q)
		}
		if q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL &&
			q.GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
			return f.execBulkHydrate(q.GetIds())
		}
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		// Serve seeded incident edges (render.IterEdges) when configured — the
		// typed edges carrier render.decodeCarrierEdges decodes (with full metadata).
		if edges, ok := f.edgesByID[id]; ok && len(edges) > 0 {
			return &knowledgev1.ExecuteResponse{Edges: bandNarrow(edges, q)}, nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// Match(NodeType) scan (no by_id, Selection.NodeType set): answer from the
	// seeded nodeMatchResults set keyed by the Target (type,name), carried in the
	// typed Nodes field (the shape engine.DecodeNodes reads).
	if id == "" && q.GetSelection().GetNodeType() != "" {
		if err, ok := f.nodeMatchErr[q.GetSelection().GetNodeType()]; ok && err != nil {
			return nil, err
		}
		if nodes, ok := f.nodeMatchResults[targetGraphKey(req.GetTarget())]; ok {
			resp := enginetest.ResponseWithNodes(nodes...)
			return resp, nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if err, ok := f.queryErrors[id]; ok {
		return nil, err
	}
	// Name-aware lookup first (when seeded): resolve only in the request's
	// (graphType,graphName). A (type,name) miss when the name-keyed map IS
	// configured for that key means "not in this graph" → not-found.
	if f.queryResponsesByGraphName != nil {
		if byID, hasKey := f.queryResponsesByGraphName[targetGraphKey(req.GetTarget())]; hasKey {
			res, ok := byID[id]
			if !ok {
				return &knowledgev1.ExecuteResponse{}, nil // not in this (type,name) graph.
			}
			return f.encodeNodeResult(res)
		}
	}
	// Graph-aware lookup first (when seeded): resolve only in the request's
	// Target graph. Empty Target → "knowledge". A (graph,id) miss when the
	// graph-keyed map IS configured for that graph means "not in this graph" →
	// not-found, even if the flat queryResponses has the id.
	if f.queryResponsesByGraph != nil {
		graph := req.GetTarget().GetGraph()
		if graph == "" {
			graph = "knowledge"
		}
		if byID, hasGraph := f.queryResponsesByGraph[graph]; hasGraph {
			res, ok := byID[id]
			if !ok {
				return &knowledgev1.ExecuteResponse{}, nil // not in this graph.
			}
			return f.encodeNodeResult(res)
		}
	}
	res, ok := f.queryResponses[id]
	if !ok {
		return &knowledgev1.ExecuteResponse{}, nil // not found.
	}
	return f.encodeNodeResult(res)
}

// MetadataStats satisfies the promote_metadata composer's metadataStatsCaller
// seam: returns the seeded stats+override response (or the seeded error). Records
// a "metadata_stats" call so dispatch-order assertions can observe it.
func (f *fakeGraphCaller) MetadataStats(_ context.Context, _ *knowledgev1.MetadataStatsRequest) (*knowledgev1.MetadataStatsResponse, error) {
	f.recordCall(recordedCall{tool: "metadata_stats"})
	if f.metadataStatsErr != nil {
		return nil, f.metadataStatsErr
	}
	if f.metadataStatsResp != nil {
		return f.metadataStatsResp, nil
	}
	return &knowledgev1.MetadataStatsResponse{}, nil
}

// Stats satisfies the statsRPC seam the per-graph stats arms type-assert for:
// returns the seeded GraphStats (or the seeded error) and records the request.
// Records a "stats" call so dispatch-order assertions can observe it.
func (f *fakeGraphCaller) Stats(_ context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	f.recordStats(req)
	if f.statsErr != nil {
		return nil, f.statsErr
	}
	return &knowledgev1.StatsResponse{GraphStats: f.statsResp}, nil
}

// The RETURN_MODE_GRAPH_NAMES serving helpers live in fake_graph_caller_graphnames_test.go.

// execBulkEdges answers a PLURAL-Ids RETURN_MODE_EDGES read with the UNION of
// the seeded incident edges over every requested id. Reuses edgesByID — the
// plural read is the same seed viewed through a different selector. Edge-TYPE
// filtering stays client-side, which is where the charge readout already does it.
// It takes the whole plan rather than just the ids because the answer must also
// honor the plan's from_id band.
func (f *fakeGraphCaller) execBulkEdges(q *knowledgev1.QueryPlan) (*knowledgev1.ExecuteResponse, error) {
	var union []*knowledgev1.Edge
	for _, id := range q.GetIds() {
		union = append(union, f.edgesByID[id]...)
	}
	return &knowledgev1.ExecuteResponse{Edges: bandNarrow(union, q)}, nil
}

// execBulkHydrate answers a PLURAL-Ids default-mode read by decoding each
// requested id's seeded response IN REQUEST ORDER, through the same typed Nodes
// carrier the single-id path uses. An unseeded or malformed id is skipped rather
// than failing the batch, mirroring a partial hydrate.
func (f *fakeGraphCaller) execBulkHydrate(ids []string) (*knowledgev1.ExecuteResponse, error) {
	nodes := make([]*knowledgev1.Node, 0, len(ids))
	for _, id := range ids {
		res, ok := f.queryResponses[id]
		if !ok {
			continue
		}
		if n, decoded := decodeSeededNode(res); decoded {
			nodes = append(nodes, n)
		}
	}
	return enginetest.ResponseWithNodes(nodes...), nil
}

// The seeded-node decode/encode helpers live in fake_graph_caller_seed_test.go.
