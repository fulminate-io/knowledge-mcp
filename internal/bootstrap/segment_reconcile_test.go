// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// reconcileEngine is an EngineServiceHandler for the segment-reconcile tests. It
// (a) enumerates per-graph-type instance names via Execute(RETURN_MODE_GRAPH_NAMES)
// so the reconcile's per-type discovery (tools.ListGraphNamesOfType, now called for
// every embeddable builtin) sees them — keyed by the requested graph type so a test
// can register code repos AND non-code instances (e.g. a cloud account); (b) serves
// a configurable segment_rebuild scan page per graph via PipelineScan, RECORDING the
// call count per graph so a test can assert a rebuild fired (RebuildSegments pages
// PipelineScan only when the probe reports degenerate); (c) serves a fixed Stats.
// It embeds countingEngine to inherit the rest of the handler surface.
type reconcileEngine struct {
	*countingEngine
	// namesByType maps a graph-type string (e.g. "code", "cloud") to the instance
	// names RETURN_MODE_GRAPH_NAMES serves for that type.
	namesByType map[string][]string
	// embedded is the BinaryVectorCount Stats serves — the embedded denominator
	// segmentPoolDegenerate reads (via tools.GraphEmbeddedCount). It defaults to 0
	// (the empty-Stats behavior the resident-vs-shipped reconcile tests rely on:
	// they probe ReconcileResidentDegenerate, NOT segmentPoolDegenerate, so they
	// never read BinaryVectorCount). A nonzero value arms segmentPoolDegenerate for
	// the heal-closure red-green, which gates on it.
	embedded int32

	mu        sync.Mutex
	scanItems map[string][]*knowledgev1.PipelineScanItem // keyed by graph name
	// scanCalls counts REBUILD-shaped PipelineScan calls per graph name, and the
	// operation split is load-bearing rather than bookkeeping. Two callers page this
	// axis: the rebuild driver and the reconcile pass's bounded tombstone-delta read.
	// Every "no rebuild fired" assertion in this package operationalizes itself as
	// "the scanner was never paged", so counting BOTH would make each of those
	// assertions fire on a delta read — and, worse, would make the mirror-image
	// "a rebuild DID fire" assertions pass without one.
	scanCalls map[string]int
	// deltaScanCalls counts the tombstone-delta reads per graph name.
	deltaScanCalls map[string]int
	// scanErr, when set, makes every PipelineScan return it — the lever a test needs
	// to model a rebuild that does NOT complete (RebuildSegments surfaces the scan
	// error as ran=false). Zero-valued by default, so no existing fixture is affected.
	scanErr error
	// servedHorizon is echoed on every page as the server's safe horizon. Zero by
	// default, which is exactly what an unset fixture means: this scan was served no
	// horizon, so no caller may persist one from it.
	servedHorizon int64
	// deltaSince records the afterStampedAtNanos ARGUMENT of every delta-merge scan,
	// per graph, in order. It is the instrument for "the next window starts from the
	// horizon the last one was served", which no response-side assertion can see —
	// seeding from a local clock instead would look identical on every other counter.
	deltaSince map[string][]int64
	// horizonSeedCalls counts the declined-graph seed's SINGLE-ROW horizon probe per
	// graph. It is the instrument for "at most once per graph per process", which
	// without a counter degrades to "a horizon was eventually written" — a claim a
	// probe re-issued on every rotation forever satisfies just as well.
	horizonSeedCalls map[string]int
}

// setScanErr arms (or clears) the injected PipelineScan failure under the same mutex
// the handler reads it through, so a test goroutine flipping it never races the
// in-flight handler goroutine.
func (e *reconcileEngine) setScanErr(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.scanErr = err
}

func (e *reconcileEngine) Execute(
	_ context.Context, req *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	q := req.Msg.GetQuery()
	if q != nil && q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		names := e.namesByType[req.Msg.GetTarget().GetGraph()]
		infos := make([]*knowledgev1.GraphInfo, 0, len(names))
		for _, n := range names {
			infos = append(infos, &knowledgev1.GraphInfo{Name: n})
		}
		return connect.NewResponse(&knowledgev1.ExecuteResponse{GraphNames: infos}), nil
	}
	return connect.NewResponse(&knowledgev1.ExecuteResponse{}), nil
}

func (e *reconcileEngine) Stats(
	context.Context, *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	return connect.NewResponse(&knowledgev1.StatsResponse{
		GraphStats: &knowledgev1.GraphStats{BinaryVectorCount: e.embedded},
	}), nil
}

func (e *reconcileEngine) PipelineScan(
	_ context.Context, req *connect.Request[knowledgev1.PipelineScanRequest],
) (*connect.Response[knowledgev1.PipelineScanResponse], error) {
	name := req.Msg.GetGraphName()
	e.mu.Lock()
	// THE THREE BUCKETS ARE THE THREE LOAD SHAPES over this one axis, and keeping them
	// apart is exactly why each carries its own operation term: scanCalls counts
	// full-corpus reads (rebuild and repair), deltaScanCalls counts bounded delta
	// windows, and horizonSeedCalls counts the single-row horizon probe. Folding the
	// probe into scanCalls would make every landed full-scan count assertion read one
	// higher for a read that pages one row.
	switch req.Msg.GetClientContext().GetOperation() {
	case string(graphclient.OpSegmentDeltaMerge):
		e.deltaScanCalls[name]++
		if e.deltaSince == nil {
			e.deltaSince = map[string][]int64{}
		}
		e.deltaSince[name] = append(e.deltaSince[name], req.Msg.GetAfterStampedAtNanos())
	case string(graphclient.OpSegmentHorizonSeed):
		if e.horizonSeedCalls == nil {
			e.horizonSeedCalls = map[string]int{}
		}
		e.horizonSeedCalls[name]++
	default:
		e.scanCalls[name]++
	}
	// Capture the injected failure AFTER the increment, so an erroring scan still
	// counts as an invocation — the counter's contract is "PipelineScan calls", and
	// keeping it means an erroring and a succeeding fixture are compared on the same
	// observable.
	scanErr := e.scanErr
	horizon := e.servedHorizon
	// Serve the page ONCE per graph (afterId empty), then an empty page so the
	// id-cursor scan terminates.
	var items []*knowledgev1.PipelineScanItem
	if req.Msg.GetAfterId() == "" {
		items = e.scanItems[name]
	}
	e.mu.Unlock()
	if scanErr != nil {
		return nil, scanErr
	}
	// The horizon rides EVERY page, including the empty terminating one, which is what
	// makes "the last page observed bounds the drain" true.
	return connect.NewResponse(&knowledgev1.PipelineScanResponse{
		Items: items, ServedHorizonNanos: horizon,
	}), nil
}

// deltaSinceArgs reports the afterStampedAtNanos each delta-merge scan asked for.
func (e *reconcileEngine) deltaSinceArgs(name string) []int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]int64(nil), e.deltaSince[name]...)
}

// horizonSeedCallCount reports how many single-row horizon probes the graph paid.
func (e *reconcileEngine) horizonSeedCallCount(name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.horizonSeedCalls[name]
}

// setServedHorizon arms the horizon every page echoes, under the handler's mutex.
func (e *reconcileEngine) setServedHorizon(h int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.servedHorizon = h
}

func (e *reconcileEngine) scanCallCount(name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.scanCalls[name]
}

// deltaScanCallCount reports how many BOUNDED tombstone-delta reads the graph paid.
func (e *reconcileEngine) deltaScanCallCount(name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.deltaScanCalls[name]
}

// makeReconcileScanPage builds n segment_rebuild scan items with 32-byte vectors —
// the embedded nodes a rebuild reads to re-build the searchable corpus.
func makeReconcileScanPage(graph string, n int) []*knowledgev1.PipelineScanItem {
	page := make([]*knowledgev1.PipelineScanItem, 0, n)
	for i := range n {
		id := fmt.Sprintf("%s-%08d", graph, i)
		vec := make([]byte, 32)
		vec[0] = byte(i)
		page = append(page, &knowledgev1.PipelineScanItem{
			NodeId:       id,
			GraphName:    graph,
			BinaryVector: vec,
			Bm25Fields:   &knowledgev1.Bm25Fields{SymbolName: id},
		})
	}
	return page
}

// TestReconcileSegmentCoverage_HealthyNoRebuild proves a graph whose probe reports
// HEALTHY (here: a sub-floor shipped corpus, which the resident-vs-shipped probe
// disarms) triggers NO rebuild — PipelineScan is never paged for it.
func TestReconcileSegmentCoverage_HealthyNoRebuild(t *testing.T) {
	c, eng, backend := buildReconcileClientWithSeg(t, 0, "healthyRepo")
	ctx := context.Background()

	// Sub-floor shipped corpus (4 < floor 64) → the probe disarms → not degenerate.
	shipHNSW(t, backend, "healthyRepo", 4)
	// Seed a scan page so that, IF a rebuild wrongly fired, it would page the scanner
	// (making the no-rebuild assertion meaningful).
	eng.scanItems["healthyRepo"] = makeReconcileScanPage("healthyRepo", 10)

	c.reconcileSegmentCoverage(ctx)

	require.Equal(t, 0, eng.scanCallCount("healthyRepo"),
		"a healthy (disarmed) graph triggers NO RebuildSegments — PipelineScan never paged")
}

// TestReconcileSegmentCoverage_DegenerateRebuilds proves a graph whose SHIPPED corpus
// is GENUINELY INCOMPLETE vs its embedded node count — and whose read engine cannot
// restore it — triggers a RebuildSegments: PipelineScan is paged for it. Post shipped-completeness gate
// the reconcile rebuilds only past the healNeedsRebuild shipped-completeness gate, so
// embedded=300 arms segmentPoolDegenerate (covered 128 < 0.5*300) — a REAL regen, not
// a merely lazily-loaded read engine (which the gate now correctly skips; see
// TestReconcileSegmentCoverage_ShippedCompleteNoRebuild).
func TestReconcileSegmentCoverage_DegenerateRebuilds(t *testing.T) {
	c, eng, backend := buildReconcileClientWithSeg(t, 300, "degenRepo")
	ctx := context.Background()

	// Shipped corpus 128 (>= floor so ReconcileResidentDegenerate arms) but genuinely
	// incomplete vs the 300 embedded nodes, and the empty Fetch means load imports
	// nothing → live resident stays 0 → degenerate AND healNeedsRebuild==true.
	shipHNSW(t, backend, "degenRepo", 64, 64)
	eng.scanItems["degenRepo"] = makeReconcileScanPage("degenRepo", 10)

	c.reconcileSegmentCoverage(ctx)

	require.GreaterOrEqual(t, eng.scanCallCount("degenRepo"), 1,
		"a genuinely-incomplete shipped corpus triggers RebuildSegments — PipelineScan is paged")
}

// TestReconcileSegmentCoverage_DegenerateNonCodeRebuilds proves the reconcile now
// enumerates + heals NON-code embeddable builtins: a cloud graph (keyed by account,
// enumerated via the per-type ListGraphNamesOfType the HasRebuildableSegments loop
// now calls for cloud) whose shipped corpus is genuinely incomplete vs its embedded
// count (>= floor so the probe arms, live resident empty after the empty Fetch, and
// past the healNeedsRebuild gate via embedded=300) triggers exactly one
// RebuildSegments — PipelineScan is paged for it. Under a code-only enumeration the
// cloud graph would never be discovered, so no rebuild would fire.
func TestReconcileSegmentCoverage_DegenerateNonCodeRebuilds(t *testing.T) {
	c, eng, backend := buildReconcileClientWithSeg(t, 300) // no code repos — exercise the non-code path alone; embedded=300 arms the shipped-completeness gate.
	ctx := context.Background()

	// Register a cloud account instance so the reconcile's per-type enumeration
	// (ListGraphNamesOfType for "cloud") discovers it.
	eng.namesByType[string(kgtypes.GraphCloud)] = []string{"acct"}

	// Shipped corpus 128 (>= floor) under the cloud account selector but genuinely
	// incomplete vs the 300 embedded nodes (covered 128 < 0.5*300 → healNeedsRebuild
	// gate confirms a real regen), and the empty Fetch means load imports nothing →
	// live resident stays 0 → degenerate. PipelineScan is keyed by the instance name.
	shipHNSWFor(t, backend, kgtypes.GraphCloud, "acct", 64, 64)
	eng.scanItems["acct"] = makeReconcileScanPage("acct", 10)

	c.reconcileSegmentCoverage(ctx)

	require.GreaterOrEqual(t, eng.scanCallCount("acct"), 1,
		"a degenerate NON-code embeddable graph (cloud/acct) is enumerated, probed, and rebuilt — PipelineScan paged")
}

// TestReconcileSegmentCoverage_SkipsNonEmbeddableBuiltins is the closed-gate side:
// the per-type enumeration skips kgtypes.GraphLinkage and kgtypes.GraphTransformers
// (HasRebuildableSegments returns false), so even if the server WOULD enumerate a
// degenerate-looking instance for them, the reconcile never probes or rebuilds it —
// they carry no rebuildable segments.
func TestReconcileSegmentCoverage_SkipsNonEmbeddableBuiltins(t *testing.T) {
	c, eng := buildReconcileClient(t)
	ctx := context.Background()

	// Register instances for the two non-embeddable sync-eligible builtins. If the
	// enumeration did NOT skip them, the loop would probe these names.
	eng.namesByType[string(kgtypes.GraphLinkage)] = []string{"lk"}
	eng.namesByType[string(kgtypes.GraphTransformers)] = []string{"recipes"}
	eng.scanItems["lk"] = makeReconcileScanPage("lk", 10)
	eng.scanItems["recipes"] = makeReconcileScanPage("recipes", 10)

	c.reconcileSegmentCoverage(ctx)

	require.Equal(t, 0, eng.scanCallCount("lk"),
		"linkage has no rebuildable segments — never enumerated/probed/rebuilt")
	require.Equal(t, 0, eng.scanCallCount("recipes"),
		"transformers has no rebuildable segments — never enumerated/probed/rebuilt")
}

// TestReconcileSegmentCoverage_TimeoutKeepsResidentNoRebuild is the Phase 2
// red-green: with the L2-first load(), a server whose manifest List times out on the
// reconcile's heal load (the down/524 shape) is a NO-OP that keeps the L2 resident,
// NEVER a from-scratch rebuild.
//
// Restart shape: a prior run warmed the on-disk L2 cache with a REAL decodable
// corpus; a FRESH consumer Manager rooted at the SAME dir then reconciles while the
// server's List times out. The L2-first load enumerates the warm disk and imports
// the corpus WITHOUT EVER calling List → resident stays >= the corpus → the probe
// reads NOT degenerate → reconcileSegmentCoverage pages ZERO PipelineScan.
//
// RED on a Phase-1-reverted tree (load() Lists the server first): the fresh
// consumer's load Lists, the List TIMES OUT → load returns the error → the resident
// pool stays EMPTY (0). The discriminating assertion is therefore
// ResidentDocCount>=corpusN, which FAILS on the revert (resident 0) and passes after
// the L2-first flip. scanCallCount==0 holds on BOTH trees — the best-effort reconcile
// arms never rebuild on a probe error (the Phase-2 no-op-on-timeout guarantee) — so
// it is asserted as the no-rebuild invariant, while the resident assertion is the
// L2-first discriminator.
func TestReconcileSegmentCoverage_TimeoutKeepsResidentNoRebuild(t *testing.T) {
	const (
		repo = "timeoutRepo"
		// >= the segmentdist resident backstop floor (64) so the shipped corpus arms
		// the resident-vs-shipped ratio (a sub-floor corpus would disarm and mask the
		// red-green). The floor constant is unexported in package segmentdist.
		corpusN = 72
	)
	// realFetch=true during the warm so the warm Manager actually imports the corpus
	// from the server into the shared on-disk L2 cache.
	c, eng, backend, dir := buildReconcileClientWithSegDir(t, 0, repo)
	ctx := context.Background()

	// Ship a REAL decodable HNSW corpus through the router the consumer loads from.
	producer := segmentdist.NewManager(c.router, t.TempDir(), 0, segmentdist.WithSegmentTransport(backend.transportBuilder()))
	require.NoError(t, producer.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, fastloadVecDocs(repo, corpusN)))
	require.NoError(t, producer.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

	// WARM the shared on-disk L2 cache via a SEPARATE Manager rooted at the SAME dir:
	// its cache-first load Lists + Fetches the real corpus and writes the .seg blobs
	// to disk under dir. After this the disk cache holds the full decodable corpus.
	warm := segmentdist.NewManager(c.router, dir, 0, segmentdist.WithSegmentTransport(backend.transportBuilder()))
	degenerate, err := warm.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.False(t, degenerate, "the warm load imports the full corpus from the server (resident >= floor)")
	require.GreaterOrEqual(t, warm.ResidentDocCount(kgtypes.GraphCode, repo), corpusN,
		"the warm Manager is coverage-passing — the on-disk L2 cache now holds the corpus")

	// Now the server's manifest List times out: every ListDelta from here on returns a
	// transport error (the down/524 shape). failListAfterN is set to the List count
	// already spent on the warm so ALL subsequent Lists (the consumer's reconcile)
	// fail. A reverted L3-first load() Lists first → errors → resident stays empty; the
	// L2-first load() never Lists → imports from the warm disk.
	backend.mu.Lock()
	backend.failReadAfterN = backend.readCalls
	backend.mu.Unlock()

	// Seed a scan page so that, IF a rebuild wrongly fired, PipelineScan would be paged
	// (making the scanCallCount==0 assertion meaningful, not vacuous).
	eng.scanItems[repo] = makeReconcileScanPage(repo, 10)

	// ACT: a FRESH consumer (c.segmentMgr — never loaded) reconciles over the warm L2
	// dir while the server's List times out.
	require.Equal(t, 0, c.segmentMgr.ResidentDocCount(kgtypes.GraphCode, repo),
		"PRE: the fresh consumer has not loaded yet (resident 0)")
	c.reconcileSegmentCoverage(ctx)

	// GREEN: the L2-first load imported the corpus from the warm disk (never Listed) →
	// not degenerate → no rebuild paged, and the resident pool is preserved.
	require.Equal(t, 0, eng.scanCallCount(repo),
		"a List-timeout reconcile is a NO-OP — RebuildSegments NEVER paged (scanCallCount==0)")
	require.GreaterOrEqual(t, c.segmentMgr.ResidentDocCount(kgtypes.GraphCode, repo), corpusN,
		"the L2 resident is preserved on the List timeout (imported from warm disk, not collapsed — the L2-first discriminator)")
}

// TestReconcileSegmentCoverage_NilManagerNoPanic proves the headless/degraded path:
// a nil segment manager is a clean no-op (no panic, no rebuild).
func TestReconcileSegmentCoverage_NilManagerNoPanic(t *testing.T) {
	c := &client{} // segmentMgr nil — degraded headless mode.
	require.NotPanics(t, func() {
		c.reconcileSegmentCoverage(context.Background())
	}, "a nil segment manager reconcile is a clean no-op")
}

// TestRunSegmentReconcileLoop_TicksAndCancels proves the periodic loop fires the
// reconcile (a graph needing a real rebuild gets rebuilt within a few ticks) and
// returns promptly on ctx cancel (no goroutine leak). embedded=300 arms the shipped-completeness
// healNeedsRebuild gate so the genuinely-incomplete shipped corpus (128 < 0.5*300)
// still rebuilds.
func TestRunSegmentReconcileLoop_TicksAndCancels(t *testing.T) {
	c, eng, backend := buildReconcileClientWithSeg(t, 300, "loopRepo")
	shipHNSW(t, backend, "loopRepo", 64, 64) // genuinely incomplete vs 300 embedded → rebuild
	eng.scanItems["loopRepo"] = makeReconcileScanPage("loopRepo", 10)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.runSegmentReconcileLoop(ctx, 5*time.Millisecond)
		close(done)
	}()

	require.Eventually(t, func() bool {
		return eng.scanCallCount("loopRepo") >= 1
	}, 2*time.Second, 5*time.Millisecond, "the periodic loop rebuilds the degenerate graph within a few ticks")

	cancel()
	select {
	case <-done:
		// returned promptly on cancel — no leak.
	case <-time.After(2 * time.Second):
		t.Fatal("runSegmentReconcileLoop did not return after ctx cancel — goroutine leak")
	}
}

// TestReconcileSegmentCoverage_EndToEndHealsWithoutSearchOrCollect is the
// load-bearing failing-first proof (Phase 4): a graph whose shipped corpus is
// genuinely incomplete vs its embedded node count (embedded=1024 via Stats, shipped
// covered 128 << 0.5*1024 so the healNeedsRebuild gate confirms a real
// regen), live resident empty, NO pending embed-drain and NO collect, is healed by
// reconcileSegmentCoverage — RebuildSegments scans the embedded nodes and re-ships a
// searchable corpus — WITHOUT any Manager.Search and WITHOUT a collect. It must fail
// on a tree without the Phase 1-2 reconcile (no rebuild fires) and pass with it.
func TestReconcileSegmentCoverage_EndToEndHealsWithoutSearchOrCollect(t *testing.T) {
	c, eng, backend := buildReconcileClientWithSeg(t, searchengine.DefaultMinSegmentDocs, "e2eRepo")
	ctx := context.Background()

	// PRE-state: a real embedded corpus exists (the scan returns >= MinSegmentDocs
	// items so the rebuild seals a full searchable segment), the server holds shipped
	// HNSW metas (>= floor) that are genuinely incomplete vs the embedded count but the
	// live engine resident is empty (empty Fetch).
	shipHNSW(t, backend, "e2eRepo", 64, 64)
	eng.scanItems["e2eRepo"] = makeReconcileScanPage("e2eRepo", searchengine.DefaultMinSegmentDocs)

	// Assert the PRE-state: the probe reports degenerate AND the live resident pool is
	// empty — with NO Search call having run.
	degenerate, err := c.segmentMgr.ReconcileResidentDegenerate(ctx, kgtypes.GraphCode, "e2eRepo")
	require.NoError(t, err)
	require.True(t, degenerate, "PRE: the post-restart collapse reads as degenerate")
	require.Equal(t, 0, c.segmentMgr.ResidentDocCount(kgtypes.GraphCode, "e2eRepo"),
		"PRE: the live searchable pool is empty (masked collapse)")

	// ACT: the startup-trigger path — no Search, no collect.
	c.reconcileSegmentCoverage(ctx)

	// POST: RebuildSegments scanned the embedded nodes (PipelineScan paged) and
	// re-shipped a searchable corpus. The rebuild path replace-prunes the old
	// degenerate corpus and ships the freshly-built segment, so the server's shipped
	// HNSW coverage now reflects the rebuilt full corpus — a healthy pool the search
	// engine loads from on its next load(), WITHOUT any Search or collect having run.
	require.GreaterOrEqual(t, eng.scanCallCount("e2eRepo"), 1,
		"POST: the rebuild scanned the embedded nodes (no collect needed)")
	covered, _, err := c.segmentMgr.ShippedSegmentDocCount(ctx, kgtypes.GraphCode, "e2eRepo")
	require.NoError(t, err)
	require.GreaterOrEqual(t, covered, searchengine.DefaultMinSegmentDocs,
		"POST: the searchable pool is rebuilt to healthy (full corpus re-shipped) WITHOUT a search and WITHOUT a collect")
}
