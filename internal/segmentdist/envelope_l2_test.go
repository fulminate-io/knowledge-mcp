// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// consolidatedPool is what a real BM25 consolidation leaves behind, located once
// so two tests can assert different things about the same run.
type consolidatedPool struct {
	dm         *distManager[bm25.Query, *bm25.CorpusStats]
	cacheRoot  string
	merged     searchengine.SegmentBlob
	superseded []searchengine.SegmentID
	// storedPath is the merged segment's file in the pool's L2 cache root.
	storedPath string
}

// driveBM25Consolidation runs a real consolidation through a real Manager and
// locates the segment it published.
//
// ONE FIXTURE, TWO TESTS. Both assertions below are about what a consolidation
// stores, and driving two hand-rolled consolidations to ask two questions about
// the same event is the duplication this package's tests otherwise avoid.
//
// THE CORPUS IS SPLIT ACROSS TWO PASSES because handing the same documents to
// both makes the second merge reproduce the first's content hash, storing no new
// file at all — the same reason the allocation harness splits it.
func driveBM25Consolidation(t *testing.T, name string) consolidatedPool {
	t.Helper()

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt := kgtypes.GraphCode

	docs := bm25FieldDocs(2048)
	half := len(docs) / 2
	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, docs[:half]))
	require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, docs[half:]))

	dm := mgr.bm25ManagerFor(gt, name)
	root := graphCacheDirFor(mgr.cacheDir, gt, name, bm25.New().Name())

	// THE MERGED SEGMENT IS THE ONE CARRYING A RECORD. A consolidation stamps its
	// output with what it replaced; a plain seal stamps nothing. Picking by that
	// rather than by size or recency is what makes this unambiguous.
	var found consolidatedPool
	matches := 0
	for _, blob := range dm.engine.Export() {
		if len(blob.Envelope) == 0 {
			continue
		}
		superseded, _, err := searchengine.SupersededBy(blob.Envelope)
		require.NoError(t, err)
		if len(superseded) == 0 {
			continue
		}
		matches++
		found = consolidatedPool{
			dm:         dm,
			cacheRoot:  root,
			merged:     blob,
			superseded: superseded,
			storedPath: filepath.Join(root, blob.ID+".seg"),
		}
	}
	require.Positive(t, matches,
		"no exported blob carried a supersession record, so no consolidation happened and every assertion below is vacuous")
	return found
}

// TestMergedL2FileCarriesTheEnvelope reads the STORED FILE a real consolidation
// wrote and asserts it still begins with the supersession record.
//
// IT IS THE CATCHER FOR THE VARIADIC ARITY HAZARD. Put takes parts, so a caller
// that passes only the payload and forgets the envelope compiles cleanly and
// writes a segment that records nothing about what it replaced — which a cold
// load will publish beside the very constituents it superseded. No grep over the
// call sites can see a missing argument; only reading the file back can.
//
// IT IS THE ONLY GATE HERE THAT READS THE ACTUAL FILE rather than an in-memory
// value, which is the whole reason it exists.
func TestMergedL2FileCarriesTheEnvelope(t *testing.T) {
	pool := driveBM25Consolidation(t, "envelopeOnDisk")

	stored, err := os.ReadFile(pool.storedPath) //nolint:gosec // path is a content-hash id under a test-owned cache root
	require.NoError(t, err, "the consolidation published a segment it did not store")

	// (a) The stored bytes record the constituents the merge consumed, read through
	// the same exported reader the distribution layer uses.
	superseded, cohort, err := searchengine.SupersededBy(stored)
	require.NoError(t, err)
	require.NotEmpty(t, superseded, "the stored merged segment records nothing about what it replaced")
	require.ElementsMatch(t, pool.superseded, superseded,
		"the stored record must name exactly the constituents the published blob names")
	require.Contains(t, cohort, pool.merged.ID, "a segment's cohort must include itself")

	// (b) The file is longer than the payload by exactly the envelope, which is what
	// distinguishes a Put that wrote both parts from one that wrote only the payload.
	require.Len(t, stored, len(pool.merged.Envelope)+len(pool.merged.Bytes),
		"the stored file is not envelope-plus-payload")
	require.Greater(t, len(stored), len(pool.merged.Bytes),
		"the stored file is no longer than the payload alone, so the envelope was dropped")

	// (c) THE RECLAIM PATH'S OWN Put, gated separately BECAUSE THE ASSERTIONS ABOVE
	// CANNOT SEE IT. Put returns early for an id already in the cache, so on the
	// path above the file is written by writeNewBlobsToL2 and reclaimMerged's Put is
	// a no-op — measured, not assumed: dropping the envelope argument from
	// reclaimMerged alone leaves every assertion above green, while dropping it from
	// writeNewBlobsToL2 turns them red.
	//
	// THAT NO-OP IS NOT A REASON TO LEAVE IT UNGATED. reclaimMerged's Put is the
	// crash-safe anchor — it is the writer whenever the merged blob is not already
	// stored, which is exactly the case its Put-before-Remove ordering exists for —
	// so an arity defect there stores an envelope-less merged segment on the one
	// path where nothing else would have written it.
	t.Run("the reclaim path writes both parts too", func(t *testing.T) {
		root := t.TempDir()
		cache := newDiskSegmentCache(root, 0, adviceRandom)
		dm := newDistManager(newMockEngine(t), cache,
			graphSelector(kgtypes.GraphCode, "reclaimEnvelope"), bm25.New().Name())

		// The merged blob from the real consolidation above, into a cache that has
		// never seen it — so this Put is the one that writes the file.
		dm.reclaimMerged(searchengine.MergeResult{
			Removed: pool.superseded,
			Merged:  pool.merged,
		})

		stored, err := os.ReadFile(filepath.Join(root, pool.merged.ID+".seg")) //nolint:gosec // test-owned cache root
		require.NoError(t, err, "the reclaim did not persist the merged blob at all")
		require.Len(t, stored, len(pool.merged.Envelope)+len(pool.merged.Bytes),
			"the reclaim path stored the payload without its envelope")

		sup, _, err := searchengine.SupersededBy(stored)
		require.NoError(t, err)
		require.ElementsMatch(t, pool.superseded, sup,
			"the blob the reclaim path stored records nothing about what it replaced")
	})

	// (d) THE NEGATIVE CONTROL, in the same run. Without it this test passes for an
	// implementation that prefixed an envelope onto EVERY segment, which would change
	// the stored bytes of every ordinary blob in the corpus.
	t.Run("a non-consolidating seal stores no envelope", func(t *testing.T) {
		ctx := context.Background()
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		gt, name := kgtypes.GraphCode, "plainSeal"
		// ONE pass only: nothing is resident, so nothing is superseded.
		require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, name, nil, bm25FieldDocs(2048)))

		root := graphCacheDirFor(mgr.cacheDir, gt, name, bm25.New().Name())
		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		require.NotEmpty(t, entries, "the seal stored nothing, so there is nothing to check")

		checked := 0
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".seg" {
				continue
			}
			blob, err := os.ReadFile(filepath.Join(root, e.Name())) //nolint:gosec // test-owned cache root
			require.NoError(t, err)
			require.NotEmpty(t, blob)

			sup, _, err := searchengine.SupersededBy(blob)
			require.NoError(t, err)
			require.Empty(t, sup, "a plain seal supersedes nothing, so its stored blob must carry no record")
			require.NotEqual(t, byte(0x00), blob[0],
				"a record-less blob must begin with the FORMAT's own version integer, not the envelope magic's leading zero")
			checked++
		}
		require.Positive(t, checked, "no stored segment was examined, so the control asserts nothing")
	})
}

// TestL2AndResidencyAccountingCountTheWholeFile pins the two byte counters the
// blob split can silently under-count.
//
// BOTH ARE SINGLE-EXPRESSION CHANGES THAT COMPILE EITHER WAY and that no
// functional test would notice, which is exactly why they need a gate of their
// own. The L2 counter is what eviction compares against the operator's configured
// cap, so an under-count lets the cache exceed that cap indefinitely; the
// residency counter is summed as the on-disk total the pressure signal reports.
// Nothing else in this changeset reads either number.
func TestL2AndResidencyAccountingCountTheWholeFile(t *testing.T) {
	pool := driveBM25Consolidation(t, "accounting")

	// THE PRECONDITION IS WHAT STOPS THIS BEING A PROXY. If the envelope were
	// empty, len(Envelope)+len(Bytes) would equal len(Bytes) and every assertion
	// below would pass for a payload-only implementation.
	require.NotEmpty(t, pool.merged.Envelope,
		"the merged blob carries no envelope, so every whole-file assertion below is satisfied by counting the payload alone")
	whole := int64(len(pool.merged.Envelope) + len(pool.merged.Bytes))

	info, err := os.Stat(pool.storedPath)
	require.NoError(t, err)
	require.Equal(t, whole, info.Size(),
		"the file on disk is not envelope-plus-payload, so it is the wrong yardstick for the counters below")

	// (a1) The L2 entry's recorded size, measured against the filesystem rather
	// than against another number this code produced.
	sized, ok := pool.dm.cache.sizeOf(pool.merged.ID)
	require.True(t, ok, "the merged segment is not L2-resident")
	require.Equal(t, info.Size(), sized,
		"the L2 entry's byte count disagrees with the file it describes")

	// (a2) THE DELTA ACROSS A REAL Put, measured directly on a fresh cache so the
	// before value is unambiguous. This is the counter eviction reads.
	fresh := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
	require.Zero(t, fresh.curByt, "a fresh cache must start at zero, or the delta below is not a delta")
	require.NoError(t, fresh.Put(pool.merged.ID, pool.merged.Envelope, pool.merged.Bytes))
	require.Equal(t, whole, fresh.curByt,
		"Put counted %d bytes for a %d-byte write — an enveloped blob whose accounting counts one part lets the cache exceed its configured cap silently",
		fresh.curByt, whole)

	// (b) THE RESIDENCY COUNTER, which is a different line in a different file and
	// under-counts the same way.
	//
	// THE L2-FIRST LOAD IS DRIVEN EXPLICITLY, and that is not a workaround for an
	// empty map — it is the only path that populates this counter, and it is the
	// path this changeset altered. Residency is recorded when segments are IMPORTED
	// from L2, which is what a cold start does; publishing a merge does not record
	// it. Driving it here means the assertion below covers the load path's split of
	// the mapped file as well as the counter it feeds.
	require.NoError(t, pool.dm.loadResidentFromL2())

	pool.dm.resMu.Lock()
	seg, resident := pool.dm.resident[pool.merged.ID]
	pool.dm.resMu.Unlock()
	require.True(t, resident, "the merged segment is not recorded resident")
	require.Equal(t, int(info.Size()), seg.mappedBytes,
		"residency accounting records the payload alone, so the on-disk total it feeds under-reports every enveloped segment")
}
