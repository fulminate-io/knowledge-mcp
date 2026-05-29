// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// UploadSink implements collector.Sink by streaming every write call over
// the IngestService bi-di + unary RPCs. Used by cmd/knowledge (the MCP
// stdio client) so collection runs client-side while indexing runs
// server-side.
type UploadSink struct {
	client knowledgev1connect.IngestServiceClient
}

// NewUploadSink constructs an UploadSink wired to the IngestService client
// exposed by the per-process GraphClient.
func NewUploadSink(client knowledgev1connect.IngestServiceClient) *UploadSink {
	return &UploadSink{client: client}
}

// Compile-time assertion.
var _ collector.Sink = (*UploadSink)(nil)

// WriteResult streams the nodes as chunks, then issues a WriteResult RPC
// referencing the chunk hashes.
func (s *UploadSink) WriteResult(ctx context.Context, collectorName string, result *collectorwire.CollectResult) error {
	hashes, err := s.uploadChunks(ctx, result.Nodes)
	if err != nil {
		return fmt.Errorf("remote sink: upload chunks: %w", err)
	}

	req := connect.NewRequest(&knowledgev1.WriteResultRequest{
		CollectorName: collectorName,
		GraphType:     string(result.GraphType),
		GraphName:     result.GraphName,
		CurrentBranch: result.CurrentBranch,
		SyncCommit:    result.SyncCommit,
		SyncTime:      result.SyncTime,
		NodeHashes:    hashes,
		Edges:         kgwire.BatchEdgesToProto(result.Edges),
	})
	if _, err := s.client.WriteResult(ctx, req); err != nil {
		return fmt.Errorf("remote sink: WriteResult: %w", err)
	}
	return nil
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
