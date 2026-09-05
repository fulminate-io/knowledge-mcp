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

// A STRADDLING FIXTURE IS THE POINT OF THIS FILE. Every other partition gate in
// this plan is sized so a partition-count change cannot happen — one is bucket
// aligned at a fixed count, one instructs sizing clear of a straddle, one counts
// segments and a segment count cannot express membership. That is a class blind
// spot rather than three coincidences, so the fixtures here deliberately CROSS a
// power-of-two boundary.
//
// THE SIZES ARE LOAD-BEARING, not arbitrary. BucketCountFor is the smallest power
// of two at or above ceil(corpus/MinSegmentDocs), so a 2000-document corpus
// derives 2 partitions and 2100 derives 4. Seeding 2000 and then writing 100 more
// therefore carries the corpus ACROSS the 2-to-4 boundary, which is the event
// under test.
//
// THE SEED GOES THROUGH ReplaceBucket, NOT THROUGH A DRAIN, and that is deliberate.
// The one-shot path derives its count from documents that are genuinely not yet
// resident, so a 2000-document seed lands aligned to 2 partitions. Seeding through
// a drain instead would derive the count from an inflated corpus and land the seed
// at 4 partitions already — no crossing would remain for the window to cause, and
// the test would pass while proving nothing.
const (
	straddleSeedN   = 2000 // derives 2 partitions
	straddleWindowN = 100  // carries the corpus to 2100, which derives 4
	straddleCorpusN = straddleSeedN + straddleWindowN
)

// straddleFixture seeds a partition-aligned corpus and returns the manager plus
// the graph it was written to. Seed and window ids are kept DISTINCT by prefix:
// colliding ids supersede one another, which removes the duplicate copies this
// file exists to detect and would mask the defect entirely.
func straddleFixture(t *testing.T, ctx context.Context, graphName string) (*Manager, kgtypes.GraphType) {
	t.Helper()

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt := kgtypes.GraphCode

	seed := prefixIDs(hnswVecDocs(straddleSeedN), "straddle-seed-")
	require.NoError(t, mgr.ReplaceBucket(ctx, gt, graphName, nil, seed))
	require.Equal(t, straddleSeedN, mgr.ResidentDocCount(gt, graphName),
		"the seed is resident exactly once per document before the crossing")
	return mgr, gt
}

// TestReEmitAcrossACountChangeKeepsCorpusExact is the MEMBERSHIP leg, and it is
// both the reproduction and the regression.
//
// A drain that carries the corpus across a doubling boundary must leave resident
// membership exactly equal to the corpus. Against the unfixed tree the previously
// partitioned content is duplicated once per extra partition the old segments span,
// so this reports roughly twice the true corpus.
//
// THE EXPECTED COUNT IS A FIXTURE CONSTANT, never read back from the engine.
// ResidentDocCount is the very quantity the defect inflates, so deriving the
// expectation from it would make the assertion an identity that holds against the
// defect as well as the fix.
func TestReEmitAcrossACountChangeKeepsCorpusExact(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	const name = "straddleExact"
	mgr, gt := straddleFixture(t, ctx, name)

	window := prefixIDs(hnswVecDocs(straddleWindowN), "straddle-win-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, window))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	require.Equal(t, straddleCorpusN, mgr.ResidentDocCount(gt, name),
		"a crossing must REPARTITION the corpus, not duplicate it: resident membership equals the corpus exactly")
}

// TestReEmitKeepsPartitionsPureAcrossACountChange is the LAYOUT leg.
//
// After a count change every segment holding members of a REBUILT partition must
// span exactly one partition. This leg does NOT subsume the membership leg and is
// not subsumed by it: at the FIRST crossing the resident segments are individually
// pure while membership is already inflated, because the duplicated copies sit in
// two different segments that each belong to one partition. Both legs are required.
func TestReEmitKeepsPartitionsPureAcrossACountChange(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	const name = "straddlePure"
	mgr, gt := straddleFixture(t, ctx, name)

	window := prefixIDs(hnswVecDocs(straddleWindowN), "straddle-win-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, window))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	count := searchengine.BucketCountFor(straddleCorpusN)
	dm := mgr.managerFor(gt, name)
	for _, blob := range dm.engine.Export() {
		spanned := spannedBuckets(t, dm, blob.ID, count)
		require.LessOrEqualf(t, len(spanned), 1,
			"segment %s spans %d partitions (%v) — a rebuilt partition must leave every segment aligned",
			blob.ID, len(spanned), spanned)
	}
}

// TestSearchAcrossACountChangeReturnsKDistinct is the USER-VISIBLE leg.
//
// THE HARM IS A SHORT RESULT SET, NOT DUPLICATE ROWS, and that decides what this
// may assert. The engine's top-k merge does not deduplicate ids, but the Manager
// fuses per-format rankings through a map keyed by id, so duplicates collapse only
// AFTER they have consumed top-k slots. A criterion asserting "no duplicate ids"
// therefore passes against the defect and is vacuous. The assertion that fires is
// that a k-result search over a corpus larger than k returns exactly k ids.
func TestSearchAcrossACountChangeReturnsKDistinct(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	const name = "straddleSearch"
	const k = 20
	mgr, gt := straddleFixture(t, ctx, name)

	window := prefixIDs(hnswVecDocs(straddleWindowN), "straddle-win-")
	probe := window[0].Vector
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, window))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	hits, err := mgr.Search(ctx, gt, name, "", probe, k)
	require.NoError(t, err)
	require.Len(t, hits, k,
		"a k-result search over a corpus far larger than k must fill every slot — duplicate copies steal slots that collapse at fusion, leaving the caller short")

	seen := make(map[searchengine.ExternalID]bool, len(hits))
	for _, h := range hits {
		require.Falsef(t, seen[h.ID], "id %s returned twice in one result set", h.ID)
		seen[h.ID] = true
	}
}

// TestTickDerivesTheTrueCorpusCount is the DOUBLE-COUNT leg, and it is independent
// of any crossing.
//
// The tick's documents are already resident when it runs — the write path seals
// them before the backlog drains — so deriving the partition count from the
// resident set PLUS the window counts them twice. That is wrong on the very first
// tick of a graph's life: a corpus of exactly one segment's worth derives one
// partition, but the doubled figure derives two.
func TestTickDerivesTheTrueCorpusCount(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	const name = "trueCount"
	const n = searchengine.DefaultMinSegmentDocs // derives exactly ONE partition

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt := kgtypes.GraphCode

	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, prefixIDs(hnswVecDocs(n), "truecount-")))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	dm := mgr.managerFor(gt, name)
	require.Len(t, dm.engine.Export(), searchengine.BucketCountFor(n),
		"the tick must derive its partition count from the TRUE corpus, not from the window counted twice")
	require.Equal(t, n, mgr.ResidentDocCount(gt, name), "and the corpus is unchanged by the tick")
}

// spannedBuckets reports the distinct partitions a resident segment holds members
// of, under the supplied count. It walks membership rather than consulting any
// stored partition, because a segment's partition is derived, never persisted.
func spannedBuckets(t *testing.T, dm *distManager[[]byte, struct{}], id searchengine.SegmentID, count int) []int {
	t.Helper()
	seen := map[int]bool{}
	for b := range count {
		for _, cid := range dm.engine.BucketConstituents(b, count) {
			if cid == id {
				seen[b] = true
			}
		}
	}
	out := make([]int, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	return out
}

// docsInBucket builds n documents whose ids all hash to the given partition under
// the given count, so a window can be aimed at PART of the partition space.
func docsInBucket(t *testing.T, bucket, count, n int, prefix string) []searchengine.Document { //nolint:unparam // bucket is the helper's whole point — which partition to aim at; that every current fixture happens to aim at 0 is a fact about those fixtures, not a dead parameter
	t.Helper()
	out := make([]searchengine.Document, 0, n)
	for i := 0; len(out) < n; i++ {
		if i > n*10000 {
			t.Fatalf("could not find %d ids in bucket %d of %d", n, bucket, count)
		}
		d := hnswVecDocs(1)[0]
		d.ID = fmt.Sprintf("%s%d", prefix, i)
		if searchengine.BucketOf(d.ID, count) == bucket {
			out = append(out, d)
		}
	}
	return out
}

// TestClosureRebuildSetStaysBounded is the COST FENCE.
//
// The constituency closure exists to make the partition predicate safe, but a
// closure that pulled in more than it needs would quietly turn every delta into a
// corpus-wide consolidation. On a STABLE count every resident segment is already
// aligned and spans one partition, so the closure must add NOTHING: a window
// confined to one partition rebuilds exactly that one.
//
// It asserts on replaceBucketGroups' published return rather than on a segment
// count, because that return names precisely the partitions the call rebuilt.
func TestClosureRebuildSetStaysBounded(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	const name = "closureBound"
	const seedN = 5000 // derives 8 partitions; +100 stays at 8, so the count is STABLE

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt := kgtypes.GraphCode

	seed := prefixIDs(hnswVecDocs(seedN), "closure-seed-")
	require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, seed))

	stable := searchengine.BucketCountFor(seedN)
	require.Equal(t, stable, searchengine.BucketCountFor(seedN+100),
		"fixture precondition: the window must NOT move the partition count, or this measures a crossing instead of the delta")

	// A window aimed at ONE partition. On a stable count the closure adds nothing,
	// so exactly one partition is rebuilt however large the corpus is.
	window := docsInBucket(t, 0, stable, 100, "closure-win-")
	dm := mgr.managerFor(gt, name)
	published, _, err := replaceBucketGroups(dm, nil, window, nil, dm.engine.ResidentDocCount()+len(window), nil)
	require.NoError(t, err)
	require.Len(t, published, 1,
		"on a stable count a window confined to one partition must rebuild exactly that partition — the delta stays a delta")
}

// TestDeleteAcrossACountChangeKeepsCorpusExact covers the DOCS-EMPTY pure-delete
// shape across a crossing.
//
// The delete seam reaches the same partition derivation and the same consolidation
// as a drain, so a delete issued while the derived count disagrees with the layout
// amplifies the corpus exactly as a write does. A delete test that only searches a
// fresh engine for the absent id stays green through that, which is why this
// asserts on total membership instead.
func TestDeleteAcrossACountChangeKeepsCorpusExact(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	const name = "straddleDelete"
	const deleteN = 40
	mgr, gt := straddleFixture(t, ctx, name)

	// Grow the resident corpus past the boundary WITHOUT ticking, so the layout is
	// still aligned to the old count when the delete derives the new one.
	window := prefixIDs(hnswVecDocs(straddleWindowN), "straddle-win-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, window))

	dead := make([]searchengine.ExternalID, 0, deleteN)
	for _, d := range window[:deleteN] {
		dead = append(dead, d.ID)
	}
	require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, dead, nil))

	require.Equal(t, straddleCorpusN-deleteN, mgr.ResidentDocCount(gt, name),
		"a pure delete across a crossing must remove exactly the deleted ids — not duplicate the survivors")
}

// TestPartialRebuildSetAcrossACountChangeLosesNothing is the ONLY leg that
// discriminates the constituency closure from its absence, and it is a
// CHARACTERIZATION GUARD rather than a red-first reproduction: it passes against
// the finished implementation and goes red against one carrying the partition
// predicate WITHOUT the closure.
//
// WHY THE FIXTURE MUST BE PARTIAL. A closure is only load-bearing when the set it
// closes over is incomplete. Every other leg here dirties every partition of the
// new count, where the rebuilt outputs simply union back to the whole corpus and
// the closure is indistinguishable from its own absence. Here the window is
// confined to ONE partition of four, so the old-count segment it consumes also
// holds members of a partition nobody asked to rebuild. Without the closure those
// members are dropped when their segment is removed — duplication turned into
// silent loss, which is strictly worse than the defect being fixed.
//
// The assertion is the fixture's own corpus constant. The exact number of
// documents a broken implementation loses depends on how ids distribute and is not
// worth pinning; that it loses ANY is the whole signal.
func TestPartialRebuildSetAcrossACountChangeLosesNothing(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	const name = "partialSet"
	const (
		seedN   = 2040 // derives 2 partitions
		windowN = 100  // carries the corpus to 2140, which derives 4
		corpusN = seedN + windowN
	)

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt := kgtypes.GraphCode

	seed := prefixIDs(hnswVecDocs(seedN), "partial-seed-")
	require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, seed))
	require.Equal(t, seedN, mgr.ResidentDocCount(gt, name), "the seed is resident exactly once before the crossing")

	// Every window document lands in partition 0 of the NEW count, so the dirty set
	// is {0} while the new count is 4 — a deliberately PARTIAL starting set.
	newCount := searchengine.BucketCountFor(corpusN)
	require.Greater(t, newCount, searchengine.BucketCountFor(seedN),
		"fixture precondition: the window must carry the corpus across a boundary")
	window := docsInBucket(t, 0, newCount, windowN, "partial-win-")

	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, window))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	require.Equal(t, corpusN, mgr.ResidentDocCount(gt, name),
		"rebuilding PART of the new partition space must not drop the members its constituents hold for the partitions it did not rebuild")
}
