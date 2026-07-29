// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/base64"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	collectorlogs "github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// fakeLogGraphCaller is the store-FREE replacement for the deleted
// store-backed bridge. It implements tools.GraphCaller (deps.go:61 — a single
// Execute method) by serving the same six carrier arms the bridge's Execute
// served, but selecting over in-memory []knowledgev1.Node / []store.Edge slices
// keyed by queryID instead of querying a real store.DB.
//
// Selection-logic anchor: the per-arm semantics are copied verbatim from the
// deleted bridge's Execute so the Phase 2 handler migrations see byte-identical
// wire behavior:
//   - ids[]            → per-id lookup, returns exactly the requested ids
//   - NodeType browse  → only nodes of that type
//   - content_b64      → base64-encode Content (DecodeNodesContentB64 reverses)
//   - RETURN_MODE_EDGES → union of the selection.ids' OUTGOING edges of the
//     requested type(s), deduped (mirrors execEdgesUnion)
//   - RETURN_MODE_GRAPH_NAMES → the seeded graph names for the GraphType
//   - DROP_GRAPH mutation → remove the named graph from the corpus
//
// The shape (per-queryID map) is modeled on fakeLogSearchStore
// (tools_logs_search_test.go:27); the Execute behavior is modeled on
// scriptedTypeFakeCaller.Execute (tools_logs_wire_fetch_test.go:37-67).
// Production code NEVER uses this fake — it is injected via the
// Handler.graphCallerOverride seam (tools_logs_handler.go:60).
type fakeLogGraphCaller struct {
	// graphs is the per-queryID log corpus. The handler's log reads target
	// graph=logs name=<queryID>; the fake resolves the corpus from the
	// envelope target's name.
	graphs map[string]*fakeLogGraph

	// execs records every Execute call for RPC-count assertions, mirroring
	// scriptedTypeFakeCaller.execs.
	execs []*knowledgev1.ExecuteRequest
}

// fakeLogGraph is one queryID's in-memory node+edge corpus.
type fakeLogGraph struct {
	nodes []*knowledgev1.Node
	edges []*knowledgev1.Edge
}

func newFakeLogGraphCaller() *fakeLogGraphCaller {
	return &fakeLogGraphCaller{graphs: map[string]*fakeLogGraph{}}
}

// compile-time proof the fake satisfies the GraphCaller seam.
var _ GraphCaller = (*fakeLogGraphCaller)(nil)

// seedLogGraph loads a node/edge corpus into the fake under queryID, replacing
// any existing corpus for that name.
func (f *fakeLogGraphCaller) seedLogGraph(queryID string, nodes []*knowledgev1.Node, edges []*knowledgev1.Edge) {
	if f.graphs == nil {
		f.graphs = map[string]*fakeLogGraph{}
	}
	f.graphs[queryID] = &fakeLogGraph{nodes: nodes, edges: edges}
}

// graphFor resolves the corpus the request targets. graph=logs + name → that
// named log graph; any other target → a synthetic empty graph (knowledge-side
// log-backend reads have no node corpus in these tests).
func (f *fakeLogGraphCaller) graphFor(target *knowledgev1.GraphSelector) *fakeLogGraph {
	if target.GetGraph() == "logs" && target.GetName() != "" {
		if g, ok := f.graphs[target.GetName()]; ok {
			return g
		}
	}
	return &fakeLogGraph{}
}

// Execute implements GraphCaller against the in-memory corpus. The dispatch
// mirrors the deleted bridge's Execute (mutation vs query, then return-mode arms).
func (f *fakeLogGraphCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.execs = append(f.execs, req)
	if m := req.GetMutation(); m != nil {
		return f.execMutation(m, req.GetTarget())
	}
	return f.execQuery(req.GetQuery(), req.GetTarget())
}

func (f *fakeLogGraphCaller) execQuery(q *knowledgev1.QueryPlan, target *knowledgev1.GraphSelector) (*knowledgev1.ExecuteResponse, error) {
	switch q.GetReturnMode() {
	case knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		return f.execEdgesUnion(q, target), nil
	case knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES:
		return f.execGraphNames(target), nil
	}

	g := f.graphFor(target)
	var nodes []*knowledgev1.Node
	switch {
	case len(q.GetIds()) > 0:
		// Per-id lookup: return EXACTLY the requested ids that exist,
		// preserving request order (mirror the deleted bridge's ids arm).
		byID := make(map[string]*knowledgev1.Node, len(g.nodes))
		for _, n := range g.nodes {
			byID[n.Id] = n
		}
		for _, id := range q.GetIds() {
			if n, ok := byID[id]; ok {
				nodes = append(nodes, n)
			}
		}
	case q.GetSelection().GetNodeType() != "":
		want := kgtypes.NodeType(q.GetSelection().GetNodeType())
		for _, n := range g.nodes {
			if kgtypes.NodeType(n.Type) == want {
				nodes = append(nodes, n)
			}
		}
	default:
		nodes = slices.Clone(g.nodes)
	}

	// content_b64 carrier: base64-encode Content on a copy so the chunk's raw
	// zstd bytes survive JSON (DecodeNodesContentB64 reverses it). Under the
	// value-embed flip knowledgev1.Node carries a noCopy, so a shallow slices.Clone of
	// []*knowledgev1.Node would still share the pointee — deep-copy each node via
	// proto.Clone before mutating Content so the seeded corpus is never mutated
	// across calls.
	out := nodes
	if q.GetContentB64() {
		out = make([]*knowledgev1.Node, len(nodes))
		for i := range nodes {
			cp := &knowledgev1.Node{}
			proto.Merge(cp, nodes[i])
			if cp.Content != "" {
				cp.Content = base64.StdEncoding.EncodeToString([]byte(cp.Content))
			}
			out[i] = cp
		}
	}
	resp := enginetest.ResponseWithNodes(out...)
	resp.Total = int64(len(out))
	return resp, nil
}

// execEdgesUnion mirrors the engine's RETURN_MODE_EDGES arm: the union over the
// selection.ids of each node's OUTGOING edges whose type is in the requested
// set, deduped on (type, from, to). Edges from sibling nodes and edges of other
// types are excluded. A plan carrying NO pivot ids is the MATCH-ALL form — every
// edge of the graph, type filter still applied (engine_edges.go
// collectEdgesForReturnMode).
func (f *fakeLogGraphCaller) execEdgesUnion(q *knowledgev1.QueryPlan, target *knowledgev1.GraphSelector) *knowledgev1.ExecuteResponse {
	g := f.graphFor(target)
	wantTypes := make(map[kgtypes.EdgeType]bool, len(q.GetSelection().GetEdgeTypes()))
	for _, t := range q.GetSelection().GetEdgeTypes() {
		wantTypes[kgtypes.EdgeType(t)] = true
	}
	pivots := q.GetSelection().GetIds()
	if len(pivots) == 0 {
		pivots = q.GetIds()
	}
	matchAll := len(pivots) == 0
	wantFrom := make(map[string]bool, len(pivots))
	for _, id := range pivots {
		wantFrom[id] = true
	}
	seen := make(map[string]bool)
	var matched []int
	for i := range g.edges {
		e := g.edges[i]
		if !matchAll && !wantFrom[e.FromId] {
			continue
		}
		if len(wantTypes) > 0 && !wantTypes[kgtypes.EdgeType(e.Type)] {
			continue
		}
		key := e.Type + "\x00" + e.FromId + "\x00" + e.ToId
		if seen[key] {
			continue
		}
		seen[key] = true
		matched = append(matched, i)
	}
	edges := make([]*knowledgev1.Edge, len(matched))
	for j, i := range matched {
		edges[j] = g.edges[i]
	}
	return &knowledgev1.ExecuteResponse{Edges: edges}
}

// execGraphNames mirrors the deleted bridge's execGraphNames: it returns the
// seeded graph names for the target GraphType. Only graph=logs carries seeded
// corpora here; any other GraphType resolves to an empty name list.
func (f *fakeLogGraphCaller) execGraphNames(target *knowledgev1.GraphSelector) *knowledgev1.ExecuteResponse {
	gt := kgtypes.GraphType(target.GetGraph())
	if gt != kgtypes.GraphLogs {
		return &knowledgev1.ExecuteResponse{}
	}
	infos := make([]*knowledgev1.GraphInfo, 0, len(f.graphs))
	for name, g := range f.graphs {
		infos = append(infos, &knowledgev1.GraphInfo{
			Name:   name,
			Loaded: true,
			Nodes:  int32(len(g.nodes)),
			Edges:  int32(len(g.edges)),
		})
	}
	// Stable order for deterministic assertions.
	slices.SortFunc(infos, func(a, b *knowledgev1.GraphInfo) int {
		switch {
		case a.GetName() < b.GetName():
			return -1
		case a.GetName() > b.GetName():
			return 1
		default:
			return 0
		}
	})
	return &knowledgev1.ExecuteResponse{GraphNames: infos}
}

// execMutation mirrors the deleted bridge's execMutation DROP_GRAPH arm: the
// only mutation the log handlers drive against the fake is discard_logs, which
// removes the named graph from the corpus. UPSERT/DELETE on knowledge-side
// log-backend records are not exercised by the migrated tests, so they return
// an empty response (matching the bridge's default arm).
func (f *fakeLogGraphCaller) execMutation(m *knowledgev1.MutationPlan, target *knowledgev1.GraphSelector) (*knowledgev1.ExecuteResponse, error) {
	if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_DROP_GRAPH {
		name := target.GetName()
		if _, ok := f.graphs[name]; !ok {
			return &knowledgev1.ExecuteResponse{AffectedCount: 0}, nil
		}
		delete(f.graphs, name)
		return &knowledgev1.ExecuteResponse{AffectedCount: 1}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// buildLogCorpus produces a realistic store-FREE log corpus for queryID:
// templates + streams + chunks + label nodes + has-label / belongs-to /
// contains edges, built entirely via collectorlogs.MaterializeLogGraph (the
// pure transform) — no real store engine, NO hand-authored log node literals. The
// synthetic entry mix (api/db ERROR + worker INFO) mirrors syntheticLogEntries
// so every downstream carrier (type-browse, content_b64 chunk bodies, edges-
// union, graph-names) has non-trivial data to select over.
func buildLogCorpus(t *testing.T, queryID string) ([]*knowledgev1.Node, []*knowledgev1.Edge) {
	t.Helper()
	base := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	provider := &fakeLogsProvider{entries: syntheticLogEntries(base)}

	pipeline := collectorlogs.NewPipeline(provider, queryID)
	q := logwire.Query{StartTime: base, EndTime: base.Add(time.Minute)}
	entries, err := collectorlogs.CollectEntries(context.Background(), provider, q)
	require.NoError(t, err, "logs.CollectEntries")
	result, err := pipeline.CollectFromEntries(context.Background(), collectorlogs.ReclassifySeverity(entries), q)
	require.NoError(t, err, "pipeline CollectFromEntries")

	nodes, batchEdges, err := collectorlogs.MaterializeLogGraph(
		result.QueryID, result.Templates, result.Streams, result.Chunks,
		result.Correlations, result.Resolutions,
	)
	require.NoError(t, err, "materialize log graph")

	return nodes, batchEdgesToEdges(batchEdges)
}

// batchEdgesToEdges converts the MaterializeLogGraph []kgwire.BatchEdge output
// (every edge carries FromIdx/ToIdx==-1 + FromID/ToID, see BatchEdgeByID) into
// the []*knowledgev1.Edge shape the fake's edges-union arm and newLogState
// consume. A straight field copy — the resolved by-ID form the server's
// CreateBatch would have produced.
func batchEdgesToEdges(in []kgwire.BatchEdge) []*knowledgev1.Edge {
	out := make([]*knowledgev1.Edge, len(in))
	for i := range in {
		e := &in[i]
		var lastValidated int64
		if !e.LastValidated.IsZero() {
			lastValidated = e.LastValidated.UnixNano()
		}
		out[i] = &knowledgev1.Edge{
			FromId:        e.FromID,
			ToId:          e.ToID,
			Type:          string(e.Type),
			Weight:        e.Weight,
			Confidence:    e.Confidence,
			Method:        e.Method,
			Evidence:      e.Evidence,
			LastValidated: lastValidated,
		}
	}
	return out
}

// setupLogTestHandler returns a *Handler wired with a fakeLogGraphCaller seeded
// from buildLogCorpus for queryID — no real store engine. The override seam
// (Handler.graphCallerOverride, tools_logs_handler.go:60) injects the fake so
// h.graphCaller() (:74) returns it.
func setupLogTestHandler(t *testing.T, queryID string) *Handler {
	t.Helper()
	fake := newFakeLogGraphCaller()
	nodes, edges := buildLogCorpus(t, queryID)
	fake.seedLogGraph(queryID, nodes, edges)
	return &Handler{graphCallerOverride: fake}
}

// logsSelector builds the graph=logs envelope target for the dispatch
// self-test's fixed query ID.
func logsSelector() *knowledgev1.GraphSelector {
	return &knowledgev1.GraphSelector{Graph: "logs", Name: "q-dispatch"}
}

// engineFromCorpus rebuilds the *logs.QueryEngine the handler would build from
// the same corpus, via the identical logs.NewQueryEngine + *AsWire path
// getOrFetchLogState uses (tools_logs_handler.go:186). Alias-family tests use it
// to resolve the exact alias the handler resolves — the process-local
// logs.LookupEngine registry is NOT populated under the fake (no real pipeline
// run), so tests reconstruct the engine from value-type corpus slices instead.
func engineFromCorpus(nodes []*knowledgev1.Node) *collectorlogs.QueryEngine {
	var templates, streams, chunks []*knowledgev1.Node
	for _, n := range nodes {
		switch kgtypes.NodeType(n.Type) {
		case kgtypes.NodeLogTemplate:
			templates = append(templates, n)
		case kgtypes.NodeLogStream:
			streams = append(streams, n)
		case kgtypes.NodeLogChunk:
			chunks = append(chunks, n)
		}
	}
	return collectorlogs.NewQueryEngine(
		streamsAsWire(streams),
		chunksAsWire(chunks),
		templatesAsWire(templates),
	)
}
