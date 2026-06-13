// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_recall_rerank.go — the recall-side rerank stage that
// brings thoughts(operation:recall) to parity with the search tool. It reuses
// the SAME rerank.Reranker.Rerank primitive search uses (search_rerank.go:52),
// bridging clientthought.ThoughtResult <-> engine.SearchResult so the wide pool
// gathered by RecallThoughts can be re-scored by Voyage. The bridge lives here
// (not in the thought package) because thought/query.go deliberately stays
// import-clean of engine/rerank (query.go:21-27).

package tools

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/rerank"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// thoughtResultsToEngine maps recall candidates onto the rerank input shape.
// Both types wrap *knowledgev1.Node + a float score, so this is a pure field
// map with no conversion logic.
func thoughtResultsToEngine(results []clientthought.ThoughtResult) []engine.SearchResult {
	out := make([]engine.SearchResult, len(results))
	for i, r := range results {
		out[i] = engine.SearchResult{Node: r.Node, Score: r.Score}
	}
	return out
}

// engineResultsToThought rebuilds the reordered []ThoughtResult from the
// reranked []engine.SearchResult and the ORIGINAL candidates. It indexes the
// originals by Node.Id, walks the reranked slice IN ORDER recovering each
// candidate's Properties + SessionName, and OVERWRITES Score with the reranked
// engine.SearchResult.Score (the Voyage relevance — voyage.go:198 sets
// Score=d.RelevanceScore; engine.SearchResult carries only Node+Score, there is
// no separate RelevanceScore field). A reranked node whose Id is not among the
// originals is skipped defensively.
func engineResultsToThought(
	reranked []engine.SearchResult, originals []clientthought.ThoughtResult,
) []clientthought.ThoughtResult {
	byID := make(map[string]clientthought.ThoughtResult, len(originals))
	for _, r := range originals {
		byID[r.Node.GetId()] = r
	}
	out := make([]clientthought.ThoughtResult, 0, len(reranked))
	for _, e := range reranked {
		orig, ok := byID[e.Node.GetId()]
		if !ok {
			continue // reranked a node we did not send — skip defensively.
		}
		orig.Score = e.Score // the reranked Voyage relevance score.
		out = append(out, orig)
	}
	return out
}

// rerankRecallResults re-scores the wide candidate pool through the supplied
// rerank.Reranker — the SAME primitive search uses (search_rerank.go:52), not a
// duplicated rerank implementation and not a package-var hook. The reranker is
// an EXPLICIT parameter so the caller (handleRecallClient) owns construction
// (rerank.NewVoyage) and the empty-key gate.
//
// Degradation: a nil reranker (the caller's empty-key gate) returns the input
// unchanged with didRerank=false and never calls Rerank — RRF ordering is
// preserved. A Rerank error likewise returns the input unchanged, didRerank
// false (search's silent-degrade contract: never swap a real result set for a
// fault). On success the pool is reordered by Voyage relevance, didRerank true.
func rerankRecallResults(
	ctx context.Context, query string, results []clientthought.ThoughtResult, reranker rerank.Reranker,
) (out []clientthought.ThoughtResult, didRerank bool) {
	if reranker == nil || len(results) == 0 {
		return results, false
	}
	reranked, err := reranker.Rerank(ctx, query, thoughtResultsToEngine(results))
	if err != nil {
		// Silent-degrade — do NOT surface the fault; keep the RRF ordering
		// (search_rerank.go:53-57's contract). RRF is preserved, didRerank=false.
		slog.Debug("recall rerank: degrading to RRF ordering", "err", err.Error(), "pool", len(results))
		return results, false
	}
	return engineResultsToThought(reranked, results), true
}
