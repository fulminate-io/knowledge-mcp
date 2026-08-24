// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"crypto/tls"
	"errors"
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

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/remote"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// capturingSink is a collector.Sink that records the *collectorwire.CollectResult
// it receives instead of persisting it. It lets the in-process ingest harness
// verify the client-side remote.UploadSink → server-side CollectChunk/Finalize
// roundtrip transfers the full node+edge payload over the wire without standing
// up a real store engine. Thread-safe.
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
// prior-knowledge (h2c) to the httptest server — what the real MCP client does.
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
// IngestServiceHandler. It mirrors the production connectAdapter
// (cmd/knowledge-server/internal/bootstrap/ingest.go) closely enough to exercise
// the CollectChunk×N → Finalize roundtrip: each CollectChunk accumulates its
// INLINE nodes + edges (no arena — chunks carry their nodes), and Finalize flushes
// the accumulated set to the capturing sink keyed by the collection epoch.
//
// Lives here (client-side): the test exercises the client's
// remote.UploadSink against an in-process server-side fixture, and the production
// connectAdapter is unreachable from cmd/knowledge/internal/ (Go's internal/ rule).
type testIngestHandler struct {
	mu sync.Mutex
	// per-epoch accumulation of the inline nodes + edges seen across CollectChunk.
	nodesByEpoch map[uint64][]*knowledgev1.Node
	edgesByEpoch map[uint64][]*knowledgev1.BatchEdge
	metaByEpoch  map[uint64]*knowledgev1.CollectChunkRequest
	sink         *capturingSink
}

var _ knowledgev1connect.IngestServiceHandler = (*testIngestHandler)(nil)

func newTestIngestHandler() *testIngestHandler {
	return &testIngestHandler{
		nodesByEpoch: map[uint64][]*knowledgev1.Node{},
		edgesByEpoch: map[uint64][]*knowledgev1.BatchEdge{},
		metaByEpoch:  map[uint64]*knowledgev1.CollectChunkRequest{},
		sink:         &capturingSink{},
	}
}

// CollectManifest answers as a genuinely EMPTY graph: no entries and zero live
// nodes is the first-collect shape, so this fake never pushes a test onto the
// fail-closed path for a reason the test did not choose.
func (h *testIngestHandler) CollectManifest(
	context.Context,
	*connect.Request[knowledgev1.CollectManifestRequest],
) (*connect.Response[knowledgev1.CollectManifestResponse], error) {
	return connect.NewResponse(&knowledgev1.CollectManifestResponse{
		ManifestId: "test-manifest", HashSchemeVersion: contribhash.ContributionHashSchemeVersion,
	}), nil
}

func (h *testIngestHandler) CollectChunk(
	_ context.Context,
	req *connect.Request[knowledgev1.CollectChunkRequest],
) (*connect.Response[knowledgev1.CollectChunkResponse], error) {
	m := req.Msg
	h.mu.Lock()
	h.nodesByEpoch[m.Epoch] = append(h.nodesByEpoch[m.Epoch], m.GetNodes()...)
	h.edgesByEpoch[m.Epoch] = append(h.edgesByEpoch[m.Epoch], m.GetEdges()...)
	h.metaByEpoch[m.Epoch] = m
	h.mu.Unlock()
	return connect.NewResponse(&knowledgev1.CollectChunkResponse{}), nil
}

func (h *testIngestHandler) Finalize(
	ctx context.Context,
	req *connect.Request[knowledgev1.FinalizeRequest],
) (*connect.Response[knowledgev1.FinalizeResponse], error) {
	m := req.Msg
	h.mu.Lock()
	nodes := h.nodesByEpoch[m.Epoch]
	edges := h.edgesByEpoch[m.Epoch]
	meta := h.metaByEpoch[m.Epoch]
	h.mu.Unlock()

	gt := kgtypes.GraphType(m.GraphType)
	name := m.GraphName
	if meta != nil {
		gt = kgtypes.GraphType(meta.GraphType)
		name = meta.GraphName
	}
	result := &collectorwire.CollectResult{
		GraphType:     gt,
		GraphName:     name,
		Nodes:         nodes,
		Edges:         batchEdgesFromProtoForTest(edges),
		CurrentBranch: m.CurrentBranch,
	}
	if err := h.sink.WriteResult(ctx, "", result); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&knowledgev1.FinalizeResponse{}), nil
}

// FinalizeStatus completes the handler interface. This handler does all its work
// in Finalize and returns no finalize_id, so the sink never polls it; UNKNOWN is
// the honest answer for a server that tracks nothing. logsIngestHandler embeds
// this type and inherits it.
func (h *testIngestHandler) FinalizeStatus(
	context.Context,
	*connect.Request[knowledgev1.FinalizeStatusRequest],
) (*connect.Response[knowledgev1.FinalizeStatusResponse], error) {
	return connect.NewResponse(&knowledgev1.FinalizeStatusResponse{
		State: knowledgev1.FinalizeState_FINALIZE_STATE_UNKNOWN,
	}), nil
}

func (h *testIngestHandler) FetchCloudSubgraph(
	context.Context,
	*connect.Request[knowledgev1.FetchCloudSubgraphRequest],
) (*connect.Response[knowledgev1.FetchCloudSubgraphResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("FetchCloudSubgraph not exercised by this test"))
}

// TestIngest_CollectChunkFinalize_Roundtrip wires a connect-go in-process test
// harness: client-side remote.UploadSink → server-side testIngestHandler.
// Drives a CollectResult through CollectChunk×N + Finalize and verifies the full
// node + edge payload crossed the wire inline.
func TestIngest_CollectChunkFinalize_Roundtrip(t *testing.T) {
	handler := newTestIngestHandler()
	mux := http.NewServeMux()
	path, h := knowledgev1connect.NewIngestServiceHandler(handler)
	mux.Handle(path, h)

	h2s := &http2.Server{}
	srv := httptest.NewServer(h2c.NewHandler(mux, h2s))
	t.Cleanup(srv.Close)

	client := knowledgev1connect.NewIngestServiceClient(h2cClient(), srv.URL, connect.WithGRPC())
	sink := remote.NewUploadSink(client)

	// A KNOWLEDGE GRAPH, NOT A CODE ONE, and the family is load-bearing for what
	// this test asserts. It pins that an INDEX-ADDRESSED edge (FromIdx/ToIdx rather
	// than FromID/ToID) crosses the wire intact. Only code graphs reach the
	// incremental diff, whose filter keeps an edge by looking its FROM NODE up by
	// ID — an index-addressed edge carries no FromID, so on a code graph the armed
	// lane would drop it and this test would be measuring the diff rather than the
	// roundtrip. The code collector only ever emits ID-addressed edges
	// (parser.ToBatchEdges sets -1/-1 with both IDs), so the shape under test here
	// belongs to the families outside that gate.
	result := &collectorwire.CollectResult{
		GraphType: kgtypes.GraphKnowledge,
		GraphName: "ingest-roundtrip-repo",
		Nodes: []*knowledgev1.Node{
			{Id: "rt-1", Type: string(kgtypes.NodeFile), SymbolName: "a.go", FilePath: "a.go", Content: "package a"},
			{Id: "rt-2", Type: string(kgtypes.NodeFile), SymbolName: "b.go", FilePath: "b.go", Content: "package b"},
		},
		Edges: []kgwire.BatchEdge{{FromIdx: 0, ToIdx: 1, Type: kgtypes.EdgeContains}},
	}
	require.NoError(t, sink.WriteResult(context.Background(), "test-collector", result))

	// Verify the full node+edge payload crossed the wire inline: the client
	// chunked the nodes into CollectChunk calls and the handler accumulated them,
	// flushing to the capturing sink on Finalize.
	captured := handler.sink.last()
	require.NotNil(t, captured, "Finalize must have captured a CollectResult")
	assert.Equal(t, kgtypes.GraphKnowledge, captured.GraphType)
	assert.Equal(t, "ingest-roundtrip-repo", captured.GraphName)

	require.Len(t, captured.Nodes, 2, "both NodeFile nodes must have crossed the wire")
	byID := map[string]*knowledgev1.Node{}
	for _, n := range captured.Nodes {
		byID[n.Id] = n
	}
	require.Contains(t, byID, "rt-1")
	require.Contains(t, byID, "rt-2")
	assert.Equal(t, "a.go", byID["rt-1"].FilePath)
	assert.Equal(t, "b.go", byID["rt-2"].FilePath)

	require.Len(t, captured.Edges, 1, "the EdgeContains edge must have crossed the wire")
	assert.Equal(t, kgtypes.EdgeContains, captured.Edges[0].Type)
	assert.Equal(t, 0, captured.Edges[0].FromIdx)
	assert.Equal(t, 1, captured.Edges[0].ToIdx)
}

// TestIngest_MultiChunk_EdgesResolveAcrossChunks proves the cross-chunk edge
// guarantee: nodes split across more than one CollectChunk still receive their
// edges (ID-addressed) on the final chunk, and every node lands. Forces N>1
// chunks with a tiny byte budget via many nodes.
func TestIngest_MultiChunk_EdgesResolveAcrossChunks(t *testing.T) {
	handler := newTestIngestHandler()
	mux := http.NewServeMux()
	path, h := knowledgev1connect.NewIngestServiceHandler(handler)
	mux.Handle(path, h)
	h2s := &http2.Server{}
	srv := httptest.NewServer(h2c.NewHandler(mux, h2s))
	t.Cleanup(srv.Close)

	client := knowledgev1connect.NewIngestServiceClient(h2cClient(), srv.URL, connect.WithGRPC())
	sink := remote.NewUploadSink(client)

	// Many nodes so DefaultBatchBytes is exceeded only if we shrink it; instead
	// rely on a large content payload to push the count of chunks above 1.
	const n = 200
	nodes := make([]*knowledgev1.Node, n)
	big := make([]byte, 64*1024) // 64 KiB content per node
	for i := range nodes {
		nodes[i] = &knowledgev1.Node{
			Id: "n-" + string(rune('A'+i%26)) + "-" + itoa(i), Type: string(kgtypes.NodeFile),
			FilePath: "f.go", Content: string(big),
		}
	}
	// An ID-addressed edge between the FIRST and LAST node — they land in
	// different chunks, so this exercises cross-chunk resolution.
	edges := []kgwire.BatchEdge{{
		FromIdx: -1, ToIdx: -1, FromID: nodes[0].Id, ToID: nodes[n-1].Id, Type: kgtypes.EdgeContains,
	}}
	result := &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCode, GraphName: "multichunk-repo", Nodes: nodes, Edges: edges,
		// Stamped as a real code collect is: an empty fingerprint aborts, and so
		// does an unstamped collector output version.
		DiscoveryFingerprint:   "fingerprint-ingest-multichunk",
		CollectorOutputVersion: parser.CollectorOutputVersion,
	}
	require.NoError(t, sink.WriteResult(context.Background(), "test-collector", result))

	captured := handler.sink.last()
	require.NotNil(t, captured)
	require.Len(t, captured.Nodes, n, "every node across every chunk must land")
	require.Len(t, captured.Edges, 1, "the ID-addressed edge must ride the final chunk")
	assert.Equal(t, nodes[0].Id, captured.Edges[0].FromID)
	assert.Equal(t, nodes[n-1].Id, captured.Edges[0].ToID)
}

// itoa is a tiny strconv.Itoa avoiding an import just for the fixture id.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
