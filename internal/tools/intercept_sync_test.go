// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// fakeExporter implements GraphCaller + Exporter (the production graphClientCaller
// shape the push intercept type-asserts UP to) and returns canned graph bytes.
type fakeExporter struct {
	bytesOut    []byte
	exportErr   error
	exportCalls int
	lastTarget  *knowledgev1.GraphSelector
}

func (f *fakeExporter) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (f *fakeExporter) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

func (f *fakeExporter) ExportGraph(_ context.Context, req *knowledgev1.ExportGraphRequest) (*knowledgev1.ExportGraphResponse, error) {
	f.exportCalls++
	f.lastTarget = req.GetTarget()
	if f.exportErr != nil {
		return nil, f.exportErr
	}
	return &knowledgev1.ExportGraphResponse{GraphBytes: f.bytesOut}, nil
}

// withTransport swaps syncTransportBuilder to return the given builder result for
// the duration of the test, restoring the production builder on cleanup.
func withTransport(t *testing.T, build func() (*auth.Transport, error)) {
	t.Helper()
	prev := syncTransportBuilder
	syncTransportBuilder = build
	t.Cleanup(func() { syncTransportBuilder = prev })
}

func syncParams(t *testing.T, args map[string]any) kgtools.CallToolParams {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: "sync", Arguments: raw}
}

// TestInterceptSync_Push_FetchesThenPosts asserts the push path: the intercept
// fetches bytes via ExportGraph, then POSTs them to the cloud transport, and
// renders the "pushed" success line.
func TestInterceptSync_Push_FetchesThenPosts(t *testing.T) {
	want := []byte{0x01, 0x02, 0x03, 0xFF}
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	withTransport(t, func() (*auth.Transport, error) {
		src := auth.StaticTokenSource{AccessToken: "tok", Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}}}
		return auth.NewSyncTransport(srv.URL, src), nil
	})

	exp := &fakeExporter{bytesOut: want}
	handled, out := InterceptSync(interceptTestDeps{gc: exp}, syncParams(t, map[string]any{"operation": "push"}))

	require.True(t, handled)
	assert.False(t, out.IsError, "push success must not be an error result: %q", textOf(out))
	assert.Equal(t, 1, exp.exportCalls, "ExportGraph fetched once")
	assert.Equal(t, "/v1/sync/push/knowledge/default", gotPath, "POSTed to the push route")
	assert.Equal(t, want, gotBody, "the exported bytes were uploaded verbatim")
	// The success line now carries a serialize/upload timing breakdown after the
	// byte count: "pushed knowledge/default (4 bytes; serialize=<dur> upload=<dur>)".
	// Assert the stable prefix (graph/name + byte count) so the durations stay free.
	assert.Contains(t, textOf(out), "pushed knowledge/default (4 bytes;")
}

// fakeOverwriter implements GraphCaller + Overwriter (the production
// graphClientCaller shape the pull intercept type-asserts the LOCAL caller UP to)
// and records the applied OverwriteGraphRequest.
type fakeOverwriter struct {
	overwriteErr   error
	overwriteCalls int
	lastReq        *knowledgev1.OverwriteGraphRequest
	nodes          int64
	edges          int64
}

func (f *fakeOverwriter) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (f *fakeOverwriter) Execute(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

func (f *fakeOverwriter) OverwriteGraph(_ context.Context, req *knowledgev1.OverwriteGraphRequest) (*knowledgev1.OverwriteGraphResponse, error) {
	f.overwriteCalls++
	f.lastReq = req
	if f.overwriteErr != nil {
		return nil, f.overwriteErr
	}
	return &knowledgev1.OverwriteGraphResponse{Nodes: f.nodes, Edges: f.edges}, nil
}

// pullDeps wires DISTINCT cloud (Exporter, via the routed GraphCaller) + local
// (Overwriter, via LocalGraphCaller) callers so the pull arm's direction is
// asserted: cloud fetch via GraphCaller(), local apply via LocalGraphCaller().
// Mirrors tailRoutingDeps.
type pullDeps struct {
	routed GraphCaller
	local  GraphCaller
	crud   GraphTypeCRUDAPI
}

func (d pullDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d pullDeps) Sink() collector.Sink                         { return nil }
func (d pullDeps) RootDir() string                              { return "" }
func (d pullDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d pullDeps) WorkerReady() bool                            { return true }
func (d pullDeps) PropReady() bool                              { return true }
func (d pullDeps) PipelineReady() bool                          { return true }
func (d pullDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d pullDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d pullDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d pullDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return d.crud }
func (d pullDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d pullDeps) BackendResolver() BackendResolver             { return nil }
func (d pullDeps) GraphCaller() GraphCaller                     { return d.routed }
func (d pullDeps) LocalGraphCaller() GraphCaller                { return d.local }
func (d pullDeps) RepoResolver() *RepoResolver                  { return nil }
func (d pullDeps) SegmentManager() SegmentSearcher              { return nil }
func (d pullDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d pullDeps) SegmentShipper() SegmentShipper               { return nil }
func (d pullDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d pullDeps) PipelineScanner() PipelineScanner             { return nil }
func (d pullDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d pullDeps) SimilarityForcer() SimilarityForcer           { return nil }

// TestInterceptSync_Pull_FetchesCloudAppliesLocal asserts the pull arm: the
// cloud Exporter (routed GraphCaller) returns canned bytes, and the local
// Overwriter (LocalGraphCaller) receives those EXACT bytes for the (gt, name),
// rendering the "pulled" success line. The transport is never built (pull does
// not POST to cloud).
func TestInterceptSync_Pull_FetchesCloudAppliesLocal(t *testing.T) {
	withTransport(t, func() (*auth.Transport, error) {
		t.Fatal("transport builder must not be called for pull")
		return nil, nil
	})
	want := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	cloud := &fakeExporter{bytesOut: want}
	local := &fakeOverwriter{nodes: 7, edges: 3}

	handled, out := InterceptSync(pullDeps{routed: cloud, local: local},
		syncParams(t, map[string]any{"operation": "pull"}))

	require.True(t, handled)
	assert.False(t, out.IsError, "pull success must not be an error result: %q", textOf(out))
	assert.Equal(t, 1, cloud.exportCalls, "cloud ExportGraph fetched once")
	require.Equal(t, 1, local.overwriteCalls, "local OverwriteGraph applied once")
	require.NotNil(t, local.lastReq)
	assert.Equal(t, want, local.lastReq.GetGraphBytes(), "the cloud bytes were applied locally verbatim")
	assert.Equal(t, "knowledge", local.lastReq.GetGraphType())
	assert.Equal(t, "default", local.lastReq.GetName())
	assert.Contains(t, textOf(out), "pulled knowledge/default")
}

// TestInterceptSync_Pull_NotLoggedIn asserts the not-logged-in pull path: the
// cloud ExportGraph errors with auth.ErrNotFound → the actionable "knowledge
// login" guidance renders and the local OverwriteGraph is NEVER called (nothing
// to apply).
func TestInterceptSync_Pull_NotLoggedIn(t *testing.T) {
	cloud := &fakeExporter{exportErr: auth.ErrNotFound}
	local := &fakeOverwriter{}

	handled, out := InterceptSync(pullDeps{routed: cloud, local: local},
		syncParams(t, map[string]any{"operation": "pull"}))

	require.True(t, handled)
	assert.True(t, out.IsError, "not-logged-in pull must be an error result")
	assert.Contains(t, textOf(out), "knowledge login")
	assert.Equal(t, 0, local.overwriteCalls, "no apply when the cloud fetch fails")
}

// TestInterceptSync_Pull_NoLocalServer_FailsLoud asserts pull fails loud when no
// local server is wired (LocalGraphCaller()==nil): the apply target is the local
// .bin, so a cloud-only install cannot pull. Mirrors the push no-local-server
// guard.
func TestInterceptSync_Pull_NoLocalServer_FailsLoud(t *testing.T) {
	cloud := &fakeExporter{bytesOut: []byte{1}}
	// routed cloud caller present, but local caller nil → overwriterSeam fails.
	handled, out := InterceptSync(pullDeps{routed: cloud, local: nil},
		syncParams(t, map[string]any{"operation": "pull"}))

	require.True(t, handled)
	assert.True(t, out.IsError, "pull with no local server must be an error result")
	assert.Contains(t, textOf(out), "no local server is wired")
	assert.Contains(t, textOf(out), "knowledge install")
}

// TestInterceptSync_Promote_Rejected asserts promote (removed) returns an error
// and never touches the cloud ExportGraph or the transport.
func TestInterceptSync_Promote_Rejected(t *testing.T) {
	withTransport(t, func() (*auth.Transport, error) {
		t.Fatal("transport builder must not be called for promote")
		return nil, nil
	})
	exp := &fakeExporter{bytesOut: []byte{1}}
	handled, out := InterceptSync(interceptTestDeps{gc: exp}, syncParams(t, map[string]any{"operation": "promote"}))
	require.True(t, handled)
	assert.True(t, out.IsError, "promote must be an error result")
	assert.Equal(t, 0, exp.exportCalls, "promote must not fetch bytes")
	assert.Contains(t, textOf(out), "promote was removed")
}

// TestOverwriteGraph_LocalOnly_NotOnRouter pins the item-3 invariant: the routed
// *graphclient.Router does NOT satisfy Overwriter (it has no OverwriteGraph
// forwarder), so GraphCaller().(Overwriter) fails and the apply can only ever
// reach the LOCAL caller. A fake that DOES implement OverwriteGraph satisfies it,
// proving the assertion mechanism itself works.
func TestOverwriteGraph_LocalOnly_NotOnRouter(t *testing.T) {
	var router *graphclient.Router
	_, ok := any(router).(Overwriter)
	assert.False(t, ok, "Router must NOT satisfy Overwriter — no OverwriteGraph forwarder (local-only invariant)")

	var local GraphCaller = &fakeOverwriter{}
	_, ok = local.(Overwriter)
	assert.True(t, ok, "a local OverwriteGraph-capable caller DOES satisfy Overwriter")
}

// errTokenSource is a TokenSource whose Token always fails with auth.ErrNotFound
// — the "not logged in" case (no persisted refresh token). PushGraph surfaces
// this through sendWithAuthBytes, and wrapPushErr maps it to the actionable
// "run knowledge login" guidance.
type errTokenSource struct{}

func (errTokenSource) Token(context.Context) (string, auth.PermissionSet, error) {
	return "", nil, auth.ErrNotFound
}

// TestInterceptSync_NotLoggedIn_ActionableMessage asserts the not-logged-in
// path (token acquisition fails with auth.ErrNotFound during PushGraph) renders
// the actionable "run knowledge login" guidance via wrapPushErr.
func TestInterceptSync_NotLoggedIn_ActionableMessage(t *testing.T) {
	withTransport(t, func() (*auth.Transport, error) {
		return auth.NewSyncTransport("http://unused.invalid", errTokenSource{}), nil
	})
	exp := &fakeExporter{bytesOut: []byte{1}}
	handled, out := InterceptSync(interceptTestDeps{gc: exp}, syncParams(t, map[string]any{"operation": "push"}))
	require.True(t, handled)
	assert.True(t, out.IsError)
	assert.Contains(t, textOf(out), "knowledge login")
}

// TestInterceptSync_Push_NoLocalServer_FailsLoud asserts the cloud-first
// invariant: a sync push with NO local GraphClient wired
// (deps.LocalGraphCaller() returns nil) surfaces the actionable
// "no local server is wired" error from exporterSeam — never a hang or a
// silent cloud-only push. Sync push is one of the only two genuinely
// local-only ops; its loud nil-guard must survive the EnsureServer/boot-spawn
// conditioning.
func TestInterceptSync_Push_NoLocalServer_FailsLoud(t *testing.T) {
	withTransport(t, func() (*auth.Transport, error) {
		t.Fatal("transport builder must not be reached when no local server is wired")
		return nil, nil
	})
	// gc nil → both GraphCaller() and LocalGraphCaller() return nil.
	handled, out := InterceptSync(interceptTestDeps{gc: nil}, syncParams(t, map[string]any{"operation": "push"}))
	require.True(t, handled)
	assert.True(t, out.IsError, "push with no local server must be an error result")
	assert.Contains(t, textOf(out), "no local server is wired",
		"surfaces the actionable cloud-first error")
	assert.Contains(t, textOf(out), "knowledge install")
}

// textOf extracts the first text content block of a ToolResult for assertions.
func textOf(r kgtools.ToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}
