// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// manager_seed_branch_test.go — the branch segment seed: what it copies, what it
// refuses to copy, and that it runs once per graph and format.

// warmBaseLiveLayer builds a REAL base corpus through the ordinary write path and
// returns the ids base's ENGINE exports.
//
// IT REPLACES A FIXED-LIST SOURCE DOUBLE, and the replacement is forced rather than
// stylistic. The seed used to take its published set from a segment source's List, so
// a double returning a fixed list defined "what base published" independently of
// what base's cache held — which is what let a test tell "copies the published set"
// apart from "copies the whole directory". The seed now reads base's ENGINE export,
// and no source is consulted at all, so a fixed-list double defines nothing and the
// seed copies nothing.
//
// THE DISTINCTION SURVIVES, on a different axis. The engine's export is the LIVE
// layer while the cache directory can also hold superseded blobs no layer
// references, so a test still separates the two by warming a real layer and then
// planting an extra file on disk that the engine never imported. That is also the
// production shape: retired blobs linger until a reclaim.
//
// The base engine is warmed by LOADING from L2, so this works on a manager that did
// not itself write the corpus — which is what the budget fixture needs, since it has
// to size its budget against blobs that already exist.
func warmBaseLiveLayer(
	t *testing.T, ctx context.Context, mgr *Manager, repo, format string,
) []searchengine.SegmentID {
	t.Helper()
	var exported []searchengine.SegmentBlob
	if format == bm25.New().Name() {
		dm := mgr.bm25ManagerFor(kgtypes.GraphCode, repo)
		require.NoError(t, dm.load(ctx))
		exported = dm.engine.Export()
	} else {
		dm := mgr.managerFor(kgtypes.GraphCode, repo)
		require.NoError(t, dm.load(ctx))
		exported = dm.engine.Export()
	}
	ids := make([]searchengine.SegmentID, 0, len(exported))
	for _, b := range exported {
		ids = append(ids, b.ID)
	}
	require.NotEmpty(t, ids,
		"fixture control: base's ENGINE must hold a live layer — a cold engine exports nothing and the seed "+
			"correctly copies nothing, which would make every assertion below pass for the wrong reason")
	return ids
}

// seedBaseCorpus writes a real base corpus for one format through a producer
// Manager rooted at cacheDir, so the blobs on disk are ones an engine can decode.
func seedBaseCorpus(t *testing.T, ctx context.Context, cacheDir, repo, format string, n int) {
	t.Helper()
	producer := closeOnCleanup(t, NewManager(cacheDir, 0))
	if format == bm25.New().Name() {
		require.NoError(t, producer.AddAndMarkDirtyFields(ctx, kgtypes.GraphCode, repo, bm25FieldDocs(n)))
	} else {
		require.NoError(t, producer.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, hnswVecDocs(n)))
	}
	require.NoError(t, producer.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))
}

// plantBlob writes one .seg file into a graph+format cache dir. It writes the
// file directly rather than through a cache handle because the seed constructs
// its OWN handles and must recover the contents from disk — which is exactly
// what a fresh process does.
// The graph type is fixed: every fixture here is a CODE graph, which is the
// only type a branch seed applies to.
func plantBlob(t *testing.T, cacheDir, name, format, id string, body []byte) {
	t.Helper()
	dir := graphCacheDirFor(cacheDir, kgtypes.GraphCode, name, format)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".seg"), body, 0o600))
}

// branchBucketIDs lists the ids resident in one graph+format bucket, read from
// disk through a fresh cache handle.
func branchBucketIDs(cacheDir, name, format string) []searchengine.SegmentID {
	return newDiskSegmentCache(graphCacheDirFor(cacheDir, kgtypes.GraphCode, name, format), 0, adviceRandom).Keys()
}

// branchEngineCache builds the cache instance a branch's engine would hold. The
// seed takes its destination from the caller, so a test calling it DIRECTLY has
// to supply what the constructors supply in production — the engine's own
// instance. maxBytes is threaded rather than fixed at 0 because the budget
// fixture needs the cache and the Manager to agree on the ceiling.
func branchEngineCache(cacheDir, name, format string, maxBytes int64) *diskSegmentCache {
	return newDiskSegmentCache(graphCacheDirFor(cacheDir, kgtypes.GraphCode, name, format), maxBytes, adviceRandom)
}

// TestSeedBranchBucketFromBase_CopiesPublishedPartitions asserts the seed copies
// exactly what the base PUBLISHES — and that a superseded blob still sitting in
// base's cache is left behind.
//
// THE SUPERSEDED BLOB IS THE POINT. Copying it would RESURRECT documents the base
// already retired, into a branch that would then serve them as live. An assertion
// that only counted the copied partitions would pass while doing exactly that.
func TestSeedBranchBucketFromBase_CopiesPublishedPartitions(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()
	ctx := context.Background()
	cacheDir := t.TempDir()
	format := hnsw.New().Name()
	const repo = "seed-repo"
	const branch = "seed-repo@feature"

	// Base's LIVE layer is two real partitions; its cache then also holds a
	// superseded blob that no layer references.
	seedBaseCorpus(t, ctx, cacheDir, repo, format, 2048) // 2048 docs -> 2 partitions
	mgr := closeOnCleanup(t, NewManager(cacheDir, 0))
	live := warmBaseLiveLayer(t, ctx, mgr, repo, format)
	require.Len(t, live, 2, "fixture control: base's live layer must be exactly two partitions")

	// PLANTED AFTER THE LAYER IS WARM, so the engine never imported it: this is the
	// retired-but-not-yet-reclaimed blob, and copying it would RESURRECT documents the
	// base already retired into a branch that would serve them as live.
	plantBlob(t, cacheDir, repo, format, "superseded", []byte("retired, still on disk"))

	// FIXTURE CONTROL on both sides of the measurement: base really holds the extra,
	// and the branch bucket really starts empty. Without the first, the
	// "superseded is absent" assertion could pass because it was never there.
	require.Len(t, branchBucketIDs(cacheDir, repo, format), 3,
		"fixture control: base's cache must hold the live layer PLUS the superseded blob")
	require.Empty(t, branchBucketIDs(cacheDir, branch, format),
		"fixture control: the branch bucket must start empty")

	seeded, err := mgr.SeedBranchBucketFromBase(ctx, kgtypes.GraphCode, repo, branch, format,
		branchEngineCache(cacheDir, branch, format, 0))
	require.NoError(t, err)
	require.Len(t, seeded, 2, "the seed must copy exactly the two live partitions")

	got := branchBucketIDs(cacheDir, branch, format)
	require.ElementsMatch(t, live, got,
		"the branch bucket must hold the live partitions and NOT the superseded one — copying a blob the "+
			"base already retired resurrects its documents into the branch")

	// The bytes are the base's, not empty placeholders.
	baseBody, ok := newDiskSegmentCache(graphCacheDirFor(cacheDir, kgtypes.GraphCode, repo, format), 0, adviceRandom).Get(live[0])
	require.True(t, ok)
	branchBody, ok := newDiskSegmentCache(graphCacheDirFor(cacheDir, kgtypes.GraphCode, branch, format), 0, adviceRandom).Get(live[0])
	require.True(t, ok)
	require.Equal(t, baseBody, branchBody, "the branch's copy is base's bytes, not an empty placeholder")
}

// TestSeedBranchBucketFromBase_RefusesToOverflowTheBudget asserts an over-budget
// seed FAILS rather than copying a prefix.
//
// A PREFIX IS WORSE THAN NOTHING HERE. The cache evicts LRU on Put, so copying
// past the budget silently drops the partitions copied first and leaves a bucket
// that looks seeded while missing documents — which is precisely what a
// shipped-complete gate downstream would believe.
func TestSeedBranchBucketFromBase_RefusesToOverflowTheBudget(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()
	ctx := context.Background()
	cacheDir := t.TempDir()
	format := hnsw.New().Name()
	const repo = "budget-repo"
	const branch = "budget-repo@feature"

	seedBaseCorpus(t, ctx, cacheDir, repo, format, 2048) // 2048 docs -> 2 partitions

	// THE BUDGET IS DERIVED FROM THE REAL BLOBS, not guessed. The partitions are
	// whatever the format produces, so a hardcoded ceiling would either fit both or
	// neither depending on an encoder detail this test has no opinion about. Sizing it
	// at "the larger partition, and not both" is the condition the seed must refuse.
	baseDir := graphCacheDirFor(cacheDir, kgtypes.GraphCode, repo, format)
	baseCache := newDiskSegmentCache(baseDir, 0, adviceRandom)
	var total, largest int64
	for _, id := range baseCache.Keys() {
		n, ok := baseCache.sizeOf(id)
		require.True(t, ok)
		total += n
		largest = max(largest, n)
	}
	require.Greater(t, total, largest, "fixture control: base must hold more than one partition")
	budget := total - 1

	mgr := closeOnCleanup(t, NewManager(cacheDir, budget))
	live := warmBaseLiveLayer(t, ctx, mgr, repo, format)
	require.Len(t, live, 2, "fixture control: base's live layer must be exactly two partitions")

	seeded, err := mgr.SeedBranchBucketFromBase(ctx, kgtypes.GraphCode, repo, branch, format,
		branchEngineCache(cacheDir, branch, format, budget))
	require.Error(t, err, "a seed that cannot fit must fail rather than copy a prefix")
	require.ErrorContains(t, err, "refusing to copy a prefix")
	require.Empty(t, seeded)
	require.Empty(t, branchBucketIDs(cacheDir, branch, format),
		"a refused seed must leave the branch bucket untouched, not half-populated")
}

// TestManagerFor_BranchGraphSeedsFromBaseOnce asserts the seed runs at
// construction for BOTH engines, that a second construction does not re-seed, and
// that a base graph is not seeded at all.
//
// "DOES NOT RE-SEED" IS ASSERTED THROUGH AN OBSERVABLE, not through a call count:
// the branch bucket is emptied between the two constructions, so a second seed
// would refill it. A counter could be satisfied by a seed that ran and did
// nothing; an empty bucket cannot.
func TestManagerFor_BranchGraphSeedsFromBaseOnce(t *testing.T) {
	requireMeasurementRun(t)
	t.Parallel()
	cacheDir := t.TempDir()
	hnswFmt := hnsw.New().Name()
	bm25Fmt := bm25.New().Name()
	const repo = "ctor-repo"
	const branch = "ctor-repo@feature"

	// Base holds a real live layer per FORMAT, so a seed wired into only one
	// constructor leaves the other bucket visibly empty.
	ctx := context.Background()
	seedBaseCorpus(t, ctx, cacheDir, repo, hnswFmt, 1024)
	seedBaseCorpus(t, ctx, cacheDir, repo, bm25Fmt, 1024)

	mgr := closeOnCleanup(t, NewManager(cacheDir, 0))
	// Each format's base engine is warmed separately: the seed reads the export of
	// the arm it is seeding, so warming only one would leave the other copying nothing
	// and the per-format assertion below would fail for a reason of the fixture's own.
	hnswLive := warmBaseLiveLayer(t, ctx, mgr, repo, hnswFmt)
	bm25Live := warmBaseLiveLayer(t, ctx, mgr, repo, bm25Fmt)

	t.Run("both_constructors_seed_their_own_format", func(t *testing.T) {
		mgr.managerFor(kgtypes.GraphCode, branch)
		mgr.bm25ManagerFor(kgtypes.GraphCode, branch)

		require.ElementsMatch(t, hnswLive,
			branchBucketIDs(cacheDir, branch, hnswFmt),
			"the HNSW constructor must seed the HNSW bucket")
		require.ElementsMatch(t, bm25Live,
			branchBucketIDs(cacheDir, branch, bm25Fmt),
			"the BM25 constructor must seed its OWN bucket — the two formats index the same nodes "+
				"independently, so a seed wired into one leaves the other rebuilding from scratch")
	})

	t.Run("second_construction_does_not_re_seed", func(t *testing.T) {
		// Empty the bucket, then construct again. The memo makes this a no-op; a
		// seed that ran unconditionally would refill it.
		require.NoError(t, os.RemoveAll(graphCacheDirFor(cacheDir, kgtypes.GraphCode, branch, hnswFmt)))
		mgr.managerFor(kgtypes.GraphCode, branch)
		require.Empty(t, branchBucketIDs(cacheDir, branch, hnswFmt),
			"a second construction must not re-seed — re-seeding reintroduces partitions the branch has since "+
				"retired")
	})

	t.Run("base_graph_is_not_seeded", func(t *testing.T) {
		// The control that keeps the assertions above from passing for the wrong
		// reason: if the seed fired for every graph it would copy base's published
		// set into a SECOND base-named bucket, which is meaningless work.
		const other = "unrelated-repo"
		mgr.managerFor(kgtypes.GraphCode, other)
		require.Empty(t, branchBucketIDs(cacheDir, other, hnswFmt),
			"a graph name with no branch qualifier must not be seeded from anything")
	})
}
