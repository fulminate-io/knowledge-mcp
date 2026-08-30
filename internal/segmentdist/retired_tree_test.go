// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

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

// TestKeepFormatPartitionsSegmentFamilies WAS DELETED HERE. It asserted the
// version-carrying format name splits old and new layouts into disjoint families —
// the break in the two-client rebuild PING-PONG — against a per-meta FILTER on
// distManager, deleted with the rail.
//
// SUCCESSOR: TestFormatFamiliesAreDisjointOnDisk (format_family_disjoint_test.go),
// which asserts the same incident shape against the mechanism that now enforces it —
// the per-(graph,format) cache root, which a client on one family never opens for
// another. That test was extended to cover the BM25 family as well as HNSW when this
// one was removed, so the format dimension this test contributed is not lost.

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
