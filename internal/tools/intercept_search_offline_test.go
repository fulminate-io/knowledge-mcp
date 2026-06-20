// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
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
// It is self-contained: a single h2c httptest server mounts BOTH the
// SegmentService (so the Manager can ship + load real BM25 segments) AND the
// EngineService (so composeKnowledgeSearch's hydrate read resolves the ranked Hit
// IDs to nodes). The Manager and the hydrate GraphCaller are therefore the SAME
// GraphClient pointed at that one server — which is exactly the production shape
// where the client's router serves both the segment RPCs and the engine reads.

// inMemSegmentService is a compact in-memory SegmentServiceHandler: monotonic
// generations on Ship, ListDelta/Fetch from a per-graphKey map. It is the minimal
// shape the client Manager's ship + cache-first load round-trips against (mirrors
// the segmentdist package's own fakeSegmentService, replicated here because that
// double is package-private to segmentdist).
type inMemSegmentService struct {
	mu    sync.Mutex
	byKey map[string][]*knowledgev1.SegmentBlobProto
	gen   uint64
	// manifests mirrors the real server's registry: graphKey -> (writerID+format)
	// -> id-set. Publish swaps a writer's manifest and refcount-GCs blobs no
	// manifest references.
	manifests map[string]map[string]map[string]bool
}

func newInMemSegmentService() *inMemSegmentService {
	return &inMemSegmentService{
		byKey:     map[string][]*knowledgev1.SegmentBlobProto{},
		manifests: map[string]map[string]map[string]bool{},
	}
}

func (f *inMemSegmentService) key(t *knowledgev1.GraphSelector) string {
	return t.GetGraph() + ":" + t.GetRepo() + t.GetAccount() + t.GetName()
}

func (f *inMemSegmentService) blobMeta(b *knowledgev1.SegmentBlobProto) *knowledgev1.SegmentMetaProto {
	return &knowledgev1.SegmentMetaProto{
		Id: b.GetId(), Format: b.GetFormat(), Generation: b.GetGeneration(), DocCount: b.GetDocCount(),
	}
}

func (f *inMemSegmentService) Ship(
	_ context.Context, req *connect.Request[knowledgev1.ShipRequest],
) (*connect.Response[knowledgev1.ShipResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(req.Msg.GetTarget())
	existing := map[string]*knowledgev1.SegmentBlobProto{}
	for _, b := range f.byKey[k] {
		existing[b.GetId()] = b
	}
	var stamped []*knowledgev1.SegmentMetaProto
	for _, b := range req.Msg.GetBlobs() {
		if cur, ok := existing[b.GetId()]; ok {
			stamped = append(stamped, f.blobMeta(cur))
			continue
		}
		f.gen++
		stored := &knowledgev1.SegmentBlobProto{
			Id: b.GetId(), Format: b.GetFormat(), Generation: f.gen,
			DocCount: b.GetDocCount(), Bytes: b.GetBytes(),
		}
		f.byKey[k] = append(f.byKey[k], stored)
		existing[b.GetId()] = stored
		stamped = append(stamped, f.blobMeta(stored))
	}
	return connect.NewResponse(&knowledgev1.ShipResponse{Stamped: stamped}), nil
}

func (f *inMemSegmentService) ListDelta(
	_ context.Context, req *connect.Request[knowledgev1.ListDeltaRequest],
) (*connect.Response[knowledgev1.ListDeltaResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(req.Msg.GetTarget())
	var metas []*knowledgev1.SegmentMetaProto
	for _, b := range f.byKey[k] {
		if b.GetGeneration() > req.Msg.GetSinceGen() {
			metas = append(metas, f.blobMeta(b))
		}
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].GetGeneration() < metas[j].GetGeneration() })
	return connect.NewResponse(&knowledgev1.ListDeltaResponse{Metas: metas}), nil
}

func (f *inMemSegmentService) Fetch(
	_ context.Context, req *connect.Request[knowledgev1.FetchRequest],
) (*connect.Response[knowledgev1.FetchResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(req.Msg.GetTarget())
	want := map[string]bool{}
	for _, id := range req.Msg.GetIds() {
		want[id] = true
	}
	var blobs []*knowledgev1.SegmentBlobProto
	for _, b := range f.byKey[k] {
		if want[b.GetId()] {
			blobs = append(blobs, b)
		}
	}
	return connect.NewResponse(&knowledgev1.FetchResponse{Blobs: blobs}), nil
}

func (f *inMemSegmentService) Prune(
	_ context.Context, req *connect.Request[knowledgev1.PruneRequest],
) (*connect.Response[knowledgev1.PruneResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(req.Msg.GetTarget())
	del := map[string]bool{}
	for _, id := range req.Msg.GetIds() {
		del[id] = true
	}
	kept := f.byKey[k][:0]
	var removed uint64
	for _, b := range f.byKey[k] {
		if del[b.GetId()] {
			removed++
			continue
		}
		kept = append(kept, b)
	}
	f.byKey[k] = kept
	return connect.NewResponse(&knowledgev1.PruneResponse{Deleted: removed}), nil
}

// Publish swaps this writer's manifest and refcount-GCs blobs no manifest
// references — the in-memory mirror of the real server's registry-model publish.
func (f *inMemSegmentService) Publish(
	_ context.Context, req *connect.Request[knowledgev1.PublishRequest],
) (*connect.Response[knowledgev1.PublishResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(req.Msg.GetTarget())
	if f.manifests[k] == nil {
		f.manifests[k] = map[string]map[string]bool{}
	}
	mk := req.Msg.GetWriterId() + "\x00" + req.Msg.GetFormat()
	set := map[string]bool{}
	for _, id := range req.Msg.GetIds() {
		set[id] = true
	}
	f.manifests[k][mk] = set

	referenced := map[string]bool{}
	for _, s := range f.manifests[k] {
		for id := range s {
			referenced[id] = true
		}
	}

	kept := f.byKey[k][:0]
	var removed uint64
	for _, b := range f.byKey[k] {
		if referenced[b.GetId()] {
			kept = append(kept, b)
			continue
		}
		removed++
	}
	f.byKey[k] = kept
	return connect.NewResponse(&knowledgev1.PublishResponse{Deleted: removed}), nil
}

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

	// One server, both services: SegmentService (Manager ship/load) + EngineService
	// (the hydrate read). The Engine handler returns the target node so
	// composeKnowledgeSearch's hydrateEngineHits resolves the ranked Hit by id-map.
	seg := newInMemSegmentService()
	eng := &dispatchEngineHandler{
		execHits: &execHits,
		resp:     cannedNodesResp(&knowledgev1.Node{Id: targetID, Type: "finding", SymbolName: "OfflineHit"}),
	}
	mux := http.NewServeMux()
	hp, hh := knowledgev1connect.NewHealthServiceHandler(eng)
	mux.Handle(hp, hh)
	ep, eh := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(ep, eh)
	sp, sh := knowledgev1connect.NewSegmentServiceHandler(seg)
	mux.Handle(sp, sh)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)

	gc := graphclient.NewGraphClientForURL(srv.URL)

	// Populate the Manager with real BM25 segments under knowledge/"default" (the
	// graph+name composeKnowledgeSearch queries). Ship the field-bearing docs only —
	// the offline degrade arm is BM25-over-existing-segments, no vectors.
	mgr := segmentdist.NewManager(gc, t.TempDir(), 0)
	require.NoError(t, mgr.AddAndShipFields(ctx, kgtypes.GraphKnowledge, knowledgeDefaultName, docs))

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
