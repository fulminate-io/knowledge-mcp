// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
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

// scriptedIngest is an in-process IngestServiceHandler whose CollectChunk and
// Finalize outcomes are programmable PER CALL, not fixed for the run. That is
// what lets one fake express both "fail once, then succeed" (which proves a
// retry happened) and "fail every time" (which proves the budget is an UPPER
// bound and not merely a lower one) — a fake with one fixed error can express
// neither distinction.
//
// Both hooks receive the 1-based call ordinal. A nil hook means "succeed".
type scriptedIngest struct {
	collectChunk atomic.Int32
	finalize     atomic.Int32

	// onCollectChunk / onFinalize return the error for the nth call (n starts
	// at 1); nil means the call succeeds.
	onCollectChunk func(n int32) error
	onFinalize     func(n int32) error

	// finalizeIDFor names the finalize id handed back on the nth Finalize, so a
	// test can prove the sink carries the id from the attempt that ACTUALLY
	// succeeded rather than one from a lost response.
	finalizeIDFor func(n int32) string

	// polled records every finalize id FinalizeStatus was asked about.
	polledMu sync.Mutex
	polled   []string
}

var _ knowledgev1connect.IngestServiceHandler = (*scriptedIngest)(nil)

func (s *scriptedIngest) CollectChunk(
	context.Context,
	*connect.Request[knowledgev1.CollectChunkRequest],
) (*connect.Response[knowledgev1.CollectChunkResponse], error) {
	n := s.collectChunk.Add(1)
	if s.onCollectChunk != nil {
		if err := s.onCollectChunk(n); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&knowledgev1.CollectChunkResponse{}), nil
}

func (s *scriptedIngest) Finalize(
	context.Context,
	*connect.Request[knowledgev1.FinalizeRequest],
) (*connect.Response[knowledgev1.FinalizeResponse], error) {
	n := s.finalize.Add(1)
	if s.onFinalize != nil {
		if err := s.onFinalize(n); err != nil {
			return nil, err
		}
	}
	var id string
	if s.finalizeIDFor != nil {
		id = s.finalizeIDFor(n)
	}
	return connect.NewResponse(&knowledgev1.FinalizeResponse{FinalizeId: id}), nil
}

// polledFinalizeIDs records every finalize id the sink actually polled, which is
// how the "the sink polls the SUCCEEDING attempt's id" assertion is expressed.
func (s *scriptedIngest) FinalizeStatus(
	_ context.Context,
	req *connect.Request[knowledgev1.FinalizeStatusRequest],
) (*connect.Response[knowledgev1.FinalizeStatusResponse], error) {
	s.polledMu.Lock()
	s.polled = append(s.polled, req.Msg.GetFinalizeId())
	s.polledMu.Unlock()
	return connect.NewResponse(&knowledgev1.FinalizeStatusResponse{
		State: knowledgev1.FinalizeState_FINALIZE_STATE_DONE,
	}), nil
}

func (s *scriptedIngest) FetchCloudSubgraph(
	context.Context,
	*connect.Request[knowledgev1.FetchCloudSubgraphRequest],
) (*connect.Response[knowledgev1.FetchCloudSubgraphResponse], error) {
	return connect.NewResponse(&knowledgev1.FetchCloudSubgraphResponse{}), nil
}

// startScriptedIngest stands an h2c httptest server in front of eng and returns
// the wired client. Same vehicle as startCountingIngest in sink_picker_test.go.
func startScriptedIngest(t *testing.T, eng *scriptedIngest) knowledgev1connect.IngestServiceClient {
	t.Helper()
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
	return knowledgev1connect.NewIngestServiceClient(cl, srv.URL, connect.WithGRPC())
}

// oneChunkResult is a CollectResult small enough to pack into exactly one
// CollectChunk, so a call count is unambiguously an ATTEMPT count.
func oneChunkResult(graph string) *collectorwire.CollectResult {
	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCode,
		GraphName: graph,
		Nodes:     []*knowledgev1.Node{{Id: "n1", Type: "func", Summary: "n1"}},
	}
}

// TestCollectChunkRetry_AmbiguousInternalRetriedOnce is the reproduction: a
// single connect CodeInternal — the shape a fronting proxy's body-read cut
// produces — used to abort the whole collect. It must now cost one re-send and
// nothing else.
func TestCollectChunkRetry_AmbiguousInternalRetriedOnce(t *testing.T) {
	eng := &scriptedIngest{
		onCollectChunk: func(n int32) error {
			if n == 1 {
				return connect.NewError(connect.CodeInternal, errors.New("400 Bad Request"))
			}
			return nil
		},
	}
	client := startScriptedIngest(t, eng)
	sink := NewUploadSink(client)

	require.NoError(t, sink.WriteResult(context.Background(), "", oneChunkResult("ambiguous-internal-repo")),
		"one transient CodeInternal must be absorbed, not fail the collect")
	assert.Equal(t, int32(2), eng.collectChunk.Load(),
		"exactly one re-send: the failed attempt plus the successful one")
}

// TestCollectChunkRetry_BudgetBoundedAndAppErrorsSurface pins BOTH bounds. The
// permanent-CodeInternal case pins the ambiguous budget as an UPPER bound (a
// widened retry that kept re-sending would show more than 2 calls), and the
// permission-denied case — the shape the production log actually contains —
// proves an auth denial is never re-sent at all.
func TestCollectChunkRetry_BudgetBoundedAndAppErrorsSurface(t *testing.T) {
	t.Run("permanent CodeInternal stops after the budget", func(t *testing.T) {
		eng := &scriptedIngest{
			onCollectChunk: func(int32) error {
				return connect.NewError(connect.CodeInternal, errors.New("ingest: relation does not exist"))
			},
		}
		sink := NewUploadSink(startScriptedIngest(t, eng))

		err := sink.WriteResult(context.Background(), "", oneChunkResult("permanent-internal-repo"))
		require.Error(t, err, "a genuine server-side error must still surface")
		assert.Equal(t, int32(2), eng.collectChunk.Load(),
			"AmbiguousUploadRetries=1 caps a real server fault at ONE extra upload")
	})

	t.Run("permission denied is never re-sent", func(t *testing.T) {
		eng := &scriptedIngest{
			onCollectChunk: func(int32) error {
				return connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
			},
		}
		sink := NewUploadSink(startScriptedIngest(t, eng))

		err := sink.WriteResult(context.Background(), "", oneChunkResult("permission-denied-repo"))
		require.Error(t, err, "an auth denial must surface immediately")
		assert.Equal(t, int32(1), eng.collectChunk.Load(),
			"re-sending an auth denial could only produce a slower identical denial")
	})
}

// TestFinalizeRetry_AmbiguousUnknownRetriedOnce covers the production line
// "remote sink: Finalize: unknown: 524" — the same ambiguous shape landing on
// the one upload call that had no retry at all.
func TestFinalizeRetry_AmbiguousUnknownRetriedOnce(t *testing.T) {
	eng := &scriptedIngest{
		onFinalize: func(n int32) error {
			if n == 1 {
				return connect.NewError(connect.CodeUnknown, errors.New("524"))
			}
			return nil
		},
		// Distinct ids per attempt: a retried Finalize mints a second id
		// server-side, and the sink must poll the one it actually received.
		finalizeIDFor: func(n int32) string {
			if n == 1 {
				return "finalize-attempt-1"
			}
			return "finalize-attempt-2"
		},
	}
	client := startScriptedIngest(t, eng)
	sink := NewUploadSink(client)

	require.NoError(t, sink.WriteResult(context.Background(), "", oneChunkResult("finalize-unknown-repo")),
		"one transient CodeUnknown on Finalize must be absorbed")
	assert.Equal(t, int32(2), eng.finalize.Load(), "exactly one Finalize re-send")

	eng.polledMu.Lock()
	defer eng.polledMu.Unlock()
	require.NotEmpty(t, eng.polled, "the sink must poll the finalize tail")
	assert.Equal(t, "finalize-attempt-2", eng.polled[0],
		"the polled id comes from the attempt that actually succeeded, not the lost one")
}
