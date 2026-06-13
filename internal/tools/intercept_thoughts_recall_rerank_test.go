// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// TestRerankRecallResults_PromotesBuriedCandidate is Phase 4 Step 1's criterion:
// a fake rerank.Reranker injected via rerankRecallResults' EXPLICIT parameter
// reorders the wide pool by relevance — promoting a buried candidate to the top —
// renders the reranked Score, and recovers Properties/SessionName by Node.Id
// through the round-trip adapter. No network, no config, no package-var hook.
func TestRerankRecallResults_PromotesBuriedCandidate(t *testing.T) {
	// Input pool in RRF/wide-pool order (descending input Score). "c" is buried
	// last in the input but the fake scores it highest — it must surface to top.
	mk := func(id string, score, valence float64, session string) clientthought.ThoughtResult {
		return clientthought.ThoughtResult{
			Node:        &knowledgev1.Node{Id: id, SymbolName: id},
			Score:       score,
			Properties:  clientthought.ThoughtProperties{Valence: valence},
			SessionName: session,
		}
	}
	input := []clientthought.ThoughtResult{
		mk("a", 0.033, 0.1, "sess-a"),
		mk("b", 0.020, 0.2, "sess-b"),
		mk("c", 0.011, 0.3, "sess-c"), // buried last in the input
	}

	// Fake reranker assigns relevance: c=0.91 (highest), a=0.55, b=0.40. Returns
	// in relevance-descending order to mirror Voyage's reordered output.
	relevance := map[string]float64{"a": 0.55, "b": 0.40, "c": 0.91}
	fake := newStubReranker(func(_ context.Context, _ string, in []engine.SearchResult) ([]engine.SearchResult, error) {
		out := make([]engine.SearchResult, len(in))
		for i, e := range in {
			out[i] = engine.SearchResult{Node: e.Node, Score: relevance[e.Node.GetId()]}
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
		return out, nil
	})

	got, didRerank := rerankRecallResults(context.Background(), "q", input, fake)
	require.True(t, didRerank, "a non-nil reranker that succeeds sets didRerank=true")
	require.Len(t, got, 3, "no candidate dropped")

	// (1)+(4) reordered by relevance — buried "c" promoted to the top.
	assert.Equal(t, "c", got[0].Node.GetId(), "buried candidate promoted to top by relevance")
	assert.Equal(t, "a", got[1].Node.GetId())
	assert.Equal(t, "b", got[2].Node.GetId())

	// (2) each rendered Score equals the fake's returned relevance score.
	assert.InDelta(t, 0.91, got[0].Score, 1e-9, "reranked Voyage relevance is rendered")
	assert.InDelta(t, 0.55, got[1].Score, 1e-9)
	assert.InDelta(t, 0.40, got[2].Score, 1e-9)

	// (3) Properties/SessionName recovered by Node.Id through the from-engine adapter.
	assert.Equal(t, "sess-c", got[0].SessionName, "SessionName recovered by Id")
	assert.InDelta(t, 0.3, got[0].Properties.Valence, 1e-9, "Properties recovered by Id")
	assert.Equal(t, "sess-a", got[1].SessionName)
	assert.InDelta(t, 0.1, got[1].Properties.Valence, 1e-9)
}

// TestRerankRecallResults_NilRerankerDegrades asserts the empty-key gate: a nil
// reranker returns the input unchanged with didRerank=false (RRF preserved),
// never calling Rerank.
func TestRerankRecallResults_NilRerankerDegrades(t *testing.T) {
	input := []clientthought.ThoughtResult{
		{Node: &knowledgev1.Node{Id: "a"}, Score: 0.033},
		{Node: &knowledgev1.Node{Id: "b"}, Score: 0.020},
	}
	got, didRerank := rerankRecallResults(context.Background(), "q", input, nil)
	assert.False(t, didRerank, "nil reranker → didRerank=false")
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Node.GetId(), "RRF order preserved")
	assert.Equal(t, "b", got[1].Node.GetId())
}
