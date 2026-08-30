// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestFormatFamiliesAreDisjointOnDisk asserts the ping-pong break AT THE MECHANISM
// rather than arguing it in prose.
//
// THE LOOP THIS PREVENTS: two clients of one account at different versions
// sharing a single format name. Each rejects the other's blobs as unreadable and
// rebuilds them in its own layout, and each rebuild is the other's next
// rejection — forever. A version-carrying name makes the two layouts disjoint
// families, so neither client ever SEES the other's metas and each rebuilds once.
//
// BOTH DIRECTIONS ARE ASSERTED, and that is the point. Showing only that the new
// client filters out old metas would leave the loop half-open: it is equally
// required that an OLD client does not pick up NEW segments it cannot read.
//
// == THE MECHANISM MOVED; THE SIGNATURE DID NOT ==
//
// PREDECESSOR: TestFormatFamiliesAreDisjointUnderKeepFormat, which asserted this
// same incident shape against a per-meta FILTER on distManager that dropped any meta
// whose format tag was not this manager's. That filter was deleted
// with the cloud segment rail, because a filter over a remote List has nothing left
// to filter: there is no remote List.
//
// THE PROPERTY IS NOW ENFORCED BY PATH RATHER THAN BY FILTER, which is a stronger
// guarantee, and this test moved onto that mechanism instead of being deleted with
// the old one. The L2 cache root is keyed by format — graphCacheDirFor(base,
// graphType, name, format) — and a read enumerates exactly one such root via
// cache.Keys(). A client on one format therefore cannot enumerate another family's
// ids, because those ids live in a directory it never opens. (This said "the local
// source's List enumerates one root", which contradicted this same file's header
// once that source was deleted: the enumeration is the cache's, and always was.)
//
// WHY IT WAS REWRITTEN RATHER THAN RETIRED: "the property probably still holds
// because of how the paths work" is exactly the reasoning a signature test exists
// to stop being persuasive. Nothing else in the tree asserts the disjointness, so
// a future change making the cache root format-agnostic — or introducing a shared
// listing path — would bring the loop back with no test standing in the way.
func TestFormatFamiliesAreDisjointOnDisk(t *testing.T) {
	t.Parallel()

	// BOTH FORMAT FAMILIES ARE COVERED. The HNSW case is this test's own; the BM25
	// case arrived when TestKeepFormatPartitionsSegmentFamilies (retired_tree_test.go)
	// was deleted with the per-meta format filter it asserted against. Running both here
	// keeps the format dimension that test contributed rather than assuming the
	// path-keying generalises — it does, but "it obviously generalises" is the
	// reasoning a signature test exists to stop being persuasive.
	t.Run("hnsw", func(t *testing.T) {
		t.Parallel()
		assertFamiliesDisjoint(t, "hnsw", hnsw.New().Name(), "hnswv3", "pingpong-repo")
	})
	t.Run("bm25", func(t *testing.T) {
		t.Parallel()
		assertFamiliesDisjoint(t, "bm25", bm25.New().Name(), "bm25v2", "pingpong-bm25-repo")
	})
}

// assertFamiliesDisjoint runs the disjointness proof for one format family pair.
func assertFamiliesDisjoint(t *testing.T, retiredName, current, wantCurrent, graphName string) {
	t.Helper()

	require.Equal(t, wantCurrent, current, "the shipped format name must be version-carrying")
	require.NotEqual(t, retiredName, current,
		"the families are only disjoint if the names actually differ — this is the whole mechanism")

	cacheDir := t.TempDir()
	plantBlob(t, cacheDir, graphName, retiredName, "old-family", []byte("blob in the retired layout"))
	plantBlob(t, cacheDir, graphName, current, "new-family", []byte("blob in the current layout"))

	// listedBy enumerates through the REAL read seam a client uses — a local source
	// over the per-(graph,format) cache root — rather than through a filter helper.
	listedBy := func(format string) []searchengine.SegmentID {
		t.Helper()
		cache := newDiskSegmentCache(
			graphCacheDirFor(cacheDir, kgtypes.GraphCode, graphName, format), 0, adviceRandom)
		// READ THE CACHE ROOT DIRECTLY. This used to list through a segment source,
		// which synthesized one meta per cached id and stamped it with the source's
		// format. The source is deleted, and the stamp it applied was never the
		// property under test: what makes the families disjoint is that each root
		// holds only its own family's ids, which is exactly what Keys() answers.
		ids := make([]searchengine.SegmentID, 0)
		for _, id := range cache.Keys() {
			ids = append(ids, id)
		}
		return ids
	}

	require.Equal(t, []searchengine.SegmentID{"new-family"}, listedBy(current),
		"a client on the current format must not see the retired family's segments")
	require.Equal(t, []searchengine.SegmentID{"old-family"}, listedBy(retiredName),
		"a client on the retired format must not see the current family's segments either — "+
			"the other half of the ping-pong")

	// KNOWN-POSITIVE: each family DOES see its own id. Without it, a List wired to
	// return nothing — or a cache root pointed at an empty directory — would satisfy
	// both assertions above while enumerating for the wrong reason, and the test
	// would be asserting emptiness rather than disjointness.
	require.Len(t, listedBy(current), 1, "the current family must actually see its own segment")
	require.Len(t, listedBy(retiredName), 1, "and the retired family must actually see its own")

	// FIXTURE CONTROL on the premise itself: the two roots really are different
	// directories. If graphCacheDirFor ever stopped keying on format, every
	// assertion above would still pass while the families silently merged.
	require.NotEqual(t,
		graphCacheDirFor(cacheDir, kgtypes.GraphCode, graphName, current),
		graphCacheDirFor(cacheDir, kgtypes.GraphCode, graphName, retiredName),
		"the disjointness is the PATH — two families sharing one root is the defect itself")
}
