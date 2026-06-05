// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// rrfK is the standard reciprocal-rank-fusion damping constant. The fused
// contribution of a hit at 0-based rank r is 1/(rrfK + r + 1), so the top hit
// of each list contributes 1/(rrfK+1) and the constant flattens the difference
// between adjacent ranks deep in a list. 60 is the canonical value from the
// original Cormack et al. RRF paper and the one Elastic/Weaviate default to.
const rrfK = 60

// reciprocalRankFusion fuses one or more ranked Hit lists into a single ranked
// list by standard reciprocal rank fusion: for each list, a hit at 0-based rank
// r accrues 1/(rrfK + r + 1) into its per-ID score; scores are summed across
// lists keyed by Hit.ID, sorted descending, and truncated to k.
//
// This is the standard RRF of decision #2, NOT the server's misnamed rrf.go
// SearchFuse (which is min-max relativeScoreFusion — a different algorithm in a
// different, soon-to-be-cut module that cmd/knowledge cannot import anyway per
// the module boundary). It is a pure fold over the engine's Hit type with no
// per-item I/O, so a serial pass over two short top-k lists is correct and
// cheap.
//
// Modality note: the signature imposes no two-list
// assumption. A single-element lists slice yields scores of 1/(rrfK+rank), i.e.
// that one ranking unchanged (no fusion), so a future single-modality graph
// degrades gracefully. All four migrated arms are both-modality today, so in
// practice Manager.Search passes two lists.
func reciprocalRankFusion(lists [][]searchengine.Hit, k int) []searchengine.Hit {
	if k <= 0 {
		return nil
	}

	sizeHint := 0
	for _, list := range lists {
		sizeHint += len(list)
	}
	scores := make(map[searchengine.ExternalID]float64, sizeHint)
	for _, list := range lists {
		for rank, hit := range list {
			scores[hit.ID] += 1.0 / float64(rrfK+rank+1)
		}
	}

	if len(scores) == 0 {
		return nil
	}

	fused := make([]searchengine.Hit, 0, len(scores))
	for id, score := range scores {
		fused = append(fused, searchengine.Hit{ID: id, Score: score})
	}
	sort.Slice(fused, func(i, j int) bool {
		if fused[i].Score != fused[j].Score {
			return fused[i].Score > fused[j].Score
		}
		// Stable tiebreak by ID so the order is deterministic across runs.
		return fused[i].ID < fused[j].ID
	})

	if len(fused) > k {
		fused = fused[:k]
	}
	return fused
}
