// SPDX-License-Identifier: Apache-2.0

// deps_segments_subset_fake_test.go — the SUBSET-seam half of the shared
// Search-only segment double.
//
// IT IS A SEPARATE SEAM AND SO A SEPARATE FILE. SegmentSubsetSearcher was added
// beside SegmentSearcher rather than folded into it, precisely so the ~15
// Search-only doubles in this package keep compiling; the double's two halves
// reading as two files says the same thing. It also keeps
// intercept_search_knowledge_test.go, which declares the double itself, inside
// the repo's 500-line file cap.

package tools

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// SearchAccepting satisfies SegmentSubsetSearcher, the narrow seam the practice
// hub-scoped search reaches for.
//
// IT APPLIES THE PREDICATE rather than accepting and discarding it. A double
// that took the predicate and returned the unfiltered hits would leave every
// hub-scoping assertion in this package green against a production path that
// had stopped narrowing — a double on the far side of the seam under test,
// which is the one thing a fixture must never be.
func (f *fakeSegmentSearcher) SearchAccepting(
	ctx context.Context, gt kgtypes.GraphType, name, queryText string, queryVec []byte, k int,
	accepts func(searchengine.ExternalID) bool,
) ([]searchengine.Hit, error) {
	hits, err := f.Search(ctx, gt, name, queryText, queryVec, k)
	if err != nil || accepts == nil {
		return hits, err
	}
	out := make([]searchengine.Hit, 0, len(hits))
	for _, h := range hits {
		if accepts(h.ID) {
			out = append(out, h)
		}
	}
	return out, nil
}
