// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// realMergeBlobs builds genuine, decodable HNSW blobs for a crash-safety test: two
// single-doc constituent blobs and one consolidated 2-doc merged blob (a distinct
// content hash), all real bytes so a fresh diskSegmentCache + Import reconstructs
// the corpus exactly as production would. Returns (constituents, merged).
func realMergeBlobs(t *testing.T) (constituents []searchengine.SegmentBlob, merged searchengine.SegmentBlob) {
	t.Helper()
	docs := vecContentDocs(2)

	// Two single-doc segments → two constituents.
	src := closeOnCleanup(t, searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{
		MinSegmentDocs: 1, DeletesPctAllowed: 2.0, SegmentCountTarget: 1 << 30,
	}))
	defer src.Close()
	for _, d := range docs {
		require.NoError(t, src.Add([]searchengine.Document{d}))
	}
	constituents = src.Export()
	require.Len(t, constituents, 2, "two single-doc constituents")

	// One 2-doc segment → the merged blob (distinct content hash from either).
	m := closeOnCleanup(t, searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{
		MinSegmentDocs: 2, DeletesPctAllowed: 2.0, SegmentCountTarget: 1 << 30,
	}))
	defer m.Close()
	require.NoError(t, m.Add(docs))
	mergedBlobs := m.Export()
	require.Len(t, mergedBlobs, 1, "one consolidated 2-doc segment")
	return constituents, mergedBlobs[0]
}

// reloadCorpusFromDir constructs a FRESH diskSegmentCache over dir (scanExisting
// re-reads whatever .seg files survived), decodes every recovered blob, Imports
// them into a fresh engine, and returns the set of external doc ids that engine
// can recover via self-recall search. Models a process restart reading L2 after a
// crash.
func reloadCorpusFromDir(t *testing.T, dir string, want []searchengine.Document) map[searchengine.ExternalID]struct{} {
	t.Helper()
	fresh := newDiskSegmentCache(dir, 0, adviceRandom)
	eng := closeOnCleanup(t, searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{
		MinSegmentDocs: 1, DeletesPctAllowed: 2.0, SegmentCountTarget: 1 << 30,
	}))
	defer eng.Close()

	// Re-import every .seg file currently on disk via the fresh cache.
	for id := range diskCacheIDs(fresh) {
		b, ok := fresh.Get(id)
		require.True(t, ok, "scanned id %s must be readable", id)
		require.NoError(t, eng.Import([]searchengine.SegmentBlob{{ID: id, Bytes: b}}, nil))
	}

	recovered := make(map[searchengine.ExternalID]struct{})
	for _, d := range want {
		for _, h := range eng.Search(d.Vector, 10) {
			if h.ID == d.ID {
				recovered[d.ID] = struct{}{}
				break
			}
		}
	}
	return recovered
}

// diskCacheIDs returns the ids the cache currently indexes (post-scanExisting).
func diskCacheIDs(c *diskSegmentCache) map[string]struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]struct{}, len(c.index))
	for id := range c.index {
		out[id] = struct{}{}
	}
	return out
}

// newReclaimDMOverCache wires a mock-engine distManager around a specific
// segmentL2Cache (the instrumented seam) so reclaimMerged can be driven directly
// with a synthetic MergeResult while faults are injected at the cache boundary.
func newReclaimDMOverCache(t *testing.T, cache segmentL2Cache) *distManager[mockQuery, mockStats] {
	t.Helper()
	target := graphSelector(kgtypes.GraphCode, "crash")
	return newDistManager(newMockEngine(t), cache, target, "")
}

// TestReclaimCrashSafety proves the Put-before-Remove crash ordering across the
// three reachable crash points, asserting the corpus is reconstructable in every
// case and that no reachable on-disk state has NEITHER the merged blob nor its
// constituents.
func TestReclaimCrashSafety(t *testing.T) {
	t.Parallel()

	constituents, merged := realMergeBlobs(t)
	docs := vecContentDocs(2)
	removed := []searchengine.SegmentID{constituents[0].ID, constituents[1].ID}

	seedConstituents := func(real *diskSegmentCache) {
		for _, b := range constituents {
			real.Put(b.ID, b.Bytes)
		}
	}

	// --- Scenario 1: no crash. Put precedes every Remove; merged present,
	// constituents reclaimed; reload yields the full corpus (from the merged blob).
	t.Run("clean_order_and_reclaim", func(t *testing.T) {
		dir := t.TempDir()
		real := newDiskSegmentCache(dir, 0, adviceRandom)
		seedConstituents(real)
		ic := newInstrumentedCache(real)
		dm := newReclaimDMOverCache(t, ic)

		dm.reclaimMerged(searchengine.MergeResult{Removed: removed, Merged: merged})

		putIdx := ic.firstIndex("put", merged.ID)
		require.GreaterOrEqual(t, putIdx, 0)
		for i, op := range ic.opLog() {
			if op.kind == "remove" {
				require.Greater(t, i, putIdx, "Put-op index < every Remove-op index")
			}
		}
		_, ok := real.Get(merged.ID)
		require.True(t, ok, "merged present after clean reclaim")
		for _, id := range removed {
			_, ok := real.Get(id)
			require.False(t, ok, "constituent reclaimed after clean reclaim")
		}
		require.Equal(t, idSet2(docs), reloadCorpusFromDir(t, dir, docs), "full corpus reloads from the merged blob")
	})

	// --- Scenario 2: crash BETWEEN Put and Remove (merged landed, constituents not
	// yet deleted). Reload sees the union (superset) → full corpus.
	t.Run("crash_between_put_and_remove", func(t *testing.T) {
		dir := t.TempDir()
		real := newDiskSegmentCache(dir, 0, adviceRandom)
		seedConstituents(real)
		ic := newInstrumentedCache(real)
		ic.blockRemove = true // Put lands; Removes are no-ops (crash before disk delete)
		dm := newReclaimDMOverCache(t, ic)

		dm.reclaimMerged(searchengine.MergeResult{Removed: removed, Merged: merged})

		_, mergedOK := real.Get(merged.ID)
		require.True(t, mergedOK, "merged blob persisted before the crash")
		// Constituents still present (the union is a superset, never a loss).
		require.Equal(t, idSet2(docs), reloadCorpusFromDir(t, dir, docs),
			"crash between Put and Remove still reloads the full corpus")
	})

	// --- Scenario 3: crash BEFORE Put (merged never persisted). Constituents are
	// untouched → reload reconstructs the full corpus from them.
	t.Run("crash_before_put", func(t *testing.T) {
		dir := t.TempDir()
		real := newDiskSegmentCache(dir, 0, adviceRandom)
		seedConstituents(real)
		ic := newInstrumentedCache(real)
		ic.blockPut = true    // merged Put is a no-op
		ic.blockRemove = true // and (defensively) so are the Removes — but the guard is the ordering
		dm := newReclaimDMOverCache(t, ic)

		dm.reclaimMerged(searchengine.MergeResult{Removed: removed, Merged: merged})

		_, mergedOK := real.Get(merged.ID)
		require.False(t, mergedOK, "merged blob NOT persisted (crash before Put)")
		require.Equal(t, idSet2(docs), reloadCorpusFromDir(t, dir, docs),
			"crash before Put leaves constituents intact → full corpus reloads")
	})

	// --- The structural guarantee: in NO scenario is the on-disk state {neither
	// merged nor constituents}. Scenarios 1-3 each reload the full corpus, which is
	// the operational proof of that property.
}

// idSet2 is the expected live id set for the 2-doc crash corpus.
func idSet2(docs []searchengine.Document) map[searchengine.ExternalID]struct{} {
	return idSetExcept(docs, map[searchengine.ExternalID]struct{}{})
}
