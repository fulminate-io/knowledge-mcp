// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"crypto/rsa"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// The fixture sizes below are DERIVED from DefaultMinSegmentDocs, not chosen, and
// the two precondition assertions in reBucketBehindFixture pin the derivation so a
// drift in that constant fails loudly instead of silently re-sizing the fixture.
//
// Seeding one partition count and confining the growth window to ONE of its
// partitions leaves, after the drain, (grown/seed + seed-1) resident segments
// against a derived grown count. That expression is unsatisfiable as a full
// doubling at a seed count of 2 or below, so 4 partitions growing to 16 is the
// smallest fixture that both reproduces the defect and can be made green.
const (
	reBucketSeedN   = 2049                            // ceil(2049/1024) = 3 -> 4 partitions
	reBucketWindowN = 6144                            // confined to partition 0 of the seed count
	reBucketCorpusN = reBucketSeedN + reBucketWindowN // 8193 -> ceil(8193/1024) = 9 -> 16 partitions

	reBucketSeedCount    = 4
	reBucketDerivedCount = 16
)

// docsInBucketFor builds n documents whose ids all hash to the given partition
// under the given count, so a growth window can be aimed at PART of the partition
// space. It is this package's counterpart of the segmentdist generator of the same
// shape, which lives in another package and cannot be imported across the boundary.
//
// Roughly one id in `count` lands in the target bucket. Exhausting the attempt cap
// is a hard stop rather than a short slice: a window quietly shorter than asked for
// would move the corpus size every other number here is derived from.
func docsInBucketFor(t *testing.T, bucket, count, n int, prefix string) []searchengine.Document {
	t.Helper()
	rng := rand.New(rand.NewPCG(0x5EED, 0xB0CC))
	out := make([]searchengine.Document, 0, n)
	for i := 0; len(out) < n; i++ {
		if i > n*10000 {
			t.Fatalf("could not find %d ids in bucket %d of %d", n, bucket, count)
		}
		id := fmt.Sprintf("%s%d", prefix, i)
		if searchengine.BucketOf(id, count) != bucket {
			continue
		}
		v := make([]byte, 32)
		for j := range v {
			v[j] = byte(rng.UintN(256))
		}
		out = append(out, searchengine.Document{ID: id, Vector: v})
	}
	return out
}

// seedRebuildScanPage installs the graph's rebuild scan page, derived from the SAME
// documents the engine holds.
//
// A reset rebuild builds from the scan PAGE, never from the resident engine, so a
// page that disagreed with the corpus would turn every post-rebuild assertion in
// this file into a measurement of the page instead of of the layout.
func seedRebuildScanPage(eng *reconcileEngine, repo string, docs []searchengine.Document) {
	page := make([]*knowledgev1.PipelineScanItem, 0, len(docs))
	for _, d := range docs {
		vec := make([]byte, len(d.Vector))
		copy(vec, d.Vector)
		page = append(page, &knowledgev1.PipelineScanItem{
			NodeId:       d.ID,
			GraphName:    repo,
			BinaryVector: vec,
			Bm25Fields:   &knowledgev1.Bm25Fields{SymbolName: d.ID},
		})
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.scanItems[repo] = page
}

// reBucketBehindFixture builds the steady state every test in this file starts
// from: a HEALTHY graph whose published layout is a FULL DOUBLING behind the count
// its corpus now derives, with its rebuild scan page already seeded.
//
// THE WINDOW IS CONFINED ON PURPOSE, and this is the one thing about the fixture
// that cannot be relaxed. A re-emit rebuilds its dirty partitions closed under
// constituency, so a window whose ids spread across the id space dirties every
// partition of the new count, consumes every old segment, and realigns the WHOLE
// layout in that single drain — leaving nothing behind for any trigger to find. A
// fixture built that way is green today and reproduces nothing. Confining the window
// to one partition of the seed count is what leaves the partitions no write ever
// reached still carrying their old alignment.
//
// After the drain the window's own partitions and their one shared constituent have
// been rebuilt while the untouched seed segments stand as they were, so the layout
// sits at 7 segments against a derived 16 — behind, but perfectly healthy.
func reBucketBehindFixture(t *testing.T) (*client, *reconcileEngine, string, int) {
	t.Helper()
	ctx := opCtx()

	require.Equal(t, reBucketSeedCount, searchengine.BucketCountFor(reBucketSeedN),
		"FIXTURE PRECONDITION: the seed must derive %d partitions — if the minimum segment size drifts, every later assertion here is vacuous",
		reBucketSeedCount)
	require.Equal(t, reBucketDerivedCount, searchengine.BucketCountFor(reBucketCorpusN),
		"FIXTURE PRECONDITION: the grown corpus must derive %d partitions — two full doublings past the seed",
		reBucketDerivedCount)

	shared := reBucketSharedCorpus(t)

	// A fresh client over a backend carrying the already-built corpus, then a cold
	// load: the same steady state, reached by reading the segments rather than by
	// re-deriving them.
	c, eng, _, _ := buildReconcileClientOnBackend(t, shared.seedBackend(t), 0, reBucketRepo)
	seedRebuildScanPage(eng, reBucketRepo, shared.corpus)
	loaded, err := c.segmentMgr.LoadResidentDocCount(ctx, kgtypes.GraphCode, reBucketRepo)
	require.NoError(t, err, "the fixture client must load the shared corpus from its backend")
	require.Equal(t, reBucketCorpusN, loaded,
		"FIXTURE PRECONDITION: the loaded corpus must be the whole %d documents — a short load would move every count derived below",
		reBucketCorpusN)

	return c, eng, reBucketRepo, reBucketCorpusN
}

// reBucketRepo is the graph the shared corpus is built against. It is FIXED because
// the published manifest and every stored object are keyed by it; a caller using a
// different name would find an empty backend.
const reBucketRepo = "reBucketBehindRepo"

// reBucketCorpusState is the built steady state, captured once: the backend's stored
// objects and published manifests, the key their DEKs are wrapped to, and the
// documents the rebuild scan page is derived from.
type reBucketCorpusState struct {
	objects   map[string][]byte
	manifests map[string][]segManifestDigest
	priv      *rsa.PrivateKey
	corpus    []searchengine.Document
}

// seedBackend returns a NEW backend preloaded with the captured corpus.
//
// THE SIGNING KEY TRAVELS WITH THE BYTES, and it is not optional: every stored
// object's data key is wrapped to the backend's public key, so a backend that
// generated a fresh key would hold objects it cannot decrypt and the load would come
// back empty — which the fixture's own loaded-corpus precondition would catch.
//
// The maps are COPIED per caller. Callers ship, publish and prune against their own
// backend, and a shared map would let one caller's writes move another's manifest.
func (s *reBucketCorpusState) seedBackend(t *testing.T) *fakeSegBackend {
	t.Helper()
	b := newFakeSegBackend(t)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.priv = s.priv
	b.objects = make(map[string][]byte, len(s.objects))
	for k, v := range s.objects {
		b.objects[k] = append([]byte(nil), v...)
	}
	b.manifests = make(map[string][]segManifestDigest, len(s.manifests))
	for k, v := range s.manifests {
		b.manifests[k] = append([]segManifestDigest(nil), v...)
	}
	return b
}

var (
	reBucketCorpusOnce  sync.Once
	reBucketCorpusBuilt *reBucketCorpusState
)

// reBucketSharedCorpus builds the behind-the-boundary corpus ONCE for this file and
// hands every caller the bytes to reload it from.
//
// The corpus is the whole cost of this fixture: two vector-index builds over 8193
// documents and a drain, paid once per test where each test only needs the state
// they leave behind. Reloading that state from the backend is milliseconds.
//
// WHAT IS SHARED IS THE ARTIFACT, NOT A LIVE HARNESS. Each caller gets its own
// backend, client, reconcile engine, scan page and cache directory; only the stored
// segment bytes and the manifest cross between them. Sharing the harness itself
// would make callers order-dependent — one publishes a converged layout and the next
// finds nothing behind to trigger on, which is precisely the state these tests are
// about.
//
// It is built under the first caller's t. Only the in-memory maps and the key are
// retained, so that caller's server and directories may be torn down freely
// afterwards.
func reBucketSharedCorpus(t *testing.T) *reBucketCorpusState {
	t.Helper()
	reBucketCorpusOnce.Do(func() {
		ctx := opCtx()
		c, eng, backend := buildReconcileClientWithSeg(t, 0, reBucketRepo)

		seed := fastloadVecDocs("rebucket-seed", reBucketSeedN)
		window := docsInBucketFor(t, 0, reBucketSeedCount, reBucketWindowN, "rebucket-win-")
		corpus := make([]searchengine.Document, 0, len(seed)+len(window))
		corpus = append(corpus, seed...)
		corpus = append(corpus, window...)
		seedRebuildScanPage(eng, reBucketRepo, corpus)

		// Seed an ALIGNED corpus through the one-shot corpus-complete path, which
		// derives its count from resident plus incoming and lands the seed count.
		require.NoError(t, c.segmentMgr.ReplaceBucket(ctx, kgtypes.GraphCode, reBucketRepo, nil, seed))
		// Grow two doublings past the boundary through the DELTA path alone — never a
		// reset, since reaching the new count WITHOUT one is the whole question.
		require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, reBucketRepo, window))
		// Drain once, so the graph is in the quiet steady state a real one idles in
		// between ticks rather than caught mid-write.
		require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, reBucketRepo))

		backend.mu.Lock()
		defer backend.mu.Unlock()
		state := &reBucketCorpusState{
			priv:      backend.priv,
			corpus:    corpus,
			objects:   make(map[string][]byte, len(backend.objects)),
			manifests: make(map[string][]segManifestDigest, len(backend.manifests)),
		}
		for k, v := range backend.objects {
			state.objects[k] = append([]byte(nil), v...)
		}
		for k, v := range backend.manifests {
			state.manifests[k] = append([]segManifestDigest(nil), v...)
		}
		reBucketCorpusBuilt = state
	})
	require.NotNil(t, reBucketCorpusBuilt, "the shared corpus builder must have run")
	return reBucketCorpusBuilt
}

// TestQuietGraphReBucketsAfterCrossingTheBoundary is the reproduction, and it
// asserts the DESIRED behavior rather than the defect: a graph that grew across two
// doublings and then stopped writing must re-bucket itself within one reconcile
// pass. A test named for what the system does today would pass today and never go
// red — a characterization of the defect wearing a reproduction's name.
//
// THE GRAPH IS HEALTHY BY CONSTRUCTION, and that is the gate rather than a detail.
// The per-graph cascade returns early for a healthy graph, so nothing but a
// re-bucket trigger can page the scanner for it; a degenerate fixture would be
// rebuilt by the heal arm and this test would pass against a tick that gained
// nothing.
func TestQuietGraphReBucketsAfterCrossingTheBoundary(t *testing.T) {
	ctx := opCtx()
	c, _, repo, corpusN := reBucketBehindFixture(t)

	degenerate, err := c.segmentMgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.False(t, degenerate,
		"PRECONDITION: the fixture graph must be HEALTHY, so the cascade reaches its healthy-graph return and only a re-bucket trigger can rebuild it")

	c.reconcileSegmentCoverage(ctx)

	// Read the layout BACK FROM THE SOURCE rather than from an in-process number: the
	// published manifest is the only value that can disagree with what this process
	// believes it laid out.
	published, err := c.segmentMgr.PublishedManifestCount(ctx, kgtypes.GraphCode, repo, hnsw.New().Name())
	require.NoError(t, err)
	require.Equal(t, searchengine.BucketCountFor(corpusN), published,
		"FUL1060-NO-REBUCKET: a quiet graph that crossed the boundary must re-bucket within one reconcile pass — "+
			"the published layout stands at %d partitions against the %d its %d-document corpus now derives",
		published, searchengine.BucketCountFor(corpusN), corpusN)
}

// TestReconcileTickDrivesTheReBucketTrigger is the WIRING gate, asserted through the
// rebuild DRIVER — the scan page count — rather than a synthesized call, so a
// detector that is never consulted from the pass cannot satisfy it.
//
// THE SECOND, ALIGNED GRAPH IS WHAT MAKES THIS A TRIGGER TEST. It rides the same
// pass and must be scanned ZERO times; without it any arm that rebuilds everything
// would pass. Both graphs are asserted HEALTHY first, so the heal arm cannot page
// the scanner and steal the credit for either result.
func TestReconcileTickDrivesTheReBucketTrigger(t *testing.T) {
	ctx := opCtx()
	c, eng, behindRepo, _ := reBucketBehindFixture(t)

	const alignedRepo = "reBucketAlignedRepo"
	eng.namesByType[string(kgtypes.GraphCode)] = []string{behindRepo, alignedRepo}

	// The aligned graph: seeded and drained, so its layout equals the count its own
	// corpus derives and the detector has nothing to find.
	aligned := fastloadVecDocs("rebucket-aligned", reBucketSeedN)
	seedRebuildScanPage(eng, alignedRepo, aligned)
	require.NoError(t, c.segmentMgr.ReplaceBucket(ctx, kgtypes.GraphCode, alignedRepo, nil, aligned))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, alignedRepo))

	for _, repo := range []string{behindRepo, alignedRepo} {
		degenerate, err := c.segmentMgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, repo)
		require.NoError(t, err)
		require.False(t, degenerate,
			"PRECONDITION: %s must be HEALTHY, or the heal arm could page the scanner and this test would credit the trigger for it", repo)
		require.Equal(t, 0, eng.scanCallCount(repo),
			"PRECONDITION: %s has not been scanned before the act", repo)
	}

	c.reconcileSegmentCoverage(ctx)

	require.GreaterOrEqual(t, eng.scanCallCount(behindRepo), 1,
		"the tick must consult the detector and run a reset rebuild for the graph that is a doubling behind")
	require.Equal(t, 0, eng.scanCallCount(alignedRepo),
		"an ALIGNED graph in the same pass must not be rebuilt — a trigger that fires on everything is not a trigger")
}

// TestReBucketTriggerLatchesAfterALandedReset is the storm-preventer of last resort:
// one crossing must yield exactly ONE reset.
//
// IT RUNS HERE RATHER THAN BESIDE THE DETECTOR because the latch closes when a RESET
// LANDS, and only the reconcile tick lands one. The two passes are driven
// synchronously, so the first rebuild has completed before the second begins and the
// single-flight cannot mask a second fire.
//
// THE CATCHER cuts both ways: this goes red for a trigger that re-fires on a
// converged layout, and equally red for one whose reset never actually replaces the
// layer — because then the layout never converges and pass two fires again.
func TestReBucketTriggerLatchesAfterALandedReset(t *testing.T) {
	ctx := opCtx()
	c, eng, repo, corpusN := reBucketBehindFixture(t)

	degenerate, err := c.segmentMgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.False(t, degenerate,
		"PRECONDITION: the fixture graph must be HEALTHY, so only a re-bucket trigger can rebuild it")

	c.reconcileSegmentCoverage(ctx)

	// Record the exact count: a latch assertion over a trigger that never fired is
	// vacuous, and this is its guard.
	afterFirstPass := eng.scanCallCount(repo)
	require.GreaterOrEqual(t, afterFirstPass, 1,
		"PASS ONE must have fired the trigger, or the silence asserted below proves nothing")

	published, err := c.segmentMgr.PublishedManifestCount(ctx, kgtypes.GraphCode, repo, hnsw.New().Name())
	require.NoError(t, err)
	require.Equal(t, searchengine.BucketCountFor(corpusN), published,
		"the landed reset must have CONVERGED the published layout — the latch rests on that and on no stored state")

	c.reconcileSegmentCoverage(ctx)

	require.Equal(t, afterFirstPass, eng.scanCallCount(repo),
		"PASS TWO must fire NOTHING: a converged layout that re-fires is the per-tick rebuild storm the full-doubling rule exists to prevent")
}
