// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// failAfterWarmSource wraps a segmentSource and, once tripped, returns a transport
// error (connect.CodeUnavailable — the 524/hang shape) from List and Fetch. It
// counts List/Fetch calls so a test can assert the L2 fallback issued ZERO server
// Fetch. Ship/Prune/PublishManifest pass through unchanged (the warm phase ships
// through this wrapper before it is tripped).
type failAfterWarmSource struct {
	inner segmentSource

	mu         sync.Mutex
	failing    bool
	fetchCalls int
	listCalls  int
}

func (c *failAfterWarmSource) trip() {
	c.mu.Lock()
	c.failing = true
	c.mu.Unlock()
}

func (c *failAfterWarmSource) fetchCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetchCalls
}

func (c *failAfterWarmSource) listCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listCalls
}

func (c *failAfterWarmSource) List(ctx context.Context, sinceGen uint64) ([]searchengine.SegmentMeta, error) {
	c.mu.Lock()
	c.listCalls++
	failing := c.failing
	c.mu.Unlock()
	if failing {
		return nil, connect.NewError(connect.CodeUnavailable, context.DeadlineExceeded)
	}
	return c.inner.List(ctx, sinceGen)
}

func (c *failAfterWarmSource) Fetch(ctx context.Context, ids []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	c.mu.Lock()
	c.fetchCalls++
	failing := c.failing
	c.mu.Unlock()
	if failing {
		return nil, connect.NewError(connect.CodeUnavailable, context.DeadlineExceeded)
	}
	return c.inner.Fetch(ctx, ids)
}

func (c *failAfterWarmSource) Ship(ctx context.Context, blobs []*knowledgev1.SegmentBlobProto) ([]*knowledgev1.SegmentMetaProto, error) {
	return c.inner.Ship(ctx, blobs)
}

func (c *failAfterWarmSource) Prune(ids []searchengine.SegmentID) (int, error) {
	return c.inner.Prune(ids)
}

func (c *failAfterWarmSource) PublishManifest(format string, digests []segmentDigest) (int, error) {
	return c.inner.PublishManifest(format, digests)
}

func (c *failAfterWarmSource) verifiesCompletenessServerSide() bool {
	return c.inner.verifiesCompletenessServerSide()
}

// bindTarget implements the targetBindable seam so a wrapped source still
// re-binds per graph. Without it the wrapper hides the inner view's own
// bindTarget (the field is named, not embedded, so nothing is promoted) and
// every graph a multi-pool test drives would resolve through one target view.
// It is a no-op for the single-graph uses above, which re-bind to the same
// target.
func (c *failAfterWarmSource) bindTarget(t *knowledgev1.GraphSelector) { bindViewTarget(c.inner, t) }

var _ segmentSource = (*failAfterWarmSource)(nil)

// TestLoadL2FallbackReachesCoverageWithoutServer is the headline red-green for the
// server-independent L2 load: with a populated L2 disk cache and a server that
// errors on ListDelta+Fetch (the slow/down/timeout shape), load() must reconstruct the resident
// set from L2 ALONE — returning nil, reaching coverage (resident >= the shipped
// doc count), issuing ZERO server Fetch, and leaving importedGen unmoved so a
// later recovery re-Lists from the same floor.
//
// RED pre-fix: load() returns the List error at the source.List error arm (resident
// stays 0). GREEN after the load() L2 fallback lands.
func TestLoadL2FallbackReachesCoverageWithoutServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "l2-fallback"
	dir := t.TempDir()

	// --- WARM PHASE: seal a coverage-passing corpus and populate the L2 disk cache.
	// >= residentBackstopFloor (64) single-doc segments so a reload reaches the
	// coverage floor from L2 alone. High count target so no merge perturbs seeding.
	const corpusN = residentBackstopFloor + 8
	corpus := vecContentDocs(corpusN)

	warm, warmIC := buildHNSWReclaimManager(t, gt, name, dir, 1<<30)
	for _, d := range corpus {
		require.NoError(t, warm.engine.Add([]searchengine.Document{d}))
	}
	// Direct .seg warm: write every sealed segment's bytes into the L2 disk cache
	// (the deterministic fixture route — no server round-trip needed to populate L2).
	warmed := warmExported(warm)
	require.GreaterOrEqual(t, len(warmed), corpusN, "every single-doc segment warmed into L2")
	require.Equal(t, corpusN, warm.engine.ResidentDocCount(), "warm engine is coverage-passing")
	_ = warmIC
	warm.engine.Close()

	// --- RECONSTRUCT over the SAME dir with an ERRORING caller. A fresh
	// diskSegmentCache scans the warmed dir; the caller errors on every ListDelta
	// and Fetch, modeling a slow/down server at restart.
	_, gc := newSegmentHarness(t)
	fail := &failAfterWarmSource{inner: gc}
	fail.trip() // server is down for the whole reconstructed-manager lifetime.

	dm, _ := buildHNSWReclaimManagerOn(t, fail, gt, name, dir, 1<<30)
	defer dm.engine.Close()
	require.Equal(t, uint64(0), dm.importedGen.Load(), "cold manager: importedGen is 0")
	require.Equal(t, 0, dm.engine.ResidentDocCount(), "cold manager: engine empty before load")

	// --- LOAD must fall back to L2: no error, coverage reached, zero Fetch.
	require.NoError(t, dm.load(ctx), "load() falls back to L2 on the server List error")
	require.GreaterOrEqual(t, dm.engine.ResidentDocCount(), corpusN,
		"load() reached coverage from L2 alone (resident >= shipped doc count)")
	require.Equal(t, 0, fail.fetchCount(),
		"L2 fallback issued ZERO server Fetch — every id was a cache hit")
	require.Equal(t, uint64(0), dm.importedGen.Load(),
		"importedGen unmoved: the server manifest was never obtained, so the load floor stays put")
}

// TestLoadL2FirstPrimaryPathIssuesZeroList is the HEADLINE red-green for the
// L2-first flip: with a populated L2 disk cache AND a fully REACHABLE server,
// load() must reconstruct the resident set from L2 on the PRIMARY path WITHOUT EVEN
// ISSUING a server List — proving startup is server-independent BY DESIGN, not just
// on a List error.
//
// This is strictly stronger than TestLoadL2FallbackReachesCoverageWithoutServer
// (which trips the caller so a List ERROR forces the L2 path): here the server is
// up, so a List would succeed — yet the L2-first load() never calls it.
//
// RED on a Phase-1-reverted tree (load() Lists the server first): listCount() >= 1
// even though L2 is populated. GREEN after the flip: the L2-first path returns
// before any List, so listCount()==0 AND fetchCount()==0.
func TestLoadL2FirstPrimaryPathIssuesZeroList(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "l2-first-primary"
	dir := t.TempDir()

	// --- WARM PHASE: seal a coverage-passing corpus, SHIP it to the server, AND
	// populate the L2 disk cache. Both sides hold the corpus — the realistic restart
	// shape: a reachable server that COULD answer a List, plus a populated L2. This
	// makes the red-green discriminate on listCount(): on a reverted (L3-first) tree
	// the server List succeeds (so resident IS reached, but listCount() >= 1); only
	// the L2-first flip reaches coverage with listCount()==0.
	const corpusN = residentBackstopFloor + 8
	corpus := vecContentDocs(corpusN)

	// One shared harness caller so the warm ship and the reconstructed load() hit the
	// SAME server.
	_, gc := newSegmentHarness(t)
	warm, _ := buildHNSWReclaimManagerOn(t, gc, gt, name, dir, 1<<30)
	for _, d := range corpus {
		require.NoError(t, warm.engine.Add([]searchengine.Document{d}))
	}
	_, err := warm.ship(ctx, warm.locallyShipped) // corpus lands on the server too.
	require.NoError(t, err)
	warmed := warmExported(warm)
	require.GreaterOrEqual(t, len(warmed), corpusN, "every single-doc segment warmed into L2")
	require.Equal(t, corpusN, warm.engine.ResidentDocCount(), "warm engine is coverage-passing")
	warm.engine.Close()

	// --- RECONSTRUCT over the SAME dir + SAME server with a REACHABLE (NON-tripped)
	// caller. The server is up and HOLDS the corpus — a List would succeed and return
	// it — but the L2-first primary path must never reach it.
	reachable := &failAfterWarmSource{inner: gc} // NOT tripped: the server is reachable.

	dm, _ := buildHNSWReclaimManagerOn(t, reachable, gt, name, dir, 1<<30)
	defer dm.engine.Close()
	require.Equal(t, uint64(0), dm.importedGen.Load(), "cold manager: importedGen is 0")
	require.Equal(t, 0, dm.engine.ResidentDocCount(), "cold manager: engine empty before load")

	// --- LOAD must take the L2-first PRIMARY path: coverage reached, ZERO List, ZERO
	// Fetch — even though the server is reachable.
	require.NoError(t, dm.load(ctx), "L2-first load() imports the resident set from L2")
	require.GreaterOrEqual(t, dm.engine.ResidentDocCount(), corpusN,
		"load() reached coverage from L2 alone (resident >= shipped doc count)")
	require.Equal(t, 0, reachable.listCount(),
		"L2-first primary path issues ZERO server List — the resident set is built from L2 before any List")
	require.Equal(t, 0, reachable.fetchCount(),
		"L2-first primary path issues ZERO server Fetch — every id was an L2 cache hit")
	require.Equal(t, uint64(0), dm.importedGen.Load(),
		"importedGen unmoved: the L2-first path never obtained a server manifest, so the load floor stays put")
}

// TestLoadL2PartialImportToleratesRacedMissWithServerDown is the red-green for
// Regression: the L2-first import must NOT be all-or-nothing. When a
// constituent id is evicted (a reclaimMerged/InvalidateLocal Remove races the
// Keys() snapshot, so the id is in the snapshot but a cache MISS by the time
// reload's Get reaches it) AND the server is unreachable for that miss, load()
// must still import the AVAILABLE L2 hits and serve the partial self-verifying
// superset — not abort the whole import.
//
// The instrumentedCache's removeAfterKeys arms exactly that race: the id is
// returned in Keys() but evicted from the inner cache immediately after, so
// reload sees it as a miss and routes it to the (tripped → erroring) server Fetch.
//
// RED pre-fix: reload returns the fetchMisses error before engine.Import, so
// loadResidentFromL2 propagates a non-sentinel error, load() returns it, and the
// engine stays empty (ResidentDocCount == 0). GREEN after the tolerateMisses fix:
// reload swallows the unfetchable miss, imports the available hits, load() returns
// nil, and the engine holds the corpus minus the single raced-out segment.
func TestLoadL2PartialImportToleratesRacedMissWithServerDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "l2-partial-import"
	dir := t.TempDir()

	// --- WARM PHASE: seal a coverage-passing corpus into the L2 disk cache (single
	// -doc segments; high count target so no merge perturbs seeding).
	const corpusN = residentBackstopFloor + 8
	corpus := vecContentDocs(corpusN)

	warm, _ := buildHNSWReclaimManager(t, gt, name, dir, 1<<30)
	for _, d := range corpus {
		require.NoError(t, warm.engine.Add([]searchengine.Document{d}))
	}
	warmed := warmExported(warm)
	require.GreaterOrEqual(t, len(warmed), corpusN, "every single-doc segment warmed into L2")
	require.Equal(t, corpusN, warm.engine.ResidentDocCount(), "warm engine is coverage-passing")
	warm.engine.Close()

	// --- RECONSTRUCT over the SAME dir with a DOWN server. Arm removeAfterKeys with
	// one resident id so it is present in the Keys() snapshot but a cache MISS when
	// reload's Get reaches it — and the tripped caller errors on the resulting Fetch.
	_, gc := newSegmentHarness(t)
	fail := &failAfterWarmSource{inner: gc}
	fail.trip() // server is down: the raced-out id can never be Fetched.

	dm, ic := buildHNSWReclaimManagerOn(t, fail, gt, name, dir, 1<<30)
	defer dm.engine.Close()

	resident := ic.Keys()
	require.GreaterOrEqual(t, len(resident), corpusN, "reconstructed L2 holds the full resident set")
	ic.removeAfterKeys = resident[0] // this id will be a raced miss the down server can't serve.

	require.Equal(t, 0, dm.engine.ResidentDocCount(), "cold manager: engine empty before load")

	// --- LOAD must tolerate the unfetchable miss and import the available hits.
	require.NoError(t, dm.load(ctx),
		"L2-first load() imports the available hits despite one raced, unfetchable miss")
	require.GreaterOrEqual(t, dm.engine.ResidentDocCount(), corpusN-1,
		"load() imported the corpus minus the single raced-out segment (partial superset served)")
	require.Equal(t, uint64(0), dm.importedGen.Load(),
		"importedGen unmoved: the L2-first path never obtained a server manifest")
}

// TestLoadL2FallbackEmptyCacheReturnsListError pins the preserved behavior: a
// genuinely cold/wiped L2 (no cached segments) + an erroring server returns the
// original List error from load() — there is nothing to fall back to.
func TestLoadL2FallbackEmptyCacheReturnsListError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "l2-empty"
	dir := t.TempDir() // empty — no .seg files warmed.

	_, gc := newSegmentHarness(t)
	fail := &failAfterWarmSource{inner: gc}
	fail.trip()

	dm, _ := buildHNSWReclaimManagerOn(t, fail, gt, name, dir, 1<<30)
	defer dm.engine.Close()

	err := dm.load(ctx)
	require.Error(t, err, "empty L2 + erroring server: load surfaces the original List error")
	require.Equal(t, 0, dm.engine.ResidentDocCount(), "nothing imported on an empty-L2 fallback")
}
