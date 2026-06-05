// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// fakeSegmentService is an in-memory SegmentServiceHandler: it stamps monotonic
// generations on Ship (server-as-ordering-point), serves ListDelta/Fetch from a
// per-graphKey map. Mirrors the real server's contract closely enough to drive
// the client source/manager round-trip tests without importing the server module.
type fakeSegmentService struct {
	mu    sync.Mutex
	byKey map[string][]*knowledgev1.SegmentBlobProto
	gen   uint64
}

func newFakeSegmentService() *fakeSegmentService {
	return &fakeSegmentService{byKey: map[string][]*knowledgev1.SegmentBlobProto{}}
}

func (f *fakeSegmentService) key(t *knowledgev1.GraphSelector) string {
	return t.GetGraph() + ":" + t.GetRepo() + t.GetAccount() + t.GetName()
}

func (f *fakeSegmentService) Ship(
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
			stamped = append(stamped, metaOf(cur))
			continue
		}
		f.gen++
		stored := &knowledgev1.SegmentBlobProto{
			Id: b.GetId(), Format: b.GetFormat(), Generation: f.gen, Bytes: b.GetBytes(),
		}
		f.byKey[k] = append(f.byKey[k], stored)
		existing[b.GetId()] = stored
		stamped = append(stamped, metaOf(stored))
	}
	return connect.NewResponse(&knowledgev1.ShipResponse{Stamped: stamped}), nil
}

func (f *fakeSegmentService) ListDelta(
	_ context.Context, req *connect.Request[knowledgev1.ListDeltaRequest],
) (*connect.Response[knowledgev1.ListDeltaResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(req.Msg.GetTarget())
	var metas []*knowledgev1.SegmentMetaProto
	for _, b := range f.byKey[k] {
		if b.GetGeneration() > req.Msg.GetSinceGen() {
			metas = append(metas, metaOf(b))
		}
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].GetGeneration() < metas[j].GetGeneration() })
	return connect.NewResponse(&knowledgev1.ListDeltaResponse{Metas: metas}), nil
}

func (f *fakeSegmentService) Fetch(
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

// Prune deletes the named content-hash ids from the per-graphKey map and returns
// how many were removed (idempotent — an absent id does not count). Mirrors the
// real server's Delete contract closely enough to drive the reconcile test.
func (f *fakeSegmentService) Prune(
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

func metaOf(b *knowledgev1.SegmentBlobProto) *knowledgev1.SegmentMetaProto {
	return &knowledgev1.SegmentMetaProto{Id: b.GetId(), Format: b.GetFormat(), Generation: b.GetGeneration()}
}

// newSegmentHarness stands up an h2c httptest server behind the fake
// SegmentService handler and returns a GraphClient pointed at it (the GraphClient
// satisfies segmentCaller).
func newSegmentHarness(t *testing.T) (*fakeSegmentService, *graphclient.GraphClient) {
	t.Helper()
	svc := newFakeSegmentService()
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewSegmentServiceHandler(svc)
	mux.Handle(path, hdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)
	return svc, graphclient.NewGraphClientForURL(srv.URL)
}

// TestRPCSegmentSourceRoundTrip Ships blobs then drives List(0) + Fetch and
// asserts the engine structs round-trip the proto carriers field-for-field.
func TestRPCSegmentSourceRoundTrip(t *testing.T) {
	_, gc := newSegmentHarness(t)
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "repoA"}
	ctx := context.Background()

	// Ship two blobs via the GraphClient passthrough (proves the client wiring).
	_, err := gc.Ship(ctx, &knowledgev1.ShipRequest{
		Target: target,
		Blobs: []*knowledgev1.SegmentBlobProto{
			blobToProto(searchengine.SegmentBlob{ID: "s1", Format: "mock", Bytes: []byte("one")}),
			blobToProto(searchengine.SegmentBlob{ID: "s2", Format: "mock", Bytes: []byte("two")}),
		},
	})
	require.NoError(t, err)

	src := newRPCSegmentSource(gc, target, ctx)

	metas, err := src.List(ctx, 0)
	require.NoError(t, err)
	require.Len(t, metas, 2)
	require.Equal(t, "s1", metas[0].ID)
	require.Equal(t, uint64(1), metas[0].Generation)
	require.Equal(t, uint64(2), metas[1].Generation)

	blobs, err := src.Fetch([]searchengine.SegmentID{"s1", "s2"})
	require.NoError(t, err)
	require.Len(t, blobs, 2)
	got := map[string]searchengine.SegmentBlob{}
	for _, b := range blobs {
		got[b.ID] = b
	}
	require.Equal(t, []byte("one"), got["s1"].Bytes)
	require.Equal(t, "mock", got["s1"].Format)
	require.Equal(t, uint64(1), got["s1"].Generation)
	require.Equal(t, []byte("two"), got["s2"].Bytes)

	// List(1) returns only generation > 1.
	delta, err := src.List(ctx, 1)
	require.NoError(t, err)
	require.Len(t, delta, 1)
	require.Equal(t, "s2", delta[0].ID)
}

// compile-time: GraphClient and Router both satisfy segmentCaller.
var (
	_ segmentCaller = (*graphclient.GraphClient)(nil)
	_ segmentCaller = (*graphclient.Router)(nil)
)
