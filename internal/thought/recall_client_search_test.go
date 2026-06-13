// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// recallFakeCaller is an Execute-only Caller that serves the hydrate ids[] read
// (RETURN_MODE_NODES) and records whether any SERVER search plan was dispatched
// — the thing the GO-LIVE recall reroute must NOT do.
type recallFakeCaller struct {
	nodes         map[string]*knowledgev1.Node
	sawServerSrch atomic.Bool
}

func (c *recallFakeCaller) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q != nil && (q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_SEARCH || len(q.GetQueries()) > 0) {
		c.sawServerSrch.Store(true)
	}
	// Serve the ids[] hydrate read.
	out := make([]*knowledgev1.Node, 0)
	if q != nil {
		for _, id := range q.GetIds() {
			if n, ok := c.nodes[id]; ok {
				out = append(out, n)
			}
		}
	}
	return &knowledgev1.ExecuteResponse{Nodes: out}, nil
}

// recallFakeSearcher returns canned RRF Hits and records the query vector it was
// handed (to prove the HNSW arm is exercised).
type recallFakeSearcher struct {
	calls   atomic.Int64
	lastVec []byte
	hits    []searchengine.Hit
}

func (s *recallFakeSearcher) Search(
	_ context.Context, _ kgtypes.GraphType, _, _ string, queryVec []byte, _ int,
) ([]searchengine.Hit, error) {
	s.calls.Add(1)
	s.lastVec = queryVec
	return s.hits, nil
}

// TestRecallSourcesFromClientEngine is Phase 3 Step 2's criterion (B): a thought
// recall with a non-empty Query sources candidates from the CLIENT knowledge
// engine (Manager.Search) WITH a query vector (HNSW arm exercised), not a server
// search dispatch. The thought-only type filter is preserved.
func TestRecallSourcesFromClientEngine(t *testing.T) {
	thoughtNode := &knowledgev1.Node{Id: "t1", Type: string(kgtypes.NodeThought), SymbolName: "a thought"}
	otherNode := &knowledgev1.Node{Id: "x1", Type: "finding", SymbolName: "not a thought"}
	caller := &recallFakeCaller{nodes: map[string]*knowledgev1.Node{"t1": thoughtNode, "x1": otherNode}}
	searcher := &recallFakeSearcher{hits: []searchengine.Hit{
		{ID: "t1", Score: 0.9},
		{ID: "x1", Score: 0.8}, // non-thought — filtered out
	}}

	opts := RecallOptions{
		Query:    "design rationale",
		QueryVec: []byte("0123456789abcdef0123456789abcdef"), // 32 bytes — the client-embedded vector
		Searcher: searcher,
		Limit:    20,
	}

	candidates, err := searchRecallCandidates(context.Background(), caller, opts)
	require.NoError(t, err)

	// Client engine drove the gather, WITH the query vector (HNSW arm).
	require.Equal(t, int64(1), searcher.calls.Load(), "Manager.Search sourced the candidates")
	require.NotEmpty(t, searcher.lastVec, "the client-embedded query vector reached the HNSW arm")
	require.False(t, caller.sawServerSrch.Load(), "recall must NOT dispatch a server search")

	// Only the thought node survives the thought-only type filter; fused score carried.
	require.Len(t, candidates, 1)
	require.Equal(t, "t1", candidates[0].Node.GetId())
	require.InDelta(t, 0.9, candidates[0].Score, 1e-12)
}

// TestRecallNoSearcherYieldsNoCandidatesNoServerSearch asserts the
// unconditional-client contract for the degraded path: with NO Searcher wired,
// searchRecallCandidates returns zero candidates and NEVER dispatches a server
// search (the server-search gather fetchQueryBySearch was deleted). The real
// recall interceptor always wires the Searcher; this guards the nil-Searcher
// harness case (gatherRecallCandidates then falls back to full iteration).
func TestRecallNoSearcherYieldsNoCandidatesNoServerSearch(t *testing.T) {
	caller := &recallServerProbeCaller{}
	opts := RecallOptions{Query: "anything", Limit: 20} // Searcher nil → no client gather

	candidates, err := searchRecallCandidates(context.Background(), caller, opts)
	require.NoError(t, err)
	require.Empty(t, candidates, "nil Searcher yields no candidates")
	require.False(t, caller.executed.Load(), "nil Searcher must NOT dispatch any server call")
}

// TestRecallThoughtsWidePoolSkipsTrim is Phase 1 Step 1's criterion: with
// WidePool>0 RecallThoughts returns ALL filtered candidates untrimmed in score
// order (so the intercept can rerank the wide pool); with WidePool=0 the result
// is trimmed to Limit. Verifies the widen+skip-trim seam is gated on WidePool.
func TestRecallThoughtsWidePoolSkipsTrim(t *testing.T) {
	// Five thought hits, descending score; Limit=2 so trimming is observable.
	nodes := map[string]*knowledgev1.Node{}
	hits := make([]searchengine.Hit, 0, 5)
	for i, score := range []float64{0.9, 0.8, 0.7, 0.6, 0.5} {
		id := string(rune('a' + i))
		nodes[id] = &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), SymbolName: id}
		hits = append(hits, searchengine.Hit{ID: id, Score: score})
	}
	newOpts := func(widePool int) RecallOptions {
		return RecallOptions{
			Query:    "q",
			Searcher: &recallFakeSearcher{hits: hits},
			Limit:    2,
			WidePool: widePool,
		}
	}

	// WidePool>0: untrimmed, score-sorted (all 5).
	wide, err := RecallThoughts(context.Background(),
		&recallFakeCaller{nodes: nodes}, newOpts(5))
	require.NoError(t, err)
	require.Len(t, wide, 5, "WidePool>0 returns the full filtered pool untrimmed")
	for i := 1; i < len(wide); i++ {
		require.GreaterOrEqual(t, wide[i-1].Score, wide[i].Score, "wide pool is score-sorted")
	}

	// WidePool=0: trimmed to Limit.
	trimmed, err := RecallThoughts(context.Background(),
		&recallFakeCaller{nodes: nodes}, newOpts(0))
	require.NoError(t, err)
	require.Len(t, trimmed, 2, "WidePool=0 trims to Limit")
}

// recallServerProbeCaller is an Execute-only Caller that records whether any
// server Execute was dispatched.
type recallServerProbeCaller struct{ executed atomic.Bool }

func (c *recallServerProbeCaller) Execute(
	_ context.Context, _ *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	c.executed.Store(true)
	return &knowledgev1.ExecuteResponse{}, nil
}
