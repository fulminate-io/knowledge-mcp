// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"testing"
	"time"

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
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
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
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
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
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
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
		fresh := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
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

// memoFixture is the shared setup every TestPublishCoverageDenominatorMemo subtest
// builds: one harness holding a corpus-owning manager (its resident IS the full
// shipped corpus, so coverage PASSES) plus a FRESH manager that has never loaded
// (resident 0, so coverage SKIPS), and a one-id live set drawn from the shipped
// corpus. Each subtest takes its own fixture under its own graph name so the shared
// server state and the fake source's counters never leak between subtests.
type memoFixture struct {
	svc     *sharedServerFake
	gc      *fakeSegmentSource
	owner   *Manager
	dm      *distManager[[]byte, struct{}]
	fresh   *Manager
	fdm     *distManager[[]byte, struct{}]
	liveSet map[searchengine.SegmentID]struct{}
}

func newMemoFixture(t *testing.T, name string) memoFixture {
	t.Helper()
	ctx := context.Background()
	svc, gc := newSegmentHarness(t)

	// Ship a large corpus (>= the coverage floor) so the ratio is armed.
	const corpusSegs = 4
	owner := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	for b := range corpusSegs {
		batch := prefixIDs(hnswVecDocs(searchCorpusN), fmt.Sprintf("%s-b%d-", name, b))
		require.NoError(t, owner.AddAndShip(ctx, kgtypes.GraphCode, name, batch))
	}
	prior := shippedHNSWIDs(svc)
	require.Len(t, prior, corpusSegs)

	// A FRESH manager that has NOT loaded the corpus: its resident set is 0, far
	// below the coverage ratio, so its publishCoverageOK always returns the skip.
	fresh := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	var anyID searchengine.SegmentID
	for id := range prior {
		anyID = id
		break
	}
	return memoFixture{
		svc:     svc,
		gc:      gc,
		owner:   owner,
		dm:      owner.managerFor(kgtypes.GraphCode, name),
		fresh:   fresh,
		fdm:     fresh.managerFor(kgtypes.GraphCode, name),
		liveSet: map[searchengine.SegmentID]struct{}{anyID: {}},
	}
}

// TestPublishCoverageDenominatorMemo covers the short-TTL memo over the PUBLISH-path
// shipped-denominator read. Without it every coverage-skip attempt pays a full
// source.List(0) round-trip through shippedDocCountForRatio, so a skip storm rings up
// one network read per attempt forever.
//
// `repeated_skips_pay_one_list` is the RED-FIRST cost reproduction: against the
// unmemoized tree five consecutive skip verdicts cost five Lists.
// `verdict_unchanged_across_repeats` is a CHARACTERIZATION GUARD, green both before
// and after — pre-change both calls are cold reads, post-change the second is a memo
// hit, and the point is that the verdict is identical either way. It is not a
// reproduction and must not be read as one.
//
// The remaining four subtests — the two invalidation hooks, the TTL expiry and the
// pass-path re-verify — are GREEN-ONLY verification, not reproductions: they name the
// coverageMemo field and the coverageDenominator type, which do not exist on an
// unmemoized tree, so they could never have been observed failing there.
func TestPublishCoverageDenominatorMemo(t *testing.T) {
	t.Run("repeated_skips_pay_one_list", func(t *testing.T) {
		ctx := context.Background()
		f := newMemoFixture(t, "memoRepo")

		// The skip verdict returns before the subset section, so each cold call costs
		// exactly the one shippedDocCountForRatio List.
		base := f.gc.listCalls.Load()
		for range 5 {
			ok, reason, err := f.fdm.publishCoverageOK(ctx, f.liveSet)
			require.NoError(t, err)
			require.False(t, ok, "a resident set far below the shipped corpus must be gated")
			require.Contains(t, reason, "coverage ratio")
		}
		require.Equal(t, int64(1), f.gc.listCalls.Load()-base,
			"five consecutive coverage skips inside the TTL pay exactly ONE denominator List")
	})

	t.Run("verdict_unchanged_across_repeats", func(t *testing.T) {
		ctx := context.Background()
		f := newMemoFixture(t, "memoVerdictRepo")

		okFirst, reasonFirst, err := f.fdm.publishCoverageOK(ctx, f.liveSet)
		require.NoError(t, err)
		okSecond, reasonSecond, err := f.fdm.publishCoverageOK(ctx, f.liveSet)
		require.NoError(t, err)
		require.Equal(t, okFirst, okSecond, "the memo does not change the verdict")
		require.Equal(t, reasonFirst, reasonSecond, "the memo does not change the skip reason")
	})

	// Asserts POINTER IDENTITY, not nil. AddAndShip does not stop at the ship: it runs
	// shipAndPublish → shipNew (which invalidates) → publishResident →
	// publishCoverageOK, and that coverage read is now a MISS, so it Lists and
	// RE-STORES a fresh entry before AddAndShip returns. The memo is therefore non-nil
	// at the end of a correct run, and `== nil` would fail a correct implementation.
	// The trailing publish cannot clear it either: after the ship the server holds
	// 4+1 segments, so the denominator is 5×searchCorpusN while this fresh manager's
	// resident is the one sealed segment — below the ratio, so the publish skips.
	//
	// NotSame is an exact discriminator: WITHOUT the shipNew hook the memo still holds
	// `before` unexpired, the denominator read is a pure hit, the below-ratio verdict
	// returns before the re-verify, and nothing is ever stored — the pointer is
	// unchanged. `before` stays referenced for the whole subtest, so the address cannot
	// be recycled underneath the comparison.
	t.Run("own_ship_invalidates_the_memo", func(t *testing.T) {
		ctx := context.Background()
		f := newMemoFixture(t, "memoShipRepo")

		_, _, err := f.fdm.publishCoverageOK(ctx, f.liveSet)
		require.NoError(t, err)
		before := f.fdm.coverageMemo.Load()
		require.NotNil(t, before, "the cold read warmed the memo")

		// THIS manager ships: a full batch seals a segment, so shipNew uploads it.
		require.NoError(t, f.fresh.AddAndShip(ctx, kgtypes.GraphCode, "memoShipRepo",
			prefixIDs(hnswVecDocs(searchCorpusN), "memo-ship-")))

		after := f.fdm.coverageMemo.Load()
		require.NotNil(t, after, "the publish leg's cold read re-stored an entry")
		require.NotSame(t, before, after, "this manager's own ship invalidated the memo it had warmed")
	})

	// Here `== nil` IS correct: publishResident's success path is the LAST thing to
	// touch the memo, so no further coverage read follows inside the call. Fails
	// precisely when the publishResident invalidation is missing.
	t.Run("successful_publish_invalidates_the_memo", func(t *testing.T) {
		ctx := context.Background()
		f := newMemoFixture(t, "memoPublishRepo")

		// The corpus-owning manager has the whole corpus resident, so its coverage
		// passes and the publish below actually LANDS.
		_, _, err := f.dm.publishCoverageOK(ctx, f.liveSet)
		require.NoError(t, err)
		require.NotNil(t, f.dm.coverageMemo.Load(), "the cold read warmed the memo")

		pubBefore := f.gc.publishCalls.Load()
		_, err = f.dm.publishResident(ctx, f.dm.engine.Export(), nil, f.dm.locallyShipped)
		require.NoError(t, err)
		require.Equal(t, pubBefore+1, f.gc.publishCalls.Load(),
			"the publish reached PublishManifest — it landed rather than being gate-skipped")

		require.Nil(t, f.dm.coverageMemo.Load(), "a successful publish invalidated the memo")
	})

	// THE SAFETY TEST: a publish never proceeds on a value served from the memo. With
	// the source verifying completeness server-side the subset branch short-circuits,
	// so a passing call costs exactly the denominator read and the List arithmetic is
	// unambiguous: the memo hit contributes 0 and the mandatory re-verify contributes 1.
	// Without the re-verify the delta is 0 — this is what fails if the second read is
	// "optimized away" and a publish is allowed to proceed on a cached denominator.
	// Single-threaded by construction: the delta-of-exactly-1 claim assumes no
	// concurrent attempt is in flight, which is true for a serial test but is NOT a
	// general invariant of the design.
	t.Run("memo_derived_pass_reverifies_fresh", func(t *testing.T) {
		ctx := context.Background()
		f := newMemoFixture(t, "memoReverifyRepo")
		f.gc.verifies = true

		ok, _, err := f.dm.publishCoverageOK(ctx, f.liveSet)
		require.NoError(t, err)
		require.True(t, ok, "the corpus-owning manager's coverage passes")

		base := f.gc.listCalls.Load()
		ok, _, err = f.dm.publishCoverageOK(ctx, f.liveSet)
		require.NoError(t, err)
		require.True(t, ok, "the verdict is still a pass after the re-verify")
		require.Equal(t, int64(1), f.gc.listCalls.Load()-base,
			"a memo-derived PASS re-derives the denominator: 0 for the hit, 1 for the mandatory re-read")
		require.NotNil(t, f.dm.coverageMemo.Load(), "the re-verify re-stored a fresh entry")
	})

	// Constructing an already-expired entry directly is deliberate: it gives
	// deterministic expiry coverage with no sleep and no injected clock, and keeps
	// coveragePublishMemoTTL a plain const rather than a mutable package var. Without
	// the expiry check the delta would be 0.
	t.Run("expired_memo_refetches", func(t *testing.T) {
		ctx := context.Background()
		f := newMemoFixture(t, "memoExpiryRepo")

		_, _, err := f.fdm.publishCoverageOK(ctx, f.liveSet)
		require.NoError(t, err)
		warm := f.fdm.coverageMemo.Load()
		require.NotNil(t, warm, "the cold read warmed the memo")

		// Same value, already expired.
		f.fdm.coverageMemo.Store(&coverageDenominator{
			shipped: warm.shipped,
			disarm:  warm.disarm,
			expires: time.Now().Add(-time.Second),
		})

		base := f.gc.listCalls.Load()
		ok, _, err := f.fdm.publishCoverageOK(ctx, f.liveSet)
		require.NoError(t, err)
		require.False(t, ok, "the fresh manager is still below the coverage ratio")
		require.Equal(t, int64(1), f.gc.listCalls.Load()-base,
			"an expired entry is not served — the denominator is re-read")
	})
}
