// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manager_bucket_delete_retry_test.go is the gate on the delete re-emit's BOUNDED
// L2 WRITE RETRY.
//
// WHAT THE RETRY IS FOR, stated so the tests below are read against the right
// claim. The delete re-emit is entirely local: replaceBucketGroups is an in-memory
// rebuild ending in the engine's own compare-and-swap, and the only step that can
// fail for an operational rather than a programming reason is the L2 disk write.
// A rebuild error is therefore a code bug and is returned immediately; a disk write
// error is rare and transient and gets a small bounded number of further attempts.
//
// THE THREE PROPERTIES ARE NOT INTERCHANGEABLE, which is why they are three tests:
// a retry that never fires, a retry that fires and then hides the final failure,
// and a retry that also wraps the rebuild would each pass one of them and fail
// another.

// TestDeleteAbsorbsATransientL2WriteFailure is property (a): a write that fails and
// then succeeds within the bound is INVISIBLE to the caller.
//
// WHY "INVISIBLE" IS THE WHOLE POINT. DeleteFromBuckets' error is the only channel
// the delete's shipped-corpus verdict travels on: the tools layer appends its
// not-durable qualifier to mutate(delete) and manage(prune) exactly when this
// returns non-nil (segmentReEmitFailureNotice, intercept_mutate_delete.go). So a
// nil return here IS "no qualifier" at the caller, and a retry that succeeded on a
// later attempt must be indistinguishable from one that succeeded first time.
//
// IT TAKES A TWO-PARTITION CORPUS, AND THE SIZE IS THE PROPERTY RATHER THAN A
// PREFERENCE. The delete re-emit writes L2 through two producers: the group swap's
// merge hook Puts the consolidated blob of the FIRST published partition, and
// persistResident writes whatever the resident export still owes the cache. On a
// SINGLE-partition delete the hook has already persisted the one blob there is, so
// persistResident's diff is empty and its retry has nothing to attempt — a fixture
// built there can only exercise this retry by failing the hook's Put instead, which
// aborts the reclaim and is a different condition entirely
// (TestDeleteSurfacesAnAbortedMergeReclaim). Two partitions give the hook one blob
// and persistResident the other, which is what this retry is for.
func TestDeleteAbsorbsATransientL2WriteFailure(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	mgr, gt, name, fdm, ic, docs := deleteFieldRetryFixture(t, "transient", twoPartitionFixtureN)

	// count-provenance: corpus-derived — docs is the whole seeded corpus this fixture
	// built, and the count is asserted as a fixture precondition below.
	bucketCount := searchengine.BucketCountFor(len(docs))
	require.Equal(t, 2, bucketCount,
		"FIXTURE PRECONDITION: the corpus must derive exactly two partitions, or the delete below "+
			"publishes one blob and persistResident has nothing to retry")
	victims := victimsInDistinctPartitions(t, docs, bucketCount)

	// Fail every attempt but the last one the bound allows — OF persistResident's
	// write, which is the write this retry is. The delete's FIRST Put through this
	// cache belongs to the other producer, the group swap's merge hook, and is
	// exempted for the reason stated in the doc above.
	//
	// THE INJECTION IS ON THE FIELD POOL because that is the leg a delete still writes:
	// the vector leg is now a live-bit kill with no L2 write of its own, so an injection
	// there would never fire on this path.
	ic.failPutSkipFirst = 1
	ic.failPutUntil = l2WriteAttemptsOnDelete - 1
	require.Positive(t, ic.failPutUntil,
		"FIXTURE PRECONDITION: the bound must allow more than one attempt, or this test injects nothing")

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, victims),
		"a disk write that succeeded within the bound must report CLEAN SUCCESS — a returned error here "+
			"is what puts the not-durable qualifier in front of a caller whose delete did land durably")

	// THE SKIP LANDED WHERE IT WAS MEANT TO. A completed reclaim removes the
	// constituents it superseded, so a non-empty removal set is the positive evidence
	// that the exempted Put was the merge hook's and that the failures below were
	// therefore persistResident's alone.
	require.NotEmpty(t, ic.removedSet(),
		"FIXTURE PRECONDITION: the exempted first Put must have been the merge hook's, leaving the "+
			"reclaim to COMPLETE — an empty removal set means the injection aborted the reclaim and "+
			"this test is measuring the wrong producer's write")

	// THE RETRY REALLY RAN. Without this the assertion above is equally satisfied by
	// an injection that never fired, and a delete whose write happened to succeed
	// first time would read identically.
	require.Greater(t, ic.maxPutsPerID(), 1,
		"some blob must have been offered more than once — one Put per id means every write landed "+
			"on its first attempt and no retry was exercised")

	// AND THE WRITE ACTUALLY LANDED. A retry that returns nil without persisting
	// would satisfy both assertions above and be strictly worse than the failure it
	// replaced, because the caller is then told a non-durable delete was durable.
	requireResidentSetBackedByL2(t, fdm, ic)

	// AND WHAT LANDED IS THE POST-DELETE IMAGE. The assertion above ties the pool's
	// CURRENT resident set to disk; these two say that current set is the one the
	// delete produced, so together they are "the rebuilt partitions, without the
	// victims, are durable" rather than "a write happened".
	require.Equal(t, len(docs)-len(victims), fdm.engine.ResidentDocCount(),
		"the resident set backed by L2 above must be the POST-delete one")
	require.Len(t, fdm.engine.UncoveredFrom(victims), len(victims),
		"and the engine's own residency predicate must agree the victims are no longer held")
}

// TestDeleteSurfacesAnExhaustedL2WriteRetry is property (b): when every attempt
// fails, the UNDERLYING error still reaches the caller, at the same place and with
// the same identity it had before the retry existed.
//
// IT IS A PIN, NOT A CHANGE, and saying so is the honest description: this leg
// passes against the pre-retry tree too, because surfacing the disk's error is
// exactly what that tree did. What it discriminates is the retry going WRONG —
// swallowing the failure once it has given up, or substituting an error of its own —
// which is the specific way a retry turns a reported non-durable delete into an
// unreported one.
//
// THE QUALIFIER TESTS ARE NOT WEAKENED BY THIS ONE, they are fed by it. The tools
// layer's two gates (segment_reemit_report_test.go, intercept_manage_prune_test.go)
// pin that a non-nil verdict from this seam is named in the result text and that a
// nil one is not; what they cannot see is whether the error that arrives is still
// the disk's own. That is asserted here by identity rather than by message.
//
// THE BOUND IS NOT ASSERTED HERE. A delete's Put calls come from TWO producers — the
// group rebuild's merge hook reclaims through this same cache before persistResident
// runs — so a per-id call count taken at this level cannot attribute a repeat to the
// retry. TestL2WriteRetryIsBounded asserts the arithmetic where there is one producer.
func TestDeleteSurfacesAnExhaustedL2WriteRetry(t *testing.T) {
	t.Parallel()

	t.Run("every attempt fails", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		mgr, gt, name, _, ic, docs := deleteFieldRetryFixture(t, "exhausted", deleteFixtureN)
		victim := docs[0]
		beforeBlobs := l2BM25IDs(mgr.cacheDir, name)
		require.NotEmpty(t, beforeBlobs, "CONTROL: the field corpus must have reached L2, or the equality below is vacuous")
		ic.failPut = true

		err := mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID})
		require.ErrorIs(t, err, errInjectedPutFailure,
			"an exhausted retry must return the DISK's error, not one the retry invented — the tools "+
				"qualifier renders this value verbatim, so a substituted error names the wrong cause")

		// AND THE REBUILT PARTITION IS NOT ON DISK, so the caller's "not durable"
		// report is true rather than merely pessimistic. The instrument is the
		// PHYSICAL resident count of a fresh engine: every write failed, so the blob
		// on disk is still the pre-delete one and a fresh load must find the whole
		// corpus in it, victim included.
		//
		// PHYSICAL AND LIVE ARE MEASURED SEPARATELY HERE BECAUSE THEY GENUINELY
		// DISAGREE, and only the physical one speaks to this property. The delete
		// writes TWO durable things — the rebuilt partition and the tombstone record —
		// and the injection fails only the first. So the tombstone DOES survive, a
		// fresh engine re-reads it and seeds the victim dead at import, and the victim
		// is masked out of the live corpus on reload even though the partition rewrite
		// never landed. Asserting through a liveness-aware probe would therefore read
		// this state as "the delete was durable" and hide the failed write completely.
		require.Equal(t, beforeBlobs, l2BM25IDs(mgr.cacheDir, name),
			"every write failed, so the rebuilt FIELD partition never reached disk and the pre-delete "+
				"blobs are still exactly what L2 holds — a delete reported non-durable that was in fact "+
				"durable would make the qualifier a false statement in the other direction")

		// THE PHYSICAL AND LIVE COUNTS OF THE VECTOR POOL, which the injection does not
		// touch at all, are the known-positive that something WAS deleted: the tombstone
		// record is durable on its own, so a fresh engine seeds the victim dead even
		// though its blob still carries it. Equal physical and live counts here would
		// mean nothing was deleted at all.
		physical, live := freshEngineCounts(t, ctx, mgr.cacheDir, gt, name)
		require.Equal(t, deleteFixtureN, physical,
			"the vector blob still carries the document, because its partition is deferred")
		require.Equal(t, deleteFixtureN-1, live,
			"and the tombstone record seeds it dead on reload")
	})

	t.Run("known-negative: a clean write is attempted once per blob", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		mgr, gt, name, fdm, ic, docs := deleteFieldRetryFixture(t, "clean", deleteFixtureN)
		victim := docs[0]

		require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{victim.ID}))

		// THIS LEG IS THE BASELINE, AND ITS LIMIT IS WORTH STATING. It shows the
		// ordinary delete writes each id once and stays durable, which is what makes
		// the failure legs above deviations from a working path rather than the only
		// behaviour observed. It is NOT the catcher for a retry loop that runs
		// unconditionally: on a clean delete the merge hook has already persisted the
		// consolidated blob, so persistResident's diff is EMPTY and an unconditional
		// loop would have nothing to write more than once. That catcher is
		// TestL2WriteRetryIsBounded's known-negative, which offers a non-empty slice.
		require.Positive(t, ic.putCallCount(),
			"CONTROL: the delete must reach the cache at all, or the equality below is vacuous")
		require.Equal(t, 1, ic.maxPutsPerID(),
			"no id is written twice on a delete whose writes all succeed")

		requireResidentSetBackedByL2(t, fdm, ic)
		require.False(t, residentInFreshEngine(t, ctx, mgr.cacheDir, gt, name, victim),
			"CONTROL: a delete whose writes all succeed IS absent from a fresh engine — so the "+
				"still-present assertion in the exhausted leg is measuring the failure, not the fixture")
	})
}

// TestL2WriteRetryIsBounded pins the retry's ARITHMETIC at the retry itself, where
// the Put calls have exactly one producer and the count is therefore attributable.
//
// IT IS THE OTHER HALF OF THE EXHAUSTION PROPERTY. The delete-level test above shows
// the disk's error reaching the caller; what it cannot show is that the loop STOPS.
// An unbounded retry would satisfy every assertion there and never return at all.
func TestL2WriteRetryIsBounded(t *testing.T) {
	t.Parallel()

	_, merged := realMergeBlobs(t)
	blobs := []searchengine.SegmentBlob{merged}

	t.Run("a permanently failing write stops at the bound", func(t *testing.T) {
		t.Parallel()

		ic := newInstrumentedCache(newDiskSegmentCache(t.TempDir(), 0, adviceRandom))
		ic.failPut = true

		err := newReclaimDMOverCache(t, ic).writeNewBlobsToL2WithAttempts(blobs, l2WriteAttemptsOnDelete)
		require.ErrorIs(t, err, errInjectedPutFailure,
			"the disk's own error is what the exhausted bound returns")
		require.Equal(t, l2WriteAttemptsOnDelete, ic.putCallCount(),
			"exactly the bounded number of attempts — fewer means the retry did not run, more means "+
				"the loop is not bounded by the constant that documents it")
	})

	t.Run("known-negative: a clean write is attempted once", func(t *testing.T) {
		t.Parallel()

		// WITHOUT THIS LEG the equality above is satisfied by a loop that runs the
		// full bound unconditionally and returns the last result, which would write
		// every blob l2WriteAttemptsOnDelete times on the ordinary path.
		ic := newInstrumentedCache(newDiskSegmentCache(t.TempDir(), 0, adviceRandom))

		require.NoError(t, newReclaimDMOverCache(t, ic).writeNewBlobsToL2WithAttempts(blobs, l2WriteAttemptsOnDelete))
		require.Equal(t, len(blobs), ic.putCallCount(),
			"a write that succeeds must be attempted exactly once per blob, however large the bound is")
	})

	t.Run("a transient failure recovers within the bound", func(t *testing.T) {
		t.Parallel()

		ic := newInstrumentedCache(newDiskSegmentCache(t.TempDir(), 0, adviceRandom))
		ic.failPutUntil = l2WriteAttemptsOnDelete - 1

		require.NoError(t, newReclaimDMOverCache(t, ic).writeNewBlobsToL2WithAttempts(blobs, l2WriteAttemptsOnDelete),
			"a write that succeeded on the last attempt the bound allows must report success")
		require.Equal(t, l2WriteAttemptsOnDelete, ic.putCallCount())
		require.Equal(t, l2WriteAttemptsOnDelete, ic.maxPutsPerID(),
			"and all of those attempts must be the SAME blob — the retry re-offers the write, it does "+
				"not move on to a different one")
	})
}

// errInjectedRebuildFailure is the engine-side failure flakyRebuildFormat injects.
// It stands for the BUG CLASS: a rebuild error means the partition machinery or the
// format is wrong, and repeating it produces the same wrong answer.
var errInjectedRebuildFailure = errors.New("injected rebuild failure")

// flakyRebuildFormat is the mock format with a switch on its two CONSTRUCTION
// entry points. It starts working so a fixture can seal a corpus through it, and is
// flipped afterwards so the rebuild that follows fails the way a format bug would.
//
// BOTH Build AND Merge ARE COVERED because which one a partition rebuild reaches
// depends on whether that partition has resident constituents, and a test that
// pinned only one would go quietly vacuous if the fixture's shape changed.
// failed COUNTS THE FAILING INVOCATIONS, and it is the only observable that can see
// a retry wrapped around the rebuild. A retry that re-runs replaceBucketGroups never
// reaches the write, so a write-call count reads zero either way and cannot tell an
// immediate return from three failed rebuilds.
type flakyRebuildFormat struct {
	mockFormat
	fail   *atomic.Bool
	failed *atomic.Int64
}

func (f flakyRebuildFormat) Build(docs []searchengine.Document) (searchengine.Segment[mockQuery, mockStats], searchengine.BuildReport, error) {
	if f.fail.Load() {
		f.failed.Add(1)
		return nil, searchengine.BuildReport{}, errInjectedRebuildFailure
	}
	return f.mockFormat.Build(docs)
}

// MergeTo MUST BE DECLARED HERE. flakyRebuildFormat embeds mockFormat, so
// without this declaration it would silently PROMOTE mockFormat.MergeTo — whose
// receiver is the embedded value — and the injected rebuild failure would never
// fire on the merge entry point. The `failed` counter would stay at zero, which
// is precisely the observable TestDeleteRebuildErrorIsNotRetried asserts on, so
// the absorption would show up as that test failing rather than as a compile
// error.
func (f flakyRebuildFormat) MergeTo(
	dst searchengine.MergeSink, segs []searchengine.Segment[mockQuery, mockStats],
	accept []func(searchengine.ExternalID) bool,
) (int64, error) {
	if f.fail.Load() {
		f.failed.Add(1)
		return 0, errInjectedRebuildFailure
	}
	return f.mockFormat.MergeTo(dst, segs, accept)
}

// rebuildFailureDM builds a pool over the flaky format with an instrumented cache,
// seals a small corpus through it and makes that corpus durable — so the ONLY write
// a later delete-shaped re-emit can perform is the rebuilt partition's own.
func rebuildFailureDM(t *testing.T, name string) (
	*distManager[mockQuery, mockStats], *instrumentedCache,
	*atomic.Bool, *atomic.Int64, []searchengine.Document,
) {
	t.Helper()

	// THE AUTOMATIC MERGE TRIGGER IS DISARMED, matching what managerFor builds for
	// every production pool — and here it is also what makes the failure counter
	// ATTRIBUTABLE. The engine's background merger calls the format's Merge on its own
	// ticker, so a fixture that left it armed counts merges this re-emit never asked
	// for; the count then depends on machine load, and it did: this read 1 in
	// isolation and 2 under the full package run before the trigger was turned off.
	fail, failed := &atomic.Bool{}, &atomic.Int64{}
	engine := closeOnCleanup(t, searchengine.New[mockQuery, mockStats](
		flakyRebuildFormat{fail: fail, failed: failed}, searchengine.Options{
			MinSegmentDocs:     1,
			SegmentCountTarget: searchengine.MergeDisabledCountTarget,
			DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		}))
	ic := newInstrumentedCache(newDiskSegmentCache(t.TempDir(), 0, adviceRandom))
	dm := newDistManager(engine, ic, graphSelector(kgtypes.GraphCode, name), "")

	docs := []searchengine.Document{
		doc("rb-1", "alpha"), doc("rb-2", "alpha"), doc("rb-3", "alpha"), doc("rb-4", "alpha"),
	}
	for _, d := range docs {
		require.NoError(t, engine.Add([]searchengine.Document{d}))
	}
	written, err := dm.persistResident()
	require.NoError(t, err)
	require.Positive(t, written,
		"FIXTURE PRECONDITION: the seed corpus must be on disk, or the delete's diff below would "+
			"include it and the write-count deltas would not isolate the rebuilt partition")
	return dm, ic, fail, failed, docs
}

// TestDeleteRebuildErrorIsNotRetried is property (c): a REBUILD failure is returned
// immediately and the write is never reached, so the retry adds zero attempts.
//
// THE RETRY IS ARMED IN BOTH LEGS. Passing l2WriteAttemptsOnDelete is what makes
// this discriminating: an implementation that wrapped the whole re-emit in the retry
// rather than only its write leg would re-run the failing rebuild and still return
// the same error, so the error assertion alone cannot catch it.
//
// AND THE COUNT THAT CATCHES IT IS THE REBUILD'S, NOT THE WRITE'S. That wrapped
// implementation never reaches the write either — a Put-count delta of zero is what
// BOTH readings produce — so counting suppressed writes is exactly the assertion
// that looks discriminating and is not. What separates them is how many times the
// failing rebuild ran.
func TestDeleteRebuildErrorIsNotRetried(t *testing.T) {
	t.Parallel()

	t.Run("a failing rebuild runs once and performs no writes", func(t *testing.T) {
		t.Parallel()

		dm, ic, fail, failed, docs := rebuildFailureDM(t, "rebuildFails")
		before := ic.putCallCount()
		fail.Store(true)

		err := replaceBucketAndPublish(
			dm, []searchengine.ExternalID{docs[0].ID}, nil, len(docs),
			l2WriteAttemptsOnDelete, surfaceAbortedReclaim)
		require.ErrorIs(t, err, errInjectedRebuildFailure,
			"a rebuild error is a code bug and must reach the caller unchanged")
		require.Equal(t, int64(1), failed.Load(),
			"the failing rebuild must have run EXACTLY ONCE — a higher count is the retry wrapping "+
				"the rebuild rather than the write, which re-runs a deterministic bug for no gain")
		require.Equal(t, before, ic.putCallCount(),
			"and a rebuild that failed must reach no write at all")
	})

	t.Run("known-positive: the same shape does write when the rebuild succeeds", func(t *testing.T) {
		t.Parallel()

		dm, ic, _, failed, docs := rebuildFailureDM(t, "rebuildWorks")
		before := ic.putCallCount()

		require.NoError(t, replaceBucketAndPublish(
			dm, []searchengine.ExternalID{docs[0].ID}, nil, len(docs),
			l2WriteAttemptsOnDelete, surfaceAbortedReclaim))
		require.Zero(t, failed.Load(),
			"CONTROL: nothing was injected here, so the failure counter the leg above reads must be "+
				"capable of reading zero — a counter wired to increment unconditionally would make "+
				"that leg's 'exactly once' meaningless")

		// WITHOUT THIS LEG the zero-delta assertion above is satisfied by a fixture
		// whose delete shape never reaches the write on ANY path — a probe pointed at
		// nothing and a genuinely suppressed write read identically.
		require.Greater(t, ic.putCallCount(), before,
			"CONTROL: this delete shape DOES drive the L2 write when the rebuild succeeds, so the "+
				"zero-write leg above is measuring suppression rather than an inert fixture")
	})
}
