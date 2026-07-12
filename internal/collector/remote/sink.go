// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// IngestClientPicker resolves the IngestService client to use for one call.
// It is invoked PER CALL (WriteResult, collectChunkWithRetry, FetchCloudSubgraph)
// so a mid-session `knowledge login` flip re-routes the next chunk to the cloud
// backend without a process restart. Router.IngestClient(ctx) satisfies this
// shape (router.go); NewUploadSink wraps a fixed client into a constant picker.
type IngestClientPicker func(ctx context.Context) (knowledgev1connect.IngestServiceClient, error)

// UploadSink implements collector.Sink by driving the unary IngestService
// CollectChunk + Finalize flow. Used by cmd/knowledge (the MCP stdio client) so
// collection runs client-side while indexing runs server-side. Stateless on the
// wire: each chunk's nodes ride INLINE, so any server replica can land any
// chunk (no per-process arena).
//
// The IngestService client is resolved PER CALL via picker so login-aware
// routing (local vs cloud) honors a mid-session login flip; the sink never
// caches a resolved client across calls.
type UploadSink struct {
	picker IngestClientPicker
	// epoch is the per-collection monotonic counter minted client-side. Single
	// process, so a local atomic is authoritative; every chunk of one collection
	// AND its Finalize share the value from one Add(1). Zero-value valid.
	epoch atomic.Uint64
}

// NewUploadSink constructs an UploadSink wired to a FIXED IngestService client.
// Retained as the constant-picker convenience for callers (and tests) that
// route to a single backend; it wraps client into a picker that always returns
// it. Login-aware callers use NewUploadSinkFunc instead.
func NewUploadSink(client knowledgev1connect.IngestServiceClient) *UploadSink {
	return &UploadSink{picker: func(context.Context) (knowledgev1connect.IngestServiceClient, error) {
		return client, nil
	}}
}

// NewUploadSinkFunc constructs an UploadSink whose IngestService client is
// resolved per call via picker — the login-aware path (Router.IngestClient).
func NewUploadSinkFunc(picker IngestClientPicker) *UploadSink {
	return &UploadSink{picker: picker}
}

// Compile-time assertion.
var _ collector.Sink = (*UploadSink)(nil)

// WriteResult mints a per-collection epoch, byte-packs the nodes and the edges
// into SEPARATE CollectChunk requests, sends each chunk, then a single Finalize
// with the same epoch. Nodes AND edges both pack at DefaultBatchBytes (4 MiB) —
// nodes carry NO edges, edge chunks carry nil Nodes — so no single request body
// exceeds the bite-sized 4 MiB frame. Edges previously packed at
// kgwire.MaxCloudRequestBytes (64 MiB), but the fronting proxy on the cloud
// ingest path rejects bodies FAR below 64 MiB: a single ~14 MiB edge chunk
// (≈110k ID-addressed CALLS edges from a large repo) drew a raw HTTP 400 from the
// proxy while the 4 MiB node chunks all passed. An intermediary only sees body
// size, so edges now share the node cap.
// Spreading edges across multiple chunks is SAFE: collector edges are
// ID-addressed (FromIdx/ToIdx == -1, FromID/ToID set — see kgwire.BatchEdge
// build sites), so they resolve regardless of which chunk a referenced node
// arrived in, and the server dedups (From,Type,To) tuples across chunks and the
// resident graph bundles. Per-chunk transport retry rides the existing reconnect
// interceptor and is content-idempotent for BOTH nodes and edges: a node
// re-lands identically through the server's carry-forward upsert + epoch GC, and
// an edge re-lands at most once because the server filters duplicate (From,Type,
// To) tuples. A retry of any edge chunk therefore does not double its edges.
func (s *UploadSink) WriteResult(ctx context.Context, collectorName string, result *collectorwire.CollectResult) error {
	client, err := s.picker(ctx)
	if err != nil {
		return fmt.Errorf("remote sink: resolve ingest client: %w", err)
	}
	epoch := s.epoch.Add(1)
	// Sanitize node text BEFORE the proto marshal: the inline-Node wire marshals
	// typed proto Node messages, and proto3 string fields reject invalid UTF-8 at
	// marshal time. (The server re-sanitizes on write; double-sanitize is safe.)
	for _, n := range result.Nodes {
		sanitizeNodeText(n)
	}
	nodeChunks := BatchNodes(result.Nodes, DefaultBatchBytes)
	protoEdges := kgwire.BatchEdgesToProto(result.Edges)
	// Edges ride their OWN chunks at the SAME 4 MiB cap as nodes, NEVER on a
	// node-chunk: both node- and edge-chunks stay ≤4 MiB, so every emitted
	// CollectChunk request body stays bite-sized regardless of how large the edge
	// tail grows. The server dedups (From,Type,To) tuples across chunks, so any N
	// edge chunks land the full edge set exactly once.
	edgeChunks := BatchEdgesProto(protoEdges, DefaultBatchBytes)

	// build assembles a CollectChunkRequest carrying one node group and/or one
	// edge group under the shared epoch + graph identity.
	build := func(nodes []*knowledgev1.Node, edges []*knowledgev1.BatchEdge) *knowledgev1.CollectChunkRequest {
		return &knowledgev1.CollectChunkRequest{
			Epoch:         epoch,
			GraphType:     string(result.GraphType),
			GraphName:     result.GraphName,
			CurrentBranch: result.CurrentBranch,
			Promote:       result.Promote,
			SyncCommit:    result.SyncCommit,
			SyncTime:      result.SyncTime,
			Nodes:         nodes,
			Edges:         edges,
		}
	}

	// Assemble the ordered request list: every node-chunk (no edges) first, then
	// every edge-chunk (nil nodes).
	var reqs []*knowledgev1.CollectChunkRequest
	for _, nc := range nodeChunks {
		reqs = append(reqs, build(nc, nil))
	}
	for _, ec := range edgeChunks {
		reqs = append(reqs, build(nil, ec))
	}
	// Always send at least one CollectChunk so an empty-node + empty-edge
	// collection (deletion-only recollect) still reaches the server and Finalize
	// has an epoch the server has seen.
	if len(reqs) == 0 {
		reqs = append(reqs, build(nil, nil))
	}
	// Timing instrumentation: the CollectChunk upload loop + Finalize are the
	// foreground of the collect tool call (the client blocks here until the
	// server acks each RPC). Logged at debug so a slow collect can be traced to
	// the exact chunk or the Finalize, not an unattributed silent gap.
	uploadStart := time.Now()
	slog.Debug("remote sink: upload start", "collector", collectorName,
		"graph_type", result.GraphType, "graph", result.GraphName, "branch", result.CurrentBranch,
		"epoch", epoch, "chunks", len(reqs), "node_chunks", len(nodeChunks), "edge_chunks", len(edgeChunks),
		"nodes", len(result.Nodes), "edges", len(result.Edges))
	for i, req := range reqs {
		chunkStart := time.Now()
		if err := s.collectChunkWithRetry(ctx, req); err != nil {
			return fmt.Errorf("remote sink: CollectChunk %d/%d: %w", i+1, len(reqs), err)
		}
		slog.Debug("remote sink: chunk sent", "i", i+1, "of", len(reqs),
			"nodes", len(req.Nodes), "edges", len(req.Edges), "dur", time.Since(chunkStart).Round(time.Millisecond))
	}
	slog.Debug("remote sink: all chunks uploaded", "graph", result.GraphName, "branch", result.CurrentBranch,
		"epoch", epoch, "chunks", len(reqs), "dur", time.Since(uploadStart).Round(time.Millisecond))

	finReq := connect.NewRequest(&knowledgev1.FinalizeRequest{
		Epoch:         epoch,
		GraphType:     string(result.GraphType),
		GraphName:     result.GraphName,
		CurrentBranch: result.CurrentBranch,
		Promote:       result.Promote,
	})
	finStart := time.Now()
	if _, err := client.Finalize(ctx, finReq); err != nil {
		return fmt.Errorf("remote sink: Finalize: %w", err)
	}
	slog.Debug("remote sink: finalize done", "graph", result.GraphName, "branch", result.CurrentBranch,
		"epoch", epoch, "dur", time.Since(finStart).Round(time.Millisecond))
	return nil
}

// collectChunkWithRetry sends one CollectChunk, retrying on a retryable
// transport error (server restart mid-collection, TCP drop) via the existing
// graphclient backoff. Content-idempotent for both nodes and edges: re-sending
// the same chunk under the same epoch re-lands nodes identically through the
// carry-forward upsert + epoch GC, and re-lands edges at most once because the
// server's collect edge-landing path filters duplicate (From,Type,To) tuples
// against the batch and the resident graph bundles.
func (s *UploadSink) collectChunkWithRetry(ctx context.Context, msg *knowledgev1.CollectChunkRequest) error {
	client, err := s.picker(ctx)
	if err != nil {
		return fmt.Errorf("remote sink: resolve ingest client: %w", err)
	}
	maxAttempts := len(graphclient.RetryBackoff) + 1
	var attemptErr error
	for attempt := range maxAttempts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, err := client.CollectChunk(ctx, connect.NewRequest(msg))
		if err == nil {
			return nil
		}
		if !graphclient.IsRetryableTransportError(err) {
			return err
		}
		attemptErr = err
		if attempt == len(graphclient.RetryBackoff) {
			break
		}
		select {
		case <-time.After(graphclient.RetryBackoff[attempt]):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("CollectChunk exhausted after %d attempts: %w", maxAttempts, attemptErr)
}

// edgesFromProto converts the typed proto Edge carrier into []knowledgev1.Edge —
// the remote-package decode for the FetchCloudSubgraph slice edges (the value
// shape cloudresolver.GraphSlice.Edges expects). Mirrors the engine package's
// EdgesFromProto (kept local so the collector/remote package does not depend on
// the engine decode package). Empty carrier → nil.
func edgesFromProto(in []*knowledgev1.Edge) []knowledgev1.Edge {
	if len(in) == 0 {
		return nil
	}
	out := make([]knowledgev1.Edge, len(in))
	for i, e := range in {
		out[i] = knowledgev1.Edge{
			FromId:        e.GetFromId(),
			ToId:          e.GetToId(),
			Type:          e.GetType(),
			Weight:        e.GetWeight(),
			Confidence:    e.GetConfidence(),
			Method:        e.GetMethod(),
			Evidence:      e.GetEvidence(),
			LastValidated: e.GetLastValidated(),
		}
	}
	return out
}
