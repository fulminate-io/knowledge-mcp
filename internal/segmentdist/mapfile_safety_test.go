// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestMappingSurvivesRenameAndUnlink re-proves on the IMPLEMENTATION what the
// design work proved on a throwaway reader: a live mapping is unaffected by the
// cache's own file operations.
//
// Both are real operations this cache performs. Put writes through a temp file
// and renames over the destination, and Remove unlinks it. Content-addressing
// makes the rename a no-op by construction — the same name can only ever carry
// the same bytes — and POSIX keeps the inode alive for a mapping across an
// unlink, which is what lets a mapped segment outlive its eviction.
func TestMappingSurvivesRenameAndUnlink(t *testing.T) {
	dir := t.TempDir()
	cache := newDiskSegmentCache(dir, 0, adviceRandom)
	id, blobBytes := mappedCorpus(t, doc("a", "alpha"), doc("b", "beta"), doc("c", "gamma"))
	cache.Put(id, blobBytes)

	data, release, ok, err := cache.GetMapped(id)
	require.NoError(t, err)
	require.True(t, ok)
	defer release()

	engine := newMockEngine(t)
	defer engine.Close()
	require.NoError(t, engine.Import([]searchengine.SegmentBlob{{ID: id, Bytes: data}}, nil))
	before := searchIDs(engine.Search(mockQuery{term: "alpha"}, 10))
	require.NotEmpty(t, before, "the mapped segment matched nothing, so the comparisons below are vacuous")

	// (a) The exact temp+rename of IDENTICAL bytes that Put performs.
	require.NoError(t, atomicWriteFile(filepath.Join(dir, id+".seg"), blobBytes))
	require.Equal(t, before, searchIDs(engine.Search(mockQuery{term: "alpha"}, 10)),
		"results changed after an identical-bytes rename over the mapped file")

	// (b) The unlink Remove performs.
	require.NoError(t, os.Remove(filepath.Join(dir, id+".seg")))
	require.Equal(t, before, searchIDs(engine.Search(mockQuery{term: "alpha"}, 10)),
		"results changed after the mapped file was unlinked")

	// A FULL re-fault of every page after the unlink: touching the whole mapping
	// is what proves the inode survived, rather than only the pages already
	// resident when the file went away.
	sum := 0
	for _, b := range data {
		sum += int(b)
	}
	require.Positive(t, sum, "re-reading every mapped page after the unlink produced nothing")
	require.True(t, bytes.Equal(blobBytes, data), "the mapping's bytes changed after rename and unlink")
}
