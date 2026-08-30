// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// manager_bucket_delete_fixtures_test.go holds the fixtures every delete-path test in
// this package shares: a seeded two-format corpus drained to L2, with ONE of the two
// pools' caches swapped for an instrumented one so a test can fail its writes on demand.
//
// THEY LIVE TOGETHER BECAUSE WHICH POOL IS INSTRUMENTED IS NOW A REAL CHOICE. A delete's
// vector leg is a live-bit kill that writes no blob, so a test about what a DELETE
// reports instruments the field pool, while a test that needs a stranded VECTOR
// constituent instruments the vector pool and drives it through the DRAIN. Keeping both
// fixtures side by side is what makes that choice visible at the point of use rather
// than buried in whichever test file happened to declare one.

// deleteRetryFixture seeds one graph's corpus through both formats, drains it to
// L2, and then swaps the HNSW pool's cache for an instrumented one so writes to the
// VECTOR pool go through a seam a test can fail on demand.
//
// IT IS NO LONGER A DELETE-PATH SEAM, and that is the whole reason the field variant
// below exists. A delete's vector leg is now a live-bit kill that writes nothing, so an
// injection here is invisible to DeleteFromBuckets; what still drives the vector pool's
// L2 write is the DRAIN (ReEmitDirtyBuckets), which is where a test wanting a stranded
// vector constituent must now aim. Tests about the DELETE's own error contract use
// deleteFieldRetryFixture instead.
//
// ONLY ONE POOL IS INSTRUMENTED PER FIXTURE, deliberately: failing one pool's writes
// keeps the observed error and the observed call counts attributable to a single leg,
// where instrumenting both would leave a count assertion summing two independently
// retrying legs.
func deleteRetryFixture(t *testing.T, name string) (
	*Manager, kgtypes.GraphType, string,
	*distManager[[]byte, struct{}], *instrumentedCache, searchengine.Document,
) {
	t.Helper()
	mgr, gt, nm, hdm, ic, docs := deleteRetryFixtureOfSize(t, name, deleteFixtureN)
	return mgr, gt, nm, hdm, ic, docs[0]
}

// deleteRetryFixtureOfSize is deleteRetryFixture with the corpus size made an
// argument, and it returns the whole document slice rather than one victim.
//
// THE SIZE IS A PARAMETER BECAUSE THE PARTITION COUNT IS DERIVED FROM IT, and one
// property below is only reachable above a partition boundary — see
// TestDeleteAbsorbsATransientL2WriteFailure, whose retry has nothing to write on a
// single-partition delete.
func deleteRetryFixtureOfSize(t *testing.T, name string, n int) (
	*Manager, kgtypes.GraphType, string,
	*distManager[[]byte, struct{}], *instrumentedCache, []searchengine.Document,
) {
	t.Helper()

	ctx := context.Background()
	gt := kgtypes.GraphCode
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	docs := bothFormatDocs(n, "delretry-"+name+"-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	hdm := mgr.managerFor(gt, name)
	inner, ok := hdm.cache.(*diskSegmentCache)
	require.True(t, ok,
		"FIXTURE PRECONDITION: the pool's cache must be the real disk cache to wrap — a manager "+
			"that constructed something else would leave every injection below inert")
	ic := newInstrumentedCache(inner)
	hdm.cache = ic

	require.Equal(t, n, hdm.engine.ResidentDocCount(),
		"FIXTURE PRECONDITION: the vector corpus holds the whole fixture, so the delete really has a partition to rebuild")
	return mgr, gt, name, hdm, ic, docs
}

// deleteFieldRetryFixture is deleteRetryFixture pointed at the FIELD pool: it seeds the
// same corpus and then swaps the BM25 pool's cache for an instrumented one.
//
// IT IS THE DELETE-PATH SEAM. The delete's field leg is the one L2 write DeleteFromBuckets
// still performs, and it is the leg that still carries both delete-only policies — the
// bounded write retry and the aborted-reclaim report — so every property about what a
// delete REPORTS is exercised here. Nothing about those properties changed; only which
// pool's disk the delete touches did.
func deleteFieldRetryFixture(t *testing.T, name string, n int) (
	*Manager, kgtypes.GraphType, string,
	*distManager[bm25.Query, *bm25.CorpusStats], *instrumentedCache, []searchengine.Document,
) {
	t.Helper()

	ctx := context.Background()
	gt := kgtypes.GraphCode
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	docs := bothFormatDocs(n, "delfield-"+name+"-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	fdm := mgr.bm25ManagerFor(gt, name)
	inner, ok := fdm.cache.(*diskSegmentCache)
	require.True(t, ok,
		"FIXTURE PRECONDITION: the pool's cache must be the real disk cache to wrap — a manager "+
			"that constructed something else would leave every injection below inert")
	ic := newInstrumentedCache(inner)
	fdm.cache = ic

	require.Equal(t, n, fdm.engine.ResidentDocCount(),
		"FIXTURE PRECONDITION: the field corpus holds the whole fixture, so the delete really has a partition to rebuild")
	return mgr, gt, name, fdm, ic, docs
}

// l2BM25IDs lists the .seg ids under the FIELD pool's L2 root, mirroring l2HNSWIDs.
func l2BM25IDs(cacheDir, name string) map[searchengine.SegmentID]struct{} {
	ids := newDiskSegmentCache(
		graphCacheDirFor(cacheDir, kgtypes.GraphCode, name, bm25.New().Name()), 0, adviceRandom).Keys()
	out := make(map[searchengine.SegmentID]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

// twoPartitionFixtureN is the corpus size that derives TWO partitions:
// BucketCountFor is a ceiling division by searchengine.DefaultMinSegmentDocs rounded
// up to a power of two, so one document past that constant is the smallest corpus
// that crosses the boundary. Sized from the constant rather than spelled as a
// literal, so a change to the engine's segment sizing moves this fixture with it.
var twoPartitionFixtureN = searchengine.DefaultMinSegmentDocs + 1

// victimsInDistinctPartitions picks two documents that hash to DIFFERENT partitions
// under bucketCount, so a delete naming both dirties two partitions and the group
// swap publishes two blobs.
//
// IT ASKS THE ENGINE'S OWN PARTITION FUNCTION. Deriving the partition any other way
// here would let this fixture and the code under test disagree, and the disagreement
// would look exactly like a delete that legitimately touched one partition.
func victimsInDistinctPartitions(
	t *testing.T, docs []searchengine.Document, bucketCount int,
) []searchengine.ExternalID {
	t.Helper()
	first := docs[0]
	firstBucket := searchengine.BucketOf(first.ID, bucketCount)
	for _, d := range docs[1:] {
		if searchengine.BucketOf(d.ID, bucketCount) != firstBucket {
			return []searchengine.ExternalID{first.ID, d.ID}
		}
	}
	t.Fatalf("FIXTURE: no two of the %d documents land in different partitions under a count of %d",
		len(docs), bucketCount)
	return nil
}

// requireResidentSetBackedByL2 asserts every segment the pool currently serves is
// readable from its L2 cache — the durability property persistResident exists to
// establish, and the one a write retry either delivers or does not.
func requireResidentSetBackedByL2[Q, S any](t *testing.T, dm *distManager[Q, S], cache segmentL2Cache) {
	t.Helper()
	resident := dm.engine.Export()
	require.NotEmpty(t, resident,
		"CONTROL: the pool must serve something, or 'every resident blob is on disk' is vacuously true")
	for _, b := range resident {
		_, present := cache.sizeOf(b.ID)
		require.True(t, present,
			"resident segment %s is not in L2 — the write that was supposed to make it durable did not land", b.ID)
	}
}
