// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// practiceExportHandler is an EngineServiceHandler that resolves a practice
// export the way the server's own resolver does — off the selector's LANGUAGE,
// falling back to the combined graph when it is empty — and serves a distinct
// byte image per graph so the test can tell WHICH graph answered.
//
// IT ALSO REFUSES A SET NAME, because the server's practice selector policy does:
// the practice row carries no instance field, so a name outside the family's root
// aliases is rejected before routing ever happens. A stub that accepted one would
// let a client-side regression onto `name` pass this seam and fail in production.
//
// The RESOLUTION RULE modeled here is proved against the real store, real
// resolver and real handler by the server module's own seam test — this half
// exists to prove the selector the client composes SURVIVES THE WIRE and steers
// the answer, which is the hop neither module's unit tests can see.
type practiceExportHandler struct {
	knowledgev1connect.UnimplementedEngineServiceHandler

	// images is keyed by the practice graph name: "default" is the combined
	// graph, any other key is one of the pre-singleton graphs.
	images map[string][]byte

	gotTarget *knowledgev1.GraphSelector
}

func (h *practiceExportHandler) ExportGraph(
	_ context.Context,
	req *connect.Request[knowledgev1.ExportGraphRequest],
) (*connect.Response[knowledgev1.ExportGraphResponse], error) {
	t := req.Msg.GetTarget()
	h.gotTarget = t

	if t.GetName() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errNamedPracticeSelector)
	}
	name := t.GetLanguage()
	if name == "" {
		name = "default"
	}
	image, ok := h.images[name]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoSuchPracticeGraph)
	}
	return connect.NewResponse(&knowledgev1.ExportGraphResponse{GraphBytes: image}), nil
}

var (
	errNamedPracticeSelector = &seamError{"graph=practice does not consume a name"}
	errNoSuchPracticeGraph   = &seamError{"practice graph not found"}
)

type seamError struct{ msg string }

func (e *seamError) Error() string { return e.msg }

// TestSyncPush_LegacyPracticeSelectorSurvivesTheWire is the client-to-server hop
// of the sync seam, with the REAL graph client (graphclient.GraphClient over a
// real HTTP round trip) and the REAL generated Connect handler
// (knowledgev1connect.NewEngineServiceHandler) on the far end — no fake exporter
// anywhere on the path.
//
// WHAT IT ADDS OVER THE FAKE-EXPORTER TESTS. Those read the selector off a Go
// struct the intercept handed them in-process; this one reads what a server
// DECODED after the selector was marshaled, sent and unmarshaled, and it makes
// the answer depend on it: the bytes that come back — and the bytes that reach
// the cloud object — are the legacy graph's, not the combined graph's.
func TestSyncPush_LegacyPracticeSelectorSurvivesTheWire(t *testing.T) {
	legacyImage := []byte("KGV4 the image of practices/go.bin")
	combinedImage := []byte("KGV4 the image of the combined practice graph")

	stub := &practiceExportHandler{images: map[string][]byte{
		"go":      legacyImage,
		"default": combinedImage,
	}}
	// The client dials cleartext HTTP/2, so the real handler is mounted behind h2c
	// exactly as the daemon mounts it.
	path, handler := knowledgev1connect.NewEngineServiceHandler(stub)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)

	// The REAL client, pointed at the real handler.
	local := graphclient.NewGraphClientForURL(srv.URL)
	t.Cleanup(local.Close)

	backend := newFakeSyncBackend(t)
	withFakeSyncTransport(t, backend)

	handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: local},
		syncParams(t, map[string]any{"operation": "push", "graph": "practice", "name": "go"}))
	require.True(t, handled)
	require.False(t, out.IsError, "the legacy push must reach the handler and succeed: %q", textOf(out))

	require.NotNil(t, stub.gotTarget, "the handler decoded a target")
	assert.Equal(t, "practice", stub.gotTarget.GetGraph())
	assert.Equal(t, "go", stub.gotTarget.GetLanguage(),
		"the legacy name arrived on the language field after a real marshal/unmarshal")
	assert.Empty(t, stub.gotTarget.GetName(), "and not on the name field, which the family refuses")

	// THE BYTES ARE THE ASSERTION, not just the field: the cloud object the push
	// uploaded decrypts to the LEGACY image and is not the combined one.
	backend.mu.Lock()
	uploaded := backend.confirmedPlaintext
	backend.mu.Unlock()
	assert.Equal(t, legacyImage, uploaded, "the pushed image is practices/go.bin")
	assert.NotEqual(t, combinedImage, uploaded, "and never the combined graph's")
	assert.Equal(t, "go", backend.lastConfirmName, "ingested as practice/go")
}

// TestSyncPush_UnselectedPracticeExportIsTheCombinedGraph is the control for the
// test above, through the same real client and real handler: with no name the
// selector carries no language, the handler resolves the combined graph, and the
// combined image is what reaches the cloud. Without it, "the legacy image came
// back" could be a handler that serves one image whatever it is asked for.
func TestSyncPush_UnselectedPracticeExportIsTheCombinedGraph(t *testing.T) {
	legacyImage := []byte("KGV4 the image of practices/go.bin")
	combinedImage := []byte("KGV4 the image of the combined practice graph")

	stub := &practiceExportHandler{images: map[string][]byte{
		"go":      legacyImage,
		"default": combinedImage,
	}}
	path, handler := knowledgev1connect.NewEngineServiceHandler(stub)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)

	local := graphclient.NewGraphClientForURL(srv.URL)
	t.Cleanup(local.Close)
	backend := newFakeSyncBackend(t)
	withFakeSyncTransport(t, backend)

	handled, out := InterceptSync(opCtx(), interceptTestDeps{gc: local},
		syncParams(t, map[string]any{"operation": "push", "graph": "practice"}))
	require.True(t, handled)
	require.False(t, out.IsError, "the unselected push must succeed: %q", textOf(out))

	require.NotNil(t, stub.gotTarget)
	assert.Empty(t, stub.gotTarget.GetLanguage(), "no legacy selector is composed for the combined graph")
	assert.Empty(t, stub.gotTarget.GetName())

	backend.mu.Lock()
	uploaded := backend.confirmedPlaintext
	backend.mu.Unlock()
	assert.Equal(t, combinedImage, uploaded, "the pushed image is the combined graph's")
	assert.NotEqual(t, legacyImage, uploaded, "and never the legacy one's")
	assert.Equal(t, "default", backend.lastConfirmName)
}
