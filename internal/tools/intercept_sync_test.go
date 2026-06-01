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

// TestInterceptSync_PullPromote_Rejected asserts pull/promote return the
// push/list-only error and never touch ExportGraph or the transport.
func TestInterceptSync_PullPromote_Rejected(t *testing.T) {
	withTransport(t, func() (*auth.Transport, error) {
		t.Fatal("transport builder must not be called for pull/promote")
		return nil, nil
	})
	for _, op := range []string{"pull", "promote"} {
		exp := &fakeExporter{bytesOut: []byte{1}}
		handled, out := InterceptSync(interceptTestDeps{gc: exp}, syncParams(t, map[string]any{"operation": op}))
		require.True(t, handled)
		assert.True(t, out.IsError, "%s must be an error result", op)
		assert.Equal(t, 0, exp.exportCalls, "%s must not fetch bytes", op)
		assert.Contains(t, textOf(out), "push/list only")
	}
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

// textOf extracts the first text content block of a ToolResult for assertions.
func textOf(r kgtools.ToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}
