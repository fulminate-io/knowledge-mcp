// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestPublishSubsetGate proves the publish-path safety gate — the
// corpus-wipe-recurrence guard: a degenerate or incomplete live
// set must NEVER drive a refcount-GC. Three cases, each asserting the prior
// corpus on the server SURVIVES because the publish is SKIPPED:
//
//	(a) NON-SUBSET: a live set referencing an id the server does NOT hold
//	    (a simulated incomplete/suspect load) → publish skipped, blobs intact.
//	(b) EMPTY: an empty live set (∅ ⊆ anything is a vacuous subset) → publish
//	    skipped before it can wipe the corpus.
//	(c) BELOW-RATIO: a non-empty live set far below the coverage ratio of the
//	    shipped corpus (a partial load) → publish skipped.
func TestPublishSubsetGate(t *testing.T) {
	t.Run("non_subset_skips_publish", func(t *testing.T) {
		svc, gc := newSegmentHarness(t)
		ctx := context.Background()

		// Ship a real corpus so the coverage denominator is armed.
		const corpusSegs = 3
		mgr := NewManager(gc, t.TempDir(), 0)
		for b := range corpusSegs {
			batch := hnswVecDocs(searchCorpusN)
			for i := range batch {
				batch[i].ID = fmt.Sprintf("ns-b%d-%s", b, batch[i].ID)
			}
			require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "nsRepo", batch))
		}
		prior := shippedHNSWIDs(svc)
		require.Len(t, prior, corpusSegs)

		dm := mgr.managerFor(kgtypes.GraphCode, "nsRepo")
		// A live set holding an id the SERVER DOES NOT have → not a subset of List(0).
		liveSet := map[searchengine.SegmentID]struct{}{"not-on-server-id": {}}
		ok, reason, err := dm.publishCoverageOK(ctx, liveSet)
		require.NoError(t, err)
		require.False(t, ok, "a non-subset live set must NOT be publishable")
		require.Contains(t, reason, "subset")

		// The prior corpus is untouched (the gate prevents any GC).
		require.Equal(t, prior, shippedHNSWIDs(svc), "non-subset gate leaves the corpus intact")
	})

	t.Run("empty_live_set_skips_publish", func(t *testing.T) {
		svc, gc := newSegmentHarness(t)
		ctx := context.Background()

		const corpusSegs = 3
		mgr := NewManager(gc, t.TempDir(), 0)
		for b := range corpusSegs {
			batch := hnswVecDocs(searchCorpusN)
			for i := range batch {
				batch[i].ID = fmt.Sprintf("mt-b%d-%s", b, batch[i].ID)
			}
			require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "emptyRepo", batch))
		}
		prior := shippedHNSWIDs(svc)
		require.Len(t, prior, corpusSegs)

		dm := mgr.managerFor(kgtypes.GraphCode, "emptyRepo")
		// publishResident over an EMPTY resident set must SKIP (return no error, no
		// dropped ids) and leave every blob intact — the vacuous-subset wipe guard.
		dropped, err := dm.publishResident(ctx, nil, nil, dm.locallyShipped)
		require.NoError(t, err)
		require.Empty(t, dropped, "an empty publish drops nothing (it is skipped, not a wipe)")
		require.Equal(t, prior, shippedHNSWIDs(svc),
			"an empty live set must NEVER drive a refcount-GC — the corpus survives")

		// The gate itself reports the empty reason.
		ok, reason, err := dm.publishCoverageOK(ctx, map[searchengine.SegmentID]struct{}{})
		require.NoError(t, err)
		require.False(t, ok)
		require.Contains(t, reason, "empty")
	})

	t.Run("below_coverage_ratio_skips_publish", func(t *testing.T) {
		svc, gc := newSegmentHarness(t)
		ctx := context.Background()

		// Ship a large corpus (>= the coverage floor) so the ratio is meaningful.
		const corpusSegs = 4
		mgr := NewManager(gc, t.TempDir(), 0)
		for b := range corpusSegs {
			batch := hnswVecDocs(searchCorpusN)
			for i := range batch {
				batch[i].ID = fmt.Sprintf("br-b%d-%s", b, batch[i].ID)
			}
			require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphCode, "ratioRepo", batch))
		}
		prior := shippedHNSWIDs(svc)
		require.Len(t, prior, corpusSegs)

		// A FRESH manager that has NOT loaded the corpus: its resident set is tiny
		// (zero / one tail) relative to the shipped corpus → below the coverage ratio.
		fresh := NewManager(gc, t.TempDir(), 0)
		fdm := fresh.managerFor(kgtypes.GraphCode, "ratioRepo")
		// Single shipped id as a stand-in resident live set — far below the ratio.
		var anyID searchengine.SegmentID
		for id := range prior {
			anyID = id
			break
		}
		liveSet := map[searchengine.SegmentID]struct{}{anyID: {}}
		ok, reason, err := fdm.publishCoverageOK(ctx, liveSet)
		require.NoError(t, err)
		require.False(t, ok, "a resident set far below the shipped corpus must be gated")
		require.Contains(t, reason, "coverage ratio")

		require.Equal(t, prior, shippedHNSWIDs(svc), "below-ratio gate leaves the corpus intact")
	})
}
