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
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// reconcileEngine is an EngineServiceHandler for the segment-reconcile tests. It
// (a) enumerates per-graph-type instance names via Execute(RETURN_MODE_GRAPH_NAMES),
// keyed by the requested graph type. The reconcile no longer consults that seam —
// its walk is the working set — so a registration here is what a REGRESSION to
// backend enumeration would pick up, and the counter beside it is how a test
// asserts no such read happened; (b) serves
// a configurable segment_rebuild scan page per graph via PipelineScan, RECORDING the
// call count per graph so a test can assert a rebuild fired (RebuildSegments pages
// PipelineScan only when the probe reports degenerate); (c) serves a fixed Stats.
// It embeds countingEngine to inherit the rest of the handler surface.
type reconcileEngine struct {
	*countingEngine
	// namesByType maps a graph-type string (e.g. "code", "cloud") to the instance
	// names RETURN_MODE_GRAPH_NAMES serves for that type.
	namesByType map[string][]string
	// embedded is the BinaryVectorCount Stats serves — the embedded denominator the
	// degeneracy predicate reads (via tools.GraphEmbeddedCount). It defaults to 0
	// (the empty-Stats behavior the per-format reconcile tests rely on: they probe
	// ResidentObservationsByFormat, which reads the resident doc count off each arm
	// and never touches BinaryVectorCount). A nonzero value ARMS the predicate for
	// the heal-closure red-green, which gates on it.
	embedded int32
	// embedFailures is the EmbedFailureCount Stats serves, and
	// embedFailuresHoldingVec is the optional subset of it still holding a vector.
	// The subset is a POINTER because its ABSENCE is a distinct fact from a zero: a
	// server that does not compute it omits the field, and the client must keep its
	// approximation caveat rather than read the omission as "none of them".
	embedFailures           int32
	embedFailuresHoldingVec *int32
	// statsCalls counts Stats invocations. It is the observable a "how many times was
	// this graph evaluated" assertion operationalizes itself as: the balance verdict
	// cannot form without reading the coverage operands, so a second evaluation is a
	// second call and an evaluation that never ran is no call at all.
	statsCalls int

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
	// execByGraph counts EVERY Execute keyed by the target graph, and mutByGraph the
	// write-carrying subset. They are catch-all recorders on purpose: an assertion
	// that a graph received no traffic is only as good as the instrument's ability to
	// have seen traffic of any shape, so nothing here filters by operation term.
	execByGraph map[string]int
	mutByGraph  map[string]int
	// graphNameCalls counts RETURN_MODE_GRAPH_NAMES enumerations. "The walk costs no
	// enumeration RPC" is a claim about a read that leaves no other trace: a pass that
	// enumerated and then filtered would look identical on every per-graph counter
	// here, so the enumeration needs a counter of its own to be assertable at all.
	graphNameCalls int
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
	if gt, name, ok := graphsel.InstanceKeyOf(req.Msg.GetTarget()); ok {
		e.mu.Lock()
		if e.execByGraph == nil {
			e.execByGraph = map[string]int{}
			e.mutByGraph = map[string]int{}
		}
		e.execByGraph[string(gt)+"/"+name]++
		if req.Msg.GetMutation() != nil {
			e.mutByGraph[string(gt)+"/"+name]++
		}
		e.mu.Unlock()
	}
	q := req.Msg.GetQuery()
	if q != nil && q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		e.mu.Lock()
		e.graphNameCalls++
		e.mu.Unlock()
		names := e.namesByType[req.Msg.GetTarget().GetGraph()]
		infos := make([]*knowledgev1.GraphInfo, 0, len(names))
		for _, n := range names {
			infos = append(infos, &knowledgev1.GraphInfo{Name: n})
		}
		return connect.NewResponse(&knowledgev1.ExecuteResponse{GraphNames: infos}), nil
	}
	return connect.NewResponse(&knowledgev1.ExecuteResponse{}), nil
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
	c, eng, dir := buildReconcileClientWithDir(t, 0, "healthyRepo")
	ctx := context.Background()

	// Sub-floor shipped corpus (4 < floor 64) → the probe disarms → not degenerate.
	seedL2Corpus(t, dir, kgtypes.GraphCode, "healthyRepo", 4)
	// Seed a scan page so that, IF a rebuild wrongly fired, it would page the scanner
	// (making the no-rebuild assertion meaningful).
	eng.scanItems["healthyRepo"] = makeReconcileScanPage("healthyRepo", 10)

	c.reconcileSegmentCoverage(ctx)

	require.Equal(t, 0, eng.scanCallCount("healthyRepo"),
		"a healthy (disarmed) graph triggers NO RebuildSegments — PipelineScan never paged")
}

// TestReconcileSegmentCoverage_DegenerateRebuilds proves a graph whose HNSW pool is LOST
// — no resident corpus at all against a non-empty embedded count — triggers a
// RebuildSegments through the periodic reconcile: PipelineScan is paged for it.
//
// THE ARMING CONDITION CHANGED WITH THE BAND FLIP, and the sibling below records the
// other half. This used to seed 128 documents against 300 embedded nodes and rely on the
// RATIO BAND (128 < 0.5 × 300) to call the graph degenerate. That band is retired for
// the HNSW arm: this periodic sweep is not the pipeline quiescence edge, so away from
// quiescence the arm asserts only a LOST POOL and the exact resident-versus-vectors
// verdict is formed at the drain edge instead. Seeding no corpus is now what arms the
// pass; a partial corpus arms nothing, which is asserted directly by
// TestReconcileSegmentCoverage_PartialCorpusNoLongerRebuilds below.
func TestReconcileSegmentCoverage_DegenerateRebuilds(t *testing.T) {
	c, eng, _ := buildReconcileClientWithDir(t, 300, "degenRepo")
	ctx := context.Background()

	// NO corpus is seeded: the empty pool against 300 embedded nodes is the lost cache,
	// which is exact under any denominator and is the one branch of the retired band
	// that survived.
	eng.scanItems["degenRepo"] = makeReconcileScanPage("degenRepo", 10)

	c.reconcileSegmentCoverage(ctx)

	require.GreaterOrEqual(t, eng.scanCallCount("degenRepo"), 1,
		"a LOST HNSW pool triggers RebuildSegments — PipelineScan is paged")
}

// TestReconcileSegmentCoverage_PartialCorpusNoLongerRebuilds is the band flip's other
// half, and it is the KNOWN-NEGATIVE that gives the test above its meaning.
//
// WITHOUT IT the pair would not discriminate: a sweep that rebuilt every graph
// unconditionally would satisfy the lost-pool test just as well. This asserts the sweep
// stays SILENT on exactly the shape the retired ratio band used to rebuild — a graph
// holding well under half its corpus — which is the behaviour change the flip is for.
func TestReconcileSegmentCoverage_PartialCorpusNoLongerRebuilds(t *testing.T) {
	c, eng, dir := buildReconcileClientWithDir(t, 300, "partialRepo")
	ctx := context.Background()

	// 128 resident against 300 embedded: 42% coverage, comfortably inside the retired
	// band's firing range and above its floor.
	seedL2Corpus(t, dir, kgtypes.GraphCode, "partialRepo", 128)
	eng.scanItems["partialRepo"] = makeReconcileScanPage("partialRepo", 10)

	c.reconcileSegmentCoverage(ctx)

	require.Zero(t, eng.scanCallCount("partialRepo"),
		"a partially-covered graph must NOT be rebuilt by the periodic sweep any more — "+
			"the ratio band is retired for the HNSW arm, and a partial shortfall is now "+
			"decided exactly at the pipeline quiescence edge instead of approximately here")
}

// TestReconcileSegmentCoverage_DegenerateNonCodeRebuilds proves the reconcile heals
// NON-code embeddable builtins: a cloud graph (keyed by account) whose HNSW pool is LOST
// triggers exactly one RebuildSegments — PipelineScan is paged for it. Under a code-only
// walk the cloud graph would never be visited, so no rebuild would fire.
//
// THE ARMING CONDITION IS A LOST POOL, not a partial shortfall — see
// TestReconcileSegmentCoverage_DegenerateRebuilds for why the ratio band no longer arms
// this sweep.
func TestReconcileSegmentCoverage_DegenerateNonCodeRebuilds(t *testing.T) {
	c, eng, _ := buildReconcileClientWithDir(t, 300) // no code repos — exercise the non-code path alone; embedded=300 makes the empty pool a lost cache.
	ctx := context.Background()

	// The cloud account is a graph THIS CLIENT INTERACTED WITH — that admission is
	// what puts it in the walk. The engine registration below is deliberately left in
	// place: it is what the retired per-type enumeration would have discovered, so a
	// walk that regressed to enumerating would still find it and this test would stop
	// discriminating between the two mechanisms.
	c.AdmitGraph(kgtypes.GraphCloud, "acct", "search")
	eng.namesByType[string(kgtypes.GraphCloud)] = []string{"acct"}

	// NO corpus under the cloud account selector: an empty pool against 300 embedded
	// nodes is the lost cache. PipelineScan is keyed by the instance name.
	eng.scanItems["acct"] = makeReconcileScanPage("acct", 10)

	c.reconcileSegmentCoverage(ctx)

	require.GreaterOrEqual(t, eng.scanCallCount("acct"), 1,
		"a degenerate NON-code embeddable graph (cloud/acct) is enumerated, probed, and rebuilt — PipelineScan paged")
}

// TestReconcileSegmentCoverage_SkipsNonEmbeddableBuiltins is the closed-gate side:
// the walk skips kgtypes.GraphLinkage and kgtypes.GraphTransformers
// (HasRebuildableSegments returns false), so even for an instance this client HAS
// interacted with, the reconcile never probes or rebuilds it — they carry no
// rebuildable segments.
//
// BOTH INSTANCES ARE ADMITTED ON PURPOSE. Membership is the other reason a graph can
// be absent from the walk, so leaving them unadmitted would make the zeros below true
// for that reason instead and this test would no longer touch the type gate it names.
func TestReconcileSegmentCoverage_SkipsNonEmbeddableBuiltins(t *testing.T) {
	c, eng := buildReconcileClient(t)
	ctx := context.Background()

	c.AdmitGraph(kgtypes.GraphLinkage, "lk", "search")
	c.AdmitGraph(kgtypes.GraphTransformers, "recipes", "search")

	// Register instances for the two non-embeddable sync-eligible builtins. If the
	// type gate did NOT skip them, the loop would probe these names.
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

// TestReconcileSegmentCoverage_TimeoutKeepsResidentNoRebuild WAS DELETED HERE. It
// warmed the on-disk cache, then made the SERVER'S List fail for every subsequent
// call, and asserted the consumer still loaded: a reverted L3-first load() would List
// first, error, and leave the resident set empty, while the L2-first load never Lists
// at all and imports from disk.
//
// The fault it injected no longer has anything to act on — there is no server List to
// fail. SUCCESSOR NAMED, and it asserts the STRONGER form: TestLoadIssuesNoSourceList
// InAnyBranch (segmentdist) drives a cold L2, a populated L2 and an evicted pool
// against a counting source and asserts List was called ZERO times in all three, with
// the counter proven non-zero-capable by a direct List in the same test. "Never Lists"
// beats "survives a failing List".

func TestReconcileSegmentCoverage_NilManagerNoPanic(t *testing.T) {
	c := &client{} // segmentMgr nil — degraded headless mode.
	require.NotPanics(t, func() {
		c.reconcileSegmentCoverage(context.Background())
	}, "a nil segment manager reconcile is a clean no-op")
}

// TestRunSegmentReconcileLoop_TicksAndCancels proves the periodic loop fires the
// reconcile (a graph needing a real rebuild gets rebuilt within a few ticks) and
// returns promptly on ctx cancel (no goroutine leak). embedded=300 against a LOST pool
// is what arms the rebuild — see TestReconcileSegmentCoverage_DegenerateRebuilds for
// why a partial corpus no longer does.
func TestRunSegmentReconcileLoop_TicksAndCancels(t *testing.T) {
	c, eng, _ := buildReconcileClientWithDir(t, 300, "loopRepo")
	// No corpus seeded: the empty pool against 300 embedded nodes is the lost cache.
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
// load-bearing proof that a graph whose HNSW pool is genuinely LOST — nothing in L2 to
// load, against a non-empty embedded node count — is healed by reconcileSegmentCoverage,
// with RebuildSegments scanning the embedded nodes and re-shipping a searchable corpus,
// WITHOUT any Manager.Search and WITHOUT a collect.
//
// THE FIXTURE NOW SEEDS NOTHING, and that is a correction of its premise rather than a
// weakening. It used to seed 128 documents and call the state a "masked collapse",
// arming the retired ratio band against an embedded count of 1024. But the reconcile's
// probe FORCE-LOADS the arm from L2 before measuring it, so those 128 documents were
// restored by the load — the pool was never collapsed, only not-yet-resident, which is
// the daemon-restart shape the arm deliberately heals WITHOUT a rebuild. With the ratio
// retired, that fixture describes a graph the sweep correctly declines. An L2 with no
// corpus at all is the state that genuinely cannot be loaded back, so it is the one that
// must drive a rebuild.
func TestReconcileSegmentCoverage_EndToEndHealsWithoutSearchOrCollect(t *testing.T) {
	c, eng, _ := buildReconcileClientWithDir(t, searchengine.DefaultMinSegmentDocs, "e2eRepo")
	ctx := context.Background()

	// PRE-state: a real embedded corpus exists server-side (the scan returns >=
	// MinSegmentDocs items so the rebuild seals a full searchable segment), and this
	// client's L2 holds NOTHING for the graph.
	eng.scanItems["e2eRepo"] = makeReconcileScanPage("e2eRepo", searchengine.DefaultMinSegmentDocs)

	// Assert the PRE-state: the live resident pool is empty — with NO Search call
	// having run — AND the probe reports the arm armed.
	//
	// THE ORDER OF THESE TWO IS LOAD-BEARING. The probe FORCE-LOADS the arm from L2
	// before measuring it, so probing first would populate the resident pool and make
	// the emptiness assertion below fail on its own side effect. Read the untouched
	// engine first, then probe. Here the load finds nothing to import, so the arm stays
	// empty across both reads and the pool is genuinely lost rather than merely cold.
	require.Equal(t, 0, c.segmentMgr.ResidentDocCount(kgtypes.GraphCode, "e2eRepo"),
		"PRE: the live searchable pool is empty — this client has neither searched nor loaded")
	require.True(t, armIsDegenerate(t, c.segmentMgr, kgtypes.GraphCode, "e2eRepo", searchengine.DefaultMinSegmentDocs),
		"PRE: a lost pool against a non-empty embedded corpus arms the HNSW arm")

	// ACT: the startup-trigger path — no Search, no collect.
	c.reconcileSegmentCoverage(ctx)

	// POST: RebuildSegments scanned the embedded nodes (PipelineScan paged) and
	// re-shipped a searchable corpus. The rebuild path replace-prunes the old
	// degenerate corpus and ships the freshly-built segment, so the server's shipped
	// HNSW coverage now reflects the rebuilt full corpus — a healthy pool the search
	// engine loads from on its next load(), WITHOUT any Search or collect having run.
	require.GreaterOrEqual(t, eng.scanCallCount("e2eRepo"), 1,
		"POST: the rebuild scanned the embedded nodes (no collect needed)")
	covered, err := c.segmentMgr.ShippedSegmentDocCount(ctx, kgtypes.GraphCode, "e2eRepo")
	require.NoError(t, err)
	require.GreaterOrEqual(t, covered, searchengine.DefaultMinSegmentDocs,
		"POST: the searchable pool is rebuilt to healthy (full corpus re-shipped) WITHOUT a search and WITHOUT a collect")
}
