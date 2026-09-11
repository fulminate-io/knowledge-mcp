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
// export the way the server's own resolver does, and serves a distinct byte
// image per graph so a test can tell WHICH graph answered.
//
// IT REFUSES A SET NAME AND A SET LANGUAGE, because the server's practice
// selector policy refuses both: the practice row carries no instance field at
// all, so either one is rejected before routing ever happens. A stub that
// accepted one would let a client-side regression onto that field pass this seam
// and fail in production — which is what the language half models now that the
// pre-singleton graphs it used to address are gone.
//
// The RESOLUTION RULE modeled here is proved against the real store, real
// resolver and real handler by the server module's own seam test — this half
// exists to prove the selector the client composes SURVIVES THE WIRE and steers
// the answer, which is the hop neither module's unit tests can see.
type practiceExportHandler struct {
	knowledgev1connect.UnimplementedEngineServiceHandler

	// images is keyed by the practice graph name. "default" is the combined
	// graph and the only key a resolve can reach; the other keys are
	// pre-singleton graphs, kept so a test can assert that their bytes were
	// NOT the ones that moved.
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
	if t.GetLanguage() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errLanguagePracticeSelector)
	}
	name := "default"
	image, ok := h.images[name]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoSuchPracticeGraph)
	}
	return connect.NewResponse(&knowledgev1.ExportGraphResponse{GraphBytes: image}), nil
}

var (
	errNamedPracticeSelector    = &seamError{"graph=practice does not consume a name"}
	errLanguagePracticeSelector = &seamError{"graph=practice does not accept language="}
	errNoSuchPracticeGraph      = &seamError{"practice graph not found"}
)

type seamError struct{ msg string }

func (e *seamError) Error() string { return e.msg }

// TestSyncPush_ANamedPracticeGraphNeverReachesTheWire is the client-to-server hop
// of the sync seam, with the REAL graph client (graphclient.GraphClient over a
// real HTTP round trip) and the REAL generated Connect handler
// (knowledgev1connect.NewEngineServiceHandler) on the far end — no fake exporter
// anywhere on the path.
//
// IT INVERTED. It used to be TestSyncPush_LegacyPracticeSelectorSurvivesTheWire:
// a push of practice/go composed a legacy selector, the selector survived a real
// marshal and unmarshal, and the bytes that came back were the legacy graph's
// rather than the combined graph's. The pre-singleton images are gone, so the
// client refuses the name before either seam and the assertion is the ABSENCE:
// nothing was exported, nothing was offered to the cloud, and the handler was
// never called.
//
// WHAT IT ADDS OVER THE FAKE-EXPORTER TEST. That one reads the refusal off an
// in-process result and counts a fake's calls; this one proves the far side was
// never reached at all, which is the property "refused before the seams" is
// actually about.
func TestSyncPush_ANamedPracticeGraphNeverReachesTheWire(t *testing.T) {
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
	require.True(t, out.IsError, "a named practice push is refused")
	assert.Contains(t, textOf(out), "ONE combined graph")

	assert.Nil(t, stub.gotTarget,
		"the handler was never called, so no selector was composed for it to decode")
	assert.Equal(t, 0, backend.presignCalls, "and no cloud object was offered")
	backend.mu.Lock()
	uploaded := backend.confirmedPlaintext
	backend.mu.Unlock()
	assert.Empty(t, uploaded, "nothing was uploaded under any name")
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
