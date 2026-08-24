// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// manager_bucket_defect_test.go holds the Manager-level catchers for the two faults
// the partitioned re-emit shipped with: constituent pollution, which destroys the
// partition, and content-hash aliasing, which destroys the corpus.

// spanningSegments reports, for each resident segment, how many distinct partitions
// it holds members of. A correctly partitioned corpus has every segment at one.
//
// It is derived from the same lookup the tick uses to pick constituents, so it sees
// exactly what the rebuild would see.
func spanningSegments[Q, S any](dm *distManager[Q, S], bucketCount int) map[searchengine.SegmentID]int {
	spans := make(map[searchengine.SegmentID]int)
	for b := range bucketCount {
		for _, id := range dm.engine.BucketConstituents(b, bucketCount) {
			spans[id]++
		}
	}
	return spans
}

// TestReEmitKeepsPartitionsPure is the CONSTITUENT-POLLUTION catcher, and it must
// run the tick's real concurrent fan-out.
//
// The lookup that nominates constituents is membership-based and ignores liveness,
// so a freshly sealed drain segment — whose documents are hash-spread across nearly
// every partition — is nominated as a constituent of nearly every partition. The
// consolidation has no partition predicate, so each rebuild that accepts it folds
// its ENTIRE membership in.
//
// Both legs are needed and the fan-out is part of the gate. Run serially the
// partition still collapses, but total membership stays equal to the corpus, so leg
// 2 alone would pass against the defect. Run concurrently — which is what the tick
// does — later partitions read pre-swap snapshots and copy the same members into
// several outputs at once, which is what leg 2 catches.
func TestReEmitKeepsPartitionsPure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// FIXTURE CONSTANTS. Leg 2's expectation is stated here and never read back from
	// the engine: ResidentDocCount sums per-segment counts and is AMPLIFIED by this
	// very defect, so comparing the engine against itself is an identity that holds
	// however badly the partition has been corrupted.
	//
	// 8 partitions is the width both legs need: more than one so leg 1's span check
	// can fail at all, and enough concurrent fan-out that later partitions read
	// pre-swap snapshots — which CPU-bounded fan-out already saturates at 8.
	const (
		corpus  = 6144 // derives 8 partitions, clear of a doubling boundary
		buckets = 8
		drain   = 100 // EmbedBatchSizeOrDefault
	)
	require.Equal(t, buckets, searchengine.BucketCountFor(corpus), "layout count")

	_, gc := newSegmentHarness(t)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	gt, name := kgtypes.GraphCode, "purity"

	docs := bucketFixtureDocs(t, corpus, buckets)
	require.NoError(t, mgr.ReplaceBucket(ctx, gt, name, nil, docs))
	dm := mgr.managerFor(gt, name)

	require.Len(t, residentIDs(dm), buckets, "the seed lands one segment per partition")
	require.Equal(t, corpus, dm.engine.ResidentDocCount(), "the seed is intact before the drain")

	// Re-write documents the corpus already holds, so the corpus size is unchanged
	// by the drain and leg 2's expectation stays exactly the fixture constant.
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs[:drain]))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	// LEG 1 — the robust discriminator: no segment may hold members of two
	// partitions. This fires whether the tick ran serially or concurrently.
	var impure []searchengine.SegmentID
	for id, span := range spanningSegments(dm, buckets) {
		if span > 1 {
			impure = append(impure, id)
		}
	}
	require.Empty(t, impure,
		"%d resident segments span more than one partition — a drain segment was folded in as a constituent, "+
			"and because the consolidation has no partition predicate its whole membership landed in the partition",
		len(impure))

	// LEG 2 — the concurrency-specific duplication. Duplicate membership is
	// user-visible, not merely untidy: a search that merges per-segment hit lists
	// does not deduplicate ids.
	require.Equal(t, corpus, dm.engine.ResidentDocCount(),
		"total membership must equal the fixture's corpus size — anything higher is the same document "+
			"copied into several partitions by concurrent rebuilds reading pre-swap snapshots")
}

// TestReEmitPreservesAliasedCorpus is the MANAGER-LEVEL total-loss leg, and it is
// the only gate that fails if the tick collects the published ids and then ignores
// them. Both engine-level catchers perform the subtraction themselves, so they pass
// against a Manager that never does it.
//
// The shape is the one a fresh graph produces naturally: the drain seals every
// document into one segment, the tick rebuilds the single partition from those same
// documents, and the rebuild therefore encodes to the same bytes and carries the
// same id. Retiring the drain segment by id then removes the rebuild.
func TestReEmitPreservesAliasedCorpus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const corpus = 30 // FIXTURE CONSTANT — one partition's worth, so the rebuild aliases

	_, gc := newSegmentHarness(t)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	gt, name := kgtypes.GraphCode, "alias"

	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, vecContentDocs(corpus)))
	dm := mgr.managerFor(gt, name)
	require.Equal(t, corpus, dm.engine.ResidentDocCount(), "the drain sealed the whole batch")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	require.NotZero(t, dm.engine.ResidentDocCount(),
		"the tick retired the segment it had just published — the corpus is gone, and nothing errored")
	require.Equal(t, corpus, dm.engine.ResidentDocCount(),
		"every document survives the re-emit")
	require.NotEmpty(t, residentIDs(dm), "a segment remains to serve reads")
}

// TestReEmitPreservesAliasedFieldCorpus is the DETERMINISTIC-FORMAT leg. The field
// format's build and merge were already convergent, so its rebuild aliased its
// drain segment on EVERY tick rather than intermittently — the field corpus was
// lost every time. It proves the fix belongs to the tick rather than to one format.
func TestReEmitPreservesAliasedFieldCorpus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const corpus = 30 // FIXTURE CONSTANT

	_, gc := newSegmentHarness(t)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	gt, name := kgtypes.GraphCode, "alias-fields"

	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, bm25FieldDocs(corpus)))
	dm := mgr.bm25ManagerFor(gt, name)
	require.Equal(t, corpus, dm.engine.ResidentDocCount(), "the drain sealed the whole batch")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	require.Equal(t, corpus, dm.engine.ResidentDocCount(),
		"the field corpus survives the re-emit")
	require.NotEmpty(t, residentIDs(dm), "a field segment remains to serve reads")
}
