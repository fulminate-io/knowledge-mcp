// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// fakeGraphCaller is a scripted GraphCaller for the intercept tests.
// queryResponses maps node id → ToolResult; queryErrors maps node id →
// error. Captures every call for post-hoc assertions.
type fakeGraphCaller struct {
	queryResponses map[string]kgtools.ToolResult
	queryErrors    map[string]error
	mutateResult   kgtools.ToolResult
	mutateError    error

	// queryResponsesByGraph is the graph-aware variant: graphType → id →
	// result. When set, an Execute ByID query consults it FIRST keyed on the
	// request's Target graph (empty Target → "knowledge"), so a test can make a
	// node resolve in one graph but not another (the cross-graph-proxy branch
	// needs FROM-in-knowledge / not-in-practice). Falls back to the flat
	// queryResponses when the (graph,id) pair is absent. Purely additive.
	queryResponsesByGraph map[string]map[string]kgtools.ToolResult

	// queryResponsesByGraphName is the NAME-aware variant: (graphType,graphName) →
	// id → result, a FLAT map keyed by graphKey (not a 3-level nested map). When
	// set, an Execute ByID query consults it BEFORE queryResponsesByGraph so a
	// test can distinguish two graphs of the SAME type by name (code-FROM vs
	// cloud-FROM resolution, or a specific practice/<slug>). Purely additive —
	// falls back to the type-only path when the (type,name,id) triple is absent.
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
	mutateAffected int64

	// metadataStatsResp, when set, is returned for a MetadataStats RPC (the
	// promote_metadata composer's stats+override read). metadataStatsErr forces a
	// load failure. Purely additive — the seam the metadataStatsCaller type-assert
	// upgrades to.
	metadataStatsResp *knowledgev1.MetadataStatsResponse
	metadataStatsErr  error

	calls []recordedCall
}

type recordedCall struct {
	tool string
	args json.RawMessage
}

// graphKey is the (graphType, graphName) lookup key for the name-aware fake
// maps. graphName is the Target's Language (practice) or Name (code/cloud/cicd);
// an empty Target → ("knowledge", "").
type graphKey struct {
	Type string
	Name string
}

// targetGraphKey extracts the (type,name) key from an Execute Target, mirroring
// the SERVER's selector contract (not the client helper): practice carries its
// name in Language, code in Repo (the server's code resolver rejects name-keyed
// selectors, so a code Target with only Name set deliberately misses here too),
// every other type in Name; an empty Target defaults to knowledge.
func targetGraphKey(target *knowledgev1.GraphSelector) graphKey {
	gt := target.GetGraph()
	switch gt {
	case "":
		return graphKey{Type: "knowledge"}
	case "practice":
		return graphKey{Type: gt, Name: target.GetLanguage()}
	case "code":
		return graphKey{Type: gt, Name: target.GetRepo()}
	default:
		return graphKey{Type: gt, Name: target.GetName()}
	}
}

func (f *fakeGraphCaller) Call(_ context.Context, tool string, args json.RawMessage) (kgtools.ToolResult, error) {
	f.calls = append(f.calls, recordedCall{tool: tool, args: append(json.RawMessage(nil), args...)})
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
	f.execRequests = append(f.execRequests, req)
	// Mutation carrier path (PersistBatch / LinkOne / UpdateBatchStatus): record
	// the MutationPlan and return the seeded Ids + an affected_count. When
	// mutateIDs is unset, fall back to the ids embedded in the seeded
	// mutateResult {ids:[...]} body (the shape most create-* tests seed) so the
	// carrier path returns the same IDs the legacy Call path used to.
	if m := req.GetMutation(); m != nil {
		f.execMutations = append(f.execMutations, m)
		f.calls = append(f.calls, recordedCall{tool: "mutate"})
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
		}
		return &knowledgev1.ExecuteResponse{Ids: ids, AffectedCount: affected}, nil
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
		f.calls = append(f.calls, recordedCall{tool: "query"})
		// overlay_of read: serve the seeded overlay keys for that base when
		// configured (the clear_llm_failures overlay fan-out path).
		if base := q.GetOverlayOf(); base != "" {
			return f.execOverlayKeys(base)
		}
		return f.execGraphNames(req.GetTarget().GetGraph())
	}
	id := q.GetById()
	f.calls = append(f.calls, recordedCall{tool: "query", args: json.RawMessage(`{"id":"` + id + `"}`)})
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL {
		// Serve seeded descendants (TraverseDescendants) keyed by the from-root id,
		// encoded into the typed traversal_results carrier.
		roots := q.GetSelection().GetFromId()
		if len(roots) > 0 {
			if nodes, ok := f.traversalByRoot[roots[0]]; ok {
				results := make([]engine.TraversalResult, len(nodes))
				for i, n := range nodes {
					results[i] = engine.TraversalResult{Distance: 1, Node: n}
				}
				return &knowledgev1.ExecuteResponse{TraversalResults: traversalResultsToProtoForTest(results)}, nil
			}
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		// Serve seeded incident edges (render.IterEdges) when configured — the
		// typed edges carrier render.decodeCarrierEdges decodes (with full metadata).
		if edges, ok := f.edgesByID[id]; ok && len(edges) > 0 {
			return &knowledgev1.ExecuteResponse{Edges: edges}, nil
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
	f.calls = append(f.calls, recordedCall{tool: "metadata_stats"})
	if f.metadataStatsErr != nil {
		return nil, f.metadataStatsErr
	}
	if f.metadataStatsResp != nil {
		return f.metadataStatsResp, nil
	}
	return &knowledgev1.MetadataStatsResponse{}, nil
}

// The RETURN_MODE_GRAPH_NAMES serving helpers live in fake_graph_caller_graphnames_test.go.

// encodeNodeResult decodes a seeded single-node JSON body into a knowledgev1.Node and
// re-emits it as the nodes_json carrier ([]knowledgev1.Node), the shape render.Fetch-
// NodeIn decodes. A malformed seed surfaces as not-found.
func (f *fakeGraphCaller) encodeNodeResult(res kgtools.ToolResult) (*knowledgev1.ExecuteResponse, error) {
	var n knowledgev1.Node
	var body string
	if len(res.Content) > 0 {
		body = res.Content[0].Text
	}
	if uerr := json.Unmarshal([]byte(body), &n); uerr != nil {
		return &knowledgev1.ExecuteResponse{}, nil //nolint:nilerr // malformed seed → not found
	}
	resp := enginetest.ResponseWithNodes([]*knowledgev1.Node{&n}...)
	return resp, nil
}

func nodeResultJSON(t *testing.T, id, typ string, metadata map[string]string) kgtools.ToolResult {
	t.Helper()
	payload := map[string]any{
		"id":       id,
		"type":     typ,
		"metadata": metadata,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	require.NoError(t, err)
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(b)}}}
}

func TestStripBackendPrivateMetadata_StripsExpected(t *testing.T) {
	in := map[string]string{
		"backend":      "linear",
		"external_url": "https://example.invalid",
		"external_id":  "ABC-1",
		"linear_id":    "uuid",
		"linear_dirty": "true",
		"priority":     "high",
		"labels":       "bug",
	}
	out := stripBackendPrivateMetadata(in, "linear")
	assert.Equal(t, map[string]string{"priority": "high", "labels": "bug"}, out)
	// Caller's input must NOT have been mutated.
	_, hadBackend := in["backend"]
	assert.True(t, hadBackend, "input map must not be mutated in place")
}

func TestStripBackendPrivateMetadata_EmptyInput(t *testing.T) {
	assert.Nil(t, stripBackendPrivateMetadata(nil, "linear"))
	assert.Nil(t, stripBackendPrivateMetadata(map[string]string{}, "linear"))
}

func TestStripBackendPrivateMetadata_AllPrivate(t *testing.T) {
	in := map[string]string{"backend": "linear", "linear_id": "x", "external_url": "y"}
	out := stripBackendPrivateMetadata(in, "linear")
	assert.Nil(t, out, "all-private input should yield nil")
}

func TestGuardBatchHasNoBackendBacked_SingleID_PassesThrough(t *testing.T) {
	fc := &fakeGraphCaller{}
	err := guardBatchHasNoBackendBacked(context.Background(), fc, []string{"id-1"})
	require.NoError(t, err)
	assert.Empty(t, fc.calls, "single-id batches must not trigger any lookup")
}

func TestGuardBatchHasNoBackendBacked_NilCaller(t *testing.T) {
	err := guardBatchHasNoBackendBacked(context.Background(), nil, []string{"a", "b"})
	require.NoError(t, err)
}

func TestGuardBatchHasNoBackendBacked_AllLocal(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"a": nodeResultJSON(t, "a", "ticket", map[string]string{}),
		"b": nodeResultJSON(t, "b", "ticket", map[string]string{}),
	}}
	err := guardBatchHasNoBackendBacked(context.Background(), fc, []string{"a", "b"})
	require.NoError(t, err)
}

func TestGuardBatchHasNoBackendBacked_RejectsMixed(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"local-1":   nodeResultJSON(t, "local-1", "ticket", map[string]string{}),
		"backend-1": nodeResultJSON(t, "backend-1", "ticket", map[string]string{"backend": "linear"}),
		"local-2":   nodeResultJSON(t, "local-2", "ticket", map[string]string{}),
	}}
	err := guardBatchHasNoBackendBacked(context.Background(), fc, []string{"local-1", "backend-1", "local-2"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backend-1")
	assert.Contains(t, err.Error(), "mixed")
}

func TestGuardBatchHasNoBackendBacked_AllBackend_Permitted(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"backend-1": nodeResultJSON(t, "backend-1", "ticket", map[string]string{"backend": "linear"}),
		"backend-2": nodeResultJSON(t, "backend-2", "ticket", map[string]string{"backend": "linear"}),
	}}
	err := guardBatchHasNoBackendBacked(context.Background(), fc, []string{"backend-1", "backend-2"})
	require.NoError(t, err, "all-backend batches are safely retryable")
}

func TestLookupNodeBackend_Tombstoned_RoundTripsWireFlag(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"tomb-1": nodeResultJSON(t, "tomb-1", "ticket", map[string]string{
			"backend":      "linear",
			"linear_id":    "uuid-1",
			"external_url": "https://example.invalid/tomb-1",
		}),
	}}
	node, backendName, externalURL, backendID, meta, err := lookupNodeBackend(context.Background(), fc, "tomb-1")
	require.NoError(t, err)
	assert.Equal(t, "linear", backendName)
	assert.Equal(t, "uuid-1", backendID)
	assert.Equal(t, "https://example.invalid/tomb-1", externalURL)
	assert.Equal(t, "tomb-1", node.Id)
	assert.NotNil(t, meta)
	// Verify the compiled by-id QueryPlan carried include_tombstones:true (the
	// render.FetchNode Execute path threads it onto the plan, not the JSON args).
	require.Len(t, fc.execRequests, 1)
	q := fc.execRequests[0].GetQuery()
	require.NotNil(t, q, "lookup compiles a by-id QueryPlan")
	assert.Equal(t, "tomb-1", q.GetById())
	assert.True(t, q.GetIncludeTombstones(), "lookup must request include_tombstones:true")
}

func TestLookupNodeBackend_LocalOnly_ReturnsEmpty(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"local-1": nodeResultJSON(t, "local-1", "ticket", map[string]string{}),
	}}
	_, backendName, externalURL, backendID, _, err := lookupNodeBackend(context.Background(), fc, "local-1")
	require.NoError(t, err)
	assert.Empty(t, backendName)
	assert.Empty(t, backendID)
	assert.Empty(t, externalURL)
}

func TestLookupNodeBackend_NotFound_NotError(t *testing.T) {
	fc := &fakeGraphCaller{}
	_, backendName, _, _, _, err := lookupNodeBackend(context.Background(), fc, "missing")
	require.NoError(t, err)
	assert.Empty(t, backendName)
}

func TestLookupNodeBackend_TransportError_Surfaced(t *testing.T) {
	wantErr := errors.New("connect: refused")
	fc := &fakeGraphCaller{queryErrors: map[string]error{"id-1": wantErr}}
	_, _, _, _, _, err := lookupNodeBackend(context.Background(), fc, "id-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect: refused", "wrap must surface transport error")
}

func TestParsePriority(t *testing.T) {
	cases := map[string]int{
		"":       0,
		"none":   0,
		"urgent": 1,
		"High":   2,
		"medium": 3,
		"NORMAL": 3,
		"low":    4,
		"bogus":  0,
		"3":      3,
	}
	for in, want := range cases {
		assert.Equal(t, want, parsePriority(in), "input %q", in)
	}
}
