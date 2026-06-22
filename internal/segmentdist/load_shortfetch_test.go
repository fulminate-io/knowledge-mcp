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
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// shortFetchService wraps fakeSegmentService and serves a SHORT-but-OK Fetch: it
// omits exactly one blob id from any Fetch that requests it, returning the rest
// with no error (the manifest skew a refcount-GC between List and Fetch
// produces). Unlike emptyFetchService (drops ALL blobs), it models the realistic
// case the C1 clamp protects: a subset of the listed corpus comes back, the rest
// is silently missing. dropID and active are guarded by mu so a test can restore
// full Fetch (active=false) for a second load.
type shortFetchService struct {
	*fakeSegmentService
	mu     sync.Mutex
	dropID string
	active bool
}

func (s *shortFetchService) setDrop(id string, active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropID = id
	s.active = active
}

func (s *shortFetchService) Fetch(
	ctx context.Context, req *connect.Request[knowledgev1.FetchRequest],
) (*connect.Response[knowledgev1.FetchResponse], error) {
	resp, err := s.fakeSegmentService.Fetch(ctx, req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	drop, active := s.dropID, s.active
	s.mu.Unlock()
	if !active {
		return resp, nil
	}
	kept := make([]*knowledgev1.SegmentBlobProto, 0, len(resp.Msg.GetBlobs()))
	for _, b := range resp.Msg.GetBlobs() {
		if b.GetId() == drop {
			continue // omit exactly this id — short-but-OK response
		}
		kept = append(kept, b)
	}
	return connect.NewResponse(&knowledgev1.FetchResponse{Blobs: kept}), nil
}

// buildHNSWSegment Adds docs to a standalone HNSW engine, force-seals the tail,
// and returns the sealed segment(s) as wire proto blobs ready to Ship. Each call
// uses its own engine so no background merge collapses segments built across
// calls — the way the C1 test stages two distinct on-server blobs.
func buildHNSWSegment(t *testing.T, docs []searchengine.Document) []*knowledgev1.SegmentBlobProto {
	t.Helper()
	eng := searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{})
	require.NoError(t, eng.Add(docs))
	require.NoError(t, eng.Flush())
	exported := eng.Export()
	require.NotEmpty(t, exported, "the engine must seal at least one segment")
	out := make([]*knowledgev1.SegmentBlobProto, 0, len(exported))
	for _, b := range exported {
		out = append(out, blobToProto(b))
	}
	return out
}

// TestLoadFromServer_ShortFetchDoesNotLoseSegment is the C1 regression: a
// short-but-OK Fetch (the server omits one listed blob, e.g. a refcount-GC raced
// between List and Fetch) must NOT permanently lose that segment. The load floor
// (importedGen) must not advance past the omitted KEPT-format segment, so a later
// load re-lists and imports it.
//
// PRE-FIX (RED): loadFromServer advances importedGen to listedMaxGen regardless of
// which blobs Fetch actually returned, so the omitted segment's generation is
// passed and a second load Lists an empty delta — the segment is gone for the
// process lifetime. POST-FIX (GREEN): importedGen is clamped below the omitted
// kept-format segment's generation, so the second load re-lists and imports it.
func TestLoadFromServer_ShortFetchDoesNotLoseSegment(t *testing.T) {
	svc := &shortFetchService{fakeSegmentService: newFakeSegmentService()}
	gc := newReconcileHarness(t, svc)
	ctx := context.Background()
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: "shortfetchRepo"}

	// Build TWO distinct real, importable HNSW segments in independent engines (so
	// no background merge collapses them) and Ship both directly to the same server
	// graphKey via gc.Ship. Direct ship (no Publish) leaves both blobs on the server
	// without the refcount-GC that a same-writerID manifest swap would trigger — so
	// the short Fetch has a real victim to omit and the consumer can decode the rest.
	blobs := append(buildHNSWSegment(t, vecContentDocsSeed(1024, 0)),
		buildHNSWSegment(t, vecContentDocsSeed(1024, 1024))...)
	require.GreaterOrEqual(t, len(blobs), 2, "two independent engines must seal two distinct HNSW blobs")
	_, err := gc.Ship(ctx, &knowledgev1.ShipRequest{Target: target, Blobs: blobs})
	require.NoError(t, err)

	// Read the listed corpus to pick a victim segment to omit. There must be >1.
	src := newRPCSegmentSource(gc, target, "", ctx)
	metas, err := src.List(ctx, 0)
	require.NoError(t, err)
	hnswName := hnsw.New().Name()
	var victim string
	var victimGen, fullGen uint64
	hnswCount := 0
	for _, mta := range metas {
		if mta.Format != hnswName {
			continue
		}
		hnswCount++
		if mta.Generation > fullGen {
			fullGen = mta.Generation
		}
		// Omit the HIGHEST-generation HNSW segment so the bug (advance to
		// listedMaxGen) is maximally visible: listedMaxGen == victimGen.
		if mta.Generation >= victimGen {
			victim = mta.ID
			victimGen = mta.Generation
		}
	}
	require.GreaterOrEqual(t, hnswCount, 2, "the producer must have sealed >1 HNSW segment")
	require.NotEmpty(t, victim)

	// Consumer loads cold (fresh L2) while the short Fetch omits the victim blob.
	svc.setDrop(victim, true)
	consumer := NewManager(gc, t.TempDir(), 0)
	dm := consumer.managerFor(kgtypes.GraphCode, "shortfetchRepo")
	require.NoError(t, dm.loadFromServer(ctx))

	// Restore full Fetch and load again. With the clamp, the second load re-lists
	// and imports the omitted segment because importedGen never advanced past it.
	svc.setDrop("", false)
	require.NoError(t, dm.loadFromServer(ctx))

	// The omitted segment must now be resident. RED on current source: the first
	// load advanced importedGen to listedMaxGen (== victimGen), so the second load
	// Lists an empty delta past it and the victim is permanently absent.
	dm.resMu.Lock()
	_, resident := dm.resident[victim]
	dm.resMu.Unlock()
	require.True(t, resident,
		"the short-Fetch-omitted segment must be recoverable on a later load (not permanently lost)")
}
