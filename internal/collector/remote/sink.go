// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"fmt"
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

// UploadSink implements collector.Sink by driving the unary IngestService
// CollectChunk + Finalize flow. Used by cmd/knowledge (the MCP stdio client) so
// collection runs client-side while indexing runs server-side. Stateless on the
// wire: each chunk's nodes ride INLINE, so any server replica can land any
// chunk (no per-process arena).
type UploadSink struct {
	client knowledgev1connect.IngestServiceClient
	// epoch is the per-collection monotonic counter minted client-side. Single
	// process, so a local atomic is authoritative; every chunk of one collection
	// AND its Finalize share the value from one Add(1). Zero-value valid.
	epoch atomic.Uint64
}

// NewUploadSink constructs an UploadSink wired to the IngestService client
// exposed by the per-process GraphClient.
func NewUploadSink(client knowledgev1connect.IngestServiceClient) *UploadSink {
	return &UploadSink{client: client}
}

// Compile-time assertion.
var _ collector.Sink = (*UploadSink)(nil)

// WriteResult mints a per-collection epoch, chunks the nodes by byte budget,
// sends one CollectChunk per chunk (nodes INLINE), then a single Finalize with
// the same epoch. All edges ride the FINAL chunk: collector edges are
// ID-addressed (FromIdx/ToIdx == -1, FromID/ToID set — see kgwire.BatchEdge
// build sites), so they resolve regardless of which chunk a referenced node
// arrived in. Per-chunk transport retry rides the existing reconnect
// interceptor (content-idempotent: the same epoch re-lands identically through
// the server's carry-forward upsert).
func (s *UploadSink) WriteResult(ctx context.Context, collectorName string, result *collectorwire.CollectResult) error {
	epoch := s.epoch.Add(1)
	// Sanitize node text BEFORE the proto marshal: the inline-Node wire marshals
	// typed proto Node messages, and proto3 string fields reject invalid UTF-8 at
	// marshal time. (The server re-sanitizes on write; double-sanitize is safe.)
	for _, n := range result.Nodes {
		sanitizeNodeText(n)
	}
	chunks := BatchNodes(result.Nodes, DefaultBatchBytes)
	protoEdges := kgwire.BatchEdgesToProto(result.Edges)

	// Always send at least one CollectChunk so the edges + an empty-node
	// collection (deletion-only recollect) still reach the server, and so
	// Finalize has an epoch the server has seen.
	if len(chunks) == 0 {
		chunks = [][]*knowledgev1.Node{nil}
	}
	for i, chunk := range chunks {
		var edges []*knowledgev1.BatchEdge
		if i == len(chunks)-1 {
			edges = protoEdges
		}
		req := &knowledgev1.CollectChunkRequest{
			Epoch:         epoch,
			GraphType:     string(result.GraphType),
			GraphName:     result.GraphName,
			CurrentBranch: result.CurrentBranch,
			SyncCommit:    result.SyncCommit,
			SyncTime:      result.SyncTime,
			Nodes:         chunk,
			Edges:         edges,
		}
		if err := s.collectChunkWithRetry(ctx, req); err != nil {
			return fmt.Errorf("remote sink: CollectChunk %d/%d: %w", i+1, len(chunks), err)
		}
	}

	finReq := connect.NewRequest(&knowledgev1.FinalizeRequest{
		Epoch:         epoch,
		GraphType:     string(result.GraphType),
		GraphName:     result.GraphName,
		CurrentBranch: result.CurrentBranch,
	})
	if _, err := s.client.Finalize(ctx, finReq); err != nil {
		return fmt.Errorf("remote sink: Finalize: %w", err)
	}
	return nil
}

// collectChunkWithRetry sends one CollectChunk, retrying on a retryable
// transport error (server restart mid-collection, TCP drop) via the existing
// graphclient backoff. Content-idempotent: re-sending the same chunk under the
// same epoch re-lands identically through the carry-forward upsert.
func (s *UploadSink) collectChunkWithRetry(ctx context.Context, msg *knowledgev1.CollectChunkRequest) error {
	maxAttempts := len(graphclient.RetryBackoff) + 1
	var attemptErr error
	for attempt := range maxAttempts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, err := s.client.CollectChunk(ctx, connect.NewRequest(msg))
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
