// SPDX-License-Identifier: Apache-2.0

// Package segmentdist is the CLIENT-side segment distribution layer: an
// RPC-backed SegmentSource, a content-addressed on-disk L2 SegmentCache, and a
// load/unload manager that ties the searchengine engine to the SegmentService
// wire. It is a CONSUMER of cmd/knowledge/internal/searchengine — deliberately a
// SIBLING package, NOT inside the engine subpackage, so the engine stays
// import-clean (stdlib + own subpkgs) for a future service extraction (locked
// contract). The server stores opaque blobs; this package is
// the only place the client engine and the SegmentService client meet.
package segmentdist

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// segmentCaller is the SegmentService surface rpcSegmentSource depends on. Both
// *graphclient.GraphClient and *graphclient.Router satisfy it, so ship/pull
// route cloud-when-logged-in / local-when-not through the same Router dispatch
// the Engine RPCs use. Declared here (consumer-side interface) so the package
// has no hard dependency on a concrete client type.
type segmentCaller interface {
	Ship(ctx context.Context, req *knowledgev1.ShipRequest) (*knowledgev1.ShipResponse, error)
	ListDelta(ctx context.Context, req *knowledgev1.ListDeltaRequest) (*knowledgev1.ListDeltaResponse, error)
	Fetch(ctx context.Context, req *knowledgev1.FetchRequest) (*knowledgev1.FetchResponse, error)
	Prune(ctx context.Context, req *knowledgev1.PruneRequest) (*knowledgev1.PruneResponse, error)
	Publish(ctx context.Context, req *knowledgev1.PublishRequest) (*knowledgev1.PublishResponse, error)
}

// rpcSegmentSource implements searchengine.SegmentSource over the SegmentService
// client. It carries the graph's GraphSelector (the routing envelope) plus a
// background ctx for the ctx-less Fetch leg of the interface.
type rpcSegmentSource struct {
	caller   segmentCaller
	target   *knowledgev1.GraphSelector
	fetchCtx context.Context
	// writerID is the stable per-machine identity threaded onto every outbound
	// RPC (Ship/List/Fetch/Publish). The server stamps it as the writer's
	// last-connection liveness signal; an empty writerID is a tolerated no-op on
	// the server. Sourced from the stable writer-id helper at construction.
	writerID string
}

var _ searchengine.SegmentSource = (*rpcSegmentSource)(nil)

// newRPCSegmentSource builds a SegmentSource for one graph. fetchCtx backs the
// ctx-less SegmentSource.Fetch leg (the engine's Fetch signature takes no ctx —
// distribution_iface.go:23 — while List does, line 22); pass context.Background
// when no scoped ctx is available. writerID is the stable per-machine identity
// carried on every outbound RPC for the server's last-connection liveness
// stamp; "" is a tolerated no-op server-side.
func newRPCSegmentSource(caller segmentCaller, target *knowledgev1.GraphSelector, writerID string, fetchCtx context.Context) *rpcSegmentSource {
	if fetchCtx == nil {
		fetchCtx = context.Background()
	}
	return &rpcSegmentSource{caller: caller, target: target, fetchCtx: fetchCtx, writerID: writerID}
}

// List issues SegmentService.ListDelta for the delta (generation > sinceGen) and
// maps the proto metas to engine metas field-for-field.
func (s *rpcSegmentSource) List(ctx context.Context, sinceGen uint64) ([]searchengine.SegmentMeta, error) {
	resp, err := s.caller.ListDelta(ctx, &knowledgev1.ListDeltaRequest{
		Target:   s.target,
		SinceGen: sinceGen,
		WriterId: s.writerID,
	})
	if err != nil {
		return nil, err
	}
	metas := make([]searchengine.SegmentMeta, 0, len(resp.GetMetas()))
	for _, m := range resp.GetMetas() {
		metas = append(metas, metaFromProto(m))
	}
	return metas, nil
}

// Fetch issues SegmentService.Fetch for the named ids and maps the proto blobs
// to engine blobs. The interface gives Fetch no ctx; it uses the source's
// fetchCtx.
func (s *rpcSegmentSource) Fetch(ids []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	resp, err := s.caller.Fetch(s.fetchCtx, &knowledgev1.FetchRequest{
		Target:   s.target,
		Ids:      ids,
		WriterId: s.writerID,
	})
	if err != nil {
		return nil, err
	}
	blobs := make([]searchengine.SegmentBlob, 0, len(resp.GetBlobs()))
	for _, b := range resp.GetBlobs() {
		blobs = append(blobs, blobFromProto(b))
	}
	return blobs, nil
}

// Prune issues SegmentService.Prune for the named ids (the merged-away segments
// the manager reconciled off the server) and returns how many the server
// actually deleted. Mirrors Fetch — it uses the source's fetchCtx and routes the
// graph's target selector.
func (s *rpcSegmentSource) Prune(ids []searchengine.SegmentID) (int, error) {
	resp, err := s.caller.Prune(s.fetchCtx, &knowledgev1.PruneRequest{
		Target: s.target,
		Ids:    ids,
	})
	if err != nil {
		return 0, err
	}
	return int(resp.GetDeleted()), nil
}

// PublishManifest issues SegmentService.Publish with this writer's full live
// id-set for one format — the registry-model manifest swap that replaces the
// unconditional Prune delete. The server atomically swaps this writer's manifest
// and reference-count-GCs blobs no manifest references; it returns how many it
// deleted. Mirrors Prune — uses the source's fetchCtx and routes the graph's
// target selector — but carries the writer/format dimensions the manifest needs.
func (s *rpcSegmentSource) PublishManifest(format string, ids []searchengine.SegmentID) (int, error) {
	resp, err := s.caller.Publish(s.fetchCtx, &knowledgev1.PublishRequest{
		Target:   s.target,
		WriterId: s.writerID,
		Format:   format,
		Ids:      ids,
	})
	if err != nil {
		return 0, err
	}
	return int(resp.GetDeleted()), nil
}

// blobToProto maps an engine SegmentBlob to the wire carrier. Reused by the
// manager's ship path (Phase 4).
func blobToProto(b searchengine.SegmentBlob) *knowledgev1.SegmentBlobProto {
	return &knowledgev1.SegmentBlobProto{
		Id:         b.ID,
		Format:     b.Format,
		Generation: b.Generation,
		DocCount:   int32(b.DocCount),
		Bytes:      b.Bytes,
	}
}

// blobFromProto maps a wire carrier to an engine SegmentBlob.
func blobFromProto(p *knowledgev1.SegmentBlobProto) searchengine.SegmentBlob {
	return searchengine.SegmentBlob{
		ID:         p.GetId(),
		Format:     p.GetFormat(),
		Generation: p.GetGeneration(),
		DocCount:   int(p.GetDocCount()),
		Bytes:      p.GetBytes(),
	}
}

// metaFromProto maps a wire meta carrier to an engine SegmentMeta.
func metaFromProto(p *knowledgev1.SegmentMetaProto) searchengine.SegmentMeta {
	return searchengine.SegmentMeta{
		ID:         p.GetId(),
		Format:     p.GetFormat(),
		Generation: p.GetGeneration(),
		DocCount:   int(p.GetDocCount()),
		DeadCount:  int(p.GetDeadCount()),
	}
}
