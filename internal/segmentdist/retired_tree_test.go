// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestRetiredTreeReclaimIsPerFormatAndOnce proves the two properties that make
// the parameterised reclaim safe for more than one format.
//
// INDEPENDENCE is the load-bearing one, and it is why the markers are distinct.
// The guard returns early when its marker exists, so a marker SHARED between
// formats would let whichever format constructed first write it and permanently
// suppress the other's reclaim — the second tree stranded on disk with nothing in
// the logs saying why. The discriminating step below is the HNSW reclaim running
// AFTER the BM25 one has already written its marker: under a shared marker the
// HNSW tree survives that call, and this test goes red exactly there.
//
// ONCE is the second property: after a format's marker is written, recreating its
// retired tree (a downgrade wrote it again) must NOT be reclaimed a second time.
func TestRetiredTreeReclaimIsPerFormatAndOnce(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	// Both retired trees exist, and both replacements have produced something —
	// so the precondition holds for BOTH formats and neither declines for a
	// reason other than its own marker.
	seedTree := func(name string) string {
		dir := filepath.Join(base, name, "knowledge", "default")
		require.NoError(t, os.MkdirAll(dir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "old.seg"), []byte("old bytes"), 0o600))
		return dir
	}
	hnswRetired := seedTree(retiredHNSWTree)
	bm25Retired := seedTree(retiredBM25Tree)
	require.NoError(t, os.MkdirAll(filepath.Join(base, hnsw.New().Name()), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(base, bm25.New().Name()), 0o750))

	// BM25 reclaims first and writes ITS marker.
	removeRetiredTree(base, retiredBM25Tree, bm25.New().Name(), retiredTreeMarker)
	require.NoDirExists(t, bm25Retired, "the BM25 reclaim removed its own tree")
	require.FileExists(t, filepath.Join(base, retiredTreeMarker))
	require.DirExists(t, hnswRetired, "the BM25 reclaim must not touch the HNSW tree")

	// THE DISCRIMINATOR: HNSW reclaims with BM25's marker already on disk. A shared
	// marker would make this a no-op and leave the tree behind.
	removeRetiredTree(base, retiredHNSWTree, hnsw.New().Name(), retiredHNSWMarker)
	require.NoDirExists(t, hnswRetired,
		"the HNSW reclaim ran even though BM25's marker already existed — a shared marker would have suppressed it")
	require.FileExists(t, filepath.Join(base, retiredHNSWMarker),
		"the HNSW reclaim wrote its OWN marker, not BM25's")

	// ONCE, per format: a resurrected tree is not reclaimed again.
	for _, f := range []struct {
		retiredName string
		replacement string
		marker      string
	}{
		{retiredBM25Tree, bm25.New().Name(), retiredTreeMarker},
		{retiredHNSWTree, hnsw.New().Name(), retiredHNSWMarker},
	} {
		resurrect := seedTree(f.retiredName)
		removeRetiredTree(base, f.retiredName, f.replacement, f.marker)
		require.DirExists(t, resurrect,
			"%s: the reclamation re-ran after its marker was written", f.retiredName)
	}
}

// TestKeepFormatPartitionsSegmentFamilies proves the version-carrying format name
// does the job it was chosen for: it splits the old and new layouts into two
// disjoint families, so a client on one never sees the other's segments.
//
// That is what breaks the two-client ping-pong. Sharing one name, a client that
// rejects the other's blobs rebuilds them in its own layout — and the other
// client then rejects THOSE and rebuilds again, each one's output being the
// other's next rejection, indefinitely. Disjoint names mean each rebuilds once.
//
// Both directions are asserted, and the second is the known-positive: it is not
// enough that the v1-named meta is filtered out, the v2-named one must survive.
// A keepFormat that rejected everything would pass the first half alone.
func TestKeepFormatPartitionsSegmentFamilies(t *testing.T) {
	t.Parallel()

	dm, _ := newReclaimManager(t, t.TempDir())
	dm.format = bm25.New().Name()
	require.Equal(t, "bm25v2", dm.format, "the shipped format name must be version-carrying")

	metas := []searchengine.SegmentMeta{
		{ID: "old-family", Format: "bm25"},
		{ID: "new-family", Format: dm.format},
	}
	var kept []searchengine.SegmentID
	for _, meta := range metas {
		if dm.keepFormat(meta.Format) {
			kept = append(kept, meta.ID)
		}
	}
	require.Equal(t, []searchengine.SegmentID{"new-family"}, kept,
		"only this client's own format family may survive the gate")

	// A manager with no format pinned is the unfiltered case and must keep both,
	// so the partition above is the format name doing the work rather than a
	// gate that happens to reject everything unfamiliar.
	dm.format = ""
	kept = nil
	for _, meta := range metas {
		if dm.keepFormat(meta.Format) {
			kept = append(kept, meta.ID)
		}
	}
	require.Len(t, kept, 2, "an unpinned manager filters nothing")
}

// TestRetiredCacheTreeRemovedOnce pins all three halves of the reclamation
// guard: it declines while the replacement tree is absent, reclaims once the
// replacement exists, and never re-scans afterwards.
//
// The decline case is the one that matters. The removal deletes a whole cache
// tree, and firing it before the new family has produced anything would throw
// away the only segments on the machine.
func TestRetiredCacheTreeRemovedOnce(t *testing.T) {
	t.Parallel()

	newBase := func(t *testing.T) (base, retired string) {
		t.Helper()
		base = t.TempDir()
		retired = filepath.Join(base, retiredBM25Tree, "knowledge", "default")
		require.NoError(t, os.MkdirAll(retired, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(retired, "old.seg"), []byte("old bytes"), 0o600))
		return base, retired
	}

	t.Run("declines while the replacement is absent", func(t *testing.T) {
		base, retired := newBase(t)
		removeRetiredTree(base, retiredBM25Tree, bm25.New().Name(), retiredTreeMarker)
		require.DirExists(t, retired,
			"the retired tree was deleted before the new family had written anything — "+
				"that is the only cached corpus on the machine")
		require.NoFileExists(t, filepath.Join(base, retiredTreeMarker),
			"a decline must not mark the reclamation as done")
	})

	t.Run("reclaims once the replacement exists", func(t *testing.T) {
		base, _ := newBase(t)
		require.NoError(t, os.MkdirAll(filepath.Join(base, bm25.New().Name()), 0o750))

		removeRetiredTree(base, retiredBM25Tree, bm25.New().Name(), retiredTreeMarker)
		require.NoDirExists(t, filepath.Join(base, retiredBM25Tree))
		require.FileExists(t, filepath.Join(base, retiredTreeMarker))

		// NEVER RE-SCANS: recreating the old tree (a downgrade wrote it again)
		// must not be reclaimed a second time, because the marker records that
		// the one-time migration already happened.
		resurrect := filepath.Join(base, retiredBM25Tree, "knowledge", "default")
		require.NoError(t, os.MkdirAll(resurrect, 0o750))
		removeRetiredTree(base, retiredBM25Tree, bm25.New().Name(), retiredTreeMarker)
		require.DirExists(t, resurrect, "the reclamation re-ran after its marker was written")
	})
}
