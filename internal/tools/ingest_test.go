// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
)

// capturingSink is a collector.Sink that records the *collectorwire.CollectResult
// it receives instead of persisting it. It lets the in-process ingest harness
// verify the client-side remote.UploadSink → server-side WriteResult roundtrip
// transfers the full node+edge payload over the wire without standing up a real
// store engine and no real persistence sink. Thread-safe so the bidi
// UploadChunks goroutine and the terminal WriteResult can race-freely hand off.
type capturingSink struct {
	mu      sync.Mutex
	results []*collectorwire.CollectResult
}

func (c *capturingSink) WriteResult(_ context.Context, _ string, result *collectorwire.CollectResult) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results = append(c.results, result)
	return nil
}

// last returns the most recently captured CollectResult, or nil when none was
// written.
func (c *capturingSink) last() *collectorwire.CollectResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.results) == 0 {
		return nil
	}
	return c.results[len(c.results)-1]
}

// h2cClient returns an *http.Client that dials plain TCP and speaks HTTP/2
// prior-knowledge (h2c) to the httptest server. This is what the
// real MCP client does: bi-di streaming ingest talks HTTP/2 over
// cleartext.
func h2cClient() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(_ context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return net.Dial(network, addr)
			},
		},
	}
}

// testIngestHandler is a minimal in-process implementation of the
// IngestServiceHandler. It mirrors the production server-side
// connectAdapter (cmd/knowledge-server/bootstrap/ingest.go) closely
// enough to exercise the UploadChunks → WriteResult roundtrip end to
// end: a per-test chunk arena holds hashes between the bidi stream and
// the terminal materialize+sink call.
//
// Lives here (client-side) post-BCN9: the test exercises the client's
// remote.UploadSink against an in-process server-side fixture, and the
// production connectAdapter is unreachable from cmd/knowledge/internal/
// because of Go's internal/ rule.
type testIngestHandler struct {
	mu     sync.Mutex
	byHash map[string][]byte
	// sink captures the materialized CollectResult on WriteResult instead of
	// persisting it — no real store engine is linked into the test binary.
	sink *capturingSink
}

var _ knowledgev1connect.IngestServiceHandler = (*testIngestHandler)(nil)

func newTestIngestHandler() *testIngestHandler {
	return &testIngestHandler{byHash: map[string][]byte{}, sink: &capturingSink{}}
}

func (h *testIngestHandler) UploadChunks(
	ctx context.Context,
	stream *connect.BidiStream[knowledgev1.ChunkBatch, knowledgev1.ChunkAck],
) error {
	for {
		batch, err := stream.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return connect.NewError(connect.CodeInternal, err)
		}
		ack := &knowledgev1.ChunkAck{}
		h.mu.Lock()
		for _, c := range batch.Chunks {
			if _, ok := h.byHash[c.Hash]; ok {
				ack.AlreadyHaveHashes = append(ack.AlreadyHaveHashes, c.Hash)
				continue
			}
			h.byHash[c.Hash] = c.NodeJson
			ack.AcceptedHashes = append(ack.AcceptedHashes, c.Hash)
		}
		h.mu.Unlock()
		if err := stream.Send(ack); err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (h *testIngestHandler) WriteResult(
	ctx context.Context,
	req *connect.Request[knowledgev1.WriteResultRequest],
) (*connect.Response[knowledgev1.WriteResultResponse], error) {
	m := req.Msg
	h.mu.Lock()
	nodes := make([]*knowledgev1.Node, 0, len(m.NodeHashes))
	for _, hash := range m.NodeHashes {
		body, ok := h.byHash[hash]
		if !ok {
			h.mu.Unlock()
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("ingest: chunk %q not found in arena", hash))
		}
		var n knowledgev1.Node
		if err := json.Unmarshal(body, &n); err != nil {
			h.mu.Unlock()
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("ingest: unmarshal chunk %q: %w", hash, err))
		}
		nodes = append(nodes, &n)
	}
	h.mu.Unlock()

	result := &collectorwire.CollectResult{
		GraphType:     kgtypes.GraphType(m.GraphType),
		GraphName:     m.GraphName,
		Nodes:         nodes,
		Edges:         batchEdgesFromProtoForTest(m.GetEdges()),
		CurrentBranch: m.CurrentBranch,
	}
	// Capture the materialized batch instead of persisting it — no real store
	// engine is linked into the test binary. The roundtrip assertion reads the
	// captured CollectResult directly.
	if err := h.sink.WriteResult(ctx, m.CollectorName, result); err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("ingest: WriteResult: %w", err))
	}
	return connect.NewResponse(&knowledgev1.WriteResultResponse{}), nil
}

func (h *testIngestHandler) FetchCloudSubgraph(
	context.Context,
	*connect.Request[knowledgev1.FetchCloudSubgraphRequest],
) (*connect.Response[knowledgev1.FetchCloudSubgraphResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("FetchCloudSubgraph not exercised by this test"))
}

// TestIngest_WriteResult_Roundtrip wires a connect-go in-process test
// harness: client-side remote.UploadSink → server-side testIngestHandler.
// Uploads a CollectResult, verifies the server materializes identical
// nodes + edges in the target graph.
func TestIngest_WriteResult_Roundtrip(t *testing.T) {
	// Minimal server: just the IngestService handler, wrapped in h2c so
	// the bi-di UploadChunks stream works (Connect bi-di requires HTTP/2).
	// The handler captures the materialized CollectResult instead of
	// persisting it — no real store engine is linked into the test binary.
	handler := newTestIngestHandler()
	mux := http.NewServeMux()
	path, h := knowledgev1connect.NewIngestServiceHandler(handler)
	mux.Handle(path, h)

	h2s := &http2.Server{}
	srv := httptest.NewServer(h2c.NewHandler(mux, h2s))
	t.Cleanup(srv.Close)

	client := knowledgev1connect.NewIngestServiceClient(h2cClient(), srv.URL, connect.WithGRPC())
	sink := remote.NewUploadSink(client)

	result := &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCode,
		GraphName: "ingest-roundtrip-repo",
		Nodes: []*knowledgev1.Node{
			{Id: "rt-1", Type: string(kgtypes.NodeFile), SymbolName: "a.go", FilePath: "a.go", Content: "package a"},
			{Id: "rt-2", Type: string(kgtypes.NodeFile), SymbolName: "b.go", FilePath: "b.go", Content: "package b"},
		},
		Edges: []kgwire.BatchEdge{{FromIdx: 0, ToIdx: 1, Type: kgtypes.EdgeContains}},
	}
	require.NoError(t, sink.WriteResult(context.Background(), "test-collector", result))

	// Verify the full node+edge payload crossed the wire by inspecting the
	// captured CollectResult — the client-side remote.UploadSink chunked the
	// nodes, the server-side WriteResult reassembled them by hash, and the
	// capturing sink recorded the materialized batch.
	captured := handler.sink.last()
	require.NotNil(t, captured, "WriteResult must have captured a CollectResult")
	assert.Equal(t, kgtypes.GraphCode, captured.GraphType)
	assert.Equal(t, "ingest-roundtrip-repo", captured.GraphName)

	require.Len(t, captured.Nodes, 2, "both NodeFile nodes must have crossed the wire")
	byID := map[string]*knowledgev1.Node{}
	for _, n := range captured.Nodes {
		byID[n.Id] = n
	}
	require.Contains(t, byID, "rt-1")
	require.Contains(t, byID, "rt-2")
	assert.Equal(t, string(kgtypes.NodeFile), byID["rt-1"].Type)
	assert.Equal(t, "a.go", byID["rt-1"].FilePath)
	assert.Equal(t, string(kgtypes.NodeFile), byID["rt-2"].Type)
	assert.Equal(t, "b.go", byID["rt-2"].FilePath)

	require.Len(t, captured.Edges, 1, "the EdgeContains edge must have crossed the wire")
	assert.Equal(t, kgtypes.EdgeContains, captured.Edges[0].Type)
	assert.Equal(t, 0, captured.Edges[0].FromIdx)
	assert.Equal(t, 1, captured.Edges[0].ToIdx)
}
