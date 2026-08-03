// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// --- scripted fakes for the context-pack composer ---------------------------

// ctxSearcher is a scripted SegmentSearcher for the context-pack tests: it
// returns a canned cross-type Hit list, or an error when searchErr is set (the
// degraded-on-error path). Satisfies tools.SegmentSearcher.
type ctxSearcher struct {
	hits      []searchengine.Hit
	searchErr error
}

func (s *ctxSearcher) Search(
	_ context.Context, _ kgtypes.GraphType, _, _ string, _ []byte, _ int,
) ([]searchengine.Hit, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.hits, nil
}

// ctxCaller is a scripted GraphCaller answering exactly the read shapes the
// composer issues, routed by QueryPlan shape:
//   - RETURN_MODE_NODES + Ids → bulk hydrate: nodesByID for each requested id.
//   - RETURN_MODE_EDGES + Ids → bulk edges: edgesForSet (the FetchEdgesForNodeSet
//     expansion read) when an expand edge-type filter is present, else the
//     FetchChargesFor EdgeChargedBy read served from chargeEdges.
//   - Selection.NodeTypes (ticket browse) → ticketNodes.
type ctxCaller struct {
	nodesByID   map[string]*knowledgev1.Node // bulk ids[] hydrate source
	edgesForSet []*knowledgev1.Edge          // expand edges (informed-by/relates-to/...)
	chargeEdges []*knowledgev1.Edge          // EdgeChargedBy edges (thought→charge)
	ticketNodes []*knowledgev1.Node          // type:ticket browse result
}

func (c *ctxCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// Ticket browse: a plural NodeTypes Selection with no ids.
	if len(q.GetIds()) == 0 && q.GetById() == "" {
		if nt := q.GetSelection().GetNodeTypes(); len(nt) > 0 {
			return &knowledgev1.ExecuteResponse{Nodes: c.ticketNodes}, nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	switch q.GetReturnMode() {
	case knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		// Distinguish the expand read (filtered to the expand edge types) from the
		// FetchChargesFor read (filtered to EdgeChargedBy) by the Selection filter.
		ets := q.GetSelection().GetEdgeTypes()
		if edgeTypesContain(ets, string(kgtypes.EdgeChargedBy)) {
			return &knowledgev1.ExecuteResponse{Edges: c.chargeEdges}, nil
		}
		return &knowledgev1.ExecuteResponse{Edges: c.edgesForSet}, nil
	default:
		// Bulk ids[] hydrate (RETURN_MODE_NODES): return every requested id present
		// in nodesByID, in request order.
		out := make([]*knowledgev1.Node, 0, len(q.GetIds()))
		for _, id := range q.GetIds() {
			if n, ok := c.nodesByID[id]; ok {
				out = append(out, n)
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: out}, nil
	}
}

func edgeTypesContain(ets []string, want string) bool {
	return slices.Contains(ets, want)
}

// ctxPackDeps wires the three accessors the context composer reads (gc, segMgr,
// emb); every other ClientDeps method is nil. Mirrors interceptTestDeps's
// method set.
type ctxPackDeps struct {
	gc     GraphCaller
	segMgr SegmentSearcher
	emb    embed.BinaryEmbedder
}

func (d ctxPackDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d ctxPackDeps) Sink() collector.Sink                         { return nil }
func (d ctxPackDeps) RootDir() string                              { return "" }
func (d ctxPackDeps) UsageAnalyzer() UsageAnalyzerAPI              { return nil }
func (d ctxPackDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d ctxPackDeps) WorkerReady() bool                            { return true }
func (d ctxPackDeps) PropReady() bool                              { return true }
func (d ctxPackDeps) PipelineReady() bool                          { return true }
func (d ctxPackDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d ctxPackDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d ctxPackDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d ctxPackDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d ctxPackDeps) Embedder() embed.BinaryEmbedder               { return d.emb }
func (d ctxPackDeps) BackendResolver() BackendResolver             { return nil }
func (d ctxPackDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d ctxPackDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d ctxPackDeps) SegmentManager() SegmentSearcher              { return d.segMgr }
func (d ctxPackDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d ctxPackDeps) SegmentShipper() SegmentShipper               { return nil }
func (d ctxPackDeps) SegmentPruner() SegmentPruner                 { return nil }

func (d ctxPackDeps) SegmentCacheDropper() SegmentCacheDropper { return nil }
func (d ctxPackDeps) SegmentDeleter() SegmentDeleter           { return nil }
func (d ctxPackDeps) SegmentCoverage() SegmentCoverageReader   { return nil }
func (d ctxPackDeps) PipelineScanner() PipelineScanner         { return nil }

func (d ctxPackDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d ctxPackDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d ctxPackDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d ctxPackDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d ctxPackDeps) ClusterProvider() ClusterProvider     { return nil }
func (d ctxPackDeps) TensionsProvider() TensionsProvider   { return nil }

// --- decoded-pack helpers ----------------------------------------------------

// ctxPack is the decoded json shape of renderContextPack's json output.
type ctxPack struct {
	Seeds        []contextRow      `json:"seeds"`
	Related      []contextRow      `json:"related"`
	Recent       []contextRow      `json:"recent"`
	Tickets      []contextRow      `json:"tickets"`
	Charges      map[string]string `json:"charges"`
	SeedDegraded bool              `json:"seed_degraded"`
	SeedMarker   string            `json:"seed_marker"`
}

func runContextPack(t *testing.T, deps ClientDeps, args map[string]any) ctxPack {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	args["operation"] = "recall"
	args["mode"] = "context"
	if _, ok := args["format"]; !ok {
		args["format"] = "json"
	}
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	res := handleRecallClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: raw,
	})
	require.False(t, res.IsError, "context pack returned error: %s", toolResultText(res))
	var pack ctxPack
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &pack))
	return pack
}

func nowNanos() int64 { return time.Now().UnixNano() }

// nodesByID builds the id→node map from a node slice.
func nodesByID(nodes ...*knowledgev1.Node) map[string]*knowledgev1.Node {
	m := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		m[n.GetId()] = n
	}
	return m
}

// --- tests -------------------------------------------------------------------

// TestContextPack_SeedsIncludeNonThoughtTypes: the pack surfaces decision /
// finding / ticket-typed seeds, not just thoughts — the core thing recall
// cannot do. Removing the seed composition arm empties this section.
func TestContextPack_SeedsIncludeNonThoughtTypes(t *testing.T) {
	seedNodes := []*knowledgev1.Node{
		{Id: "d1", Type: string(kgtypes.NodeDecision), SymbolName: "ADecision", UpdatedAt: nowNanos()},
		{Id: "f1", Type: string(kgtypes.NodeFinding), SymbolName: "AFinding", UpdatedAt: nowNanos()},
		{Id: "t1", Type: string(kgtypes.NodeThought), SymbolName: "AThought", UpdatedAt: nowNanos()},
	}
	deps := ctxPackDeps{
		segMgr: &ctxSearcher{hits: []searchengine.Hit{
			{ID: "d1", Score: 0.9}, {ID: "f1", Score: 0.8}, {ID: "t1", Score: 0.7},
		}},
		gc: &ctxCaller{nodesByID: nodesByID(seedNodes...)},
	}
	pack := runContextPack(t, deps, map[string]any{"query": "auth design"})

	require.NotEmpty(t, pack.Seeds)
	types := map[string]bool{}
	for _, r := range pack.Seeds {
		types[r.Type] = true
	}
	assert.True(t, types[string(kgtypes.NodeDecision)], "seed must include a decision-typed node")
	assert.True(t, types[string(kgtypes.NodeFinding)], "seed must include a finding-typed node")
	assert.False(t, pack.SeedDegraded, "a successful seed is not degraded")
}

// TestContextPack_EdgeExpandedNodesPresent: a node reachable only via an
// informed-by edge from a seed (and NOT itself a seed hit) appears in Related.
// Removing the expand arm empties Related.
func TestContextPack_EdgeExpandedNodesPresent(t *testing.T) {
	seed := &knowledgev1.Node{Id: "d1", Type: string(kgtypes.NodeDecision), SymbolName: "Seed", UpdatedAt: nowNanos()}
	neighbor := &knowledgev1.Node{Id: "f9", Type: string(kgtypes.NodeFinding), SymbolName: "Neighbor", UpdatedAt: nowNanos()}
	deps := ctxPackDeps{
		segMgr: &ctxSearcher{hits: []searchengine.Hit{{ID: "d1", Score: 0.9}}},
		gc: &ctxCaller{
			nodesByID: nodesByID(seed, neighbor),
			edgesForSet: []*knowledgev1.Edge{
				{Type: string(kgtypes.EdgeInformedBy), FromId: "d1", ToId: "f9"},
			},
		},
	}
	pack := runContextPack(t, deps, map[string]any{"query": "auth design"})

	require.NotEmpty(t, pack.Related)
	found := false
	for _, r := range pack.Related {
		if r.ID == "f9" {
			found = true
		}
	}
	assert.True(t, found, "edge-expanded neighbor f9 must appear in Related")
}

// TestContextPack_ChargeStateAttached: a thought row carries a validated /
// contested charge state derived from the scripted charges. Removing the charge
// arm drops the annotation.
func TestContextPack_ChargeStateAttached(t *testing.T) {
	thought := &knowledgev1.Node{Id: "t1", Type: string(kgtypes.NodeThought), SymbolName: "AThought", UpdatedAt: nowNanos()}
	posCharge := &knowledgev1.Node{Id: "c1", Type: string(kgtypes.NodeCharge)}
	kgtypes.SetValue(posCharge, "polarity", "positive")
	deps := ctxPackDeps{
		segMgr: &ctxSearcher{hits: []searchengine.Hit{{ID: "t1", Score: 0.9}}},
		gc: &ctxCaller{
			nodesByID: nodesByID(thought, posCharge),
			chargeEdges: []*knowledgev1.Edge{
				{Type: string(kgtypes.EdgeChargedBy), FromId: "t1", ToId: "c1"},
			},
		},
	}
	pack := runContextPack(t, deps, map[string]any{"query": "auth design"})

	assert.Equal(t, "validated", pack.Charges["t1"], "thought t1 must carry derived validated charge state")
}

// TestContextPack_RecencyApplied: the Recently-active section is ordered by
// UpdatedAt half-life — a newer node outranks an older node that has a higher
// base score. Removing the recency overlay loses this ordering.
func TestContextPack_RecencyApplied(t *testing.T) {
	old := time.Now().Add(-365 * 24 * time.Hour).UnixNano()
	newer := time.Now().UnixNano()
	// "stale" has the SLIGHTLY higher seed base score but is a year old; "fresh"
	// is fractionally lower base but far newer. applyTemporalRerank is a
	// multiplicative UpdatedAt half-life BOOST (score *= 1+temporal), so the
	// near-zero boost on the year-old node leaves it ~unchanged while the ~1.0
	// boost on the day-old node nearly doubles it — flipping the order. This
	// proves the recency overlay reordered (it could not flip if absent).
	stale := &knowledgev1.Node{Id: "stale", Type: string(kgtypes.NodeFinding), SymbolName: "Stale", UpdatedAt: old}
	fresh := &knowledgev1.Node{Id: "fresh", Type: string(kgtypes.NodeFinding), SymbolName: "Fresh", UpdatedAt: newer}
	deps := ctxPackDeps{
		segMgr: &ctxSearcher{hits: []searchengine.Hit{
			{ID: "stale", Score: 0.55}, {ID: "fresh", Score: 0.50},
		}},
		gc: &ctxCaller{nodesByID: nodesByID(stale, fresh)},
	}
	pack := runContextPack(t, deps, map[string]any{"query": "auth design"})

	require.GreaterOrEqual(t, len(pack.Recent), 2)
	assert.Equal(t, "fresh", pack.Recent[0].ID, "recency overlay must rank the far-newer node first")
}

// TestContextPack_OpenTicketsSurfaced: a non-terminal-status ticket appears; a
// terminal-status (completed) ticket is excluded. Removing the terminal filter
// would let the completed ticket through.
func TestContextPack_OpenTicketsSurfaced(t *testing.T) {
	seed := &knowledgev1.Node{Id: "d1", Type: string(kgtypes.NodeDecision), SymbolName: "Seed", UpdatedAt: nowNanos()}
	deps := ctxPackDeps{
		segMgr: &ctxSearcher{hits: []searchengine.Hit{{ID: "d1", Score: 0.9}}},
		gc: &ctxCaller{
			nodesByID: nodesByID(seed),
			ticketNodes: []*knowledgev1.Node{
				{Id: "open1", Type: string(kgtypes.NodeTicket), SymbolName: "OpenTicket", Status: "In Progress"},
				{Id: "done1", Type: string(kgtypes.NodeTicket), SymbolName: "DoneTicket", Status: "completed"},
				{Id: "done2", Type: string(kgtypes.NodeTicket), SymbolName: "CancelledTicket", Status: "Cancelled"},
			},
		},
	}
	pack := runContextPack(t, deps, map[string]any{"query": "auth design"})

	ids := map[string]bool{}
	for _, r := range pack.Tickets {
		ids[r.ID] = true
	}
	assert.True(t, ids["open1"], "non-terminal ticket must surface")
	assert.False(t, ids["done1"], "completed ticket must be excluded")
	assert.False(t, ids["done2"], "Cancelled ticket must be excluded (case-insensitive)")
}

// TestContextPack_BoundedSizes: with oversized scripted inputs, each section is
// capped at its named cap and total rows stay within the sum of the caps.
func TestContextPack_BoundedSizes(t *testing.T) {
	var hits []searchengine.Hit
	byID := map[string]*knowledgev1.Node{}
	for i := range contextSeedCap * 3 {
		id := "s" + itoa(i)
		hits = append(hits, searchengine.Hit{ID: id, Score: 1.0 - float64(i)*0.001})
		byID[id] = &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeFinding), SymbolName: "S", UpdatedAt: nowNanos()}
	}
	var tickets []*knowledgev1.Node
	for i := range contextTicketCap * 3 {
		id := "tk" + itoa(i)
		tickets = append(tickets, &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeTicket), SymbolName: "T", Status: "Todo"})
	}
	deps := ctxPackDeps{
		segMgr: &ctxSearcher{hits: hits},
		gc:     &ctxCaller{nodesByID: byID, ticketNodes: tickets},
	}
	pack := runContextPack(t, deps, map[string]any{"query": "auth design"})

	assert.LessOrEqual(t, len(pack.Seeds), contextSeedCap)
	assert.LessOrEqual(t, len(pack.Related), contextExpandCap)
	assert.LessOrEqual(t, len(pack.Recent), contextRecentCap)
	assert.LessOrEqual(t, len(pack.Tickets), contextTicketCap)
}

// TestContextPack_DegradedSeedRendersMarker is the T2 fails-when-absent test:
// an empty-query (and a nil-SegmentManager, and a scripted Search error) call
// renders the explicit "semantic seed unavailable" marker, while a non-degraded
// zero-result seed renders NO marker. Deleting the seedDegraded marker
// rendering arm makes this test fail.
func TestContextPack_DegradedSeedRendersMarker(t *testing.T) {
	seedlessNodes := nodesByID()

	t.Run("empty query is degraded", func(t *testing.T) {
		deps := ctxPackDeps{
			segMgr: &ctxSearcher{hits: nil},
			gc:     &ctxCaller{nodesByID: seedlessNodes},
		}
		// json: explicit flag + marker.
		pack := runContextPack(t, deps, map[string]any{"query": ""})
		assert.True(t, pack.SeedDegraded)
		assert.Equal(t, seedDegradedMarker, pack.SeedMarker)
		// text: the marker line renders in the seed section.
		assertTextMarker(t, deps, map[string]any{"query": ""}, true)
	})

	t.Run("nil SegmentManager is degraded", func(t *testing.T) {
		deps := ctxPackDeps{segMgr: nil, gc: &ctxCaller{nodesByID: seedlessNodes}}
		pack := runContextPack(t, deps, map[string]any{"query": "has text"})
		assert.True(t, pack.SeedDegraded)
		assert.Equal(t, seedDegradedMarker, pack.SeedMarker)
	})

	t.Run("Search error is degraded", func(t *testing.T) {
		deps := ctxPackDeps{
			segMgr: &ctxSearcher{searchErr: errors.New("engine offline")},
			gc:     &ctxCaller{nodesByID: seedlessNodes},
		}
		pack := runContextPack(t, deps, map[string]any{"query": "has text"})
		assert.True(t, pack.SeedDegraded, "a transient Search fault must render the degraded marker")
		assert.Equal(t, seedDegradedMarker, pack.SeedMarker)
	})

	t.Run("non-degraded zero-result seed has NO marker", func(t *testing.T) {
		// A real run (non-nil mgr, non-empty query) that simply returns zero hits
		// must render a normal empty seed section — NO marker.
		deps := ctxPackDeps{
			segMgr: &ctxSearcher{hits: nil}, // zero hits, no error
			gc:     &ctxCaller{nodesByID: seedlessNodes},
		}
		pack := runContextPack(t, deps, map[string]any{"query": "has text"})
		assert.False(t, pack.SeedDegraded, "a genuine zero-result seed is NOT degraded")
		assert.Empty(t, pack.SeedMarker)
		assert.Empty(t, pack.Seeds)
		// text: no marker line.
		assertTextMarker(t, deps, map[string]any{"query": "has text"}, false)
	})
}

// assertTextMarker renders the pack in text format and asserts the degraded
// marker line is present (or absent).
func assertTextMarker(t *testing.T, deps ClientDeps, args map[string]any, wantMarker bool) {
	t.Helper()
	a := map[string]any{}
	maps.Copy(a, args)
	a["format"] = "text"
	a["operation"] = "recall"
	a["mode"] = "context"
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	res := handleRecallClient(context.Background(), deps, kgtools.CallToolParams{Name: "thoughts", Arguments: raw})
	require.False(t, res.IsError)
	body := toolResultText(res)
	if wantMarker {
		assert.Contains(t, body, seedDegradedMarker)
	} else {
		assert.NotContains(t, body, seedDegradedMarker)
	}
}
