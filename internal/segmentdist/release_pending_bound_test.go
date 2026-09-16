// SPDX-License-Identifier: Apache-2.0

// release_pending_bound_test.go covers ONE property of mapping republication that is
// about neither producer in particular: how many failures a release pass may leave
// waiting for a consumer touch.
//
// IT IS ITS OWN FILE BECAUSE THE BOUND IS ITS OWN CONCERN. The seal path's arms live in
// release_resident_test.go and the reset path's in rebuild_layer_release_test.go; the
// pending set is what they SHARE, and every row here is about its size rather than
// about the release that filled it.
//
// WHAT THE CAP PROTECTS, stated once for every row below: drainRemapPending walks the
// pending set after EVERY consumer search, spending an os.Stat and an mmap per id. So
// the cap is a bound on per-SEARCH work. It is NOT a bound on the pass, and the
// difference is the defect these rows were written against — a loop that stopped at the
// cap released nothing at all for as long as the set stayed full, observed live as
// `heap_backed=3401 released=0 deferred=3401`.

package segmentdist

import (
	"bytes"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// overCapSegments is the size every row in this file builds its pool at: the cap plus
// a remainder, which is the ONLY size that can tell a bounded enqueue from an
// unbounded one. It is a constant beside the fixture rather than a parameter because
// every case shares it, and a parameter every caller passes the same value to is a
// promise of variation the tests do not keep.
const overCapSegments = releasePendingCap + 16

// sealedPoolOfN is sealedPoolFixture at scale: overCapSegments segments sealed by this
// engine, each its own document, so a release pass over them is a pass over a resident
// SET rather than over one segment.
func sealedPoolOfN(t *testing.T) (*distManager[mockQuery, mockStats], *instrumentedCache) {
	t.Helper()
	n := overCapSegments
	ic := newInstrumentedCache(newDiskSegmentCache(t.TempDir(), 0, adviceRandom))
	// MERGE DISABLED, and it is the fixture's whole point: the background merger
	// would consolidate N tiny sealed segments into ONE mapping-backed output, so a
	// pass over a resident SET would silently become a pass over nothing. That is
	// what a first draft of this test measured.
	engine := searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
	})
	t.Cleanup(engine.Close)
	dm := newDistManager[mockQuery, mockStats](
		engine, ic, graphSelector(kgtypes.GraphCode, "release-scale"), "")
	for i := range n {
		id := "doc-" + strconv.Itoa(i)
		require.NoError(t, dm.engine.Add([]searchengine.Document{doc(id, "alpha")}))
	}
	require.NoError(t, dm.engine.Flush())
	require.Len(t, dm.engine.HeapBackedResidentIDs(), n,
		"fixture control: every sealed segment must be heap-backed before the release")
	return dm, ic
}

// TestAFailedReleasePassAnnouncesOnceAndBoundsThePendingSet is the row for the two
// things that scale with the RESIDENT SET rather than with merges: the log and the
// per-search drain.
//
// Both reachable causes of a failed release — an mmap that is structurally
// unavailable, and an L2 cache whose cap evicted the blobs this drain just wrote —
// fail EVERY id in the pass, so the per-segment announcement the merge path uses
// would be thousands of lines that say one thing, and an unbounded pending set
// would spend an os.Stat and an mmap per id on every consumer search for the next
// remapMaxAttempts rounds, re-armed by the next drain tick.
//
// THE HISTOGRAM READS THE WHOLE HEAP-BACKED SET, NOT THE CAP, and that expectation
// changed deliberately. This row used to assert cause_map_failed=releasePendingCap,
// which encoded a loop that ABORTED at the cap: once the pending set was full the pass
// stopped on its first id and released nothing, observed live as `heap_backed=3401
// released=0 deferred=3401`. The cap bounds ENQUEUES now, so every id is attempted and
// every failure is counted, while the two properties this row exists for — the pending
// set never exceeds the cap, and one drain costs at most the cap in cache reads — are
// asserted unchanged below.
func TestAFailedReleasePassAnnouncesOnceAndBoundsThePendingSet(t *testing.T) {
	const segments = overCapSegments
	dm, ic := sealedPoolOfN(t)

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ic.mu.Lock()
	ic.failMapping = true
	ic.mu.Unlock()

	_, err := dm.persistResident()
	require.NoError(t, err, "a mapping failure must not fail the durability path")

	warns := strings.Count(logBuf.String(), "level=WARN")
	require.Equal(t, 1, warns,
		"a pass that failed %d segments must announce ONCE, not once per segment", segments)
	require.Contains(t, logBuf.String(), "cause_map_failed="+strconv.Itoa(segments),
		"the one line must carry the cause histogram over every id the pass attempted, "+
			"or it replaces detail with silence")
	require.Contains(t, logBuf.String(), "deferred="+strconv.Itoa(segments-releasePendingCap),
		"and it must say how many of those failures it declined to enqueue under the cap")

	require.LessOrEqual(t, dm.pendingRemapCount(), releasePendingCap,
		"one release pass must not enqueue more than the per-search drain can afford")

	// THE DRAIN'S WORK IS THE PENDING SET'S SIZE, and this is the assertion the cap
	// exists for: a consumer search pays a bounded number of cache reads, whatever
	// the resident set holds.
	before := countAllOps(ic, "getmapped")
	require.NoError(t, dm.drainRemapPending())
	require.LessOrEqual(t, countAllOps(ic, "getmapped")-before, releasePendingCap,
		"one drain must cost at most the cap in cache reads")

	// The deferred remainder is not lost: it stays heap-backed and correct, and the
	// next durability pass is what releases it.
	require.GreaterOrEqual(t, len(dm.engine.HeapBackedResidentIDs()), segments-releasePendingCap,
		"the segments this pass deferred must still be heap-backed and still releasable")
}

// TestASecondAllFailingPassCountsOnlyTheFailuresItLeavesWithNothingOwed pins what the
// release line's `deferred=` key MEANS, on the one input class that can tell the two
// readings apart: a second all-failing pass over a pool whose pending set is already at
// the cap.
//
// THE TWO READINGS. "Failures this pass did not enqueue" counts all 80 — including the
// 64 the drain is already holding a repair for — and prints `failed=80 deferred=80`
// beside `pending=64`, three numbers that cannot all be true of disjoint sets. "Failures
// left with NOTHING OWED" counts 16, and then failed - deferred is exactly what the
// drain holds. The second is what the key says and what the comment beside the log
// composition states.
//
// RED BEFORE THIS ROUND: the pass incremented deferred for every failure it did not
// newly enqueue, so this fixture printed deferred=80.
func TestASecondAllFailingPassCountsOnlyTheFailuresItLeavesWithNothingOwed(t *testing.T) {
	const segments = overCapSegments
	dm, ic := sealedPoolOfN(t)

	ic.mu.Lock()
	ic.failMapping = true
	ic.mu.Unlock()

	// PASS ONE fills the pending set to its cap through the ordinary failure arm.
	_, err := dm.persistResident()
	require.NoError(t, err)
	require.Equal(t, releasePendingCap, dm.pendingRemapCount(),
		"control: the pending set must be AT the cap, or the second pass has no already-pending ids to miscount")

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// PASS TWO fails every id again, and enqueues none of them: the set is full.
	_, err = dm.persistResident()
	require.NoError(t, err)

	require.Contains(t, logBuf.String(), "failed="+strconv.Itoa(segments),
		"control: every id was attempted and every one failed, which is what distinguishes this line "+
			"from the pre-fix latch's `released=0 failed=0 deferred=N`")
	require.Contains(t, logBuf.String(), "deferred="+strconv.Itoa(segments-releasePendingCap),
		"deferred counts the failures left with NOTHING OWED — the %d already pending are owed a repair "+
			"on the next consumer touch, and the WARN reports them as pending", releasePendingCap)
	require.Contains(t, logBuf.String(), "pending="+strconv.Itoa(releasePendingCap),
		"and failed - deferred must be exactly what the drain is holding")
	require.Equal(t, releasePendingCap, dm.pendingRemapCount(),
		"a second pass at the cap enqueues nothing new")
}

// TestAReleaseBatchThatFailsToDecodeBoundsThePendingSet is the OTHER failure arm's
// bound. A mapping failure is caught before the engine is asked to swap anything; a
// mapping that arrives cleanly and is REFUSED AT DECODE reaches the batch, comes back
// as RemapFailed, and is enqueued from a different site.
//
// BOTH SITES OWE THE SAME BOUND, because both write the set drainRemapPending walks
// after every consumer search. This arm was unbounded while the pass aborted at the cap
// — it could never see more than a capped number of ids — and bounding the enqueues
// rather than the pass is exactly what makes a whole resident set reachable here.
func TestAReleaseBatchThatFailsToDecodeBoundsThePendingSet(t *testing.T) {
	const segments = overCapSegments
	dm, ic := sealedPoolOfN(t)

	ic.mu.Lock()
	ic.corruptMapping = true
	ic.mu.Unlock()

	_, err := dm.persistResident()
	require.NoError(t, err, "an undecodable stored copy must not fail the durability path")

	require.Len(t, dm.engine.HeapBackedResidentIDs(), segments,
		"control: every payload stays heap-backed and CORRECT when its replacement will not decode")
	require.LessOrEqual(t, dm.pendingRemapCount(), releasePendingCap,
		"a batch whose every blob failed to decode must not enqueue more than the per-search drain can afford")
	require.Equal(t, []searchengine.ExternalID{"doc-0"}, searchHits(dm.engine, "alpha")[:1],
		"and the corpus must still answer from its correct heap payloads")
}

// TestAReleasePassAtThePendingCapStillReleasesEverythingReleasable is requirement 3's
// row. The enqueue budget bounds how many FAILURES a pass may add to the pending set;
// it must never bound the pass itself, because an id whose mapping succeeds enqueues
// nothing and costs the pending set nothing.
//
// RED ON THE BASE: the loop opens `if enqueueBudget <= 0 { break }`, so once the
// pending set is at the cap the next pass exits on its first id having released
// nothing at all — observed live as `heap_backed=3401 released=0 deferred=3401`.
func TestAReleasePassAtThePendingCapStillReleasesEverythingReleasable(t *testing.T) {
	const segments = overCapSegments
	dm, ic := sealedPoolOfN(t)

	// FILL THE PENDING SET TO ITS CAP, through the ordinary failure arm rather than by
	// writing the map directly: a pass whose every mapping fails enqueues up to the cap
	// and leaves every segment heap-backed.
	ic.mu.Lock()
	ic.failMapping = true
	ic.mu.Unlock()
	_, err := dm.persistResident()
	require.NoError(t, err, "a mapping failure must not fail the durability path")
	require.Equal(t, releasePendingCap, dm.pendingRemapCount(),
		"control: the pending set must be AT the cap, or this row is not testing the cap at all")
	require.Len(t, dm.engine.HeapBackedResidentIDs(), segments,
		"control: every segment is still heap-backed and still releasable")

	log := captureLogs(t)
	ic.mu.Lock()
	ic.failMapping = false
	ic.mu.Unlock()

	_, err = dm.persistResident()
	require.NoError(t, err)

	require.Empty(t, dm.engine.HeapBackedResidentIDs(),
		"a pass that starts with %d remaps pending must still release every releasable segment", releasePendingCap)
	require.Contains(t, log.String(), "released="+strconv.Itoa(segments),
		"and it must report them released, not deferred")
	require.Zero(t, dm.pendingRemapCount(),
		"every id that was pending has now been republished, so nothing is owed")
}

// TestARebuildWhoseReleasesAllFailStaysCorrectAndBoundsThePendingSet is the reset
// path's error arm, and it carries two properties the release seam owes at scale.
//
// FIRST, A FAILED RELEASE MUST NOT FAIL THE REBUILD. The partitions are durable and
// correct; only where their bytes live was not improved, and a rebuild verdict must
// never turn on a memory property.
//
// SECOND, THE PENDING SET STAYS UNDER THE CAP. What the cap protects is per-SEARCH
// work: drainRemapPending walks the pending set after every consumer search, spending a
// stat and an mmap per id. A layer of many partitions failing structurally — an mmap
// that is unavailable, an L2 cache whose cap evicted what this build just wrote — fails
// EVERY partition in one pass, so an unbounded record would convert one rebuild into
// thousands of syscalls per search.
//
// HONEST RED-FIRST LABEL: the release does not run at all on the base, so this row's
// red there is the absence of the whole mechanism rather than a different value. Its
// discriminating power is the mutation named on it — remove the enqueue bound in
// recordUnreleased and the pending assertion below goes red at 80 against a cap of 64.
func TestARebuildWhoseReleasesAllFailStaysCorrectAndBoundsThePendingSet(t *testing.T) {
	dm, ic := mergeDisabledMockPool(t, "layer-release-failure")

	const partitions = releasePendingCap + 16
	work := resetWorkForDocs(bm25ReleaseCorpus(512), partitions)
	require.Len(t, work, partitions,
		"fixture control: every partition must carry documents, or fewer than the cap would fail")

	ic.mu.Lock()
	ic.failMapping = true
	ic.mu.Unlock()

	log := captureLogs(t)
	_, swapped, err := finalizeResetLayer(t.Context(), kgtypes.GraphCode, "layer-release-failure", dm, work)
	require.NoError(t, err, "a mapping failure must not fail the rebuild: the bytes were written")
	require.True(t, swapped, "and the layer must still be published")

	require.Len(t, dm.engine.HeapBackedResidentIDs(), partitions,
		"every partition stays heap-backed and CORRECT when its release fails")
	require.NotEmpty(t, dm.engine.Search(mockQuery{term: "alpha"}, 10),
		"and the published layer must still answer")

	require.LessOrEqual(t, dm.pendingRemapCount(), releasePendingCap,
		"one rebuild must not enqueue more repairs than the per-search drain can afford")
	require.Contains(t, log.String(), "deferred="+strconv.Itoa(partitions-releasePendingCap),
		"and the remainder must be reported as deferred rather than silently dropped")

	// CONVERGENCE: the enqueued repairs are made on the next consumer touch, and the
	// deferred remainder is released by the next durability pass.
	ic.mu.Lock()
	ic.failMapping = false
	ic.mu.Unlock()
	require.NoError(t, dm.drainRemapPending())
	require.Zero(t, dm.pendingRemapCount(), "the next consumer touch must drain what was enqueued")
	require.Len(t, dm.engine.HeapBackedResidentIDs(), partitions-releasePendingCap,
		"the drain repairs exactly what was enqueued; the rest waits for the next durability pass")

	_, err = dm.persistResident()
	require.NoError(t, err)
	require.Empty(t, dm.engine.HeapBackedResidentIDs(),
		"and that pass releases the deferred remainder")
}
