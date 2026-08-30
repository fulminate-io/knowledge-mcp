// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// WHY THE FIXTURE SIZES ARE WHAT THEY ARE, and why they are constants rather than
// literals in the assertions. BucketCountFor (searchengine/bucket.go:65) is the
// smallest power of two at or above ceil(corpusDocs/DefaultMinSegmentDocs), and
// DefaultMinSegmentDocs is 1024. ceil(5000/1024)=5 and ceil(5100/1024)=5, so BOTH
// derive count 8: the partition count is STABLE across the seed, the window seal and
// the delete. That stability is what makes "closed_buckets == 1" a correct
// expectation rather than a lucky one — under a doubling the seed's own segments
// would legitimately span two partitions and the closure would legitimately widen to
// 2. Every number these tests assert is derived in-test from
// searchengine.BucketCountFor(partSeedN+partWindowN), never written as the literal
// 8, because a literal silently decouples the assertion from the fixture the moment
// anyone resizes it.
const (
	partSeedN   = 5000
	partWindowN = 100
)

// partitionFixture builds the shared corpus both tests in this file measure: a seed
// landed through ReplaceBucketFields, then a window of DISJOINT ids landed through
// AddAndMarkDirtyFields and deliberately NOT drained.
//
// THE SEED IS NOT WHAT THESE TESTS MEASURE. ReplaceBucketFields builds one segment
// per partition by construction (replaceBucketGroups -> harvestPartition), so the
// seed already conforms; the un-drained WRITE tail is the subject.
func partitionFixture(
	t *testing.T, mgr *Manager, gt kgtypes.GraphType, name string,
) (seed, window []searchengine.Document) {
	t.Helper()
	ctx := context.Background()
	seed = prefixIDs(vecContentDocsSeed(partSeedN, 0), "part-seed-")
	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, seed))
	window = prefixIDs(vecContentDocsSeed(partWindowN, partSeedN), "part-win-")
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, window))
	return seed, window
}

// TestWriteSealsOneSegmentPerPartition asserts the invariant DIRECTLY, on the
// engine's own span pass: after a write batch seals, no resident segment holds
// members of more than one partition.
//
// THIS ASSERTION IS GLOBAL, AND THAT IS DELIBERATE. diagMaxSpan walks EVERY resident
// segment — the seed's partition segments as well as the window's tails — so `== 1`
// asserts the unqualified whole-engine form, which is NOT the production invariant: a
// segment aligned to an older count legitimately spans one partition per doubling it
// has missed (searchengine/bucket.go:61-64). It is correct HERE only because this
// fixture holds the partition count constant — BucketCountFor(5000) = 8 and
// BucketCountFor(5100) = 8, so the count is 8 at the seed, at the seal and at the
// delete, and no segment can legitimately span two. Keeping the global form is
// deliberate: it is strictly STRONGER than the production invariant, so it can only
// ever false-RED on a fixture drift, never false-green on a real defect. If a later
// change makes the counts diverge, narrow the assertion to the window's recorded
// tails rather than weakening it to a bound.
func TestWriteSealsOneSegmentPerPartition(t *testing.T) {
	const name = "write-seal-partition"

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt := kgtypes.GraphCode

	_, window := partitionFixture(t, mgr, gt, name)

	count := searchengine.BucketCountFor(partSeedN + partWindowN)

	// ANTI-VACUITY: the window's ids must genuinely spread across partitions. A
	// window that landed in a single partition would satisfy the span assertion for
	// free, and the test would report green on a corpus that could not have widened.
	spread := map[int]struct{}{}
	for _, d := range window {
		spread[searchengine.BucketOf(d.ID, count)] = struct{}{}
	}
	require.Greater(t, len(spread), 1,
		"DEGENERATE FIXTURE: the window's %d ids hash into %d partition(s) under count %d; "+
			"a single-partition window cannot demonstrate a per-partition seal",
		len(window), len(spread), count)

	// REUSE, NOT REINVENTION: diagMaxSpan reads the same SegmentSpans pass
	// replaceBucketGroups reads, so it measures the engine the assertions report on.
	span := diagMaxSpan(mgr.bm25ManagerFor(gt, name), count)
	t.Logf("ful1603_prefix_max_span=%d", span)

	require.Equal(t, 1, span,
		"the widest resident segment spans %d of the %d partitions; a write batch must seal "+
			"one segment per partition, or a single delete inside the window closes over every "+
			"partition the batch touched", span, count)
}

// TestWriteSealsPartitionsInADeterministicOrder pins the ordering half of the split,
// which no other test in this file can see: both span assertions are order-blind, so a
// seal loop ranging the bucket map directly would satisfy them on every run while
// appending this batch's segments in a different order each time.
//
// THE ORDER REACHES DURABLE STATE, which is why it is worth a test rather than a
// comment. Export walks the resident entries in append order and emits the blob list in
// that order, and that list is what persistResident makes durable — so a map-order loop
// writes a different L2 blob order for byte-identical input.
//
// TWO ENGINES RATHER THAN TWO READS OF ONE. Reading one engine twice would compare a
// slice to itself and pass whatever the loop did; two managers driven with the same
// documents make the comparison an independent one. Go randomizes map iteration per
// range, and this fixture spreads over eight partitions, so an unsorted loop has 8!
// orderings to disagree across.
func TestWriteSealsPartitionsInADeterministicOrder(t *testing.T) {
	seal := func(name string) []searchengine.SegmentID {
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		gt := kgtypes.GraphCode
		partitionFixture(t, mgr, gt, name)
		return mgr.bm25ManagerFor(gt, name).engine.ResidentSegmentIDs()
	}

	first := seal("deterministic-order-a")
	second := seal("deterministic-order-b")

	// ANTI-VACUITY: equality over two empty or one-element slices is free.
	require.Greater(t, len(first), 1,
		"DEGENERATE FIXTURE: %d resident segment(s); with fewer than two there is no order to pin",
		len(first))
	require.Equal(t, first, second,
		"two engines given the same documents must hold their segments in the same order; "+
			"a differing order means the seal loop ranged the bucket map instead of sorted keys, "+
			"and that order reaches the durable L2 blob list through Export")
}

// TestResidentDeleteDoesNotWidenPastItsPartition asserts the USER-VISIBLE
// consequence, off the production log record rather than off an in-test
// recomputation: a single-node delete landing while a write window is still resident
// must close over ONE partition, not over the whole corpus.
//
// THE DELETE NAMES A SEED ID, NEVER A WINDOW ID, and that choice is load-bearing.
// DeleteFromBuckets purges the write backlog unconditionally and first
// (manager_bucket.go:244), so deleting a window id would empty the very backlog whose
// wide tail this test exists to observe. A seed id leaves the window's tail resident
// when the re-emit runs.
//
// THE FORMAT SELECTOR IS LOAD-BEARING TOO: DeleteFromBuckets drives the HNSW leg then
// the BM25 leg and BOTH emit a group_rebuild_begin, so a reader taking the first
// matching record reads the wrong engine.
func TestResidentDeleteDoesNotWidenPastItsPartition(t *testing.T) {
	ctx := context.Background()
	const name = "resident-delete-partition"

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt := kgtypes.GraphCode

	seed, _ := partitionFixture(t, mgr, gt, name)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	require.NoError(t, mgr.DeleteFromBuckets(ctx, gt, name, []searchengine.ExternalID{seed[0].ID}))
	slog.SetDefault(prev)

	logged := buf.String()
	begin := diagRecord(t, logged, `msg="segmentdist: group_rebuild_begin"`, name, "bm25v2")

	closedBuckets := diagInt(t, begin, "closed_buckets")
	t.Logf("ful1603_prefix_closed_buckets=%d", closedBuckets)

	require.Equal(t, 1, closedBuckets,
		"the delete closed over %d partitions for one id; with every write-path segment holding "+
			"one partition's members the closure has nothing to widen over\nbegin: %s",
		closedBuckets, begin)
}
