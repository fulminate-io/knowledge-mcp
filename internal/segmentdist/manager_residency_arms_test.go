// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// budgetManager builds a Manager over a fake source, ready for pools to be
// seeded into it. The budget pass reads the engine maps directly, so a Manager
// plus the pools seeded below is the whole surface it needs.
//
// IT TAKES NO BUDGET, and that is a consequence of the metering unit rather than
// a convenience. The budget is now compared against the engine's MODELED HEAP,
// whose magnitude is a property of the seeded fixture and not a number a caller
// can pick in advance — so every case seeds first, measures the per-pool heap,
// and assigns m.residencyBudgetBytes itself. Constructing with a ceiling high
// enough to evict nothing keeps the manager inert until that assignment lands.
func budgetManager(t *testing.T) *Manager {
	t.Helper()
	svc, _ := newSegmentHarness(t)
	return closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0,
		withSegmentSource(svc.viewFor(nil, "")),
		WithResidencyBudget(noEvictionCeiling)))
}

// noEvictionCeiling is a residency budget no fixture can reach, so a Manager is
// inert between construction and the case assigning its real budget.
const noEvictionCeiling = int64(1) << 40

const (
	// seededSegmentsPerPool is how many blobs seedResidentPool puts in each pool.
	// More than one so an eviction that dropped a single segment could not pass for
	// one that dropped the pool.
	seededSegmentsPerPool = 3
	// seededDocsPerSegment sizes each seeded segment. Small — these cases assert
	// ORDERING, not magnitude.
	seededDocsPerSegment = 4
)

// seededSegmentDocs builds the document set for one seeded segment. It is keyed
// only by the segment index, deliberately NOT by the pool name, so every pool
// seeded by seedResidentPool holds byte-identical segments and therefore an
// IDENTICAL modeled heap. The budget cases below compare pools against each
// other, so unequal pools would let size rather than touch time decide the
// ordering and the tests would pass for the wrong reason.
func seededSegmentDocs(segIdx int) []searchengine.Document {
	docs := make([]searchengine.Document, seededDocsPerSegment)
	for i := range docs {
		vec := make([]byte, 32)
		for b := range vec {
			vec[b] = byte((segIdx*17 + i*31 + b*7) % 251)
		}
		docs[i] = searchengine.Document{
			ID:     fmt.Sprintf("seeded-%02d-%02d", segIdx, i),
			Vector: vec,
			Fields: map[string]string{searchengine.FieldSymbolName: fmt.Sprintf("seeded%d%d", segIdx, i)},
		}
	}
	return docs
}

// seedResidentPool makes one graph's HNSW pool genuinely resident to the budget
// pass: seededSegmentsPerPool REAL, decodable segments put in L2 (so the
// re-materializability gate passes), imported into the pool's engine and recorded
// in the resident map through the production pair, with the pool's last
// consumer-search touch stamped at touchNanos.
//
// THE SEGMENTS MUST BE REAL, and that is a consequence of the metering change
// rather than a preference. The budget's unit is now the ENGINE's modeled heap
// (searchengine.SegmentedIndex.ResidentHeapBytes), so a fixture that only wrote
// entries into the resident map — as this helper previously did, with 100 bytes
// of 'x' per fake segment — leaves the engine empty and every pool reporting a
// heap of zero. Importing through engine.Import + recordResident also keeps the
// resident ids and the engine's segment ids IDENTICAL, which is what lets
// evictResident's Unload actually drop the heap it snapshotted.
//
// It still seeds rather than driving a ship+load, because the budget pass's
// contract is entirely about ordering and skipping — which pool goes first, which
// pools are spared. The evict/reload semantics it stands in for are covered
// end-to-end in manager_residency_test.go.
func seedResidentPool(
	t *testing.T, m *Manager, name string, touchNanos int64,
) *distManager[[]byte, struct{}] {
	t.Helper()
	dm := m.managerFor(kgtypes.GraphCode, name)
	blobs := make([]searchengine.SegmentBlob, 0, seededSegmentsPerPool)
	for i := range seededSegmentsPerPool {
		b := consolidatedHNSWBlob(t, seededSegmentDocs(i))
		dm.cache.Put(b.ID, b.Bytes)
		blobs = append(blobs, b)
	}
	require.NoError(t, dm.engine.Import(blobs, dm.knownTombstones()))
	dm.recordResident(blobs)
	dm.lastSearchNanos.Store(touchNanos)
	require.Positive(t, dm.residentBytes(),
		"PRECONDITION: a seeded pool must report modeled resident heap, or the budget pass sees nothing to evict")
	return dm
}

// seedThreePools seeds cold/warm/hot pools and returns them with the per-pool
// modeled heap, asserting the three are equal so the budget arithmetic in each
// case is decided by touch time alone.
func seedThreePools(t *testing.T, m *Manager) (cold, warm, hot *distManager[[]byte, struct{}], perPool int64) {
	t.Helper()
	cold = seedResidentPool(t, m, "coldgraph", 100)
	warm = seedResidentPool(t, m, "warmgraph", 200)
	hot = seedResidentPool(t, m, "hotgraph", 300)
	perPool = cold.residentBytes()
	require.Equal(t, perPool, warm.residentBytes(), "pools must be equal-sized or size, not coldness, decides")
	require.Equal(t, perPool, hot.residentBytes(), "pools must be equal-sized or size, not coldness, decides")
	return cold, warm, hot, perPool
}

// TestResidencyBudgetEvictsColdestPoolFirst pins the budget pass's ordering and its
// skips. The parent asserts coldest-first; the subtest asserts the exclude set,
// which is the deadlock guard's checkable belt — omit exclude handling and ONLY the
// subtest goes red.
func TestResidencyBudgetEvictsColdestPoolFirst(t *testing.T) {
	t.Parallel()

	// THE BUDGETS ARE DERIVED FROM THE MEASURED PER-POOL HEAP, not written as
	// literals. The unit is now the engine's modeled heap, whose magnitude is a
	// property of the fixture's documents rather than a number this test chooses,
	// so each case seeds first and then sets the budget it needs. Three equal
	// pools against a budget of 2.5x one pool: evicting exactly one takes the
	// total to 2x and the pass must then STOP.

	t.Run("coldest_pool_is_evicted_first", func(t *testing.T) {
		m := budgetManager(t)
		cold, warm, hot, perPool := seedThreePools(t, m)
		m.residencyBudgetBytes = 2*perPool + perPool/2

		m.enforceResidencyBudget(nil)

		require.True(t, cold.isEvicted(), "the least-recently-searched pool is evicted first")
		require.False(t, warm.isEvicted(), "the pass stops as soon as it is back under budget")
		require.False(t, hot.isEvicted())
		require.Zero(t, cold.residentBytes(), "an evicted pool holds no modeled heap")
		require.Equal(t, perPool, warm.residentBytes())
		require.True(t, m.PoolEvicted(kgtypes.GraphCode, "coldgraph"))
		require.False(t, m.PoolEvicted(kgtypes.GraphCode, "warmgraph"))
	})

	t.Run("excluded_pool_is_never_evicted", func(t *testing.T) {
		m := budgetManager(t)
		cold, warm, hot, perPool := seedThreePools(t, m)
		m.residencyBudgetBytes = 2*perPool + perPool/2

		// The graph a caller is still serving. Evicting it would take
		// residencyMu.Lock underneath a reader that still holds RLock, which on Go's
		// non-reentrant RWMutex is a self-deadlock rather than a slow search.
		m.enforceResidencyBudget([]graphKey{{graphType: kgtypes.GraphCode, graphName: "coldgraph"}})

		require.False(t, cold.isEvicted(), "an excluded pool must survive even as the coldest candidate")
		require.True(t, warm.isEvicted(), "the next-coldest pool is evicted in its place")
		require.False(t, hot.isEvicted())
	})

	t.Run("under_budget_evicts_nothing", func(t *testing.T) {
		// The known-positive control for the two assertions above: with the SAME
		// pools under a budget that accommodates them, the pass must evict NOTHING,
		// so an implementation that simply evicted the coldest pool unconditionally
		// cannot satisfy the suite.
		m := budgetManager(t)
		cold := seedResidentPool(t, m, "coldgraph", 100)
		warm := seedResidentPool(t, m, "warmgraph", 200)
		perPool := cold.residentBytes()
		m.residencyBudgetBytes = 4 * perPool

		m.enforceResidencyBudget(nil)

		require.False(t, cold.isEvicted())
		require.False(t, warm.isEvicted())
		require.Equal(t, perPool, cold.residentBytes())
	})

	t.Run("a_graph_with_a_write_backlog_is_skipped", func(t *testing.T) {
		m := budgetManager(t)
		cold, warm, _, perPool := seedThreePools(t, m)
		m.residencyBudgetBytes = 2*perPool + perPool/2

		// Queued writes are about to be sealed and shipped FROM the resident set.
		m.mu.Lock()
		m.dirty[graphKey{graphType: kgtypes.GraphCode, graphName: "coldgraph"}] = &graphDirtyState{
			hnsw: formatDirtyState{tails: []searchengine.SegmentID{"coldgraph-seg-00"}},
		}
		m.mu.Unlock()

		m.enforceResidencyBudget(nil)

		require.False(t, cold.isEvicted(), "a pool with queued writes must not be evicted underneath them")
		require.True(t, warm.isEvicted(), "the next-coldest pool is evicted in its place")
	})
}

// evictedGraphFixture builds a real graph on both formats, makes both pools
// resident by running one search, and then evicts both — the starting state every
// background-arm subtest below needs. It returns the manager and the shared fake
// source view whose per-leg counters prove an arm paid no network.
func evictedGraphFixture(t *testing.T) (*Manager, *fakeSegmentSource, kgtypes.GraphType, string) {
	return evictedGraphFixtureN(t, "evictedArms", 8)
}

// evictedGraphFixtureN is evictedGraphFixture with the corpus size chosen by the
// caller. THE SIZE IS NOT COSMETIC: the publish path's coverage-ratio gate DISARMS
// itself below residentBackstopFloor (64) — a small graph legitimately publishes its
// whole tiny set — so a test whose property depends on that gate biting must pass a
// corpus at or above the floor, or it measures the disarm instead.
func evictedGraphFixtureN(t *testing.T, name string, docCount int) (
	*Manager, *fakeSegmentSource, kgtypes.GraphType, string,
) {
	t.Helper()
	ctx := context.Background()
	gt := kgtypes.GraphKnowledge

	_, gc := newSegmentHarness(t)
	mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc)))

	docs := bothFormatDocs(docCount, "arms-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, docs))
	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	// One search populates the resident tracking from L2 (recordResident runs on the
	// load path, not the write path), which is what evictResident snapshots.
	_, err := mgr.Search(ctx, gt, name, "common", docs[0].Vector, 5)
	require.NoError(t, err)

	_, hnswOK := mgr.managerFor(gt, name).evictResident()
	_, bm25OK := mgr.bm25ManagerFor(gt, name).evictResident()
	require.True(t, hnswOK, "PRECONDITION: the HNSW pool must evict")
	require.True(t, bm25OK, "PRECONDITION: the BM25 pool must evict")
	require.True(t, mgr.PoolEvicted(gt, name))
	return mgr, gc, gt, name
}

// TestBackgroundArmsDoNotResurrectAnEvictedPool is ticket constraint 2's
// prohibition — "the reconcile/coverage-probe/rebuild arms MUST NOT count as
// touches or re-load evicted pools" — with ONE SUBTEST PER REGION, so a single
// fixed region cannot green the others.
//
// EVERY DECLINING SUBTEST ALSO ASSERTS ZERO List/Fetch CALLS. That is the named
// catcher for an arm that returns the right value but pays the network anyway,
// which no value-only assertion can see.
//
// resident_doc_count_materializes asserts the OPPOSITE of its four siblings, and
// deliberately: LoadResidentDocCount is CONSUMER-side (its OSS branch feeds the
// unified-search completeness verdict, which is search-visible AND cached), so it
// keeps the materializing load(). Its one background caller declines upstream at
// the decider instead.
func TestBackgroundArmsDoNotResurrectAnEvictedPool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("reconcile_arm_skips", func(t *testing.T) {
		mgr, gc, gt, name := evictedGraphFixture(t)
		lists, fetches := gc.listCalls.Load(), gc.fetchCalls.Load()

		verdicts, err := mgr.ReconcileResidentDegenerateByFormat(ctx, gt, name)
		require.NoError(t, err)
		require.Len(t, verdicts, 2, "one verdict per format arm")
		for _, v := range verdicts {
			require.True(t, v.Evicted, "%s arm must report Evicted", v.Format)
			require.False(t, v.Degenerate, "an unmeasured arm must never drive a rebuild")
			require.NoError(t, v.Err)
			require.Zero(t, v.ResidentAfterLoad, "a declined arm measured nothing")
		}
		require.True(t, mgr.PoolEvicted(gt, name), "the probe must not have resurrected the pool")
		require.Equal(t, lists, gc.listCalls.Load(), "a declined arm pays no List")
		require.Equal(t, fetches, gc.fetchCalls.Load(), "a declined arm pays no Fetch")
	})

	t.Run("completeness_arm_skips", func(t *testing.T) {
		mgr, gc, gt, name := evictedGraphFixture(t)
		lists, fetches := gc.listCalls.Load(), gc.fetchCalls.Load()

		for _, arm := range []completenessArm{mgr.managerFor(gt, name), mgr.bm25ManagerFor(gt, name)} {
			require.NoError(t, mgr.convergeArmToManifest(ctx, gt, name, arm))
		}

		require.True(t, mgr.PoolEvicted(gt, name))
		require.Equal(t, lists, gc.listCalls.Load(), "a declined arm pays no List")
		require.Equal(t, fetches, gc.fetchCalls.Load(), "a declined arm pays no Fetch")
	})

	t.Run("resident_doc_count_materializes", func(t *testing.T) {
		// THE SCOPE FENCE, asserted rather than assumed: this probe is consumer-side
		// and keeps its materializing load.
		mgr, _, gt, name := evictedGraphFixture(t)

		count, err := mgr.LoadResidentDocCount(ctx, gt, name)
		require.NoError(t, err)
		require.Positive(t, count, "the consumer-side probe re-materializes and reports the real count")
		require.False(t, mgr.managerFor(gt, name).isEvicted(), "materializing clears the latch")
	})

	t.Run("live_resident_doc_count_reads_zero", func(t *testing.T) {
		mgr, gc, gt, name := evictedGraphFixture(t)
		lists, fetches := gc.listCalls.Load(), gc.fetchCalls.Load()

		count, err := mgr.LoadLiveResidentDocCount(ctx, gt, name)
		require.NoError(t, err)
		require.Zero(t, count, "an evicted pool reads 0 — nobody looked, which is not the same as empty")
		require.True(t, mgr.PoolEvicted(gt, name))
		require.Equal(t, lists, gc.listCalls.Load(), "a declined probe pays no List")
		require.Equal(t, fetches, gc.fetchCalls.Load(), "a declined probe pays no Fetch")

		// KNOWN-POSITIVE CONTROL for the zero: the SAME probe against the SAME graph
		// reads non-zero once the pool is materialized, so the zero above is the
		// decline and not a fixture with no documents in it.
		require.NoError(t, mgr.managerFor(gt, name).load(ctx))
		live, err := mgr.LoadLiveResidentDocCount(ctx, gt, name)
		require.NoError(t, err)
		require.Positive(t, live)
	})

	t.Run("uncovered_members_errors", func(t *testing.T) {
		mgr, gc, gt, name := evictedGraphFixture(t)
		lists, fetches := gc.listCalls.Load(), gc.fetchCalls.Load()
		probe := []searchengine.ExternalID{"arms-n0"}

		missHNSW, missBM25, err := mgr.UncoveredMembers(ctx, gt, name, probe)
		require.Error(t, err, "membership is NOT determinable for a pool nobody loaded")
		require.Contains(t, err.Error(), "evicted")
		require.Nil(t, missHNSW, "no manufactured missing-set")
		require.Nil(t, missBM25)
		require.True(t, mgr.PoolEvicted(gt, name))
		require.Equal(t, lists, gc.listCalls.Load(), "a declined probe pays no List")
		require.Equal(t, fetches, gc.fetchCalls.Load(), "a declined probe pays no Fetch")

		// KNOWN-POSITIVE CONTROL for the error: with the pools materialized the SAME
		// call answers cleanly, so the error above is the decline rather than a
		// permanently-broken probe.
		require.NoError(t, mgr.managerFor(gt, name).load(ctx))
		require.NoError(t, mgr.bm25ManagerFor(gt, name).load(ctx))
		_, _, err = mgr.UncoveredMembers(ctx, gt, name, probe)
		require.NoError(t, err)
	})
}

// TestWriteRematerializesAnEvictedPool asserts the REAL mechanism rather than a
// proxy for it: after a write lands on an evicted pool, the drain's publish must
// COMPLETE A MANIFEST SWAP.
//
// Without the re-materialization the write seals a handful of documents into an
// emptied engine, publishResident's coverage gate refuses the swap (the pool is far
// below the ratio against its shipped denominator), the completed-swap counter never
// rises, and markCoverageSkip accumulates a suppression streak that manage(status)
// eventually renders as the STUCK band — which ticket constraint 6 forbids.
//
// THE CORPUS IS SIZED AT OR ABOVE residentBackstopFloor (64) DELIBERATELY, and the
// reason was measured rather than assumed: at 8 documents the coverage-ratio gate
// DISARMS itself (a tiny graph legitimately publishes its whole set), so stripping
// the materialization out left this test GREEN — it was measuring the disarm. At 80
// the gate arms, and stripping the materialization turns the swap assertions red on
// the mechanism they name.
func TestWriteRematerializesAnEvictedPool(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgr, _, gt, name := evictedGraphFixtureN(t, "evictedWrites", 80)
	hdm, bdm := mgr.managerFor(gt, name), mgr.bm25ManagerFor(gt, name)

	hnswSwapsBefore, bm25SwapsBefore := hdm.completedSwapCount(), bdm.completedSwapCount()

	more := bothFormatDocs(2, "postevict-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, more))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, more))
	require.False(t, hdm.isEvicted(), "the HNSW write re-materialized its own pool")
	require.False(t, bdm.isEvicted(), "the BM25 write re-materialized its own pool — not the HNSW one twice")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	require.Greater(t, hdm.completedSwapCount(), hnswSwapsBefore,
		"the HNSW drain must LAND a manifest swap, not be refused by the coverage gate")
	require.Greater(t, bdm.completedSwapCount(), bm25SwapsBefore,
		"the BM25 drain must LAND a manifest swap, not be refused by the coverage gate")
	require.Zero(t, hdm.coverageSuppressedSince(), "no HNSW coverage-suppression episode was opened")
	require.Zero(t, bdm.coverageSuppressedSince(), "no BM25 coverage-suppression episode was opened")
}
