// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestDeterministicShipKeepsBothFormatsResolvable models the REAL post-rebuild
// state: manage(rebuild_segments) Adds BOTH the deterministic HNSW segments and
// the BM25 segments for one graph, then FinalizeRebuild lands them in one finalize.
//
// THE ORIGINAL INCIDENT, carried forward because the hazard outlived its mechanism.
// The remote store keyed blobs by graph ONLY, with no format dimension, so each
// engine's remote listing returned BOTH formats' blobs. Without a per-meta format
// filter in the seeding path, the BM25 side's reconcile-prune read the just-landed
// HNSW segment as "held but no longer exported" — the BM25 engine never exports an
// HNSW blob — and DELETED it. The vectors vanished while BM25 text search kept
// working, which is what made the loss quiet enough to survive review.
//
// WHAT ENFORCES IT NOW is the cache layout rather than a filter: each format's
// blobs live under their own cache root, so neither side can enumerate — or delete —
// the other's. This test fails-when-absent either way: with the loss, the
// require.True(ok) below fails because the HNSW segment is gone.
func TestDeterministicShipKeepsBothFormatsResolvable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// searchCorpusN == MinSegmentDocs → exactly one full deterministic chunk per format.
	docs, targetID, targetVec, _ := searchCorpus(11)

	// Ship via the RESET rebuild path: stage the partition carrying BOTH formats, then
	// the single serial FinalizeRebuild — exactly what RebuildSegments drives.
	cacheDir := t.TempDir()
	shipper := closeOnCleanup(t, NewManager(cacheDir, 0))
	require.NoError(t, shipper.StageRebuildPartition(ctx, kgtypes.GraphKnowledge, "kg", docs, docs))
	_, err := shipper.FinalizeRebuild(ctx, kgtypes.GraphKnowledge, "kg")
	require.NoError(t, err)

	// BOTH formats must survive ON DISK — the BM25 write must NOT reclaim the HNSW
	// segment (and vice versa).
	hnswCount, bm25Count := countCachedByFormat(cacheDir, kgtypes.GraphKnowledge, "kg")
	require.Positive(t, hnswCount, "the HNSW segment must survive the BM25 write — a cross-format reclaim would take it")
	require.Positive(t, bm25Count, "the BM25 segment must survive too")

	// A FRESH read manager over the SAME cache dir resolves the target's STORED vector
	// by id — the exact read mode:'similar' performs, and the user-visible failure
	// under the bug. It must share the dir: the blobs live only in L2 now, so a manager
	// rooted anywhere else would resolve nothing no matter how correct the write was.
	fresh := closeOnCleanup(t, NewManager(cacheDir, 0))
	vec, ok, err := fresh.VectorByID(ctx, kgtypes.GraphKnowledge, "kg", targetID)
	require.NoError(t, err)
	require.True(t, ok, "VectorByID must resolve the deterministic-shipped vector after a both-formats rebuild ship")
	require.True(t, bytes.Equal(vec, targetVec), "resolved vector byte-equal to the shipped vector")
}

// TestEmbedShipKeepsBothFormatsResolvable is the embed-path twin of the
// deterministic test: the HNSW and BM25 write paths interleave the
// per-format ships, which is the same cross-format prune exposure. Both formats
// must survive and the by-id read must resolve.
func TestEmbedShipKeepsBothFormatsResolvable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	docs, targetID, targetVec, _ := searchCorpus(7)

	cacheDir := t.TempDir()
	mgr := closeOnCleanup(t, NewManager(cacheDir, 0))
	seedShipped(t, ctx, mgr, kgtypes.GraphKnowledge, "kg", docs)
	seedShippedFields(t, ctx, mgr, kgtypes.GraphKnowledge, "kg", docs)

	hnswCount, bm25Count := countCachedByFormat(cacheDir, kgtypes.GraphKnowledge, "kg")
	require.Positive(t, hnswCount, "the embed HNSW segment must survive the BM25 write")
	require.Positive(t, bm25Count, "embed BM25 segment must survive")

	// Same dir as the writer, for the reason stated in the sibling above.
	fresh := closeOnCleanup(t, NewManager(cacheDir, 0))
	vec, ok, err := fresh.VectorByID(ctx, kgtypes.GraphKnowledge, "kg", targetID)
	require.NoError(t, err)
	require.True(t, ok, "VectorByID resolves the embed-shipped vector when both formats coexist on one graph key")
	require.True(t, bytes.Equal(vec, targetVec), "byte-equal")
}

// countCachedByFormat tallies the blobs one graph holds ON DISK, per format.
//
// IT USED TO TALLY THE FAKE SERVER'S stored blobs. There is no server: the durable
// store is the per-(graph,format) L2 cache root, and each format's blobs live under
// their own directory. Counting the directories is the same measurement — how many
// of each format's bytes survived the other format's write — against the authority
// that still holds them.
//
// THE PER-FORMAT ROOTING IS ALSO WHAT NOW ENFORCES THE PROPERTY, which is worth
// stating beside the counter: neither format's reclaim can enumerate the other's ids
// because it never opens that directory. The count is still asserted rather than
// assumed, because "the paths obviously keep them apart" is exactly the reasoning a
// regression test exists to stop being persuasive.
func countCachedByFormat(cacheDir string, gt kgtypes.GraphType, name string) (hnswCount, bm25Count int) {
	hnswCount = len(newDiskSegmentCache(
		graphCacheDirFor(cacheDir, gt, name, hnsw.New().Name()), 0, adviceRandom).Keys())
	bm25Count = len(newDiskSegmentCache(
		graphCacheDirFor(cacheDir, gt, name, bm25.New().Name()), 0, adviceRandom).Keys())
	return hnswCount, bm25Count
}
