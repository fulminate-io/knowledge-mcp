// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
)

// mcphost_failures_test.go — the host's ERROR ARMS, split out of
// mcphost_test.go when that file crossed this repository's file-length cap.
// One table drives every way a provider can fail the host: it never starts, it
// dies before the handshake, it dies with the call in flight, it refuses, it
// breaks its own advertised schema, or its url never answers. The conforming
// paths and the shared refusal helper stay in mcphost_test.go, same package.

// startHTTPStubHangupOnCall is the http sibling of stubModeExitMidSession: the
// server answers initialize and tools/list normally and then HANGS UP on the
// tools/call request, without replying. It hijacks the connection and closes it
// rather than returning a status, because a status is a response — the arm under
// test is a provider that stops talking mid-call.
func startHTTPStubHangupOnCall(t *testing.T) string {
	t.Helper()
	server := newStubServer(stubModeConforming, "")
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"method":"tools/call"`)) {
			// t.Errorf rather than require: this runs on the SERVER's goroutine,
			// where require's FailNow is not allowed. A failure here means the
			// hangup never happened, and the caller's own refusal assertion will
			// fail too — this names the cause rather than leaving that failure
			// unexplained.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Errorf("the test server must support hijacking to model a mid-call hangup")
				return
			}
			conn, _, hjErr := hj.Hijack()
			if hjErr != nil {
				t.Errorf("hijacking the connection for the mid-call hangup: %v", hjErr)
				return
			}
			_ = conn.Close()
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		for ss := range server.Sessions() {
			_ = ss.Close()
		}
		srv.Close()
	})
	return srv.URL
}

// --- error arms ---

func TestRunMCP_ProviderRuntimeFailures(t *testing.T) {
	t.Run("process fails to start", func(t *testing.T) {
		def := stdioDef(t, stubModeConforming)
		def.Def.GetCollector().GetStdio().Command = "/ful1776/definitely-not-a-binary"
		res, _, err := RunMCP(context.Background(), def, nil, "board", nil)
		requireRefusal(t, res, err, []string{"not executable", "/ful1776/definitely-not-a-binary"})
	})

	t.Run("provider exits before the handshake", func(t *testing.T) {
		res, _, err := RunMCP(context.Background(), stdioDef(t, stubModeExitBeforeHandshake), nil, "board", nil)
		requireRefusal(t, res, err, []string{"handshake"})
	})

	// THE MID-SESSION DEATH, on both transports. It is a DIFFERENT arm from
	// "exits before the handshake": that one dies on the dial and its refusal
	// names the handshake, while this one has already been listed and
	// schema-verified, so its refusal comes from the tool CALL and must name the
	// tool. Nothing may be admitted from a provider that died with the call in
	// flight, which is what the nil result asserts here and what the
	// collect-level sibling asserts against the sink.
	t.Run("provider exits mid-session (stdio)", func(t *testing.T) {
		res, _, err := RunMCP(context.Background(), stdioDef(t, stubModeExitMidSession), nil, "board", nil)
		requireRefusal(t, res, err, []string{"calling tool", defaultStubTool})
		assert.NotContains(t, err.Error(), "handshake",
			"a provider that died AFTER the handshake must not be reported as a handshake failure")
	})

	t.Run("provider hangs up mid-session (http)", func(t *testing.T) {
		res, _, err := RunMCP(context.Background(), httpDef(startHTTPStubHangupOnCall(t)), nil, "board", nil)
		requireRefusal(t, res, err, []string{"calling tool", defaultStubTool})
	})

	t.Run("provider returns a tool error result", func(t *testing.T) {
		res, _, err := RunMCP(context.Background(), stdioDef(t, stubModeToolError), nil, "board", nil)
		requireRefusal(t, res, err, []string{"error result", "the stub provider refused this collect"})
	})

	t.Run("provider breaks its own advertised schema", func(t *testing.T) {
		res, _, err := RunMCP(context.Background(), stdioDef(t, stubModeBreaksOwnWord), nil, "board", nil)
		requireRefusal(t, res, err, []string{"the schema the provider itself advertised"})
	})

	t.Run("node with an empty type", func(t *testing.T) {
		res, _, err := RunMCP(context.Background(), stdioDef(t, stubModeEmptyNodeType), nil, "board", nil)
		requireRefusal(t, res, err, []string{"empty type"})
	})

	t.Run("result carrying a field the envelope does not define", func(t *testing.T) {
		res, _, err := RunMCP(context.Background(), stdioDef(t, stubModeUnknownField), nil, "board", nil)
		requireRefusal(t, res, err, []string{"does not define", "summry"})
	})

	t.Run("http provider returns a non-2xx", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "provider is down", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		res, _, err := RunMCP(context.Background(), httpDef(srv.URL), nil, "board", nil)
		requireRefusal(t, res, err, []string{"handshake"})
	})

	t.Run("http provider url does not resolve", func(t *testing.T) {
		res, _, err := RunMCP(context.Background(), httpDef("http://ful1776-no-such-host.invalid/mcp"), nil, "board", nil)
		requireRefusal(t, res, err, []string{"handshake"})
	})
}
