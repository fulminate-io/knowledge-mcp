// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptsync"
)

// cloudStatusDeps satisfies ClientDeps AND the optional cloudStatusInfo
// interface so handleServerStatus's logged-in cloud branch can be driven
// without a live server. GraphCaller() returns a fake statsRPC (gc);
// CloudStatusInfo() reports the configured login state + host. GraphClient()
// returns a real *graphclient.GraphClient pointed at an unreachable/closed
// URL — non-nil so the logged-out path's gc.Healthy() returns false WITHOUT
// the nil-receiver panic that GraphClient()==nil would cause in
// (*graphclient.GraphClient).Healthy().
type cloudStatusDeps struct {
	gc       GraphCaller
	local    *graphclient.GraphClient
	loggedIn bool
	host     string
}

func (d *cloudStatusDeps) LocalLiveness() LocalLiveness                 { return d.local }
func (d *cloudStatusDeps) Sink() collector.Sink                         { return nil }
func (d *cloudStatusDeps) RootDir() string                              { return "" }
func (d *cloudStatusDeps) UsageAnalyzer() UsageAnalyzerAPI              { return nil }
func (d *cloudStatusDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d *cloudStatusDeps) WorkerReady() bool                            { return true }
func (d *cloudStatusDeps) PropReady() bool                              { return true }
func (d *cloudStatusDeps) PipelineReady() bool                          { return true }
func (d *cloudStatusDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d *cloudStatusDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d *cloudStatusDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d *cloudStatusDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d *cloudStatusDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d *cloudStatusDeps) BackendResolver() BackendResolver             { return nil }
func (d *cloudStatusDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d *cloudStatusDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d *cloudStatusDeps) SegmentManager() SegmentSearcher              { return nil }
func (d *cloudStatusDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d *cloudStatusDeps) SegmentShipper() SegmentShipper               { return nil }
func (d *cloudStatusDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d *cloudStatusDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d *cloudStatusDeps) PipelineScanner() PipelineScanner             { return nil }
func (d *cloudStatusDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d *cloudStatusDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (d *cloudStatusDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d *cloudStatusDeps) ClusterProvider() ClusterProvider     { return nil }
func (d *cloudStatusDeps) TensionsProvider() TensionsProvider   { return nil }

func (d *cloudStatusDeps) CloudStatusInfo() (bool, string) { return d.loggedIn, d.host }

// statsCallRecorder is a statsRPC whose Stats records whether it was ever
// invoked — used by the logged-out branch-selection gate test to prove the
// cloud branch was NOT taken.
type statsCallRecorder struct {
	statsCalled atomic.Bool // Stats may be called concurrently by the coverage fan-out
	stats       *knowledgev1.GraphStats
}

func (r *statsCallRecorder) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

func (r *statsCallRecorder) Stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	r.statsCalled.Store(true)
	return &knowledgev1.StatsResponse{GraphStats: r.stats}, nil
}

// closedGraphClient returns a non-nil *graphclient.GraphClient whose backing
// server is already shut down, so Healthy() returns false without panicking.
// Mirrors the interceptDeps harness shape (intercept_search_query_dispatch_test.go)
// but closes the server immediately.
func closedGraphClient(t *testing.T) *graphclient.GraphClient {
	t.Helper()
	srv := httptest.NewServer(http.NewServeMux())
	srv.Close()
	return graphclient.NewGraphClientForURL(srv.URL)
}

// TestHandleServerStatus_LoggedIn drives the logged-in cloud branch: it routes
// the Stats RPC through GraphCaller() and renders the cloud graph stats with a
// "Backend: cloud (<host>)" preamble, omitting all local-daemon-only fields.
func TestHandleServerStatus_LoggedIn(t *testing.T) {
	stats := &knowledgev1.GraphStats{
		NodeCount: 1000, EdgeCount: 500, BinaryVectorCount: 200,
		NodesByType: map[string]int64{"thought": 700},
	}
	deps := &cloudStatusDeps{
		gc:       &modFake{stats: stats},
		loggedIn: true,
		host:     "https://dev.fulminate.io",
	}

	t.Run("text", func(t *testing.T) {
		res := handleServerStatus(deps, "")
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.Contains(t, body, "Backend: cloud (https://dev.fulminate.io)")
		assert.Contains(t, body, "Nodes: 1000")
		assert.Contains(t, body, "Edges: 500")
		// Local-daemon-only fields must be absent in the cloud body.
		assert.NotContains(t, body, "PID")
		assert.NotContains(t, body, "Path:")
		assert.NotContains(t, body, "Summarization:")
		assert.NotContains(t, body, "Embedding:")
	})

	t.Run("json", func(t *testing.T) {
		res := handleServerStatus(deps, "json")
		require.False(t, res.IsError, textBodyTools(res))
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(res)), &got))
		assert.Equal(t, "cloud", got["backend"])
		assert.Equal(t, "https://dev.fulminate.io", got["host"])
		assert.EqualValues(t, 1000, got["nodes"])
		assert.EqualValues(t, 500, got["edges"])
		// Local-daemon-only keys must be absent in the cloud json body.
		assert.NotContains(t, got, "pid")
		assert.NotContains(t, got, "graph_path")
		assert.NotContains(t, got, "summary_queued")
		assert.NotContains(t, got, "embed_queued")
	})
}

// TestHandleServerStatus_LoggedOut_SelectsLocalBranch is a BRANCH-SELECTION
// gate: with CloudStatusInfo() reporting loggedIn=false, the cloud branch must
// be skipped and the local path selected. We assert the routed Stats RPC was
// NEVER invoked (recorder.statsCalled == false) — proving the cloud branch is
// not taken — without depending on the un-fakeable concrete local client.
//
// Full logged-out local-render coverage is out of scope per the ticket
// ("behavior UNCHANGED") and would require a bootstrap-level e2e with a real
// in-process GraphClient. GraphClient() returns a non-nil client pointed at a
// closed server so the local path's gc.Healthy() returns false rather than
// panicking on a nil receiver.
func TestHandleServerStatus_LoggedOut_SelectsLocalBranch(t *testing.T) {
	rec := &statsCallRecorder{stats: &knowledgev1.GraphStats{NodeCount: 1}}
	deps := &cloudStatusDeps{
		gc:       rec,
		local:    closedGraphClient(t),
		loggedIn: false,
		host:     "https://dev.fulminate.io",
	}

	res := handleServerStatus(deps, "")
	// Local path with a closed server reports NOT RUNNING — it does not panic
	// and it does not route to cloud.
	assert.Contains(t, textBodyTools(res), "NOT RUNNING")
	assert.False(t, rec.statsCalled.Load(), "logged-out status must not route to the cloud Stats RPC")
}

// fakeLiveness is a healthy LocalLiveness returning a fixed status map, so the local
// running-render path can be driven without a real in-process GraphClient.
type fakeLiveness struct{ status map[string]any }

func (f fakeLiveness) Healthy() bool { return true }

// Status returns a FRESH copy each call, mirroring the production GraphClient.Status
// (the overlay helpers mutate the returned map, so a shared map would leak overlay keys
// across calls).
func (f fakeLiveness) Status() (map[string]any, error) {
	out := make(map[string]any, len(f.status))
	maps.Copy(out, f.status)
	return out, nil
}

// healthDeps embeds cloudStatusDeps (for all the ClientDeps + cloudStatusInfo methods),
// overrides LocalLiveness with an injectable fake, and implements transcriptUploadHealther
// so the manage(status) health overlay renders. healthOK gates the wired flag.
type healthDeps struct {
	*cloudStatusDeps
	live     LocalLiveness
	health   transcriptsync.UploadHealth
	healthOK bool
}

func (d *healthDeps) LocalLiveness() LocalLiveness {
	if d.live != nil {
		return d.live
	}
	return d.cloudStatusDeps.LocalLiveness()
}

func (d *healthDeps) TranscriptUploadHealth() (transcriptsync.UploadHealth, bool) {
	return d.health, d.healthOK
}

// localNoHealthDeps is a healthy-local deps that does NOT implement
// transcriptUploadHealther — the degrade fixture proving the overlay is additive.
type localNoHealthDeps struct {
	*cloudStatusDeps
	live LocalLiveness
}

func (d *localNoHealthDeps) LocalLiveness() LocalLiveness { return d.live }

func runningStatusMap() map[string]any {
	return map[string]any{
		"pid": float64(4242), "nodes": float64(10), "edges": float64(5),
		"binary_vectors": float64(3), "bm25_docs": float64(7), "graph_path": "/tmp/g",
	}
}

// TestHandleServerStatus_TranscriptHealth_LocalPath drives the logged-OUT local running
// path: a deps implementing transcriptUploadHealther renders the transcript-upload block
// in both the text body and the format:json map; a deps NOT implementing it renders
// neither (the additive degrade contract).
func TestHandleServerStatus_TranscriptHealth_LocalPath(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	live := fakeLiveness{status: runningStatusMap()}

	t.Run("renders when implemented", func(t *testing.T) {
		deps := &healthDeps{
			cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
			live:            live,
			health: transcriptsync.UploadHealth{
				LastTransportOK: now, LastShip: now, TotalPasses: 4, FilesShippedLifetime: 9,
			},
			healthOK: true,
		}

		body := textBodyTools(handleServerStatus(deps, ""))
		assert.Contains(t, body, "Graph server: RUNNING")
		assert.Contains(t, body, "Transcript upload:")
		assert.Contains(t, body, "Last transport OK:")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(deps, "json"))), &got))
		assert.Contains(t, got, "transcript_last_transport_ok")
		assert.Contains(t, got, "transcript_files_shipped_lifetime")
	})

	t.Run("degrades when not implemented", func(t *testing.T) {
		deps := &localNoHealthDeps{cloudStatusDeps: &cloudStatusDeps{loggedIn: false}, live: live}

		body := textBodyTools(handleServerStatus(deps, ""))
		assert.Contains(t, body, "Graph server: RUNNING")
		assert.NotContains(t, body, "Transcript upload:")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(deps, "json"))), &got))
		assert.NotContains(t, got, "transcript_last_transport_ok")
	})
}

// TestHandleServerStatus_TranscriptHealth_CloudPath is the DEAD-RENDER-PATH regression
// guard: transcript upload requires login, so the logged-in cloud path is the real
// audience. A logged-in deps implementing transcriptUploadHealther must route to
// handleCloudStatus AND render the transcript-upload block in both text and json; a
// logged-in deps NOT implementing it renders neither.
func TestHandleServerStatus_TranscriptHealth_CloudPath(t *testing.T) {
	stats := &knowledgev1.GraphStats{NodeCount: 1000, EdgeCount: 500, BinaryVectorCount: 200}
	now := time.Unix(1_700_000_000, 0)

	t.Run("renders on the cloud path when implemented", func(t *testing.T) {
		deps := &healthDeps{
			cloudStatusDeps: &cloudStatusDeps{gc: &modFake{stats: stats}, loggedIn: true, host: "https://dev.fulminate.io"},
			health:          transcriptsync.UploadHealth{LastTransportOK: now, LastShip: now, TotalPasses: 2},
			healthOK:        true,
		}

		body := textBodyTools(handleServerStatus(deps, ""))
		assert.Contains(t, body, "Backend: cloud (https://dev.fulminate.io)", "routed to the cloud status path")
		assert.Contains(t, body, "Transcript upload:", "the real (logged-in) audience sees the health block")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(deps, "json"))), &got))
		assert.Equal(t, "cloud", got["backend"])
		assert.Contains(t, got, "transcript_last_transport_ok")
	})

	t.Run("degrades on the cloud path when not implemented", func(t *testing.T) {
		deps := &cloudStatusDeps{gc: &modFake{stats: stats}, loggedIn: true, host: "https://dev.fulminate.io"}

		body := textBodyTools(handleServerStatus(deps, ""))
		assert.Contains(t, body, "Backend: cloud")
		assert.NotContains(t, body, "Transcript upload:")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(deps, "json"))), &got))
		assert.NotContains(t, got, "transcript_last_transport_ok")
	})
}

// TestRenderTranscriptHealthText_Degraded proves the two failure axes are separately
// visible: a degraded snapshot (per-file failure with no systemic streak) renders a
// distinct "degraded" line carrying the last error, does NOT render a "systemic" line,
// and never reads healthy with a hidden error. A clean snapshot renders no problem lines.
func TestRenderTranscriptHealthText_Degraded(t *testing.T) {
	degraded := transcriptsync.UploadHealth{
		LastTransportOK:     time.Unix(1_700_000_000, 0),
		LastError:           "raw transcript exceeds cap",
		FilesFailedLastTick: 2,
		ConsecutiveFailures: 0,
	}
	out := renderTranscriptHealthText(degraded)
	assert.Contains(t, out, "degraded: 2 file(s) failing to ship this tick; last error: raw transcript exceeds cap")
	assert.NotContains(t, out, "systemic:", "no systemic streak, so no systemic line")
	assert.NotContains(t, out, "healthy")

	// A systemic streak with no error text still surfaces the systemic line.
	systemic := transcriptsync.UploadHealth{ConsecutiveFailures: 3, LastError: "transport down", LastFailure: time.Unix(1_700_000_000, 0)}
	sysOut := renderTranscriptHealthText(systemic)
	assert.Contains(t, sysOut, "systemic: 3 consecutive failed tick(s)")
	assert.Contains(t, sysOut, "last error: transport down", "the error is shown even with no per-file signal")

	// A clean snapshot renders no problem lines and no hidden-error surprise.
	clean := renderTranscriptHealthText(transcriptsync.UploadHealth{LastTransportOK: time.Unix(1_700_000_000, 0), LastShip: time.Unix(1_700_000_000, 0)})
	assert.NotContains(t, clean, "degraded:")
	assert.NotContains(t, clean, "systemic:")
	assert.NotContains(t, clean, "last error:")
}
