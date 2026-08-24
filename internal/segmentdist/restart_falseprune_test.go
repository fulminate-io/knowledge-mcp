// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestRestartShipDoesNotPruneFullCorpus is the load-bearing restart proof. It
// drives the REAL embed write + tick path, so the per-caller-role
// choice (locallyShipped vs shippedIDs) is exactly the production code under test.
//
// Mechanic: process 1 ships a multi-segment HNSW corpus to the server. Process 2
// is a RESTART — a FRESH Manager pointed at the SAME server, whose per-graph
// shippedIDs seeds from the full server List(0) but whose locallyShipped is EMPTY
// (it has shipped nothing yet this run). Process 2 does a SINGLE write plus its
// tick BEFORE any Search/VectorByID load() of the prior corpus.
//
// On HEAD (the embed ship reconciled against shippedIDs) this fresh ship computes
// pruneSet = {entire prior corpus} − {the one new tail segment} = the whole prior
// corpus, and issues a Prune RPC that collapses the server to tail-only — the
// silent search collapse. Post-fix (the embed ship reconciles against
// locallyShipped, which holds ONLY what this process shipped) pruneSet is empty:
// ZERO Prune RPCs and the prior corpus stays intact alongside the new tail.
// REVERTING that role choice back to shippedIDs reproduces the HEAD collapse (the
// observed RED that motivates the fix).
func TestRestartShipDoesNotPruneFullCorpus(t *testing.T) {
	t.Parallel()

	svc, gc := newSegmentHarness(t)
	ctx := context.Background()

	// Process 1: ship a multi-segment HNSW corpus via the real embed path. Each
	// batch of MinSegmentDocs (searchCorpusN) vectors is force-sealed, and the tick
	// re-emits the accumulated corpus into partitions and ships them.
	const corpusSegs = 3
	p1 := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	for b := range corpusSegs {
		batch := hnswVecDocs(searchCorpusN)
		for i := range batch {
			batch[i].ID = fmt.Sprintf("p1b%d-%s", b, batch[i].ID)
		}
		require.NoError(t, p1.AddAndMarkDirty(ctx, kgtypes.GraphCode, "restartRepo", batch))
	}
	require.NoError(t, p1.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "restartRepo"))
	priorCorpus := shippedHNSWIDs(svc)
	require.NotEmpty(t, priorCorpus, "process 1 ships a multi-segment corpus to the server")

	// Process 2: RESTART. Fresh Manager against the SAME server. A fresh per-graph
	// distManager has locallyShipped EMPTY; ensureShippedSeeded seeds shippedIDs
	// from the full server List(0) on the first ship.
	cc2 := gc.server.viewFor(&knowledgev1.GraphSelector{}, "")
	p2 := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(cc2)))

	// A SINGLE write plus its tick, BEFORE any Search/VectorByID load().
	tail := hnswVecDocs(searchCorpusN)
	for i := range tail {
		tail[i].ID = fmt.Sprintf("tail-%s", tail[i].ID)
	}
	require.NoError(t, p2.AddAndMarkDirty(ctx, kgtypes.GraphCode, "restartRepo", tail))
	require.NoError(t, p2.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "restartRepo"))

	// Post-fix: the embed (ROLE-B) ship reconciles against locallyShipped (= just
	// what this process shipped), so it prunes NOTHING — zero Prune RPCs.
	require.Equal(t, int64(0), cc2.pruneCalls.Load(),
		"the restart ship issues ZERO Prune RPCs (HEAD pruned the whole corpus here)")

	// The server still holds the full prior corpus PLUS the new tail — the corpus
	// did NOT collapse to tail-only.
	now := shippedHNSWIDs(svc)
	require.Greater(t, len(now), len(priorCorpus),
		"server retains the full prior corpus + the new tail (no false-prune collapse)")
	for id := range priorCorpus {
		require.Contains(t, now, id, "every prior-corpus segment survives the restart ship")
	}
}

// TestRebuildReplacesDegeneratePoolPrunesOld pins the ROLE-A corpus-replacement
// contract that a locallyShipped-only mechanic would have BROKEN: the
// deterministic rebuild (FinalizeRebuild) MUST prune the OLD degenerate corpus
// it supersedes, even on a fresh process whose locallyShipped is empty.
//
// Process 1 ships a degenerate old HNSW corpus via the embed path. Process 2 is a
// fresh Manager (restart) that runs the deterministic rebuild of the SAME graph,
// producing a byte-different deterministic corpus. The rebuild ship reconciles
// against shippedIDs (seeded from the server = the old corpus), so it prunes the
// old degenerate ids: >=1 Prune RPC fires and the old ids are gone server-side.
//
// Under a locallyShipped-only mechanic process 2's locallyShipped would be empty,
// so the rebuild would ship the new corpus ALONGSIDE the stale old one and never
// prune it — the regression the first plan draft would have shipped. This test is
// GREEN only because FinalizeRebuild passes shippedIDs (ROLE A), not
// locallyShipped.
func TestRebuildReplacesDegeneratePoolPrunesOld(t *testing.T) {
	t.Parallel()

	svc, gc := newSegmentHarness(t)
	ctx := context.Background()

	// Process 1: ship a degenerate old corpus via the embed HNSW path. One sealed
	// segment of MinSegmentDocs vectors is the "old pool" the rebuild will replace.
	oldDocs := hnswVecDocs(searchCorpusN)
	p1 := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	require.NoError(t, p1.AddAndMarkDirty(ctx, kgtypes.GraphCode, "rebuildRepo", oldDocs))
	require.NoError(t, p1.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "rebuildRepo"))
	oldIDs := shippedHNSWIDs(svc)
	require.NotEmpty(t, oldIDs, "process 1 ships at least one old HNSW segment")

	// Process 2: RESTART. Fresh Manager runs the DETERMINISTIC rebuild of the SAME
	// graph with a DIFFERENT corpus, producing byte-different deterministic ids.
	rebuildDocs := hnswVecDocs(searchCorpusN + 32)
	p2 := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	require.NoError(t, p2.StageRebuildPartition(ctx, kgtypes.GraphCode, "rebuildRepo", rebuildDocs, nil))
	res, err := p2.FinalizeRebuild(ctx, kgtypes.GraphCode, "rebuildRepo")
	require.NoError(t, err)
	pruned := res.HNSWSuperseded

	// The rebuild (ROLE A) MUST have pruned the old degenerate corpus.
	require.NotEmpty(t, pruned, "the deterministic rebuild prunes the old degenerate corpus ids")
	nowIDs := shippedHNSWIDs(svc)
	for oldID := range oldIDs {
		require.NotContains(t, nowIDs, oldID,
			"every old degenerate HNSW segment id is pruned after the deterministic rebuild")
	}
	require.NotEmpty(t, nowIDs, "the rebuilt corpus is present on the server")
}

// TestLegitimateMergePruneStillWorks proves the per-role split did NOT disable the
// legitimate embed-path prune: a real seal+merge cycle on ONE manager (no restart)
// still prunes the merged-away constituents, because they are this-process-shipped
// ids in locallyShipped.
func TestLegitimateMergePruneStillWorks(t *testing.T) {
	t.Parallel()

	svc, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "mergeRepo"}
	ctx := context.Background()

	// MinSegmentDocs=1 → each Add seals one segment; SegmentCountTarget=4 → the
	// background merger consolidates once more than 4 accumulate.
	eng := closeOnCleanup(t, searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: 4,
	}))
	defer eng.Close()
	mgr, cc := buildManager(eng, gc, target, t.TempDir())

	const n = 8
	for i := range n {
		require.NoError(t, eng.Add([]searchengine.Document{
			doc(fmt.Sprintf("m%d", i), fmt.Sprintf("merge body %d", i)),
		}))
		_, err := mgr.ship(ctx, mgr.locallyShipped)
		require.NoError(t, err)
	}

	// Force-seal the tail, then WAIT for the merger to actually consolidate before
	// reconciling. The prune under test can only fire once a shipped id has LEFT
	// Export() (reconcilePrune diffs `against` minus the exported set), and an id
	// only leaves Export when the background merger publishes the consolidated
	// segment — a separate goroutine woken by a 50ms ticker or a non-blocking write
	// signal (searchengine/merge.go startMerger). Shipping without this wait races
	// the merger: on a busy 2-core runner the merge lands after the ship and the
	// prune count is legitimately 0. Production has no such race — its ship is
	// tick-driven, so a merge that lands late is reconciled by the next tick.
	require.NoError(t, eng.Flush())
	require.Eventually(t, func() bool { return eng.MergeCount() > 0 },
		30*time.Second, 2*time.Millisecond,
		"the background merger consolidates the >SegmentCountTarget accumulation")

	// The reconciling ship: the consolidated blob lands and the merged-away
	// constituents — this-process-shipped ids in locallyShipped — are pruned.
	_, err := mgr.ship(ctx, mgr.locallyShipped)
	require.NoError(t, err)

	require.GreaterOrEqual(t, cc.pruneCalls.Load(), int64(1),
		"a real seal+merge cycle still prunes the merged-away constituents (>=1 Prune RPC)")
	require.Less(t, serverSegCount(t, svc, target), n,
		"the server segment count is bounded below the pre-merge accumulation")
}

// TestPostLoadCorpusMergeLeakIsBoundedThenRebuildPrunes pins the ACCEPTED +
// DOCUMENTED ROLE-B leak as a contract, not a silent leak: after a load() pulls
// the prior corpus into the SAME embed engine and an in-process merge consolidates
// corpus segments whose ids are NOT in locallyShipped, the embed (ROLE-B) ship
// does NOT prune those merged-away corpus ids (the documented leak). A later
// deterministic rebuild (ROLE A) DOES prune them.
//
// The leak is bounded: it is reclaimed by the next ROLE-A rebuild OR coverage heal
// (lever 2); across restart cycles the stale set is bounded by the HEAL CADENCE,
// not a single rebuild event.
func TestPostLoadCorpusMergeLeakIsBoundedThenRebuildPrunes(t *testing.T) {
	t.Parallel()

	svc, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "leakRepo"}
	ctx := context.Background()

	// Process 1 ships a multi-segment corpus.
	const corpusN = 6
	p1Eng := closeOnCleanup(t, searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{MinSegmentDocs: 1}))
	defer p1Eng.Close()
	p1Mgr, _ := buildManager(p1Eng, gc, target, t.TempDir())
	for i := range corpusN {
		require.NoError(t, p1Eng.Add([]searchengine.Document{
			doc(fmt.Sprintf("c%d", i), fmt.Sprintf("corpus body %d", i)),
		}))
	}
	_, err := p1Mgr.ship(ctx, p1Mgr.locallyShipped)
	require.NoError(t, err)
	require.Equal(t, corpusN, serverSegCount(t, svc, target))

	// Process 2 (restart): fresh engine that MERGES on load. SegmentCountTarget=2
	// so loading the >2 corpus segments triggers the in-process merger to
	// consolidate corpus ids the fresh process never shipped (not in locallyShipped).
	p2Eng := closeOnCleanup(t, searchengine.New[mockQuery, mockStats](mockFormat{}, searchengine.Options{
		MinSegmentDocs:     1,
		SegmentCountTarget: 2,
	}))
	defer p2Eng.Close()
	p2Mgr, cc2 := buildManager(p2Eng, gc, target, t.TempDir())

	// load() the prior corpus into THIS engine (what a Search/VectorByID does),
	// then force-seal so the in-process merger consolidates the loaded corpus
	// segments — whose ids the fresh process never shipped (not in locallyShipped).
	require.NoError(t, p2Mgr.load(ctx))
	require.Eventually(t, func() bool { return p2Eng.MergeCount() > 0 },
		2*time.Second, 2*time.Millisecond,
		"loading >SegmentCountTarget corpus segments fires the in-process merger")

	// An embed (ROLE-B) ship after the post-load merge: it reconciles against
	// locallyShipped (EMPTY this run), so it does NOT prune the merged-away corpus
	// ids — the DOCUMENTED leak. The stale constituents remain on the server.
	beforePrune := cc2.pruneCalls.Load()
	_, err = p2Mgr.ship(ctx, p2Mgr.locallyShipped)
	require.NoError(t, err)
	require.Equal(t, beforePrune, cc2.pruneCalls.Load(),
		"the embed ROLE-B ship does NOT prune the post-load merged-away corpus ids (documented leak)")
	require.GreaterOrEqual(t, serverSegCount(t, svc, target), corpusN,
		"the merged-away corpus constituents leak (remain on the server) under the embed ship")

	// A later ROLE-A replace-prune (the same shape FinalizeRebuild uses: ship
	// reconciling against shippedIDs — the full server set) reclaims the leak: it
	// prunes the stale constituents the in-process merge superseded, because the
	// consolidated Export no longer contains them. Same engine, same graph, same
	// format — exactly the rebuild's authoritative corpus-replacement role.
	rolePruned, err := p2Mgr.ship(ctx, p2Mgr.shippedIDs)
	require.NoError(t, err)
	require.NotEmpty(t, rolePruned,
		"the ROLE-A replace-prune reclaims the leaked post-load merged-away corpus ids")
	require.Equal(t, len(p2Eng.Export()), serverSegCount(t, svc, target),
		"after the ROLE-A reclaim the server matches the consolidated live set — leak reclaimed")
}

// TestRestartLoadImportsFullCorpusAfterShip is the read-side restart proof: a
// cold process must import the FULL stored corpus on its first load(),
// even though an embed-writeback ship ran first and advanced the (server-stamped)
// generation past the stored corpus. It exercises BOTH coupled fixes at once:
//
//   - the cursor split: shipNew advances shippedGen, NOT the load floor
//     (importedGen). On HEAD (one shared cursor) the fresh tail ship pushed the
//     shared cursor to the server gen N, so the first load()'s List(N) returned an
//     empty delta and imported NOTHING — the engine served the tail only (Export
//     len ~1). With the split, importedGen stays 0, so load() Lists(0) and imports
//     the whole corpus.
//   - the Import dedup: load()'s List(0) RE-lists this process's own just-shipped
//     tail T (gen N>0) alongside the prior corpus. Without the publishImport
//     segment-ID dedup, Import would APPEND T a second time (T already resident
//     from this process's own seal) → one more Export entry than the prior corpus
//     plus T, and T's docIDs duplicated across two result slots (mergeTopK does NOT
//     dedup docIDs). With the dedup, T is dropped on re-import → Export is EXACTLY
//     the prior corpus plus T, every docID once.
//
// The no-duplicate-docID assertion is the load-bearing one: the regression that
// bites users is duplicated/inflated hits, not merely a wrong count.
func TestRestartLoadImportsFullCorpusAfterShip(t *testing.T) {
	t.Parallel()

	svc, gc := newSegmentHarness(t)
	ctx := context.Background()

	// Process 1: ship a multi-segment HNSW corpus via the real embed path. Each
	// batch of searchCorpusN (== MinSegmentDocs) vectors is force-sealed, and the
	// tick re-emits the accumulated corpus into partitions and ships them.
	const corpusSegs = 3
	p1 := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	for b := range corpusSegs {
		batch := hnswVecDocs(searchCorpusN)
		for i := range batch {
			batch[i].ID = fmt.Sprintf("p1b%d-%s", b, batch[i].ID)
		}
		require.NoError(t, p1.AddAndMarkDirty(ctx, kgtypes.GraphCode, "restartLoadRepo", batch))
	}
	require.NoError(t, p1.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "restartLoadRepo"))
	priorCorpus := shippedHNSWIDs(svc)
	require.NotEmpty(t, priorCorpus, "process 1 ships a multi-segment corpus to the server")

	// Process 2: RESTART. Fresh Manager against the SAME server. A SINGLE write plus
	// its tick — this seals T into the engine (resident once) and shipNew advances
	// shippedGen to the server-stamped gen N, leaving importedGen at 0 (the cursor
	// split: ship does NOT poison the load floor). Half a threshold keeps T inside a
	// SINGLE partition so the double-import check below counts one tail, not several.
	p2 := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))
	tail := hnswVecDocs(searchCorpusN / 2)
	for i := range tail {
		tail[i].ID = fmt.Sprintf("tail-%s", tail[i].ID)
	}
	require.NoError(t, p2.AddAndMarkDirty(ctx, kgtypes.GraphCode, "restartLoadRepo", tail))
	require.NoError(t, p2.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, "restartLoadRepo"))

	dm := p2.managerFor(kgtypes.GraphCode, "restartLoadRepo")
	require.Equal(t, uint64(0), dm.importedGen.Load(),
		"the ship must NOT have advanced the load floor (importedGen): the cursor is split")
	require.Positive(t, dm.shippedGen.Load(),
		"the ship advanced shippedGen (server-stamped tail generation)")
	require.Len(t, dm.engine.Export(), 1, "before load() the fresh engine holds only its own tail T")

	// The discriminating SERVER import: on HEAD List(sharedCursor==N) → empty delta →
	// engine stays tail-only (Export len 1). With the cursor split List(0) imports
	// the prior corpus; with the Import dedup the re-listed tail T is dropped. Drive
	// loadFromServer directly: load() is now L2-FIRST, and this process's L2 (a fresh
	// tempdir) holds ONLY its just-shipped tail, not the prior server corpus — so a
	// plain load() would import the tail from L2 and never List the server (by
	// design). The cursor-split + Import-dedup invariants this test pins live in the
	// cold-L2 server-import path (loadFromServer), which is exactly what a
	// genuinely-cold restart (empty L2) or the background reconcile drives.
	require.NoError(t, dm.loadFromServer(ctx))

	// EXACTLY the prior corpus plus the one tail T — NOT one more (T double-imported).
	// On un-split HEAD this is ~1 (tail-only).
	require.Len(t, dm.engine.Export(), len(priorCorpus)+1,
		"cold load imports the full corpus + tail exactly once (no double-import of the just-shipped tail)")

	// Corpus-wide search: WITHIN any single result set every docID appears in AT
	// MOST one slot. A doc resident in two segments (no Import dedup) would occupy
	// two of the k slots OF THE SAME query. Query with each tail doc's own vector
	// (exact-match NN) at large k; the duplicate check is per-query (accumulating
	// across queries would legitimately recount a doc that ranks in several
	// distinct queries' top-k). The tail docs are the ones at risk of double-import
	// (re-listed by load() after this process's own seal), so each tail vector's own
	// search is the discriminating probe.
	anyTailHit := false
	for _, d := range tail {
		hits := dm.engine.Search(d.Vector, 64)
		perQuery := make(map[string]int, len(hits))
		for _, h := range hits {
			perQuery[h.ID]++
		}
		for id, n := range perQuery {
			require.LessOrEqualf(t, n, 1,
				"docID %s appears in %d slots of ONE query result — duplicate hit (no Import dedup)", id, n)
		}
		if _, ok := perQuery[d.ID]; ok {
			anyTailHit = true
		}
	}
	// Sanity: the tail docs are actually retrievable (the corpus is loaded,
	// searchable, and the tail survived dedup as a single resident copy).
	require.True(t, anyTailHit, "the tail docs are searchable after the cold load")
}

// shippedHNSWIDs returns the set of HNSW-format segment ids the shared server holds.
func shippedHNSWIDs(svc *sharedServerFake) map[string]struct{} {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	ids := map[string]struct{}{}
	for _, blobs := range svc.byKey {
		for _, b := range blobs {
			if b.GetFormat() == hnsw.New().Name() {
				ids[b.GetId()] = struct{}{}
			}
		}
	}
	return ids
}
