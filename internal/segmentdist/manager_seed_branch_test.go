// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// manager_seed_branch_test.go — the branch segment seed: what it copies, what it
// refuses to copy, and that it runs once per graph and format.

// publishedOnlySource is a segmentSource whose List returns a FIXED published
// set, independent of what any cache holds.
//
// IT EXISTS TO SEPARATE TWO THINGS THE LOCAL SOURCE CONFLATES. On the L2-only
// path the cache IS the manifest, so a test built on it cannot tell "copies the
// published set" from "copies the whole directory" — the two are the same set
// there, and the assertion would pass against an implementation that listed the
// directory. Fixing List independently of the cache makes the difference
// observable.
type publishedOnlySource struct {
	metas []searchengine.SegmentMeta
	// onList fires inside List, which the seed calls BETWEEN capturing base's
	// rebuild record and copying the partitions. It is the only hook that can
	// reproduce that window, which is what the capture-first ordering exists to
	// close.
	onList func()
}

func (s *publishedOnlySource) List(context.Context, uint64) ([]searchengine.SegmentMeta, error) {
	if s.onList != nil {
		s.onList()
	}
	return s.metas, nil
}

func (s *publishedOnlySource) Fetch(context.Context, []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	return nil, nil
}

func (s *publishedOnlySource) Ship(context.Context, []*knowledgev1.SegmentBlobProto) ([]*knowledgev1.SegmentMetaProto, error) {
	return nil, nil
}

func (s *publishedOnlySource) Prune([]searchengine.SegmentID) (int, error)          { return 0, nil }
func (s *publishedOnlySource) PublishManifest(string, []segmentDigest) (int, error) { return 0, nil }
func (s *publishedOnlySource) verifiesCompletenessServerSide() bool                 { return false }

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
	t.Parallel()
	ctx := context.Background()
	cacheDir := t.TempDir()
	format := hnsw.New().Name()
	const repo = "seed-repo"
	const branch = "seed-repo@feature"

	// Base's cache holds THREE blobs; its manifest publishes only two of them.
	plantBlob(t, cacheDir, repo, format, "pub-one", []byte("first published"))
	plantBlob(t, cacheDir, repo, format, "pub-two", []byte("second published"))
	plantBlob(t, cacheDir, repo, format, "superseded", []byte("retired, still on disk"))

	published := &publishedOnlySource{metas: []searchengine.SegmentMeta{
		{ID: "pub-one", Format: format},
		{ID: "pub-two", Format: format},
	}}
	mgr := closeOnCleanup(t, NewManager(loginStateStub{}, cacheDir, 0, withSegmentSource(published)))

	// FIXTURE CONTROL on both sides of the measurement: base really holds all
	// three, and the branch bucket really starts empty. Without the first, the
	// "superseded is absent" assertion could pass because it was never there.
	require.Len(t, branchBucketIDs(cacheDir, repo, format), 3,
		"fixture control: base's cache must hold all three blobs, published or not")
	require.Empty(t, branchBucketIDs(cacheDir, branch, format),
		"fixture control: the branch bucket must start empty")

	seeded, err := mgr.SeedBranchBucketFromBase(ctx, kgtypes.GraphCode, repo, branch, format,
		branchEngineCache(cacheDir, branch, format, 0))
	require.NoError(t, err)
	require.Len(t, seeded, 2, "the seed must copy exactly the two published partitions")

	got := branchBucketIDs(cacheDir, branch, format)
	require.ElementsMatch(t, []searchengine.SegmentID{"pub-one", "pub-two"}, got,
		"the branch bucket must hold the published partitions and NOT the superseded one — copying a blob the "+
			"base already retired resurrects its documents into the branch")

	// The bytes are the base's, not empty placeholders.
	body, ok := newDiskSegmentCache(graphCacheDirFor(cacheDir, kgtypes.GraphCode, branch, format), 0, adviceRandom).Get("pub-one")
	require.True(t, ok)
	require.Equal(t, "first published", string(body))
}

// TestSeedBranchBucketFromBase_RefusesToOverflowTheBudget asserts an over-budget
// seed FAILS rather than copying a prefix.
//
// A PREFIX IS WORSE THAN NOTHING HERE. The cache evicts LRU on Put, so copying
// past the budget silently drops the partitions copied first and leaves a bucket
// that looks seeded while missing documents — which is precisely what a
// shipped-complete gate downstream would believe.
func TestSeedBranchBucketFromBase_RefusesToOverflowTheBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cacheDir := t.TempDir()
	format := hnsw.New().Name()
	const repo = "budget-repo"
	const branch = "budget-repo@feature"

	big := make([]byte, 4096)
	plantBlob(t, cacheDir, repo, format, "blob-a", big)
	plantBlob(t, cacheDir, repo, format, "blob-b", big)

	published := &publishedOnlySource{metas: []searchengine.SegmentMeta{
		{ID: "blob-a", Format: format},
		{ID: "blob-b", Format: format},
	}}
	// A budget that fits one blob but not both.
	mgr := closeOnCleanup(t, NewManager(loginStateStub{}, cacheDir, 5000, withSegmentSource(published)))

	seeded, err := mgr.SeedBranchBucketFromBase(ctx, kgtypes.GraphCode, repo, branch, format,
		branchEngineCache(cacheDir, branch, format, 5000))
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
	t.Parallel()
	cacheDir := t.TempDir()
	hnswFmt := hnsw.New().Name()
	bm25Fmt := bm25.New().Name()
	const repo = "ctor-repo"
	const branch = "ctor-repo@feature"

	// Base holds one partition per FORMAT, so a seed wired into only one
	// constructor leaves the other bucket visibly empty.
	plantBlob(t, cacheDir, repo, hnswFmt, "hnsw-one", []byte("hnsw payload"))
	plantBlob(t, cacheDir, repo, bm25Fmt, "bm25-one", []byte("bm25 payload"))

	published := &publishedOnlySource{metas: []searchengine.SegmentMeta{
		{ID: "hnsw-one", Format: hnswFmt},
		{ID: "bm25-one", Format: bm25Fmt},
	}}
	mgr := closeOnCleanup(t, NewManager(loginStateStub{}, cacheDir, 0, withSegmentSource(published)))

	t.Run("both_constructors_seed_their_own_format", func(t *testing.T) {
		mgr.managerFor(kgtypes.GraphCode, branch)
		mgr.bm25ManagerFor(kgtypes.GraphCode, branch)

		require.Equal(t, []searchengine.SegmentID{"hnsw-one"},
			branchBucketIDs(cacheDir, branch, hnswFmt),
			"the HNSW constructor must seed the HNSW bucket")
		require.Equal(t, []searchengine.SegmentID{"bm25-one"},
			branchBucketIDs(cacheDir, branch, bm25Fmt),
			"the BM25 constructor must seed its OWN bucket — the two formats carry separate manifests, so a "+
				"seed wired into one leaves the other rebuilding from scratch")
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
