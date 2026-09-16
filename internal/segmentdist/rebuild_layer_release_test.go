// SPDX-License-Identifier: Apache-2.0

// rebuild_layer_release_test.go covers the RESET path's half of mapping
// republication. The seal path's half is release_resident_test.go, and the two
// differ in the one respect that decides the shape: a seal publishes first and the
// durability write follows it, so its release can only run afterwards, while a reset
// BUILDS A WHOLE LAYER ASIDE before publishing anything — so its release has to run
// DURING the build, one partition at a time, or the layer's every encoded blob is on
// the heap at once at the exact moment the process is at its largest.
//
// THE MEASURED STATE THESE ROWS WERE WRITTEN AGAINST: at the corpus benchmark's crest
// the rebuild's built layer was 38.5 % of the client's live heap, and the daemon
// emitted ZERO "released sealed segments to their mappings" lines for a rebuild,
// because finalizeResetLayer's layer-swap branch called writeBuiltLayerToL2 and
// ReplaceLayer and never persistResident — the only caller of the release.

package segmentdist

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// captureLogs redirects the default slog handler into a buffer for the duration of
// one test and returns the buffer.
//
// A TEST USING IT MUST NOT BE PARALLEL: slog.SetDefault is process-global, so two
// parallel tests would interleave their records and each would read the other's.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// releaseLineFor returns the one "released sealed segments to their mappings" record
// emitted for format, or fails naming what the log did hold.
//
// IT MATCHES ON THE FORMAT ATTRIBUTE RATHER THAN ON ORDER because a rebuild finalizes
// both formats through one body, and a row that took "the first release line" would
// pass against a run in which only one format released.
func releaseLineFor(t *testing.T, log *bytes.Buffer, format string) string {
	t.Helper()
	var lines []string
	for line := range strings.SplitSeq(log.String(), "\n") {
		if !strings.Contains(line, "released sealed segments to their mappings") {
			continue
		}
		lines = append(lines, line)
		if strings.Contains(line, "format="+format) {
			return line
		}
	}
	t.Fatalf("no release record for format %s; the release records in this run were:\n%s",
		format, strings.Join(lines, "\n"))
	return ""
}

// resetWorkForDocs buckets docs into one BucketWork per partition at an EXPLICIT
// count, which is the shape StageRebuildPartition accumulates and the finalize
// consumes.
//
// THE COUNT IS A PARAMETER RATHER THAN BucketCountFor(len(docs)), and the reason is
// the test economics: the derived count reaches two only above
// DefaultMinSegmentDocs documents, and this file's property — each partition released
// before the next is built — is a property of the LOOP over partitions, identical at
// four partitions and at a hundred and twenty-eight. Deriving it would buy a corpus
// two orders of magnitude larger and the same assertion.
func resetWorkForDocs(docs []searchengine.Document, partitions int) []searchengine.BucketWork {
	byBucket := make(map[int][]searchengine.Document, partitions)
	for _, d := range docs {
		b := searchengine.BucketOf(d.ID, partitions)
		byBucket[b] = append(byBucket[b], d)
	}
	work := make([]searchengine.BucketWork, 0, len(byBucket))
	for b := range partitions {
		if ds, ok := byBucket[b]; ok {
			work = append(work, searchengine.BucketWork{Bucket: b, Docs: ds})
		}
	}
	return work
}

// mergeDisabledMockPool is a mock-format pool whose background merger is OFF, wired
// around an instrumentedCache so a test can read the ORDER of the cache operations a
// finalize performs.
//
// MERGE IS DISABLED FOR THE SAME REASON sealedPoolOfN DISABLES IT: a merger that
// consolidated the built partitions would put its own blobs through the same cache
// and the operation order this file reads would stop being the reset's.
func mergeDisabledMockPool(t *testing.T, name string) (*distManager[mockQuery, mockStats], *instrumentedCache) {
	t.Helper()
	engine := searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
	})
	t.Cleanup(engine.Close)
	ic := newInstrumentedCache(newDiskSegmentCache(t.TempDir(), 0, adviceRandom))
	return newDistManager[mockQuery, mockStats](engine, ic, graphSelector(kgtypes.GraphCode, name), ""), ic
}

// TestResetRebuildReleasesItsBuiltLayerForBothFormats is requirement 1's row, driven
// through the REAL Manager and both real formats.
//
// RED ON THE BASE: the layer-swap branch never reaches persistResident, so no release
// record is emitted for either format and every published partition still holds its
// encoder output on the heap.
func TestResetRebuildReleasesItsBuiltLayerForBothFormats(t *testing.T) {
	ctx := context.Background()
	log := captureLogs(t)

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt, name := kgtypes.GraphCode, "rebuild-release-both"

	stageRebuildRun(t, ctx, mgr, gt, name, vecContentDocs(8))
	res, err := mgr.FinalizeRebuild(ctx, gt, name)
	require.NoError(t, err)
	require.True(t, res.Swapped, "the rebuild's swap must LAND, or there is no published layer to release")

	for _, arm := range []struct {
		format string
		heap   func() []searchengine.SegmentID
		count  func() int
	}{
		{
			format: bm25FormatName,
			heap:   mgr.bm25ManagerFor(gt, name).engine.HeapBackedResidentIDs,
			count:  mgr.bm25ManagerFor(gt, name).engine.ResidentSegmentCount,
		},
		{
			format: hnswFormatName,
			heap:   mgr.managerFor(gt, name).engine.HeapBackedResidentIDs,
			count:  mgr.managerFor(gt, name).engine.ResidentSegmentCount,
		},
	} {
		t.Run(arm.format, func(t *testing.T) {
			resident := arm.count()
			require.Positive(t, resident,
				"control: this format must have published a layer, or the release row below is vacuous")

			require.Empty(t, arm.heap(),
				"every partition this rebuild published must read its bytes from the L2 mapping, "+
					"not from the encoder output the build retained")

			line := releaseLineFor(t, log, arm.format)
			require.Contains(t, line, "heap_backed="+strconv.Itoa(resident),
				"control: the release must have had this format's whole built layer to give up")
			require.Contains(t, line, "released="+strconv.Itoa(resident),
				"and it must report every one of them SWAPPED, which is what released= means")
		})
	}
}

// TestBuiltLayerReleasesEachPartitionBeforeBuildingTheNext is requirement 2's row: the
// property is not "the layer is released" but "the layer is never WHOLLY on the heap".
//
// THE OBSERVABLE IS THE CACHE OPERATION ORDER, which is the only place the question is
// answerable from outside the engine. Each partition's blob is Put (it becomes
// durable) and then GetMapped (its heap bytes are given up for the mapping); the
// number of partitions Put but not yet mapped is exactly how many encoded blobs the
// built layer is holding at that instant.
//
// THE PREFIX ENDS AT THE LAST GetMapped, deliberately: a reset that retires a prior
// layer fires the engine's merge hook on its first entry (ReplaceLayer → fireMergeHook,
// only when something was retired), and that reclaim writes through this same cache
// after the build window this row is about; this fixture's first reset retires nothing,
// so the bound is a guard for the shape rather than a need of this run.
//
// RED ON THE BASE: no partition is ever mapped, so the prefix is empty and the peak
// reads the whole layer.
func TestBuiltLayerReleasesEachPartitionBeforeBuildingTheNext(t *testing.T) {
	t.Parallel()
	dm, ic := mergeDisabledMockPool(t, "layer-release-order")

	// SEVERAL PARTITIONS, or the peak below is 1 by arithmetic rather than by the
	// release: a single-partition layer holds one blob whatever the code does.
	const partitions = 8
	work := resetWorkForDocs(bm25ReleaseCorpus(64), partitions)
	require.Len(t, work, partitions,
		"fixture control: every partition must carry documents, so the segment count is the partition count")

	_, swapped, err := finalizeResetLayer(t.Context(), kgtypes.GraphCode, "layer-release-order", dm, work)
	require.NoError(t, err)
	require.True(t, swapped, "the layer swap must land")
	require.Equal(t, partitions, dm.engine.ResidentSegmentCount(),
		"control: one segment per partition, so the peak below counts partitions")

	ops := ic.opLog()
	last := -1
	for i, op := range ops {
		if op.kind == "getmapped" {
			last = i
		}
	}
	require.NotEqual(t, -1, last, "no partition was ever mapped: the built layer was never released at all")

	held, peak := 0, 0
	for _, op := range ops[:last+1] {
		switch op.kind {
		case "put":
			held++
			peak = max(peak, held)
		case "getmapped":
			held--
		}
	}
	require.Equal(t, 1, peak,
		"the built layer held %d partitions' encoded blobs at once; each partition must be released to its "+
			"mapping before the next is built, so at most ONE is ever held", peak)
	require.Empty(t, dm.engine.HeapBackedResidentIDs(),
		"and nothing may still be heap-backed once the layer is published")
}

// TestASecondResetRebuildWritesNothingAndStaysMapped is the re-run equivalence row: a
// segment id IS its payload's content hash, so rebuilding an unchanged corpus mints
// the ids the cache already holds and must write nothing.
//
// THE RISK IT COVERS is a release that re-arms on every rebuild: the second pass's
// partitions are byte-identical, already durable and already mapped, so a per-partition
// persist that keyed on "the finalize ran" rather than on cache presence would rewrite
// and re-map the whole layer each time.
// IT IS NOT PARALLEL, because it reads the default slog handler: see captureLogs.
func TestASecondResetRebuildWritesNothingAndStaysMapped(t *testing.T) {
	docs := bm25ReleaseCorpus(32)
	work := resetWorkForDocs(docs, 4)

	dm := bm25ReleasePoolWithCache(t, "rerun", newInstrumentedCache(newDiskSegmentCache(t.TempDir(), 0, adviceRandom)))
	ic := dm.cache.(*instrumentedCache)

	_, swapped, err := finalizeResetLayer(t.Context(), kgtypes.GraphCode, "rerun", dm, work)
	require.NoError(t, err)
	require.True(t, swapped)
	first := dm.engine.Search(bm25.NewQuery("alpha"), 20)
	require.NotEmpty(t, first, "control: the first rebuild must publish a searchable corpus")
	putsAfterFirst := countAllOps(ic, "put")
	require.Positive(t, putsAfterFirst, "control: the first rebuild wrote its layer")

	log := captureLogs(t)
	_, swapped, err = finalizeResetLayer(t.Context(), kgtypes.GraphCode, "rerun", dm, work)
	require.NoError(t, err)
	require.True(t, swapped, "an unchanged re-run still swaps: it publishes the same ids")

	// THE WRITE-DIFF RECORD IS STILL ONE LINE FOR THE LAYER, not one per partition, and
	// it still reports both halves. It is the line an operator reads to tell a rebuild
	// that wrote a layer from one that found every blob already present, and splitting
	// the write across partitions must not split the record.
	require.Contains(t, log.String(), "resident="+strconv.Itoa(len(work))+" written=0 skipped_as_present="+
		strconv.Itoa(len(work)),
		"the re-run's L2 write diff must report the whole layer present and nothing written")
	require.Equal(t, 1, strings.Count(log.String(), "L2 write diff resolved"),
		"and it must be ONE record for the layer, not one per partition")

	require.Equal(t, putsAfterFirst, countAllOps(ic, "put"),
		"a content-hash-unchanged rebuild must write NOTHING: every blob is already in L2")
	require.Empty(t, dm.engine.HeapBackedResidentIDs(),
		"and the re-published layer must still be mapping-backed")

	second := dm.engine.Search(bm25.NewQuery("alpha"), 20)
	require.Len(t, second, len(first))
	for i := range first {
		require.Equal(t, first[i].ID, second[i].ID, "hit %d must be unchanged by the re-run", i)
		require.InDelta(t, first[i].Score, second[i].Score, 0, "hit %s must score identically", first[i].ID)
	}
}

// bm25ReleaseCorpus is the shared corpus for the rows above: every document carries
// the same three common terms plus a unique symbol name, so a query can be answered by
// every partition (which is what makes a per-partition release defect visible in the
// merged result) and by exactly one (which pins routing).
func bm25ReleaseCorpus(n int) []searchengine.Document {
	docs := make([]searchengine.Document, 0, n)
	for i := range n {
		docs = append(docs, searchengine.Document{
			ID: "node-" + strconv.Itoa(i),
			Fields: map[string]string{
				searchengine.FieldContent:    "alpha beta gamma delta epsilon " + strconv.Itoa(i),
				searchengine.FieldSymbolName: "Symbol" + strconv.Itoa(i),
			},
		})
	}
	return docs
}

// bm25ReleasePool is a real-bm25 pool over a real disk cache, with the background
// merger disabled so a published layer stays the layer the finalize published.
func bm25ReleasePool(t *testing.T, name string) *distManager[bm25.Query, *bm25.CorpusStats] {
	t.Helper()
	return bm25ReleasePoolWithCache(t, name, nil)
}

// hnswReleasePool is bm25ReleasePool's VECTOR counterpart, on the same terms and for
// the same reason: the release runs for both formats, and hnsw's decoded graph reads
// its vectors and neighbor lists IN PLACE over the mapped bytes (openGraphV3), so it
// carries the same correctness claim and the same use-after-unmap fault window.
func hnswReleasePool(t *testing.T, name string) *distManager[[]byte, struct{}] {
	t.Helper()
	engine := searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{
		MinSegmentDocs:     1 << 20,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		ScratchDir:         t.TempDir(),
	})
	t.Cleanup(engine.Close)
	return newDistManager[[]byte, struct{}](
		engine, newDiskSegmentCache(t.TempDir(), 0, adviceRandom),
		graphSelector(kgtypes.GraphCode, name), hnsw.New().Name())
}

// bm25ReleasePoolWithCache is bm25ReleasePool with the L2 cache supplied, so a row
// that reads the cache operation log can pass an instrumented one.
func bm25ReleasePoolWithCache(
	t *testing.T, name string, cache segmentL2Cache,
) *distManager[bm25.Query, *bm25.CorpusStats] {
	t.Helper()
	engine := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{
		MinSegmentDocs:     1 << 20,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		ScratchDir:         t.TempDir(),
	})
	t.Cleanup(engine.Close)
	if cache == nil {
		cache = newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
	}
	return newDistManager[bm25.Query, *bm25.CorpusStats](
		engine, cache, graphSelector(kgtypes.GraphCode, name), bm25.New().Name())
}
