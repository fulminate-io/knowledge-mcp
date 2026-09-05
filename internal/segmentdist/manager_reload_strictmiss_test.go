// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestReloadStrictMissIsAnError pins the STRICT re-materialization contract that
// survives the deletion of reload's Fetch leg.
//
// While the cloud rail existed, tolerateMisses=false meant "a failed Source.Fetch
// aborts the reload". With that rail gone there is no Fetch to fail, and the naive
// collapse — drop the miss branch entirely and import whatever the cache still
// holds — would silently convert the strict caller into a tolerant one. That
// caller is load()'s EVICTED branch, whose own doc states the rule this test
// enforces: "an evicted pool must be indistinguishable to a searcher from a
// never-loaded one, so a re-materialization that cannot complete ERRORS".
//
// BOTH DIRECTIONS RUN OVER THE SAME FIXTURE, which is what makes the strict leg
// readable: an assertion that reload errors is satisfiable by a reload that
// errors on everything, so the tolerant leg is the known-positive proving the
// identical pool re-materializes cleanly when misses are tolerated — and proving
// the surviving hit was actually IMPORTED, not merely not-rejected.
func TestReloadStrictMissIsAnError(t *testing.T) {
	t.Parallel()

	// Decodable mock-format bytes: the tolerant leg reaches engine.Import, so a
	// payload that fails Decode would fail that leg for the wrong reason.
	presentBytes := []byte(`[{"id":"d1","content":"alpha"}]`)
	// THE IDS ARE THE CONTENT HASHES OF THEIR OWN BYTES, because Put now verifies
	// the address it is given. `present` is the id of the bytes actually stored;
	// `removedUnderneath` names a segment that is deliberately never written, so
	// any distinct hash serves.
	present := sha256Hex(presentBytes)
	// Removed from L2 underneath the evicted pool — a Remove racing the
	// re-materialization.
	removedUnderneath := sha256Hex([]byte("seg-removed-underneath"))

	newFixture := func(t *testing.T) *distManager[mockQuery, mockStats] {
		t.Helper()
		cache := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
		target := &knowledgev1.GraphSelector{Graph: "code", Repo: "strictreload"}
		dm := newDistManager[mockQuery, mockStats](newMockEngine(t), cache, target, "")
		dm.cache.Put(present, presentBytes)
		return dm
	}

	// The exact set an evictResident would have proved L2-resident and recorded.
	ids := []searchengine.SegmentID{present, removedUnderneath}

	t.Run("strict reload errors and names the absent id", func(t *testing.T) {
		dm := newFixture(t)

		err := dm.reload(ids, false)

		require.Error(t, err, "a strict reload that cannot complete must ERROR, not import a short list")
		require.Contains(t, err.Error(), removedUnderneath,
			"the error must NAME the absent id — an operator cannot act on a bare count")
		require.Empty(t, dm.engine.Search(mockQuery{term: "alpha"}, 10),
			"a strict reload that errored must not have imported the partial set")
	})

	t.Run("tolerant reload over the same fixture imports the available hit", func(t *testing.T) {
		dm := newFixture(t)

		err := dm.reload(ids, true)

		require.NoError(t, err, "the L2-first path tolerates a miss and serves the available superset")
		require.Len(t, dm.engine.Search(mockQuery{term: "alpha"}, 10), 1,
			"the surviving L2 hit must actually be IMPORTED, not merely not-rejected")
	})
}
