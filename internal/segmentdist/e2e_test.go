// SPDX-License-Identifier: Apache-2.0

package segmentdist

// e2e_test.go drives the WHOLE distribution flow end to end on the mock
// SegmentFormat: engine -> L2 cache -> a second process's engine -> search, plus
// eviction and re-materialization.
//
// WHY AN END-TO-END TEST SURVIVES AT ALL when unit tests cover each leg: covering a
// surface is not covering the path through it. Every hop here has its own focused
// test, and none of them proves the hops HAND OVER — that what one process wrote to
// L2 is exactly what another process's engine imports and searches. This is the only
// test that drives the real construction from one end to the other.
//
// WHAT WAS DELETED FROM IT, and why nothing is owed for those parts. The original
// wired an in-memory server registry and asserted its behaviours: monotonic
// generation stamping, idempotent-by-content-hash ship, list-delta pulls, cold Fetch
// batching, manifest publish with refcount-GC, and a writer_id carried on every
// outbound RPC for last-connection liveness. Every one of those mechanisms is gone
// with the cloud rail, so the assertions have no referent — the mechanism is ABSENT,
// not weakened, and an absent mechanism owes no successor.
//
// TestRegistryReclaimE2E went WHOLE. Its subject was the server refcount-GC keeping
// the registry bounded across ship+publish cycles, plus the writer_id liveness
// wiring. There is no registry and no refcount. The LOCAL bound on accumulated
// segments is a different mechanism with its own coverage: the merge reclaim
// (TestLegitimateMergeStillReclaims) and the rebuild supersede
// (TestPartialLayerNeverRetiresAGoodLayer), both in restart_falseprune_test.go.
//
// THE ZERO-NETWORK ASSERTIONS ARE ALSO GONE, deliberately. The original counted
// Fetch calls to prove a reload took no network. With no network on the path at all
// that count is structurally zero and asserting it here would be a check that cannot
// fail. What survives is the POSITIVE form, in TestLoadReadsL2InEveryBranch below:
// each of the three load branches is asserted on what it imported from L2. The old
// negative phrasing — "zero source calls in every branch" — is not merely satisfied
// now but unstatable, because no source exists to be called.

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestSegmentDistributionE2E is the hand-over proof: what one process writes to L2 is
// what another process's engine imports, searches, can lose to eviction, and can
// recover by re-materializing from the same cache.
func TestSegmentDistributionE2E(t *testing.T) {
	t.Parallel()

	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "e2e"}
	ctx := context.Background()

	// ONE cache directory shared by both processes — it is the hand-over medium, and
	// the whole point of the test. Two directories would make every assertion below
	// about a single process talking to itself.
	cacheDir := t.TempDir()

	// PRODUCER: seal three documents and write the sealed blobs to L2.
	prodEng := newMockEngine(t)
	require.NoError(t, prodEng.Add([]searchengine.Document{doc("d1", "alpha alpha")}))
	require.NoError(t, prodEng.Add([]searchengine.Document{doc("d2", "alpha beta")}))
	require.NoError(t, prodEng.Add([]searchengine.Document{doc("d3", "gamma")}))
	prodMgr := buildManager(prodEng, target, cacheDir)

	exported := prodEng.Export()
	require.Len(t, exported, 3, "fixture: three sealed segments must exist to hand over")
	require.NoError(t, prodMgr.writeNewBlobsToL2(exported))

	// IDEMPOTENT: writing the same content-hash blobs again is a no-op rather than a
	// duplication. The ids are content hashes, so a second write must not grow the set.
	require.NoError(t, prodMgr.writeNewBlobsToL2(exported))
	require.Len(t, prodMgr.cache.Keys(), 3,
		"a second write of identical content-hash blobs adds nothing — the cache is content-addressed")

	// CONSUMER: a DIFFERENT engine over the SAME cache directory. This is the
	// hand-over: nothing but the on-disk cache connects it to the producer.
	consEng := newMockEngine(t)
	consMgr := buildManager(consEng, target, cacheDir)
	require.NoError(t, consMgr.load(ctx))
	require.Len(t, consEng.Search(mockQuery{term: "alpha"}, 10), 2,
		"the consumer searches what the producer wrote — d1 and d2 match 'alpha'")

	// EVICTION: drop the resident set; the search loses its hits.
	unloaded := consMgr.evictAllResidentForTest()
	require.NotEmpty(t, unloaded, "fixture: eviction must actually drop something")
	require.Empty(t, consEng.Search(mockQuery{term: "alpha"}, 10),
		"an evicted pool serves nothing — this is the state re-materialization has to recover from")

	// RE-MATERIALIZE from the same L2 cache and get the hits back. The strict form
	// (tolerateMisses=false) is used deliberately: an id absent from L2 must ERROR
	// here rather than quietly returning a short set, because L2 is the only place it
	// could have been recovered from.
	require.NoError(t, consMgr.reload(unloaded, false))
	require.Len(t, consEng.Search(mockQuery{term: "alpha"}, 10), 2,
		"re-materialization from L2 restores exactly the hits eviction removed")

	// A SECOND load is a no-op rather than a duplication — re-importing an
	// already-resident id must not double-add it.
	require.NoError(t, consMgr.load(ctx))
	require.Len(t, consEng.Search(mockQuery{term: "alpha"}, 10), 2,
		"a repeat load leaves the corpus unchanged — imports are idempotent by segment id")

	// CONCURRENT load + search. The engine read path is lock-free by contract; this
	// asserts no panic and a consistent final read, and carries the race detector
	// when the suite is run under -race.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _ = consMgr.load(ctx) })
		wg.Go(func() { _ = consEng.Search(mockQuery{term: "alpha"}, 10) })
	}
	wg.Wait()
	require.Len(t, consEng.Search(mockQuery{term: "alpha"}, 10), 2,
		"the final search is consistent after concurrent load+search")

	// KNOWN-NEGATIVE, same corpus: a term nobody indexed returns nothing. Without it
	// every "2 hits" above is equally satisfied by a search that ignores its query.
	require.Empty(t, consEng.Search(mockQuery{term: "no-such-term"}, 10),
		"CONTROL: the search is really matching the query, not returning the corpus")
}

// TestLoadReadsL2InEveryBranch covers the three states a pool can be in when load
// runs, and asserts each one actually imports from the disk cache.
//
// IT REPLACES TestLoadIssuesNoSourceListInAnyBranch, which asserted the same three
// branches issued ZERO calls to a segment source. That test was written while a
// source seam still existed and was already inert; with the seam DELETED it became
// unfalsifiable in the most literal way — its counting double was a free-standing
// object nothing was wired to, so its counters read zero because nothing COULD call
// them, not because the load declined to. Renaming it would have kept a test whose
// three zeros were guaranteed by construction.
//
// WHAT SURVIVED THE REWRITE IS THE COVERAGE, NOT THE ASSERTION. The three branches
// take genuinely different paths through load — an EMPTY cache (nothing to import), a
// POPULATED cache (the import runs), and an EVICTED pool (the re-materialization
// runs) — and each is now asserted on what it produced rather than on what it did not
// call. "The load consults no remote source" is no longer a property to test: there is
// no source, in this package or in searchengine, so the claim is structural and a test
// asserting it could only ever be green.
func TestLoadReadsL2InEveryBranch(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "no-list-repo"
	cacheDir := t.TempDir()

	// A real corpus on disk, written by a SEPARATE producer so the manager under test
	// starts cold against a populated cache — the restart shape.
	producer := closeOnCleanup(t, NewManager(cacheDir, 0))
	require.NoError(t, producer.AddAndMarkDirty(ctx, gt, name, hnswVecDocs(1024)))
	require.NoError(t, producer.ReEmitDirtyBuckets(ctx, gt, name))
	stored := l2IDsFor(cacheDir, name, hnswFormatName)
	require.NotEmpty(t, stored, "fixture control: the populated branch needs a real corpus on disk")

	mgr := closeOnCleanup(t, NewManager(cacheDir, 0))

	// BRANCH 1 — EMPTY L2. A graph nothing ever wrote. The load must SUCCEED and
	// import nothing: an error here would make a cold graph unsearchable, and an
	// import would mean it read someone else's bytes.
	emptyDM := mgr.managerFor(gt, "a-graph-with-no-corpus")
	require.NoError(t, emptyDM.load(ctx), "an empty cache is not an error — it is a graph with no segments yet")
	require.Empty(t, emptyDM.engine.Export(), "and nothing may be imported into it")

	// BRANCH 2 — POPULATED L2. The import runs off the cache, and it must bring in the
	// WHOLE stored set rather than a tail.
	dm := mgr.managerFor(gt, name)
	require.NoError(t, dm.load(ctx))
	require.Len(t, dm.engine.Export(), len(stored),
		"a populated cache imports the whole stored set — a short import is the read-side half of a false prune")

	// BRANCH 3 — EVICTED POOL. The re-materialization path: the same bytes come back
	// from the same place.
	//
	// IT IS DRIVEN THROUGH reload, NOT load, and the difference is the branch. load
	// carries a once-guard, so a second call on an already-loaded manager returns
	// immediately and imports nothing — which is correct, and which is also why the
	// predecessor test's third "zero source calls" reading was guaranteed before the
	// branch it named was ever entered. reload is what actually re-materializes, and
	// STRICT (tolerateMisses=false) is the right mode here: the cache is the only
	// store, so an id it cannot supply must error rather than yield a short set.
	evicted := dm.evictAllResidentForTest()
	require.NotEmpty(t, evicted, "fixture control: the eviction must have dropped something")
	require.Empty(t, dm.engine.Export(), "fixture control: the pool is genuinely empty before the reload")
	require.NoError(t, dm.reload(evicted, false))
	require.Len(t, dm.engine.Export(), len(stored),
		"an evicted pool re-materializes in full from the cache it was evicted from")
}
