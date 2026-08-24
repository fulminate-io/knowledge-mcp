// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// SearchOverlay is the TWO-POOL consumer entry point: it searches a base graph
// and its branch-overlay graph together and returns ONE ranked, fused hit list.
// It exists because a branch overlay and its base are two PARTITIONS of a single
// logical corpus, and the only place they can be compared on comparable numbers
// is BEFORE fusion, while raw engine scores still carry magnitude.
//
// Shape, per pool and then across them:
//   - each pool runs the same per-graph preamble and both engine arms via
//     searchPoolArms, and the two pools run CONCURRENTLY — this is four
//     independent engine searches, and serializing them would double branch
//     search latency on the hot read path;
//   - the two pools' arms are merged PER MODALITY on raw score (mergePoolHits),
//     so an HNSW hit is only ever compared against another HNSW hit;
//   - reciprocalRankFusion then runs EXACTLY ONCE, over the two merged modality
//     lists — the same single fusion Manager.Search performs.
//
// ASYMMETRIC ERROR CONTRACT, deliberately not symmetric. A base-pool failure is
// returned to the caller: the base pool is the bulk of the corpus, and serving
// an overlay-only result set under a healthy banner is exactly the failure this
// arm exists to prevent. An overlay-pool failure is logged and the base arms are
// served alone: that is precisely the pre-overlay behavior, and it is safe.
func (m *Manager) SearchOverlay(
	ctx context.Context,
	gt kgtypes.GraphType,
	base, overlay, queryText string,
	queryVec []byte,
	k int,
) ([]searchengine.Hit, error) {
	if k <= 0 {
		return nil, nil
	}

	var (
		baseHNSW, baseBM25       []searchengine.Hit
		overlayHNSW, overlayBM25 []searchengine.Hit
		baseErr, overlayErr      error
		wg                       sync.WaitGroup
	)
	wg.Go(func() {
		baseHNSW, baseBM25, baseErr = m.searchPoolArms(ctx, gt, base, queryText, queryVec, k)
	})
	wg.Go(func() {
		overlayHNSW, overlayBM25, overlayErr = m.searchPoolArms(ctx, gt, overlay, queryText, queryVec, k)
	})
	wg.Wait()

	if baseErr != nil {
		return nil, baseErr
	}
	if overlayErr != nil {
		slog.Warn("segmentdist: overlay pool search failed, serving the base pool alone",
			"graph", string(gt), "name", overlay, "error", overlayErr)
		overlayHNSW, overlayBM25 = nil, nil
	}

	// TRIGGER SITE for the residency budget, the overlay twin of Manager.Search's.
	// It runs after wg.Wait and the error arms, so BOTH searchPoolArms calls have
	// returned and released their read locks. The exclude set names BOTH graphs this
	// search served: a set built from one of them would leave the other evictable
	// while its goroutine may still hold a read lock, and enforceResidencyBudget
	// evicts under a write lock that Go's RWMutex will not grant reentrantly.
	m.enforceResidencyBudget([]graphKey{
		{graphType: gt, graphName: base},
		{graphType: gt, graphName: overlay},
	})

	hnsw := mergePoolHits(overlayHNSW, baseHNSW, k)
	bm25 := mergePoolHits(overlayBM25, baseBM25, k)

	return reciprocalRankFusion([][]searchengine.Hit{hnsw, bm25}, k), nil
}

// mergePoolHits merges one modality's hit lists from the overlay pool and the
// base pool into a single ranked list: overlay ids win a duplicate, everything
// else is ordered by RAW score descending with an ID-ascending tiebreak, then
// truncated to k. The tiebreak is the codebase's existing determinism rule
// (rrf.go's fused sort and searchengine/topk.go's mergeTopK use the same one).
//
// WHY THIS IS NOT THE OLD OVERLAY-OVER-BASE MERGE RELOCATED — the shapes look
// alike and the difference is the entire point. That merge ordered hits whose
// scores were ALREADY per-pool fusion outputs, i.e. 1/(60+rank): identical
// across pools at equal rank, and therefore carrying no relevance information at
// all. Ordering them could only interleave the two pools, which handed a branch
// changeset roughly half of every result set no matter how irrelevant it was.
// mergePoolHits orders RAW engine scores, before any fusion: HNSW scores are
// vector-to-vector similarity and corpus-independent, and BM25 scores carry
// term-rarity magnitude. An overlay document must now actually OUT-SCORE a base
// document to take its slot instead of inheriting one for free.
//
// THE RESIDUAL, ITS DIRECTION AND ITS ROUGH SIZE. The comparison is approximate,
// and the approximation is worth naming precisely rather than hedging. The two
// pools score against different set-level corpus stats — each pool's segment set
// carries its own — and BM25 computes idf as ln((N-n+0.5)/(n+0.5)+1). So the
// SMALLER pool's idf is computed against its own smaller N: for a term appearing
// once in each pool, idf is about 6.50 in a 1e3-document overlay against about
// 11.11 in a 1e5-document base, roughly 0.59x. The bias therefore runs AGAINST
// the overlay — the OPPOSITE direction from the defect this replaces, which is
// what makes an approximate cross-pool comparison acceptable here.
//
// CONSIDERED AND NOT TAKEN: the BM25 format can aggregate one combined corpus
// stats value across several segment sets, which would remove the mismatch
// entirely. It is not taken because the segmented index's Search offers no seam
// to inject stats from outside a pool's own segment set, so routing aggregated
// stats in would require a new search-engine API — a larger change than this fix
// carries.
//
// Dedup keeps the OVERLAY copy, unchanged and deliberately: the overlay copy of
// a changed file must shadow the stale base copy of the same node id.
func mergePoolHits(overlay, base []searchengine.Hit, k int) []searchengine.Hit {
	if k <= 0 {
		return nil
	}

	merged := make([]searchengine.Hit, 0, len(overlay)+len(base))
	seen := make(map[searchengine.ExternalID]struct{}, len(overlay))
	for _, hit := range overlay {
		seen[hit.ID] = struct{}{}
		merged = append(merged, hit)
	}
	for _, hit := range base {
		if _, dup := seen[hit.ID]; dup {
			continue
		}
		merged = append(merged, hit)
	}

	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		return merged[i].ID < merged[j].ID
	})

	if len(merged) > k {
		merged = merged[:k]
	}
	return merged
}
