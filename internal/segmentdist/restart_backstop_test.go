// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// noMergeEngine builds a mock engine that seals one segment per Add (MinSegmentDocs
// =1) but NEVER background-merges (SegmentCountTarget huge) — so n Adds stay n
// distinct sealed segments and the server holds n single-doc blobs. The corpus must
// stay un-merged so a degenerate manager's recovery imports a real multi-segment
// corpus, not one consolidated blob.
func noMergeEngine() *searchengine.SegmentedIndex[mockQuery, mockStats] {
	return searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: 1 << 30,
	})
}

// shipCorpus seals + ships n single-doc mock segments to the server under target
// via a throwaway ship-only manager. Each mock doc carries DocCount=1, so the
// server's shipped doc total == n. The ids are prefixed so a later degenerate
// manager's own tail ids never collide with the corpus.
func shipCorpus(t *testing.T, gc *fakeSegmentSource, target *knowledgev1.GraphSelector, n int) {
	t.Helper()
	ctx := context.Background()
	eng := noMergeEngine()
	defer eng.Close()
	for i := range n {
		require.NoError(t, eng.Add([]searchengine.Document{
			doc(fmt.Sprintf("corpus-%d", i), fmt.Sprintf("corpus body %d", i)),
		}))
	}
	require.Equal(t, n, eng.ResidentDocCount(), "the corpus engine sealed all n docs without merging")
	mgr, _ := buildManager(eng, gc, target, t.TempDir())
	_, err := mgr.ship(ctx, mgr.locallyShipped)
	require.NoError(t, err)
}

// degenerateManager builds a fresh manager that has REALLY sealed AND SHIPPED a
// small tail (tailN single-doc segments → resident == tailN, all searchable, AND
// present on the server) but whose load floor (importedGen) is POISONED past the
// server's corpus — the exact read-side degeneracy state: a cold process whose tail
// ship advanced a shared cursor so the lazy load() Lists an empty delta and never
// imports the stored corpus. A plain load() here is a no-op (List(poison) → empty);
// only the backstop recovers it. Because the tail is ALSO on the server, the
// recovery's List(0) RE-lists it — so the Import dedup is exercised: without it the
// already-resident tail would be double-imported (the no-duplicate-docID witness).
func degenerateManager(
	t *testing.T, gc *fakeSegmentSource, target *knowledgev1.GraphSelector, tailN int, poison uint64,
) (*distManager[mockQuery, mockStats], *fakeSegmentSource) {
	t.Helper()
	ctx := context.Background()
	eng := noMergeEngine()
	t.Cleanup(eng.Close)
	dm, cc := buildManager(eng, gc, target, t.TempDir())
	for i := range tailN {
		require.NoError(t, eng.Add([]searchengine.Document{
			doc(fmt.Sprintf("tail-%d", i), fmt.Sprintf("tail body %d", i)),
		}))
	}
	require.Equal(t, tailN, dm.engine.ResidentDocCount(), "the tail is REALLY sealed (resident == tailN)")
	// Ship the tail so it lands on the server too (and shippedGen advances) — the
	// authentic poisoned-cursor shape where the just-shipped tail is re-listed on a
	// cold load. ship() advances ONLY shippedGen; importedGen stays untouched.
	_, err := dm.ship(ctx, dm.locallyShipped)
	require.NoError(t, err)
	dm.importedGen.Store(poison) // poison the load floor past the corpus
	require.NoError(t, dm.load(ctx))
	require.Equal(t, tailN, dm.engine.ResidentDocCount(),
		"a plain load() over the poisoned floor recovers NOTHING (empty delta)")
	return dm, cc
}

// TestBackstopRecoversDegenerateEngine is the CASE degenerate: a real sealed tail
// far below the floor, a server corpus far above it, importedGen poisoned. The
// backstop resets the floor and re-imports the corpus exactly once.
func TestBackstopRecoversDegenerateEngine(t *testing.T) {
	t.Parallel()

	svc, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "backstopDegen"}
	ctx := context.Background()

	const corpusN = 128 // >> residentBackstopFloor(64)
	shipCorpus(t, gc, target, corpusN)
	require.Equal(t, corpusN, serverSegCount(t, svc, target))

	dm, cc := degenerateManager(t, gc, target, 2, uint64(corpusN+10))
	require.Less(t, dm.engine.ResidentDocCount(), residentBackstopFloor)

	beforeFetch := cc.fetchCalls.Load()
	require.NoError(t, dm.recoverIfDegenerate(ctx))

	// Recovered: the full corpus is now resident alongside the 2 tail docs.
	require.Equal(t, corpusN+2, dm.engine.ResidentDocCount(),
		"the backstop re-imported the full corpus (resident jumps to corpus + tail)")
	require.Greater(t, cc.fetchCalls.Load(), beforeFetch, "a recovery Fetch fired")

	// Corpus-wide search has no duplicated docID within any single query result.
	requireNoDuplicateDocIDs(t, dm)
}

// TestBackstopHealthyEngineNoReload is the CASE healthy: resident >= floor → the
// backstop returns immediately with ZERO List/Fetch (the floor gate).
func TestBackstopHealthyEngineNoReload(t *testing.T) {
	t.Parallel()

	svc, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "backstopHealthy"}
	ctx := context.Background()

	const corpusN = 128
	shipCorpus(t, gc, target, corpusN)

	// A fresh manager that loads the corpus normally → resident == corpusN (healthy).
	eng := newMockEngine()
	defer eng.Close()
	dm, cc := buildManager(eng, gc, target, t.TempDir())
	require.NoError(t, dm.load(ctx))
	require.GreaterOrEqual(t, dm.engine.ResidentDocCount(), residentBackstopFloor)

	beforeFetch := cc.fetchCalls.Load()
	require.NoError(t, dm.recoverIfDegenerate(ctx))
	require.Equal(t, beforeFetch, cc.fetchCalls.Load(),
		"a healthy engine (resident >= floor) issues ZERO extra Fetch — the floor gate returns early")
	_ = svc
}

// TestBackstopBelowFloorServerNoReload is the CASE below-floor: the in-memory
// engine is below the floor but so is the SERVER corpus (a legitimately tiny
// graph) → no forced reload (shipped < floor disarms the ratio).
func TestBackstopBelowFloorServerNoReload(t *testing.T) {
	t.Parallel()

	svc, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "backstopTiny"}
	ctx := context.Background()

	const corpusN = 4 // < residentBackstopFloor(64): a legitimately tiny graph
	shipCorpus(t, gc, target, corpusN)
	require.Equal(t, corpusN, serverSegCount(t, svc, target))

	dm, cc := degenerateManager(t, gc, target, 1, uint64(corpusN+10))
	require.Less(t, dm.engine.ResidentDocCount(), residentBackstopFloor)

	beforeFetch := cc.fetchCalls.Load()
	require.NoError(t, dm.recoverIfDegenerate(ctx))
	require.Equal(t, beforeFetch, cc.fetchCalls.Load(),
		"a below-floor SERVER corpus disarms the ratio — no forced reload")
	require.Equal(t, 1, dm.engine.ResidentDocCount(), "the engine is left unchanged (no recovery)")
}

// TestBackstopConcurrentSingleFlight is the CASE concurrent: K simultaneous
// recoverIfDegenerate calls on one degenerate engine converge it to the full
// corpus EXACTLY ONCE — the recovery does NOT issue K full re-imports. The
// recovering atomic.Bool CAS elects one winner; the rest skip. Run under -race.
func TestBackstopConcurrentSingleFlight(t *testing.T) {
	t.Parallel()

	svc, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "backstopConcurrent"}
	ctx := context.Background()

	const corpusN = 128
	shipCorpus(t, gc, target, corpusN)
	require.Equal(t, corpusN, serverSegCount(t, svc, target))

	dm, cc := degenerateManager(t, gc, target, 2, uint64(corpusN+10))

	const K = 16
	beforeFetch := cc.fetchCalls.Load()
	var wg sync.WaitGroup
	for range K {
		wg.Go(func() {
			require.NoError(t, dm.recoverIfDegenerate(ctx))
		})
	}
	wg.Wait()

	// Converged to full-corpus resident exactly once (no inflation: dedup + single
	// flight). corpusN + 2 tail, each docID once.
	require.Equal(t, corpusN+2, dm.engine.ResidentDocCount(),
		"K concurrent recoveries converge the engine to the full corpus exactly once (no inflation)")
	requireNoDuplicateDocIDs(t, dm)

	// Single-flight: the recovery Fetch RPCs do NOT scale with K. At most one
	// winner's load() fetches the corpus (others CAS-lose and skip); a generous
	// bound well below K proves RPCs did not fan out per goroutine.
	fetches := cc.fetchCalls.Load() - beforeFetch
	require.LessOrEqual(t, fetches, int64(2),
		"recovery Fetch RPCs are single-flighted — they do NOT scale with K (%d goroutines)", K)
	_ = svc
}

// requireNoDuplicateDocIDs searches the mock engine for every doc body token and
// asserts no docID appears in two slots of the SAME query result — the read-side
// witness that the Import dedup held through the recovery (a doc resident in two
// segments would duplicate in mergeTopK).
func requireNoDuplicateDocIDs(t *testing.T, dm *distManager[mockQuery, mockStats]) {
	t.Helper()
	// "body" is a shared token across every corpus + tail doc, so one query fans
	// across the whole resident set.
	hits := dm.engine.Search(mockQuery{term: "body"}, dm.engine.ResidentDocCount()+8)
	seen := make(map[string]int, len(hits))
	for _, h := range hits {
		seen[h.ID]++
	}
	for id, n := range seen {
		require.LessOrEqualf(t, n, 1, "docID %s appears in %d slots of one query — duplicate hit", id, n)
	}
}
