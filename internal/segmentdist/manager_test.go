// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// buildManager wires a manager around a counting caller + mock engine + cache.
func buildManager(
	engine *searchengine.SegmentedIndex[mockQuery, mockStats],
	caller segmentCaller,
	target *knowledgev1.GraphSelector,
	cacheDir string,
) (*distManager[mockQuery, mockStats], *countingCaller) {
	cc := &countingCaller{inner: caller}
	src := newRPCSegmentSource(cc, target, context.Background())
	cache := newDiskSegmentCache(cacheDir, 0)
	return newDistManager(engine, src, cache, target, ""), cc
}

// TestManagerShipDiffIdempotent verifies that after an initial ship() of N
// segments, a second ship() with no intervening Add sends ZERO blobs (empty
// diff), the server stamps ZERO new generations, and ZERO new RPCs fire.
func TestManagerShipDiffIdempotent(t *testing.T) {
	svc, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "shipdiff"}
	ctx := context.Background()

	eng := newMockEngine()
	require.NoError(t, eng.Add([]searchengine.Document{doc("d1", "alpha")}))
	require.NoError(t, eng.Add([]searchengine.Document{doc("d2", "beta")}))

	mgr, cc := buildManager(eng, gc, target, t.TempDir())

	_, err := mgr.ship(ctx)
	require.NoError(t, err)
	firstShips := cc.shipCalls.Load()
	firstBlobs := cc.shipBlobs.Load()
	require.Equal(t, int64(1), firstShips, "first ship issues one batched Ship RPC")
	require.Equal(t, int64(2), firstBlobs, "first ship sends both segments")

	// Server now holds 2 segments with generations 1,2.
	svc.mu.Lock()
	genAfterFirst := svc.gen
	svc.mu.Unlock()
	require.Equal(t, uint64(2), genAfterFirst)

	// Second ship — no intervening Add → empty diff → ZERO Ship RPC, ZERO blobs.
	_, err = mgr.ship(ctx)
	require.NoError(t, err)
	require.Equal(t, firstShips, cc.shipCalls.Load(), "second ship must issue ZERO new Ship RPCs")
	require.Equal(t, firstBlobs, cc.shipBlobs.Load(), "second ship must send ZERO new blobs")

	svc.mu.Lock()
	genAfterSecond := svc.gen
	svc.mu.Unlock()
	require.Equal(t, genAfterFirst, genAfterSecond, "second ship must stamp ZERO new generations")
}

// TestManagerShipWarmsCacheAndGen verifies ship() warms the L2 cache with each
// shipped blob and advances last-seen generation to the max stamped generation.
func TestManagerShipWarmsCacheAndGen(t *testing.T) {
	_, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "warmcache"}
	ctx := context.Background()

	eng := newMockEngine()
	require.NoError(t, eng.Add([]searchengine.Document{doc("d1", "alpha")}))
	require.NoError(t, eng.Add([]searchengine.Document{doc("d2", "beta")}))

	mgr, _ := buildManager(eng, gc, target, t.TempDir())
	_, err := mgr.ship(ctx)
	require.NoError(t, err)

	// Each shipped segment is in the L2 cache.
	exported := eng.Export()
	require.Len(t, exported, 2)
	for _, b := range exported {
		_, ok := mgr.cache.Get(b.ID)
		require.True(t, ok, "shipped blob %s must be warm in L2 cache", b.ID)
	}
	require.Equal(t, uint64(2), mgr.lastSeenGen.Load(), "lastSeenGen advances to max stamped generation")
}

// TestManagerLoadDeltaCacheAndImport verifies: a fresh load() Lists 3 metas,
// Fetches all 3 cold, Imports, search hits; a second load() at advanced gen Lists
// an empty delta and issues ZERO Fetch; a load() with 2 of 3 cached issues ONE
// Fetch for the 1 miss.
func TestManagerLoadDeltaCacheAndImport(t *testing.T) {
	svc, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "loaddelta"}
	ctx := context.Background()

	// Shipper engine ships 3 segments to the server.
	shipEng := newMockEngine()
	require.NoError(t, shipEng.Add([]searchengine.Document{doc("d1", "alpha")}))
	require.NoError(t, shipEng.Add([]searchengine.Document{doc("d2", "alpha beta")}))
	require.NoError(t, shipEng.Add([]searchengine.Document{doc("d3", "gamma")}))
	shipMgr, _ := buildManager(shipEng, gc, target, t.TempDir())
	_, err := shipMgr.ship(ctx)
	require.NoError(t, err)

	// Loader engine (distinct cache dir) loads cold.
	loadEng := newMockEngine()
	loadMgr, loadCC := buildManager(loadEng, gc, target, t.TempDir())

	require.NoError(t, loadMgr.load(ctx))
	require.Equal(t, int64(1), loadCC.fetchCalls.Load(), "cold load issues one batched Fetch for all 3 misses")
	hits := loadEng.Search(mockQuery{term: "alpha"}, 10)
	require.Len(t, hits, 2, "search must hit the imported segments (d1, d2)")
	require.Equal(t, uint64(3), loadMgr.lastSeenGen.Load())

	// Second load at advanced gen → empty delta → ZERO Fetch.
	beforeSecond := loadCC.fetchCalls.Load()
	require.NoError(t, loadMgr.load(ctx))
	require.Equal(t, beforeSecond, loadCC.fetchCalls.Load(), "second load must issue ZERO Fetch (empty delta)")

	// Ship a 4th segment; a fresh loader with 3 of 4 already cached issues ONE
	// Fetch for the 1 miss. Pre-seed the new loader's cache with the first 3.
	require.NoError(t, shipEng.Add([]searchengine.Document{doc("d4", "delta")}))
	_, err = shipMgr.ship(ctx)
	require.NoError(t, err)
	svc.mu.Lock()
	require.Equal(t, uint64(4), svc.gen)
	svc.mu.Unlock()

	partialEng := newMockEngine()
	partialMgr, partialCC := buildManager(partialEng, gc, target, t.TempDir())
	// Warm the partial cache with 3 of the 4 server segments so only one is a
	// miss. The server orders segments by ascending generation; cache the 3
	// lowest-generation ids (everything except the newest, d4's segment).
	delta, err := partialMgr.source.List(ctx, 0)
	require.NoError(t, err)
	require.Len(t, delta, 4)
	prime, err := partialMgr.source.Fetch([]searchengine.SegmentID{delta[0].ID, delta[1].ID, delta[2].ID})
	require.NoError(t, err)
	require.Len(t, prime, 3)
	for _, b := range prime {
		partialMgr.cache.Put(b.ID, b.Bytes)
	}
	// Reset the Fetch counter so the assertion below counts only the load()'s Fetch.
	partialCC.fetchCalls.Store(0)
	require.NoError(t, partialMgr.load(ctx))
	require.Equal(t, int64(1), partialCC.fetchCalls.Load(), "partial load issues ONE Fetch for the single miss")
}

// TestManagerUnloadReloadFromL2 verifies: load N segments; unloadUnderPressure
// drops resident bytes below target via engine.Unload and search drops the
// unloaded hits; reload(unloadedIds) restores from L2 (ZERO Source.Fetch) and
// search hits return.
func TestManagerUnloadReloadFromL2(t *testing.T) {
	_, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "unloadreload"}
	ctx := context.Background()

	shipEng := newMockEngine()
	require.NoError(t, shipEng.Add([]searchengine.Document{doc("d1", "alpha")}))
	require.NoError(t, shipEng.Add([]searchengine.Document{doc("d2", "alpha")}))
	require.NoError(t, shipEng.Add([]searchengine.Document{doc("d3", "alpha")}))
	shipMgr, _ := buildManager(shipEng, gc, target, t.TempDir())
	_, err := shipMgr.ship(ctx)
	require.NoError(t, err)

	loadEng := newMockEngine()
	loadMgr, loadCC := buildManager(loadEng, gc, target, t.TempDir())
	require.NoError(t, loadMgr.load(ctx))
	require.Len(t, loadEng.Search(mockQuery{term: "alpha"}, 10), 3)

	fetchAfterLoad := loadCC.fetchCalls.Load()

	// Unload under pressure: target 0 bytes evicts everything.
	unloaded := loadMgr.unloadUnderPressure(0)
	require.NotEmpty(t, unloaded, "must unload at least one segment under target 0")
	require.Less(t, len(loadEng.Search(mockQuery{term: "alpha"}, 10)), 3,
		"search must drop the unloaded hits")

	// Reload from L2 — the bytes are cached, so ZERO Source.Fetch.
	require.NoError(t, loadMgr.reload(unloaded))
	require.Equal(t, fetchAfterLoad, loadCC.fetchCalls.Load(),
		"reload must restore from L2 with ZERO Source.Fetch")
	require.Len(t, loadEng.Search(mockQuery{term: "alpha"}, 10), 3,
		"search hits must return after reload")
}
