// SPDX-License-Identifier: Apache-2.0

package tools

// raw_graph_hybrid_search_test.go is the read-path gate for the segment-backed
// raw-graph arm, driven through BOTH RAILS. The phase exists because the two
// surfaces disagreed about the same request — the search tool served a raw
// hybrid call by silently running BM25 while the query tool refused it — so
// gating only one of them would leave "do the search-rail work and stop" green.
//
// THE FILENAME IS LOAD-BEARING. A Go test file whose name ends in a GOOS or
// GOARCH token before _test.go is SILENTLY EXCLUDED from the build: an earlier
// draft named raw_graph_hybrid_arm_test.go compiled to nothing and `go test`
// reported ok with no tests run. Do not rename this to *_arm_test.go.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// rawSegmentHandler answers EACH READ FOR WHAT IT ASKED, and that is the whole
// reason the heading leg below can discriminate.
//
// A CANNED ONE-RESPONSE HARNESS CANNOT SEE THE DEFECT — measured, not predicted.
// Serving one node set to every Execute hands the parent section back on the HIT
// hydrate, so the heading resolves whether or not the parent hydrate exists, and
// deleting the parent hydrate leaves the leg GREEN. So: an ids[] node read
// returns ONLY the requested ids, an edges read returns only CONTAINS edges
// pointing at the requested ids, and a graph-names read returns the collected
// catalog.
//
// It embeds dispatchEngineHandler for the rest of the EngineService surface
// (Stats, Index, the pipeline methods) rather than restating ten unimplemented
// stubs, and overrides Execute alone.
type rawSegmentHandler struct {
	*dispatchEngineHandler

	mu         sync.Mutex
	reqs       []*knowledgev1.ExecuteRequest
	nodes      map[string]*knowledgev1.Node
	edges      []*knowledgev1.Edge
	graphNames []string
}

func (h *rawSegmentHandler) Execute(
	_ context.Context, req *connect.Request[knowledgev1.ExecuteRequest],
) (*connect.Response[knowledgev1.ExecuteResponse], error) {
	h.mu.Lock()
	h.reqs = append(h.reqs, req.Msg)
	h.mu.Unlock()

	q := req.Msg.GetQuery()
	switch q.GetReturnMode() {
	case knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES:
		infos := make([]*knowledgev1.GraphInfo, 0, len(h.graphNames))
		for _, n := range h.graphNames {
			infos = append(infos, &knowledgev1.GraphInfo{Name: n})
		}
		return connect.NewResponse(&knowledgev1.ExecuteResponse{GraphNames: infos}), nil

	case knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		want := map[string]bool{}
		for _, id := range q.GetIds() {
			want[id] = true
		}
		out := make([]*knowledgev1.Edge, 0, len(h.edges))
		for _, e := range h.edges {
			if want[e.GetToId()] {
				out = append(out, e)
			}
		}
		return connect.NewResponse(&knowledgev1.ExecuteResponse{Edges: out}), nil

	default:
		// The ids[] node read — the hit hydrate AND the parent hydrate both land
		// here, and each gets back exactly what it named.
		out := make([]*knowledgev1.Node, 0, len(q.GetIds()))
		for _, id := range q.GetIds() {
			if n, ok := h.nodes[id]; ok {
				out = append(out, n)
			}
		}
		return connect.NewResponse(&knowledgev1.ExecuteResponse{Nodes: out}), nil
	}
}

// rawSegmentHarness wires the discriminating handler behind a real h2c server and
// returns a GraphClient pointed at it, so the arms under test issue genuine RPCs.
func rawSegmentHarness(t *testing.T, h *rawSegmentHandler) *graphclient.GraphClient {
	t.Helper()
	mux := http.NewServeMux()
	hp, hh := knowledgev1connect.NewHealthServiceHandler(h)
	mux.Handle(hp, hh)
	ep, eh := knowledgev1connect.NewEngineServiceHandler(h)
	mux.Handle(ep, eh)

	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	gc := graphclient.NewGraphClientForURL(srv.URL)
	t.Cleanup(gc.Close)
	return gc
}

// rawStaleUpdatedAt and rawFreshUpdatedAt bracket the recency leg: one node far
// enough in the past that its half-life boost is negligible, one at ~now so its
// boost is near maximal. Absolute nanos rather than time.Now() arithmetic, so the
// leg cannot drift into flakiness as the clock moves.
var (
	rawStaleUpdatedAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	rawFreshUpdatedAt = time.Now().UnixNano()
)

// rawSegmentFixture is the seeded web document. THE CONTAINS EDGE NAMES A PARENT
// THAT IS NOT AMONG THE HITS — sec1 is never ranked, so it reaches the heading
// ladder only through the explicit parent hydrate. A parent that was itself a hit
// would arrive on the hit hydrate and prove nothing.
func rawSegmentFixture() *rawSegmentHandler {
	return &rawSegmentHandler{
		dispatchEngineHandler: &dispatchEngineHandler{execHits: new(atomic.Int64)},
		nodes: map[string]*knowledgev1.Node{
			"para1": {
				Id: "para1", Type: "paragraph", Source: "web-collect",
				Content: "idempotent retries deduplicate a replayed request",
				// page_first is what the locality line's "p. 42" segment reads.
				Metadata: map[string]string{"page_first": "42"},
			},
			"sec1": {
				Id: "sec1", Type: "section", Source: "web-collect",
				SymbolName: "Retry Semantics",
			},
			// The recency pair. OLDCHUNK is the RELEVANCE winner and NEWCHUNK the
			// FRESHNESS winner, so the two orderings disagree and the rendered order
			// discriminates between them. Timestamps are unix NANOS, which is what
			// computeTemporalScore reads.
			"oldchunk": {
				Id: "oldchunk", Type: "paragraph", Source: "web-collect",
				Content:   "stale retry guidance",
				UpdatedAt: rawStaleUpdatedAt,
			},
			"newchunk": {
				Id: "newchunk", Type: "paragraph", Source: "web-collect",
				Content:   "fresh retry guidance",
				UpdatedAt: rawFreshUpdatedAt,
			},
		},
		edges: []*knowledgev1.Edge{
			{FromId: "sec1", ToId: "para1", Type: string(kgtypes.EdgeContains)},
		},
		graphNames: []string{"doc-slug"},
	}
}

// TestRawGraphHybridArm_ServesHybridThroughSegments is the eight-leg read-path
// gate. Each leg names the wrong-but-compiling implementation it rejects.
func TestRawGraphHybridArm_ServesHybridThroughSegments(t *testing.T) {
	const graph = "web"
	hits := []searchengine.Hit{{ID: "para1", Score: 0.9}}

	t.Run("hybrid", func(t *testing.T) {
		h := rawSegmentFixture()
		mgr := &fakeSegmentSearcher{hits: hits}
		deps := &interceptDeps{gc: rawSegmentHarness(t, h), emb: healthyEmbedder{}, segMgr: mgr}

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": graph, "name": "doc-slug", "query": "idempotent retries", "mode": "hybrid",
		}))
		require.True(t, handled, "a raw-graph hybrid search must be claimed")
		require.False(t, out.IsError, "hybrid must be served, not refused: %s", engine.FirstTextContent(out))

		assert.Equal(t, int64(1), mgr.calls.Load(),
			"the segment engine must be asked — an arm still ranking client-side never gets here")
		assert.NotEmpty(t, mgr.lastVec,
			"a query vector must reach the engine, or the vector arm never ran")
		assert.Contains(t, engine.FirstTextContent(out), "_search mode: vector+text_",
			"the footer must disclose the arms that actually ran, not the retired hardcoded literal")
	})

	t.Run("text", func(t *testing.T) {
		// THE CONTROL for the hybrid leg: without it, an arm that embeds
		// unconditionally would pass hybrid and be indistinguishable from a
		// correct one.
		h := rawSegmentFixture()
		mgr := &fakeSegmentSearcher{hits: hits}
		deps := &interceptDeps{gc: rawSegmentHarness(t, h), emb: healthyEmbedder{}, segMgr: mgr}

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": graph, "name": "doc-slug", "query": "idempotent retries", "mode": "text",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "text must be served: %s", engine.FirstTextContent(out))

		assert.Equal(t, int64(1), mgr.calls.Load(), "the segment engine must be asked on the text arm too")
		assert.Empty(t, mgr.lastVec,
			"mode:text must reach the engine with NO vector — an arm that always embeds fails here")
		assert.Contains(t, engine.FirstTextContent(out), "_search mode: BM25-only_",
			"a text-only run must disclose BM25-only")
	})

	t.Run("vector_without_embedder_refuses", func(t *testing.T) {
		// THIS LEG IS THE SURVIVING HALF OF TestInterceptSearch_WebPDFVectorModeRefused,
		// which this phase retires. That test guarded two things: that a raw-graph
		// vector search never silently returns an empty list, and that raw graphs
		// are never embedded. The second half is correctly retired by the
		// embed-only enrollment. The FIRST half is this leg. A reader tracing why
		// that test disappeared lands here rather than concluding the property was
		// dropped with it.
		//
		// The plausible-wrong implementation is easy and compiles: copy the
		// registered-custom composer's body and drop the four-line arm between the
		// engine-arm split and the limit default. Every other leg stays green and
		// the shipped client answers a vector search with zero rows, which a reader
		// takes for "no matches" when the truth is "there is no semantic index to
		// ask".
		h := rawSegmentFixture()
		mgr := &fakeSegmentSearcher{hits: hits}
		deps := &interceptDeps{gc: rawSegmentHarness(t, h), segMgr: mgr} // emb nil: no embedder configured

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": graph, "name": "doc-slug", "query": "idempotent retries", "mode": "vector",
		}))
		require.True(t, handled, "mode:vector is claimed, then refused")

		body := engine.FirstTextContent(out)
		require.NotEmpty(t, body, "a vector search with no embedder must produce a refusal, not an empty result set")
		assert.Contains(t, body, graph, "the refusal must name the graph")
		assert.Contains(t, body, "mode:vector", "the refusal must name the mode")
		assert.Contains(t, body, "embedder", "the refusal must name the missing embedder")
		assert.Equal(t, int64(0), mgr.calls.Load(),
			"the refusal must fire BEFORE the engine — asking it, getting nothing and rendering zero rows "+
				"is the exact implementation this leg exists to reject")
	})

	t.Run("web_heading", func(t *testing.T) {
		// REJECTS AN ARM THAT REUSES rawGraphParentHeadings UNCHANGED. Its
		// precondition was that byID already held the PARENT nodes, which the
		// whole-graph drain met implicitly and a hit-only hydrate does not. sec1 is
		// not among the hits, so the heading survives only if the parent hydrate
		// exists.
		h := rawSegmentFixture()
		mgr := &fakeSegmentSearcher{hits: hits}
		deps := &interceptDeps{gc: rawSegmentHarness(t, h), emb: healthyEmbedder{}, segMgr: mgr}

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": graph, "name": "doc-slug", "query": "idempotent retries", "mode": "hybrid",
		}))
		require.True(t, handled)
		body := engine.FirstTextContent(out)

		assert.Contains(t, body, "under: Retry Semantics",
			"the locality line is lost: the parent section is not among the hits, so it reaches byID "+
				"only through an explicit parent hydrate")
		assert.Contains(t, body, "p. 42", "the hit's own page locality must render beside the heading")
	})

	t.Run("query_rail_hybrid", func(t *testing.T) {
		// BOTH HALVES ARE ASSERTED: "claimed" alone is satisfiable by an arm that
		// claims the call and then denies it, which is the shape this rail had
		// before — mode:hybrid fell through to the generic deny while the search
		// tool served the same request.
		h := rawSegmentFixture()
		mgr := &fakeSegmentSearcher{hits: hits}
		deps := &interceptDeps{gc: rawSegmentHarness(t, h), emb: healthyEmbedder{}, segMgr: mgr}

		args := map[string]any{
			"graph": graph, "name": "doc-slug", "text": "idempotent retries", "mode": "hybrid",
		}
		handled, out := routeWebPDFClient(opCtx(), deps,
			queryArgs{Graph: graph, Name: "doc-slug", Text: "idempotent retries", Mode: "hybrid"},
			webPDFParams(t, args).Arguments)

		require.True(t, handled,
			"the query rail declined mode:hybrid — it still falls through to the generic deny")
		assert.Equal(t, int64(1), mgr.calls.Load(),
			"claiming the call is not serving it: the segment engine must actually be asked")
		assert.False(t, out.IsError, "the query rail must serve hybrid: %s", engine.FirstTextContent(out))
	})

	t.Run("nameless_call_refuses", func(t *testing.T) {
		// REJECTS A REPLACEMENT SPECIFIED AS A LIST OF REUSED PARTS. The deleted
		// composer's name gate is carried by none of the reused helpers, so a
		// port assembled from them loses it silently — which is how it went missing
		// once already.
		h := rawSegmentFixture()
		mgr := &fakeSegmentSearcher{hits: hits}
		deps := &interceptDeps{gc: rawSegmentHarness(t, h), emb: healthyEmbedder{}, segMgr: mgr}

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": graph, "query": "idempotent retries", "mode": "hybrid",
		}))
		require.True(t, handled, "a nameless raw-graph search is claimed, then refused")

		body := engine.FirstTextContent(out)
		require.NotEmpty(t, body, "a nameless call must produce a refusal, not an empty result set")
		assert.Contains(t, body, "name", "the refusal must name the missing parameter")
		assert.Contains(t, body, `mode:"modules"`,
			"the refusal must point at the remedy that lists the collected graphs")
		assert.Equal(t, int64(0), mgr.calls.Load(),
			"an unresolvable selector must never reach the engine")
	})

	t.Run("uncollected_name_refuses", func(t *testing.T) {
		// REJECTS THE PORT THAT DROPS THE EXISTENCE GATE, which is otherwise
		// unheld: the catalog check at the top of composeRawGraphSegmentSearch is
		// carried by none of the reused helpers. The deleted composer got this
		// refusal for free because a never-collected name failed its drain's very
		// first read; the segment engine has no such read and simply returns zero
		// hits for a graph it holds nothing for. An arm without the gate therefore
		// answers a never-collected name with "this document says nothing about
		// your query" when the truth is "this graph is not collected" — the two
		// answers a reader most needs told apart.
		//
		// THE ENGINE IS SEEDED WITH HITS ON PURPOSE. Against an empty searcher an
		// ungated arm would render nothing anyway and this leg would pass on a
		// deleted gate, proving nothing. Seeded, an ungated arm hydrates para1 and
		// renders a row, so the leg goes red exactly when the gate is gone. The
		// fixture's catalog holds only "doc-slug", so "never-collected" is absent
		// from the same RETURN_MODE_GRAPH_NAMES read the gate consults.
		//
		// BOTH RAILS, because the gate lives in the shared composer and a check
		// wired on one rail only would leave the other serving rows.
		t.Run("search_rail", func(t *testing.T) {
			h := rawSegmentFixture()
			mgr := &fakeSegmentSearcher{hits: hits}
			deps := &interceptDeps{gc: rawSegmentHarness(t, h), emb: healthyEmbedder{}, segMgr: mgr}

			handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
				"graph": graph, "name": "never-collected", "query": "idempotent retries", "mode": "hybrid",
			}))
			require.True(t, handled, "a raw-graph search naming an uncollected graph is claimed, then refused")
			require.True(t, out.IsError,
				"an uncollected name must be an ERROR, never an empty result set: %s", engine.FirstTextContent(out))

			assert.Contains(t, engine.FirstTextContent(out), "is not collected",
				"the refusal must say the graph is not collected — the distinction the gate exists to draw")
			assert.Equal(t, int64(0), mgr.calls.Load(),
				"the gate must fire BEFORE the engine: asking it and rendering its zero rows is precisely "+
					"the implementation this leg rejects")
		})

		t.Run("query_rail", func(t *testing.T) {
			// Driven the way query_rail_hybrid drives its own leg — through
			// routeWebPDFClient with webPDFParams — so this exercises the rail's real
			// entry point rather than the search rail's.
			h := rawSegmentFixture()
			mgr := &fakeSegmentSearcher{hits: hits}
			deps := &interceptDeps{gc: rawSegmentHarness(t, h), emb: healthyEmbedder{}, segMgr: mgr}

			args := map[string]any{
				"graph": graph, "name": "never-collected", "text": "idempotent retries", "mode": "hybrid",
			}
			handled, out := routeWebPDFClient(opCtx(), deps,
				queryArgs{Graph: graph, Name: "never-collected", Text: "idempotent retries", Mode: "hybrid"},
				webPDFParams(t, args).Arguments)

			require.True(t, handled, "the query rail must claim the call before refusing it")
			require.True(t, out.IsError,
				"the query rail must refuse an uncollected name too: %s", engine.FirstTextContent(out))

			assert.Contains(t, engine.FirstTextContent(out), "is not collected",
				"the query rail's refusal must draw the same distinction as the search rail's")
			assert.Equal(t, int64(0), mgr.calls.Load(),
				"the query rail's gate must fire before the engine as well")
		})
	})

	t.Run("recent_reranks_by_freshness", func(t *testing.T) {
		// ROUTING THIS ARM THROUGH THE SHARED CLAIM PREDICATE WIDENED ITS CLAIMED
		// MODES TO INCLUDE "recent", and this composer does NOT end in
		// finishSegmentSearchRender, where the sibling arms get the temporal rerank
		// for free. Without an explicit call the arm ACCEPTS a recency request and
		// ranks by relevance anyway — the worst of the three possible behaviours,
		// because a caller reading rows back cannot tell an unhonoured mode from a
		// corpus whose newest chunk genuinely ranked last.
		//
		// THE FIXTURE MAKES THE TWO ORDERINGS DISAGREE: the engine returns OLDCHUNK
		// ahead of NEWCHUNK on relevance, so relevance order and freshness order are
		// opposites and the rendered order says which one ran.
		h := rawSegmentFixture()
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{
			{ID: "oldchunk", Score: 0.9},
			{ID: "newchunk", Score: 0.8},
		}}
		deps := &interceptDeps{gc: rawSegmentHarness(t, h), emb: healthyEmbedder{}, segMgr: mgr}

		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": graph, "name": "doc-slug", "query": "retry guidance", "mode": "recent",
		}))
		require.True(t, handled, "mode:recent must be claimed by the raw arm")
		require.False(t, out.IsError, "mode:recent must be served: %s", engine.FirstTextContent(out))

		body := engine.FirstTextContent(out)
		newAt := strings.Index(body, "newchunk")
		oldAt := strings.Index(body, "oldchunk")
		require.NotEqual(t, -1, newAt, "the fresh chunk is missing from the render")
		require.NotEqual(t, -1, oldAt, "the stale chunk is missing from the render")
		assert.Less(t, newAt, oldAt,
			"mode:recent was accepted and then ignored — the engine's relevance order survived, "+
				"so the temporal rerank never ran")
	})
}
