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
	return closeOnCleanup(t, NewManager(t.TempDir(), 0,
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
func evictedGraphFixture(t *testing.T) (*Manager, kgtypes.GraphType, string) {
	return evictedGraphFixtureN(t, "evictedArms", 8)
}

// evictedGraphFixtureN is evictedGraphFixture with the corpus size chosen by the
// caller. THE SIZE IS NOT COSMETIC: the publish path's coverage-ratio gate DISARMS
// itself below residentBackstopFloor (64) — a small graph legitimately publishes its
// whole tiny set — so a test whose property depends on that gate biting must pass a
// corpus at or above the floor, or it measures the disarm instead.
func evictedGraphFixtureN(t *testing.T, name string, docCount int) (
	*Manager, kgtypes.GraphType, string,
) {
	t.Helper()
	ctx := context.Background()
	gt := kgtypes.GraphKnowledge

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

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
	return mgr, gt, name
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
		mgr, gt, name := evictedGraphFixture(t)

		obs, err := mgr.ResidentObservationsByFormat(ctx, gt, name)
		require.NoError(t, err)
		require.Len(t, obs, 2, "one observation per format arm")
		for _, v := range obs {
			require.True(t, v.Evicted, "%s arm must report Evicted", v.Format)
			require.NoError(t, v.Err)
			require.Zero(t, v.ResidentAfterLoad, "a declined arm measured nothing")
		}
		// THE DEGENERATE ASSERTION IS GONE WITH THE VERDICT. This probe no longer
		// decides anything — ArmObservation carries measurements only, and the
		// caller holding the embedded denominator makes the call. "An unmeasured arm
		// must never drive a rebuild" is now asserted where the decision lives:
		// bootstrap/per_format_degeneracy_test.go's evicted-pool arm.
		require.True(t, mgr.PoolEvicted(gt, name), "the probe must not have resurrected the pool")
		// THE "PAYS NO List / NO Fetch" ASSERTIONS ARE GONE WITH THE SOURCE. They
		// compared a segment source's per-leg counters either side of the call, to prove
		// a declined arm did no work. Nothing calls a source — there is none — so those
		// counters could only ever have read zero, which is a check that cannot fail.
		// What the pair was really asserting survives one line up: the pool is STILL
		// evicted afterwards, which is the observable that goes red if an arm quietly
		// re-materializes it.
	})

	// THE completeness_arm_skips SUBTEST WAS DELETED HERE. It drove
	// convergeArmToManifest over the completenessArm seam — the arm that reconciled a
	// pool against its published MANIFEST. Both the seam and the manifest are gone, so
	// there is no arm left to decline. The property it shared with its sibling above —
	// an evicted pool is declined without paying a read — is still asserted by
	// reconcile_arm_skips against the surviving observation probe, so nothing is lost
	// by its removal.

	t.Run("resident_doc_count_materializes", func(t *testing.T) {
		// THE SCOPE FENCE, asserted rather than assumed: this probe is consumer-side
		// and keeps its materializing load.
		mgr, gt, name := evictedGraphFixture(t)

		count, err := mgr.LoadResidentDocCount(ctx, gt, name)
		require.NoError(t, err)
		require.Positive(t, count, "the consumer-side probe re-materializes and reports the real count")
		require.False(t, mgr.managerFor(gt, name).isEvicted(), "materializing clears the latch")
	})

	t.Run("live_resident_doc_count_reads_zero", func(t *testing.T) {
		mgr, gt, name := evictedGraphFixture(t)

		count, err := mgr.LoadLiveResidentDocCount(ctx, gt, name)
		require.NoError(t, err)
		require.Zero(t, count, "an evicted pool reads 0 — nobody looked, which is not the same as empty")
		require.True(t, mgr.PoolEvicted(gt, name))
		// The source-call counters these two lines compared are gone with the source;
		// PoolEvicted one line up is the surviving observable, and its known-positive
		// control follows immediately below.

		// KNOWN-POSITIVE CONTROL for the zero: the SAME probe against the SAME graph
		// reads non-zero once the pool is materialized, so the zero above is the
		// decline and not a fixture with no documents in it.
		require.NoError(t, mgr.managerFor(gt, name).load(ctx))
		live, err := mgr.LoadLiveResidentDocCount(ctx, gt, name)
		require.NoError(t, err)
		require.Positive(t, live)
	})

	t.Run("uncovered_members_errors", func(t *testing.T) {
		mgr, gt, name := evictedGraphFixture(t)
		probe := []searchengine.ExternalID{"arms-n0"}

		missHNSW, missBM25, err := mgr.UncoveredMembers(ctx, gt, name, probe)
		require.Error(t, err, "membership is NOT determinable for a pool nobody loaded")
		require.Contains(t, err.Error(), "evicted")
		require.Nil(t, missHNSW, "no manufactured missing-set")
		require.Nil(t, missBM25)
		require.True(t, mgr.PoolEvicted(gt, name))
		// The source-call counters these two lines compared are gone with the source;
		// PoolEvicted one line up is the surviving observable, and its known-positive
		// control follows immediately below.

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
// proxy for it: after a write lands on an evicted pool, the drain must actually
// REACH L2 on both arms.
//
// Without the re-materialization the write seals a handful of documents into an
// emptied engine and the drain has nothing to land, so the pool stays effectively
// empty while every call still returns a nil error. That is the shape worth guarding:
// a silent no-op that reads as success to its caller.
//
// THE CORPUS IS SIZED AT OR ABOVE residentBackstopFloor (64) DELIBERATELY, and the
// reason was measured rather than assumed: at 8 documents the size-based guard
// DISARMS itself (a tiny graph legitimately writes its whole set), so stripping the
// materialization out left this test GREEN — it was measuring the disarm. At 80 the
// guard arms, and stripping the materialization turns the per-arm cache assertions
// red on the mechanism they name.
func TestWriteRematerializesAnEvictedPool(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mgr, gt, name := evictedGraphFixtureN(t, "evictedWrites", 80)
	hdm, bdm := mgr.managerFor(gt, name), mgr.bm25ManagerFor(gt, name)

	hnswBefore, bm25Before := sortedCacheIDs(hdm.cache), sortedCacheIDs(bdm.cache)

	more := bothFormatDocs(2, "postevict-")
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, more))
	require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, name, more))
	require.False(t, hdm.isEvicted(), "the HNSW write re-materialized its own pool")
	require.False(t, bdm.isEvicted(), "the BM25 write re-materialized its own pool — not the HNSW one twice")

	require.NoError(t, mgr.ReEmitDirtyBuckets(ctx, gt, name))

	// THE DRAIN MUST LAND ON BOTH ARMS. The original read the publish-gate's swap
	// counter and then asserted no coverage-suppression episode had opened; the gate,
	// its counter and its suppression clock are all deleted. What "landed" means
	// locally is that the arm's blobs reached L2, and it is asserted PER ARM because
	// the defect being guarded is one arm re-materializing twice while the other never
	// drains — a single combined observable would hide exactly that.
	//
	// THE ID SET IS THE OBSERVABLE, NOT ITS SIZE: this drain rewrites the partition it
	// touches, so the count is unchanged while the bytes are not. See sortedCacheIDs.
	require.NotEqual(t, hnswBefore, sortedCacheIDs(hdm.cache),
		"the HNSW drain must LAND — its blobs reach L2")
	require.NotEqual(t, bm25Before, sortedCacheIDs(bdm.cache),
		"the BM25 drain must LAND — its blobs reach L2, and not the HNSW arm's twice")
}
