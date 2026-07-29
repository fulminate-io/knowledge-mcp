// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
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
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
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

// TestInterceptSync_Push_PresignSealPutConfirm asserts the push path end-to-end
// over the presigned-GCS flow: the intercept serializes LOCAL bytes via
// ExportGraph, requests a presign, seals the bytes into a push envelope, PUTs the
// ciphertext to GCS, and calls confirm. The fake backend unwraps + GCM-opens the
// uploaded ciphertext and proves it recovers the ORIGINAL serialized bytes (the
// GCS object is ciphertext, not plaintext). Renders the "pushed" success line.
func TestInterceptSync_Push_PresignSealPutConfirm(t *testing.T) {
	want := []byte("KGV4 the local serialized graph bytes")
	backend := newFakeSyncBackend(t)

	withTransport(t, func() (*auth.Transport, error) {
		src := auth.StaticTokenSource{AccessToken: "tok", Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}}}
		return auth.NewSyncTransport(backend.srv.URL, src), nil
	})

	exp := &fakeExporter{bytesOut: want}
	handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: exp}, syncParams(t, map[string]any{"operation": "push"}))

	require.True(t, handled)
	assert.False(t, out.IsError, "push success must not be an error result: %q", textOf(out))
	assert.Equal(t, 1, exp.exportCalls, "ExportGraph fetched once (local serialize)")
	assert.Equal(t, 1, backend.presignCalls, "presign called once")
	assert.Equal(t, 1, backend.confirmCalls, "confirm called once")

	// The uploaded GCS object must be CIPHERTEXT (not the plaintext bytes), and
	// the backend's confirm (unwrap + GCM-open) must recover the original bytes.
	backend.mu.Lock()
	uploaded := backend.objects["push-obj"]
	recovered := backend.confirmedPlaintext
	backend.mu.Unlock()
	require.NotEmpty(t, uploaded, "an object was PUT to GCS")
	assert.NotEqual(t, want, uploaded, "the GCS object is ciphertext, not plaintext")
	assert.Equal(t, want, recovered, "confirm decrypted the envelope back to the original bytes")

	assert.Contains(t, textOf(out), "pushed knowledge/default")
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
func (d pullDeps) UsageAnalyzer() UsageAnalyzerAPI              { return nil }
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
func (d pullDeps) SegmentManager() SegmentSearcher              { return nil }
func (d pullDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d pullDeps) SegmentShipper() SegmentShipper               { return nil }
func (d pullDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d pullDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d pullDeps) PipelineScanner() PipelineScanner             { return nil }

func (d pullDeps) ClearHealLatch(kgtypes.GraphType, string) {}
func (d pullDeps) ReflectionForcer() ReflectionForcer       { return nil }
func (d pullDeps) SimilarityForcer() SimilarityForcer       { return nil }

func (d pullDeps) BlindSpotProvider() BlindSpotProvider { return nil }
func (d pullDeps) ClusterProvider() ClusterProvider     { return nil }
func (d pullDeps) TensionsProvider() TensionsProvider   { return nil }

// TestInterceptSync_Pull_FetchesCloudAppliesLocal asserts the pull arm end-to-end
// over the presigned-GCS flow: /v1/sync/pull returns {download_url, dek} for an
// agent-encrypted object, the client GETs the ciphertext from GCS, decrypts it
// with the returned DEK, and the local Overwriter (LocalGraphCaller) receives the
// recovered bytes for the (gt, name), rendering the "pulled" success line. The
// pull now goes through the control-plane transport (not a cloud ExportGraph).
func TestInterceptSync_Pull_FetchesCloudAppliesLocal(t *testing.T) {
	want := []byte("KGV4 the authoritative cloud graph image")
	backend := newFakeSyncBackend(t)
	backend.pullPlaintext = want

	withTransport(t, func() (*auth.Transport, error) {
		src := auth.StaticTokenSource{AccessToken: "tok", Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}}}
		return auth.NewSyncTransport(backend.srv.URL, src), nil
	})
	local := &fakeOverwriter{nodes: 7, edges: 3}

	handled, out := InterceptSync(opCtx(), pullDeps{local: local}, syncParams(t, map[string]any{"operation": "pull"}))

	require.True(t, handled)
	assert.False(t, out.IsError, "pull success must not be an error result: %q", textOf(out))
	assert.Equal(t, 1, backend.pullCalls, "pull control endpoint called once")
	require.Equal(t, 1, local.overwriteCalls, "local OverwriteGraph applied once")
	require.NotNil(t, local.lastReq)
	assert.Equal(t, want, local.lastReq.GetGraphBytes(), "the decrypted cloud bytes were applied locally verbatim")
	assert.Equal(t, "knowledge", local.lastReq.GetGraphType())
	assert.Equal(t, "default", local.lastReq.GetName())

	// The GCS object itself must be ciphertext (undecryptable without the DEK).
	backend.mu.Lock()
	stored := backend.objects["pull-obj"]
	backend.mu.Unlock()
	assert.NotEqual(t, want, stored, "the GCS pull object is ciphertext, not plaintext")

	assert.Contains(t, textOf(out), "pulled knowledge/default")
}

// TestInterceptSync_Pull_NotLoggedIn asserts the not-logged-in pull path: the
// pull control call's token acquisition fails with auth.ErrNotFound → the
// actionable "knowledge login" guidance renders and the local OverwriteGraph is
// NEVER called (nothing to apply).
func TestInterceptSync_Pull_NotLoggedIn(t *testing.T) {
	local := &fakeOverwriter{}
	withTransport(t, func() (*auth.Transport, error) {
		return auth.NewSyncTransport("http://unused.invalid", errTokenSource{}), nil
	})

	handled, out := InterceptSync(opCtx(), pullDeps{local: local}, syncParams(t, map[string]any{"operation": "pull"}))

	require.True(t, handled)
	assert.True(t, out.IsError, "not-logged-in pull must be an error result")
	assert.Contains(t, textOf(out), "knowledge login")
	assert.Equal(t, 0, local.overwriteCalls, "no apply when the cloud fetch fails")
}

// TestInterceptSync_Pull_NoLocalServer_FailsLoud asserts pull fails loud when no
// local server is wired (LocalGraphCaller()==nil): the apply target is the local
// .bin, so a cloud-only install cannot pull. overwriterSeam fails BEFORE the
// transport is built. Mirrors the push no-local-server guard.
func TestInterceptSync_Pull_NoLocalServer_FailsLoud(t *testing.T) {
	withTransport(t, func() (*auth.Transport, error) {
		t.Fatal("transport builder must not be reached when no local server is wired")
		return nil, nil
	})
	// local caller nil → overwriterSeam fails before any transport build.
	handled, out := InterceptSync(opCtx(), pullDeps{local: nil}, syncParams(t, map[string]any{"operation": "pull"}))

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
	handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: exp}, syncParams(t, map[string]any{"operation": "promote"}))
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
	handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: exp}, syncParams(t, map[string]any{"operation": "push"}))
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
	handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: nil}, syncParams(t, map[string]any{"operation": "push"}))
	require.True(t, handled)
	assert.True(t, out.IsError, "push with no local server must be an error result")
	assert.Contains(t, textOf(out), "no local server is wired",
		"surfaces the actionable cloud-first error")
	assert.Contains(t, textOf(out), "knowledge install")
}

// TestSetSyncTransportBuilder_PushPullResolveThroughIt proves the exported
// production injection setter is the seam the push AND pull intercepts resolve
// their transport through: a builder installed via SetSyncTransportBuilder (the
// call the daemon makes with c.buildCloudSyncTransport) is invoked once per arm,
// and a nil fn is ignored so the default is never cleared.
func TestSetSyncTransportBuilder_PushPullResolveThroughIt(t *testing.T) {
	// SetSyncTransportBuilder mutates the package var directly (no restore of
	// its own), so snapshot + restore around the test.
	prev := syncTransportBuilder
	t.Cleanup(func() { syncTransportBuilder = prev })

	backend := newFakeSyncBackend(t)
	backend.pullPlaintext = []byte("KGV4 cloud image")

	var builderCalls int
	SetSyncTransportBuilder(func() (*auth.Transport, error) {
		builderCalls++
		src := auth.StaticTokenSource{AccessToken: "tok", Permissions: auth.PermissionSet{auth.PermMCPKnowledgeWrite: {}}}
		return auth.NewSyncTransport(backend.srv.URL, src), nil
	})

	// A nil fn must be ignored (the fallback is never cleared).
	SetSyncTransportBuilder(nil)

	// Push arm resolves the installed builder.
	exp := &fakeExporter{bytesOut: []byte("KGV4 local bytes")}
	handledPush, outPush := InterceptSync(opCtx(), interceptTestDeps{gc: exp}, syncParams(t, map[string]any{"operation": "push"}))
	require.True(t, handledPush)
	assert.False(t, outPush.IsError, "push must succeed through the installed builder: %q", textOf(outPush))
	assert.Equal(t, 1, builderCalls, "push resolved its transport through the SetSyncTransportBuilder-installed builder")

	// Pull arm resolves the SAME installed builder.
	local := &fakeOverwriter{nodes: 1, edges: 0}
	handledPull, outPull := InterceptSync(opCtx(), pullDeps{local: local}, syncParams(t, map[string]any{"operation": "pull"}))
	require.True(t, handledPull)
	assert.False(t, outPull.IsError, "pull must succeed through the installed builder: %q", textOf(outPull))
	assert.Equal(t, 2, builderCalls, "pull also resolved its transport through the installed builder")
}

// textOf extracts the first text content block of a ToolResult for assertions.
func textOf(r kgtools.ToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}
