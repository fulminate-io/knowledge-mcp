// SPDX-License-Identifier: Apache-2.0

package segmentdist

// manager_test.go holds the shared distManager test constructors plus the
// unload/reload-from-L2 unit.
//
// THREE TESTS WERE DELETED HERE, each with its successor named rather than assumed:
//   - TestManagerShipDiffIdempotent asserted that a second ship() of an unchanged
//     corpus sent zero blobs and stamped zero server generations. Ship and the
//     generation axis are gone. The surviving half of that property — writing the
//     same content-hash blobs twice adds nothing, because the cache is
//     content-addressed — is asserted in TestSegmentDistributionE2E.
//   - TestManagerShipWarmsCacheAndGen asserted ship warmed the L2 cache AND advanced
//     the shipped/imported generation cursors. The cache-warming half is now
//     writeNewBlobsToL2 and is covered by the same e2e hand-over; the cursor half has
//     no successor and needs none, because L2 has no generation axis to advance —
//     segment identity is the content hash and there is no ordering authority.
//   - TestManagerLoadDeltaCacheAndImport asserted a delta-pull from the server
//     populated the cache and imported into the engine. There is no delta and no
//     server; the L2 import it ultimately verified is covered by
//     TestSegmentDistributionE2E and by TestManagerUnloadReloadFromL2 below.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// buildManager wires a distManager around a mock engine + cache for one target.
//
// IT USED TO TAKE AND RETURN A SEGMENT SOURCE, rebinding the harness view to target
// so callers could read its per-leg call counters. There is no source and there are
// no legs to count: the manager reads its cache directly, so the cache dir is the
// whole wiring.
func buildManager(
	engine *searchengine.SegmentedIndex[mockQuery, mockStats],
	target *knowledgev1.GraphSelector,
	cacheDir string,
) *distManager[mockQuery, mockStats] {
	cache := newDiskSegmentCache(cacheDir, 0, adviceRandom)
	return newDistManager(engine, cache, target, "")
}

// evictAllResidentForTest drops every resident segment out of the engine and the
// resident-tracking map, returning the ids it dropped. It lets a test construct the
// "gone from the engine, still on disk" state that the prune, force-load and
// reload-from-L2 paths have to survive. Note the resident map is populated by
// load()/reload() (recordResident), NOT by an engine Add, so a test must load before
// it can evict.
//
// TEST SCAFFOLDING ONLY — production has no eviction path at all. The unprotected
// unloadUnderPressure this replaces was retired on 2026-08-02: it CAS-removed
// member-bearing segments without asking whether their members had another searchable
// home, so wiring it to a real memory-pressure path would have silently dropped live
// documents out of the serving corpus. If pressure-eviction is ever genuinely needed
// it gets built with the LiveMembersOutside coverage gate the drain path in
// manager_bucket_backlog.go uses — retaining any tail whose members are not proven
// covered elsewhere — rather than by promoting this helper into production.
func (m *distManager[Q, S]) evictAllResidentForTest() []searchengine.SegmentID {
	m.resMu.Lock()
	defer m.resMu.Unlock()

	ids := make([]searchengine.SegmentID, 0, len(m.resident))
	for id := range m.resident {
		ids = append(ids, id)
	}
	m.engine.Unload(ids)
	clear(m.resident)
	return ids
}

// TestManagerUnloadReloadFromL2 is the focused unit for re-materialization: load N
// segments, evict them so search loses the hits, then reload the evicted ids from L2
// and get exactly those hits back.
//
// IT IS NOT REDUNDANT WITH THE E2E TEST. That one proves the whole path hands over
// across two processes; this one isolates the evict/reload leg so a regression in
// re-materialization reports here with a small blast radius rather than only as a
// failure somewhere in a six-stage flow.
//
// THE TWO MANAGERS SHARE ONE CACHE DIRECTORY, and that is a change from the original.
// They used to point at separate directories because the SERVER was the medium
// between them; with the cache as the only store, separate directories would mean the
// loader had nothing to load and the test would assert against an empty engine.
func TestManagerUnloadReloadFromL2(t *testing.T) {
	t.Parallel()

	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "unloadreload"}
	ctx := context.Background()
	cacheDir := t.TempDir()

	writeEng := newMockEngine(t)
	require.NoError(t, writeEng.Add([]searchengine.Document{doc("d1", "alpha")}))
	require.NoError(t, writeEng.Add([]searchengine.Document{doc("d2", "alpha")}))
	require.NoError(t, writeEng.Add([]searchengine.Document{doc("d3", "alpha")}))
	writeMgr := buildManager(writeEng, target, cacheDir)
	require.NoError(t, writeMgr.writeNewBlobsToL2(writeEng.Export()))

	loadEng := newMockEngine(t)
	loadMgr := buildManager(loadEng, target, cacheDir)
	require.NoError(t, loadMgr.load(ctx))
	require.Len(t, loadEng.Search(mockQuery{term: "alpha"}, 10), 3,
		"fixture: all three segments must be resident before eviction, or the reload proves nothing")

	// Evict the whole resident set.
	unloaded := loadMgr.evictAllResidentForTest()
	require.NotEmpty(t, unloaded, "must unload at least one segment")
	require.Less(t, len(loadEng.Search(mockQuery{term: "alpha"}, 10)), 3,
		"search must drop the unloaded hits")

	// Reload the evicted ids from L2. STRICT (tolerateMisses=false): an id absent
	// from the cache must ERROR rather than yield a short set, because the cache is
	// the only place it could have been recovered from.
	require.NoError(t, loadMgr.reload(unloaded, false))
	require.Len(t, loadEng.Search(mockQuery{term: "alpha"}, 10), 3,
		"search hits must return after reload")
}
