// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// recordingIngest is an IngestServiceHandler that captures every
// CollectChunkRequest it receives so a test can assert per-request size + full
// node/edge reassembly across all chunks. It clones each request (the connect
// runtime may recycle the message) and guards the slice for concurrency safety.
type recordingIngest struct {
	mu     sync.Mutex
	chunks []*knowledgev1.CollectChunkRequest
}

var _ knowledgev1connect.IngestServiceHandler = (*recordingIngest)(nil)

func (e *recordingIngest) CollectChunk(
	_ context.Context,
	req *connect.Request[knowledgev1.CollectChunkRequest],
) (*connect.Response[knowledgev1.CollectChunkResponse], error) {
	e.mu.Lock()
	e.chunks = append(e.chunks, proto.Clone(req.Msg).(*knowledgev1.CollectChunkRequest))
	e.mu.Unlock()
	return connect.NewResponse(&knowledgev1.CollectChunkResponse{}), nil
}

func (e *recordingIngest) Finalize(
	context.Context,
	*connect.Request[knowledgev1.FinalizeRequest],
) (*connect.Response[knowledgev1.FinalizeResponse], error) {
	return connect.NewResponse(&knowledgev1.FinalizeResponse{}), nil
}

func (e *recordingIngest) FetchCloudSubgraph(
	context.Context,
	*connect.Request[knowledgev1.FetchCloudSubgraphRequest],
) (*connect.Response[knowledgev1.FetchCloudSubgraphResponse], error) {
	return connect.NewResponse(&knowledgev1.FetchCloudSubgraphResponse{}), nil
}

// startRecordingIngest stands up an h2c httptest.Server fronting a recordingIngest
// handler and returns the wired IngestServiceClient + the handler pointer.
func startRecordingIngest(t *testing.T) (knowledgev1connect.IngestServiceClient, *recordingIngest) {
	t.Helper()
	eng := &recordingIngest{}
	mux := http.NewServeMux()
	path, hdlr := knowledgev1connect.NewIngestServiceHandler(eng)
	mux.Handle(path, hdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)

	cl := &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(_ context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return net.Dial(network, addr)
			},
		},
	}
	// The server frames CollectChunk requests too; raise the read-max so the test
	// harness accepts the ≤64 MiB edge chunks (the production server is configured
	// for large bodies; this is purely the test transport ceiling).
	client := knowledgev1connect.NewIngestServiceClient(
		cl, srv.URL, connect.WithGRPC(), connect.WithReadMaxBytes(kgwire.MaxCloudRequestBytes*2))
	return client, eng
}

// partitionSanityBound is the test-side regression tripwire for the partitioned
// request bodies. 64 MiB (MaxCloudRequestBytes) is a PARTITION TARGET, not a hard
// ceiling: the packer accumulates whole objects until the next would cross the
// budget, so a chunk can land SLIGHTLY over 64 MiB (a lone object at/over the
// budget gets its own chunk; the request envelope adds a little overhead). What
// must NEVER happen is a chunk approaching Cloudflare's ~100 MiB edge cap — so
// the tripwire is a sanity bound comfortably under that (~90 MiB), not a strict
// ≤64 MiB. With ~1 MiB test objects no chunk gets anywhere near this; the bound
// guards the partition-range invariant without forcing exact-≤64 MiB chunks.
const partitionSanityBound = 90 << 20 // 94371840 bytes — well under Cloudflare's ~100 MiB cap

// TestWriteResult_PartitionsOversizedEdgesAndReassembles feeds WriteResult a
// result whose edge tail far exceeds the 64 MiB partition target in aggregate
// and asserts:
//
//	(a) more than one CollectChunk is emitted (the input was partitioned);
//	(b) proto.Size of EVERY captured CollectChunkRequest stays within the
//	    partition sanity bound (~90 MiB, well under Cloudflare's ~100 MiB cap) —
//	    the must-never-fire tripwire, expressed as a partition-range guard rather
//	    than a strict ≤64 MiB cap;
//	(c) the union of all captured Nodes equals the input nodes and the union of all
//	    captured Edges equals the input edges — none dropped, regardless of input size.
func TestWriteResult_PartitionsOversizedEdgesAndReassembles(t *testing.T) {
	client, rec := startRecordingIngest(t)
	sink := NewUploadSink(client)

	// 70 edges, each ~1 MiB of Evidence → ~70 MiB total, well over the 64 MiB cap.
	const numEdges = 70
	const evidenceBytes = 1 << 20
	bigEvidence := strings.Repeat("x", evidenceBytes)
	edges := make([]kgwire.BatchEdge, numEdges)
	for i := range edges {
		// Unique FromID/ToID per edge so the reassembly check is exact.
		edges[i] = kgwire.BatchEdge{
			FromIdx:  -1,
			ToIdx:    -1,
			FromID:   uniqueID("from", i),
			ToID:     uniqueID("to", i),
			Type:     kgtypes.EdgeType("relates-to"),
			Evidence: bigEvidence,
		}
	}

	nodes := []*knowledgev1.Node{
		{Id: "n1", Type: "func", Summary: "n1"},
		{Id: "n2", Type: "func", Summary: "n2"},
		{Id: "n3", Type: "func", Summary: "n3"},
	}

	result := &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCode,
		GraphName: "chunksize-repo",
		Nodes:     nodes,
		Edges:     edges,
	}

	require.NoError(t, sink.WriteResult(context.Background(), "", result))

	rec.mu.Lock()
	captured := rec.chunks
	rec.mu.Unlock()

	// (a) the oversized edge tail was partitioned into more than one CollectChunk.
	require.Greater(t, len(captured), 1, "an oversized edge tail must be partitioned into multiple CollectChunk requests")

	// (b) every captured request body stays within the partition sanity bound
	// (~90 MiB) — comfortably under Cloudflare's ~100 MiB edge cap. 64 MiB is the
	// partition TARGET, so a chunk landing slightly over 64 MiB is acceptable; the
	// tripwire is this sanity bound, not a strict ≤64 MiB cap.
	for i, req := range captured {
		size := proto.Size(req)
		assert.Lessf(t, size, partitionSanityBound,
			"CollectChunk %d/%d proto.Size %d must stay within the partition sanity bound %d (the must-never-fire tripwire)",
			i+1, len(captured), size, partitionSanityBound)
	}

	// (c) full reassembly: union of captured nodes == input nodes, union of
	// captured edges == input edges, none dropped.
	gotNodes := map[string]bool{}
	gotEdges := map[string]bool{}
	for _, req := range captured {
		for _, n := range req.GetNodes() {
			gotNodes[n.GetId()] = true
		}
		for _, e := range req.GetEdges() {
			gotEdges[edgeKey(e)] = true
		}
	}
	require.Len(t, gotNodes, len(nodes), "every input node must land across the chunks")
	for _, n := range nodes {
		assert.True(t, gotNodes[n.GetId()], "node %s must be delivered", n.GetId())
	}
	require.Len(t, gotEdges, len(edges), "every input edge must land across the chunks")
	for i := range edges {
		k := uniqueID("from", i) + "\x00relates-to\x00" + uniqueID("to", i)
		assert.Truef(t, gotEdges[k], "edge %d (%s) must be delivered", i, k)
	}
}

func uniqueID(prefix string, i int) string {
	return prefix + "-node-" + strconv.Itoa(i)
}

func edgeKey(e *knowledgev1.BatchEdge) string {
	return e.GetFromId() + "\x00" + e.GetType() + "\x00" + e.GetToId()
}
