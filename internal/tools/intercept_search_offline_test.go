// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// intercept_search_offline_test.go proves the offline-search behavior end-to-end
// through the INTERCEPT path: an offline-but-ready daemon (no embedder, pipeline
// reported ready, segment Manager wired and populated) returns REAL BM25 results
// rather than the "client segment engine unavailable" error.
//
// With the SegmentService deleted, the offline (NOT-logged-in) Manager runs on the
// L2-local segment source: AddAndShipFields ships to the on-disk L2 cache with ZERO
// server RPC, and the search arm reads BM25 over those L2 segments. The httptest
// server mounts only the EngineService (so composeKnowledgeSearch's hydrate read
// resolves the ranked Hit IDs to nodes) — the segment lifecycle is entirely local.

// notLoggedInCaller is the not-logged-in loginState the offline Manager takes so the
// source factory selects the L2-local segmentSource (no server segment RPC). It is
// the tools-side analog of segmentdist's own login stub (unexported there).
type notLoggedInCaller struct{}

func (notLoggedInCaller) LoggedIn(context.Context) bool { return false }

// offlineBM25Corpus builds a fixed-size BM25 corpus (== MinSegmentDocs default, so
// AddAndShipFields seals exactly one segment) where one designated target doc
// carries a unique high-IDF term. Mirrors segmentdist/manager_search_test.go's
// searchCorpus, scoped to the BM25 fields the text-only degrade arm needs (no
// vectors — the offline path never embeds).
func offlineBM25Corpus(targetIdx int) (docs []searchengine.Document, targetID, uniqueTerm string) {
	const n = 1024 // == segmentdist MinSegmentDocs default → one sealed segment.
	uniqueTerm = "zzqqxxuniquetarget"
	docs = make([]searchengine.Document, n)
	for i := range docs {
		id := fmt.Sprintf("n%d", i)
		summary := "shared corpus filler body common token"
		if i == targetIdx {
			summary = uniqueTerm + " " + summary
			targetID = id
		}
		docs[i] = searchengine.Document{
			ID:     id,
			Fields: map[string]string{searchengine.FieldSummary: summary},
		}
	}
	return docs, targetID, uniqueTerm
}

// TestInterceptSearchKnowledge_OfflineReturnsRealResults (FAILS-WHEN-ABSENT) is
// the headline offline-search proof: with Embedder()==nil (offline — no client
// embedder), PipelineReady()==true, and a POPULATED segment Manager wired (the
// state the unconditional construction produces), the knowledge search arm returns
// res.IsError==false with non-empty content for a BM25 term query carrying NO
// query vector. When the read Manager is left nil offline (the prior bug, the
// Manager constructed only inside the pipeline wire), this same arm returned the
// "client segment engine unavailable" isError — so reverting the hoist (Manager
// nil offline) flips this test red.
func TestInterceptSearchKnowledge_OfflineReturnsRealResults(t *testing.T) {
	ctx := context.Background()
	var execHits atomic.Int64

	docs, targetID, uniqueTerm := offlineBM25Corpus(7)

	// One server, EngineService only (the hydrate read): the Engine handler returns
	// the target node so composeKnowledgeSearch's hydrateEngineHits resolves the
	// ranked Hit by id-map. The segment lifecycle is entirely local (L2), so no
	// SegmentService mount exists.
	eng := &dispatchEngineHandler{
		execHits: &execHits,
		resp:     cannedNodesResp(&knowledgev1.Node{Id: targetID, Type: "finding", SymbolName: "OfflineHit"}),
	}
	mux := http.NewServeMux()
	hp, hh := knowledgev1connect.NewHealthServiceHandler(eng)
	mux.Handle(hp, hh)
	ep, eh := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(ep, eh)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)

	gc := graphclient.NewGraphClientForURL(srv.URL)

	// Populate the Manager with real BM25 segments under knowledge/"default" (the
	// graph+name composeKnowledgeSearch queries). A NOT-logged-in caller routes the
	// Manager to the L2-local source, so AddAndShipFields + Flush seal the field-bearing
	// docs into the on-disk L2 cache with ZERO server RPC — the offline degrade arm is
	// BM25-over-local-L2, no vectors.
	mgr := segmentdist.NewManager(notLoggedInCaller{}, t.TempDir(), 0)
	require.NoError(t, mgr.AddAndShipFields(ctx, kgtypes.GraphKnowledge, knowledgeDefaultName, docs))
	require.NoError(t, mgr.Flush(ctx, kgtypes.GraphKnowledge, knowledgeDefaultName))

	// Offline-but-ready: Embedder()==nil (emb left nil), PipelineReady()==true
	// (pipelineNotReady false), and the wired populated Manager.
	deps := &interceptDeps{gc: gc, segMgr: mgr}
	require.True(t, deps.PipelineReady(), "ready precondition")
	require.Nil(t, deps.Embedder(), "offline precondition: no client embedder")

	// Text-only query (no query vector) on a unique high-IDF term → the BM25 arm
	// ranks the target #1; composeKnowledgeSearch hydrates it via the Engine read.
	handled, res := InterceptSearch(deps, searchParams(t, map[string]any{
		"query": uniqueTerm, "graph": "knowledge",
	}))

	require.True(t, handled, "the knowledge arm claims the call")
	require.False(t, res.IsError,
		"offline + ready + populated Manager must serve real BM25 results, not the unavailable error")
	require.NotEmpty(t, res.Content, "real results carry content")
	assert.NotEmpty(t, res.Content[0].Text, "rendered content is non-empty")
	assert.Contains(t, res.Content[0].Text, "OfflineHit",
		"the hydrated target node is rendered (real result, not a degraded stub)")
}
