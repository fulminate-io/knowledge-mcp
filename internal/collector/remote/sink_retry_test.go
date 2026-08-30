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
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
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

// CollectManifest answers as a genuinely EMPTY graph — see recordingIngest's
// twin for why the empty shape is the right fake default.
func (s *scriptedIngest) CollectManifest(
	context.Context,
	*connect.Request[knowledgev1.CollectManifestRequest],
) (*connect.Response[knowledgev1.CollectManifestResponse], error) {
	return connect.NewResponse(&knowledgev1.CollectManifestResponse{
		ManifestId: "test-manifest", HashSchemeVersion: contribhash.ContributionHashSchemeVersion,
	}), nil
}

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

// startScriptedIngestProto is startScriptedIngest with the PROTOCOL chosen by the
// caller, so a test can exercise the wire format PRODUCTION uses.
//
// WHY IT EXISTS: both production constructors (graphclient/client.go and
// cloud_auth.go) build the ingest client with NO protocol option — the DEFAULT
// connect protocol — while the helper above pins gRPC. Error METADATA travels
// differently across the two (gRPC trailers vs connect's error body), so a
// Retry-After proven only over gRPC proves nothing about the path prod takes.
// Pass no options for the default protocol.
func startScriptedIngestProto(
	t *testing.T, eng *scriptedIngest, opts ...connect.ClientOption,
) knowledgev1connect.IngestServiceClient {
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
	return knowledgev1connect.NewIngestServiceClient(cl, srv.URL, opts...)
}

// oneChunkResult is a CollectResult small enough to pack into exactly one
// CollectChunk, so a call count is unambiguously an ATTEMPT count.
func oneChunkResult(graph string) *collectorwire.CollectResult {
	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCode,
		GraphName: graph,
		Nodes:     []*knowledgev1.Node{{Id: "n1", Type: "func", Summary: "n1"}},
		// A code collect always carries one — codesync stamps it from the discovery
		// pass — and an EMPTY value now aborts the collect as a producer regression.
		DiscoveryFingerprint:   "fingerprint-retry-fixture",
		CollectorOutputVersion: testCollectorOutputVersion,
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
	isolateDiscoveryStore(t)
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

// TestFinalizeRetry_ShedThenSucceeds is the END-TO-END proof of the shedding
// pair: the server sheds a Finalize with ResourceExhausted + Retry-After, and the
// collect completes anyway because the client backs off for the stated delay and
// re-sends.
//
// IT IS THE TREE'S FIRST WIRE ROUND-TRIP PROOF THAT Retry-After SURVIVES THE
// PRODUCTION PROTOCOL. Both production ingest clients are built with NO protocol
// option — the default connect protocol — while every other test here pins gRPC.
// Error metadata travels differently across the two, so the `connect` row is the
// one that matters and the `grpc` row preserves the existing file's coverage.
//
// THE ELAPSED-TIME BOUND IS NOT OPTIONAL. Counting attempts alone cannot tell a
// backoff from a hot loop, and honoring the server's stated number is the whole
// contract. One second is the smallest usable value: the shared parser rejects
// anything <= 0 and reads only integer seconds or an HTTP date.
//
// WHAT IT DOES AND DOES NOT PROVE: it proves the abort this ticket caught — a shed
// Finalize killing a collect whose payload was already fully uploaded — no longer
// happens. It does NOT prove the server sheds at the floor; that is the admission
// test's job, and the live pgdog check's.
func TestFinalizeRetry_ShedThenSucceeds(t *testing.T) {
	isolateDiscoveryStore(t)
	for _, tc := range []struct {
		name string
		opts []connect.ClientOption
	}{
		{name: "connect"}, // the protocol production actually uses
		{name: "grpc", opts: []connect.ClientOption{connect.WithGRPC()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := &scriptedIngest{
				onFinalize: func(n int32) error {
					if n == 1 {
						// Byte-for-byte the shape the server's shed paths produce.
						e := connect.NewError(connect.CodeResourceExhausted,
							errors.New("server at capacity: retry shortly"))
						e.Meta().Set("Retry-After", "1")
						return e
					}
					return nil
				},
				finalizeIDFor: func(n int32) string {
					if n == 1 {
						return "finalize-shed-1"
					}
					return "finalize-shed-2"
				},
			}
			client := startScriptedIngestProto(t, eng, tc.opts...)
			sink := NewUploadSink(client)

			start := time.Now()
			require.NoError(t, sink.WriteResult(context.Background(), "", oneChunkResult("finalize-shed-repo")),
				"a shed Finalize must be retried to success, not aborted after a full upload")
			elapsed := time.Since(start)

			assert.Equal(t, int32(2), eng.finalize.Load(), "exactly one Finalize re-send")
			assert.GreaterOrEqual(t, elapsed, time.Second,
				"the client returned faster than the server's stated Retry-After: it retried in a hot loop "+
					"instead of honoring the delay")
		})
	}
}

// TestFinalizeRetry_ShedWithoutRetryAfterStillSurfaces is the twin guard on the
// predicate's CONJUNCTION: ResourceExhausted alone is not a shed.
//
// It is a CHARACTERIZATION GUARD, green before and after — but for opposite
// reasons. Before the shed class nothing retried the code at all; now it must stay
// green because the HEADER is absent, which is what keeps the new class from
// swallowing a permanent refusal that happens to share the code.
func TestFinalizeRetry_ShedWithoutRetryAfterStillSurfaces(t *testing.T) {
	eng := &scriptedIngest{
		onFinalize: func(int32) error {
			return connect.NewError(connect.CodeResourceExhausted, errors.New("permanent refusal"))
		},
	}
	client := startScriptedIngestProto(t, eng)
	sink := NewUploadSink(client)

	require.Error(t, sink.WriteResult(context.Background(), "", oneChunkResult("finalize-noheader-repo")),
		"a ResourceExhausted with NO Retry-After is not a shed and must surface")
	assert.Equal(t, int32(1), eng.finalize.Load(),
		"exactly one attempt: the header is the discriminator, so a headerless refusal must not be retried")
}
