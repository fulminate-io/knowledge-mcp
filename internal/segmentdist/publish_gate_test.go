// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// sharedCorpusBlobs seals the publish-gate corpus ONCE for the whole package and
// hands every fixture the same in-memory blobs to import.
//
// The corpus is the expensive part by a wide margin — the vector index build and
// its re-emit, paid nine times over when each subtest sealed its own. The document
// slices are cheap and are NOT what is shared here; the sealed segments are.
//
// Only the artifact is shared. Every subtest still gets its own server, its own
// managers and its own counters, because the alternative — one live harness across
// subtests — makes them order-dependent: one ships an extra segment and moves the
// coverage denominator for everyone after it, another drives a refcount GC on the
// shared server, and a third flips a verify flag that would leak forward. Order
// dependence is exactly the class of defect this package's ticket is about.
//
// The builder outlives every test, so nothing else will clean up after it; it owns
// its cache directory and removes it on the way out, and it closes its own Manager
// for the same reason — there is no test to register either cleanup with. Both are
// safe because Export returns in-memory blobs: the on-disk files are never read
// again, and Close retires the engines' background mergers only.
var sharedCorpusBlobs = sync.OnceValue(func() []searchengine.SegmentBlob {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "segmentdist-sharedcorpus-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	svc := newSharedServerFake()
	gc := svc.viewFor(&knowledgev1.GraphSelector{}, "")
	owner := NewManager(loginStateStub{loggedIn: true}, dir, 0, withSegmentSource(gc))
	defer owner.Close()
	const corpusSegs = 4
	for b := range corpusSegs {
		batch := prefixIDs(hnswVecDocs(searchCorpusN), fmt.Sprintf("shared-b%d-", b))
		if err := owner.AddAndMarkDirty(ctx, kgtypes.GraphCode, "sharedCorpus", batch); err != nil {
			panic(err)
		}
	}
	if err := owner.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "sharedCorpus"); err != nil {
		panic(err)
	}
	blobs := owner.managerFor(kgtypes.GraphCode, "sharedCorpus").engine.Export()
	if len(blobs) != 4 {
		panic(fmt.Sprintf("shared corpus must seal into 4 partitions, got %d", len(blobs)))
	}
	return blobs
})

// seedSharedCorpus gives one fixture a corpus-owning manager whose resident set IS
// the full shipped corpus, by importing the prebuilt blobs and shipping them. It
// returns that manager, its engine and the set of ids now on the caller's server.
//
// Each caller passes its OWN server and source, so the shared bytes are the only
// thing crossing subtest boundaries.
func seedSharedCorpus(t *testing.T, svc *sharedServerFake, gc *fakeSegmentSource, name string,
) (*Manager, *distManager[[]byte, struct{}], map[searchengine.SegmentID]struct{}) {
	t.Helper()
	ctx := context.Background()
	owner := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	dm := owner.managerFor(kgtypes.GraphCode, name)
	require.NoError(t, dm.engine.Import(sharedCorpusBlobs(), nil))
	warmExported(dm)
	_, err := dm.shipAndPublish(ctx, dm.locallyShipped)
	require.NoError(t, err)
	prior := shippedHNSWIDs(svc)
	require.NotEmpty(t, prior)
	return owner, dm, prior
}

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
//
// Each case seeds from the package-wide prebuilt corpus into its own harness, so
// the sealed segments are shared but the server, managers and counters are not.
func TestPublishSubsetGate(t *testing.T) {
	t.Parallel()

	t.Run("non_subset_skips_publish", func(t *testing.T) {
		svc, gc := newSegmentHarness(t)
		ctx := context.Background()

		_, dm, prior := seedSharedCorpus(t, svc, gc, "nsRepo")

		// A live set holding an id the SERVER DOES NOT have → not a subset of List(0).
		liveSet := map[searchengine.SegmentID]struct{}{"not-on-server-id": {}}
		ok, reason, err := dm.publishCoverageOK(ctx, liveSet, dm.engine.ResidentDocCount())
		require.NoError(t, err)
		require.False(t, ok, "a non-subset live set must NOT be publishable")
		require.Contains(t, reason, "subset")

		// The prior corpus is untouched (the gate prevents any GC).
		require.Equal(t, prior, shippedHNSWIDs(svc), "non-subset gate leaves the corpus intact")
	})

	t.Run("empty_live_set_skips_publish", func(t *testing.T) {
		svc, gc := newSegmentHarness(t)
		ctx := context.Background()

		_, dm, prior := seedSharedCorpus(t, svc, gc, "emptyRepo")

		// publishResident over an EMPTY resident set must SKIP (return no error, no
		// dropped ids) and leave every blob intact — the vacuous-subset wipe guard.
		dropped, err := dm.publishResident(ctx, nil, dm.locallyShipped)
		require.NoError(t, err)
		require.Empty(t, dropped, "an empty publish drops nothing (it is skipped, not a wipe)")
		require.Equal(t, prior, shippedHNSWIDs(svc),
			"an empty live set must NEVER drive a refcount-GC — the corpus survives")

		// The gate itself reports the empty reason.
		ok, reason, err := dm.publishCoverageOK(ctx, map[searchengine.SegmentID]struct{}{}, dm.engine.ResidentDocCount())
		require.NoError(t, err)
		require.False(t, ok)
		require.Contains(t, reason, "empty")
	})

	t.Run("below_coverage_ratio_skips_publish", func(t *testing.T) {
		svc, gc := newSegmentHarness(t)
		ctx := context.Background()

		_, _, prior := seedSharedCorpus(t, svc, gc, "ratioRepo")

		// A FRESH manager that has NOT loaded the corpus: its resident set is tiny
		// (zero / one tail) relative to the shipped corpus → below the coverage ratio.
		fresh := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
		fdm := fresh.managerFor(kgtypes.GraphCode, "ratioRepo")
		// Single shipped id as a stand-in resident live set — far below the ratio.
		var anyID searchengine.SegmentID
		for id := range prior {
			anyID = id
			break
		}
		liveSet := map[searchengine.SegmentID]struct{}{anyID: {}}
		ok, reason, err := fdm.publishCoverageOK(ctx, liveSet, fdm.engine.ResidentDocCount())
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
// server state and the fake source's counters never leak between subtests — only
// the sealed corpus bytes are common, and they are read-only.
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
	svc, gc := newSegmentHarness(t)

	// The corpus (>= the coverage floor, so the ratio is armed) is imported from
	// the package-wide prebuilt blobs rather than sealed again here.
	owner, dm, prior := seedSharedCorpus(t, svc, gc, name)

	// A FRESH manager that has NOT loaded the corpus: its resident set is 0, far
	// below the coverage ratio, so its publishCoverageOK always returns the skip.
	fresh := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	var anyID searchengine.SegmentID
	for id := range prior {
		anyID = id
		break
	}
	return memoFixture{
		svc:     svc,
		gc:      gc,
		owner:   owner,
		dm:      dm,
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
	t.Parallel()

	t.Run("repeated_skips_pay_one_list", func(t *testing.T) {
		ctx := context.Background()
		f := newMemoFixture(t, "memoRepo")

		// The skip verdict returns before the subset section, so each cold call costs
		// exactly the one shippedDocCountForRatio List.
		base := f.gc.listCalls.Load()
		for range 5 {
			ok, reason, err := f.fdm.publishCoverageOK(ctx, f.liveSet, f.fdm.engine.ResidentDocCount())
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

		okFirst, reasonFirst, err := f.fdm.publishCoverageOK(ctx, f.liveSet, f.fdm.engine.ResidentDocCount())
		require.NoError(t, err)
		okSecond, reasonSecond, err := f.fdm.publishCoverageOK(ctx, f.liveSet, f.fdm.engine.ResidentDocCount())
		require.NoError(t, err)
		require.Equal(t, okFirst, okSecond, "the memo does not change the verdict")
		require.Equal(t, reasonFirst, reasonSecond, "the memo does not change the skip reason")
	})

	// Asserts POINTER IDENTITY, not nil. The tick does not stop at the ship: it runs
	// shipAndPublish → shipNew (which invalidates) → publishResident →
	// publishCoverageOK, and that coverage read is now a MISS, so it Lists and
	// RE-STORES a fresh entry before the tick returns. The memo is therefore non-nil
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

		_, _, err := f.fdm.publishCoverageOK(ctx, f.liveSet, f.fdm.engine.ResidentDocCount())
		require.NoError(t, err)
		before := f.fdm.coverageMemo.Load()
		require.NotNil(t, before, "the cold read warmed the memo")

		// THIS manager ships: the write seals a segment and the tick uploads it.
		require.NoError(t, f.fresh.AddAndMarkDirty(ctx, kgtypes.GraphCode, "memoShipRepo",
			prefixIDs(hnswVecDocs(searchCorpusN), "memo-ship-")))
		require.NoError(t, f.fresh.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "memoShipRepo"))

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
		_, _, err := f.dm.publishCoverageOK(ctx, f.liveSet, f.dm.engine.ResidentDocCount())
		require.NoError(t, err)
		require.NotNil(t, f.dm.coverageMemo.Load(), "the cold read warmed the memo")

		pubBefore := f.gc.publishCalls.Load()
		_, err = f.dm.publishResident(ctx, f.dm.engine.Export(), f.dm.locallyShipped)
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

		ok, _, err := f.dm.publishCoverageOK(ctx, f.liveSet, f.dm.engine.ResidentDocCount())
		require.NoError(t, err)
		require.True(t, ok, "the corpus-owning manager's coverage passes")

		base := f.gc.listCalls.Load()
		ok, _, err = f.dm.publishCoverageOK(ctx, f.liveSet, f.dm.engine.ResidentDocCount())
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

		_, _, err := f.fdm.publishCoverageOK(ctx, f.liveSet, f.fdm.engine.ResidentDocCount())
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
		ok, _, err := f.fdm.publishCoverageOK(ctx, f.liveSet, f.fdm.engine.ResidentDocCount())
		require.NoError(t, err)
		require.False(t, ok, "the fresh manager is still below the coverage ratio")
		require.Equal(t, int64(1), f.gc.listCalls.Load()-base,
			"an expired entry is not served — the denominator is re-read")
	})
}
