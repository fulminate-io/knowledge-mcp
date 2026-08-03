// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// countingIngest is a minimal in-process IngestServiceHandler that counts the
// CollectChunk + Finalize + FetchCloudSubgraph hits it receives. Two of these
// stand in for the local + cloud backends so the per-call picker test can
// assert which backend serviced a given WriteResult.
type countingIngest struct {
	collectChunk      atomic.Int32
	finalize          atomic.Int32
	fetchCloudSubgrph atomic.Int32
}

var _ knowledgev1connect.IngestServiceHandler = (*countingIngest)(nil)

func (e *countingIngest) CollectChunk(
	context.Context,
	*connect.Request[knowledgev1.CollectChunkRequest],
) (*connect.Response[knowledgev1.CollectChunkResponse], error) {
	e.collectChunk.Add(1)
	return connect.NewResponse(&knowledgev1.CollectChunkResponse{}), nil
}

func (e *countingIngest) Finalize(
	context.Context,
	*connect.Request[knowledgev1.FinalizeRequest],
) (*connect.Response[knowledgev1.FinalizeResponse], error) {
	e.finalize.Add(1)
	return connect.NewResponse(&knowledgev1.FinalizeResponse{}), nil
}

// FinalizeStatus completes the handler interface. This fake does all its work in
// Finalize and returns no finalize_id, so the sink never polls it; UNKNOWN is the
// honest answer for a server that tracks nothing.
func (e *countingIngest) FinalizeStatus(
	context.Context,
	*connect.Request[knowledgev1.FinalizeStatusRequest],
) (*connect.Response[knowledgev1.FinalizeStatusResponse], error) {
	return connect.NewResponse(&knowledgev1.FinalizeStatusResponse{
		State: knowledgev1.FinalizeState_FINALIZE_STATE_UNKNOWN,
	}), nil
}

func (e *countingIngest) FetchCloudSubgraph(
	context.Context,
	*connect.Request[knowledgev1.FetchCloudSubgraphRequest],
) (*connect.Response[knowledgev1.FetchCloudSubgraphResponse], error) {
	e.fetchCloudSubgrph.Add(1)
	return connect.NewResponse(&knowledgev1.FetchCloudSubgraphResponse{}), nil
}

// startCountingIngest stands up an h2c httptest.Server fronting a countingIngest
// handler and returns the wired IngestServiceClient + the handler pointer.
func startCountingIngest(t *testing.T) (knowledgev1connect.IngestServiceClient, *countingIngest) {
	t.Helper()
	eng := &countingIngest{}
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
	client := knowledgev1connect.NewIngestServiceClient(cl, srv.URL, connect.WithGRPC())
	return client, eng
}

// TestUploadSink_PickerRepicksPerCall: an UploadSink built via NewUploadSinkFunc
// with a picker flipping between two in-process ingest servers (local + cloud)
// routes the FIRST WriteResult to the picker's current choice; after the picker
// flips, the NEXT WriteResult lands on the OTHER server — proving the sink
// re-resolves the IngestService client per call (mid-session login-flip at the
// sink layer), never caching the resolved client.
func TestUploadSink_PickerRepicksPerCall(t *testing.T) {
	localClient, localEng := startCountingIngest(t)
	cloudClient, cloudEng := startCountingIngest(t)

	// loggedIn flips the picker's choice mid-test, mirroring `knowledge login`.
	var loggedIn atomic.Bool
	sink := NewUploadSinkFunc(func(context.Context) (knowledgev1connect.IngestServiceClient, error) {
		if loggedIn.Load() {
			return cloudClient, nil
		}
		return localClient, nil
	})

	result := &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCode,
		GraphName: "picker-repick-repo",
		Nodes:     []*knowledgev1.Node{{Id: "n1", Type: "func", Summary: "n1"}},
	}

	ctx := context.Background()

	// Not logged in → first WriteResult lands on local (CollectChunk + Finalize).
	require.NoError(t, sink.WriteResult(ctx, "", result))
	assert.Equal(t, int32(1), localEng.finalize.Load(), "local should have serviced the first WriteResult")
	assert.Equal(t, int32(0), cloudEng.finalize.Load(), "cloud should not yet have been called")

	// Flip the picker (simulate mid-session login).
	loggedIn.Store(true)

	// Logged in → next WriteResult lands on cloud; local count must not advance.
	require.NoError(t, sink.WriteResult(ctx, "", result))
	assert.Equal(t, int32(1), localEng.finalize.Load(), "local count must NOT advance after the picker flip")
	assert.Equal(t, int32(1), cloudEng.finalize.Load(), "cloud should have serviced the second WriteResult after the flip")
}

// TestUploadSink_FetchCloudSubgraphRoutesByPicker: the logs collect path's
// FetchCloudSubgraph re-picks the IngestService client per call like WriteResult
// — local when logged out, cloud after a login flip (T3-1).
func TestUploadSink_FetchCloudSubgraphRoutesByPicker(t *testing.T) {
	localClient, localEng := startCountingIngest(t)
	cloudClient, cloudEng := startCountingIngest(t)

	var loggedIn atomic.Bool
	sink := NewUploadSinkFunc(func(context.Context) (knowledgev1connect.IngestServiceClient, error) {
		if loggedIn.Load() {
			return cloudClient, nil
		}
		return localClient, nil
	})

	ctx := context.Background()

	// Logged out → FetchCloudSubgraph hits the local server.
	_, err := sink.FetchCloudSubgraph(ctx, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(1), localEng.fetchCloudSubgrph.Load(), "logged-out FetchCloudSubgraph must hit local")
	assert.Equal(t, int32(0), cloudEng.fetchCloudSubgrph.Load(), "cloud must not be called when logged out")

	// Flip to logged in → next FetchCloudSubgraph hits the cloud server.
	loggedIn.Store(true)
	_, err = sink.FetchCloudSubgraph(ctx, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(1), localEng.fetchCloudSubgrph.Load(), "local count must NOT advance after the flip")
	assert.Equal(t, int32(1), cloudEng.fetchCloudSubgrph.Load(), "logged-in FetchCloudSubgraph must hit cloud")
}
