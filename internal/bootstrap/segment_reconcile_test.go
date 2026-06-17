// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// reconcileEngine is an EngineServiceHandler for the segment-reconcile tests. It
// (a) enumerates code repos via Execute(RETURN_MODE_GRAPH_NAMES) so the reconcile's
// code-repo discovery (tools.ListGraphNamesOfType) sees them; (b) serves a
// configurable segment_rebuild scan page per graph via PipelineScan, RECORDING the
// call count per graph so a test can assert a rebuild fired (RebuildSegments pages
// PipelineScan only when the probe reports degenerate); (c) serves a fixed Stats.
// It embeds countingEngine to inherit the rest of the handler surface.
type reconcileEngine struct {
	*countingEngine
	codeRepos []string

	mu        sync.Mutex
	scanItems map[string][]*knowledgev1.PipelineScanItem // keyed by graph name
	scanCalls map[string]int                             // PipelineScan calls per graph name
}

func (e *reconcileEngine) Execute(
	_ context.Context, req *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	q := req.Msg.GetQuery()
	if q != nil && q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		if req.Msg.GetTarget().GetGraph() == "code" {
			infos := make([]*knowledgev1.GraphInfo, 0, len(e.codeRepos))
			for _, r := range e.codeRepos {
				infos = append(infos, &knowledgev1.GraphInfo{Name: r})
			}
			return connect.NewResponse(&knowledgev1.ExecuteResponse{GraphNames: infos}), nil
		}
		return connect.NewResponse(&knowledgev1.ExecuteResponse{}), nil
	}
	return connect.NewResponse(&knowledgev1.ExecuteResponse{}), nil
}

func (e *reconcileEngine) Stats(
	context.Context, *connect.Request[knowledgev1.StatsRequest],
) (*connect.Response[knowledgev1.StatsResponse], error) {
	return connect.NewResponse(&knowledgev1.StatsResponse{GraphStats: &knowledgev1.GraphStats{}}), nil
}

func (e *reconcileEngine) PipelineScan(
	_ context.Context, req *connect.Request[knowledgev1.PipelineScanRequest],
) (*connect.Response[knowledgev1.PipelineScanResponse], error) {
	name := req.Msg.GetGraphName()
	e.mu.Lock()
	e.scanCalls[name]++
	// Serve the page ONCE per graph (afterId empty), then an empty page so the
	// id-cursor scan terminates.
	var items []*knowledgev1.PipelineScanItem
	if req.Msg.GetAfterId() == "" {
		items = e.scanItems[name]
	}
	e.mu.Unlock()
	return connect.NewResponse(&knowledgev1.PipelineScanResponse{Items: items}), nil
}

func (e *reconcileEngine) scanCallCount(name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.scanCalls[name]
}

// buildReconcileClient wires a *client over ONE h2c server fronting the
// reconcileEngine + a healSegmentService (empty Fetch — a shipped corpus loads to
// an empty resident pool, the post-restart collapse). The returned engine handle
// lets a test seed scan pages + read the per-graph PipelineScan count.
func buildReconcileClient(t *testing.T, codeRepos ...string) (*client, *reconcileEngine) {
	t.Helper()
	seg := newHealSegmentService()
	eng := &reconcileEngine{
		countingEngine: &countingEngine{},
		codeRepos:      codeRepos,
		scanItems:      map[string][]*knowledgev1.PipelineScanItem{},
		scanCalls:      map[string]int{},
	}

	mux := http.NewServeMux()
	segPath, segHdlr := knowledgev1connect.NewSegmentServiceHandler(seg)
	mux.Handle(segPath, segHdlr)
	engPath, engHdlr := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(engPath, engHdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)

	local := graphclient.NewGraphClientForURL(srv.URL)
	authState := auth.NewAuthState(newFakeAuthStore(), time.Minute)
	router := graphclient.NewRouter(local, srv.URL, staticTokenSource{tok: "tok"}, authState)

	c := &client{
		local:      local,
		router:     router,
		authState:  authState,
		segmentMgr: segmentdist.NewManager(router, t.TempDir(), 0),
	}
	return c, eng
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
	c, eng := buildReconcileClient(t, "healthyRepo")
	ctx := context.Background()

	// Sub-floor shipped corpus (4 < floor 64) → the probe disarms → not degenerate.
	shipHNSW(t, c, "healthyRepo", 4)
	// Seed a scan page so that, IF a rebuild wrongly fired, it would page the scanner
	// (making the no-rebuild assertion meaningful).
	eng.scanItems["healthyRepo"] = makeReconcileScanPage("healthyRepo", 10)

	c.reconcileSegmentCoverage(ctx)

	require.Equal(t, 0, eng.scanCallCount("healthyRepo"),
		"a healthy (disarmed) graph triggers NO RebuildSegments — PipelineScan never paged")
}

// TestReconcileSegmentCoverage_DegenerateRebuilds proves a degenerate graph (server
// corpus >= floor but live resident empty after load — the empty-Fetch collapse)
// triggers exactly one RebuildSegments: PipelineScan is paged for it.
func TestReconcileSegmentCoverage_DegenerateRebuilds(t *testing.T) {
	c, eng := buildReconcileClient(t, "degenRepo")
	ctx := context.Background()

	// Full shipped corpus (128 >> floor) but the heal segment service's empty Fetch
	// means load imports nothing → live resident stays 0 → degenerate.
	shipHNSW(t, c, "degenRepo", 64, 64)
	eng.scanItems["degenRepo"] = makeReconcileScanPage("degenRepo", 10)

	c.reconcileSegmentCoverage(ctx)

	require.GreaterOrEqual(t, eng.scanCallCount("degenRepo"), 1,
		"a degenerate graph triggers RebuildSegments — PipelineScan is paged")
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
// reconcile (a degenerate graph gets rebuilt within a few ticks) and returns
// promptly on ctx cancel (no goroutine leak).
func TestRunSegmentReconcileLoop_TicksAndCancels(t *testing.T) {
	c, eng := buildReconcileClient(t, "loopRepo")
	shipHNSW(t, c, "loopRepo", 64, 64) // degenerate
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
// load-bearing failing-first proof (Phase 4): a graph in the post-restart degenerate
// state (server holds the full shipped corpus, embedded > 0 via the scan, live
// resident empty, NO pending embed-drain and NO collect) is healed by
// reconcileSegmentCoverage — RebuildSegments scans the embedded nodes and re-ships a
// searchable corpus — WITHOUT any Manager.Search and WITHOUT a collect. It must fail
// on a tree without the Phase 1-2 reconcile (no rebuild fires) and pass with it.
func TestReconcileSegmentCoverage_EndToEndHealsWithoutSearchOrCollect(t *testing.T) {
	c, eng := buildReconcileClient(t, "e2eRepo")
	ctx := context.Background()

	// PRE-state: a real embedded corpus exists (the scan returns >= MinSegmentDocs
	// items so the rebuild seals a full searchable segment), the server holds shipped
	// HNSW metas (>= floor) but the live engine resident is empty (empty Fetch).
	shipHNSW(t, c, "e2eRepo", 64, 64)
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
