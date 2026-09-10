// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_custom_refusals_test.go — WHAT A REFUSED CUSTOM COLLECT WRITES,
// which is nothing, split out of collect_custom_test.go when that file crossed
// this repository's file-length cap. Both tests here pair a refusing provider
// with a live one in the same run, so an empty sink is evidence of the refusal
// rather than of a sink that never works. The fixtures they share
// (customDeps, customDef, startCustomProvider) stay in collect_custom_test.go,
// same package.

// TestCustomCollect_RefusalWritesNothing is R2(f) at the dispatch: a provider
// whose tool does not satisfy the contract is refused and the sink stays empty.
func TestCustomCollect_RefusalWritesNothing(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	def := customDef(url)
	def.entry.Tool = "no_such_tool"
	deps := newCustomDeps(t, def)

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.True(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "no_such_tool")
	assert.Nil(t, deps.sink.last(), "a refused collect must write nothing")
	assert.Equal(t, 0, deps.wakeCount(), "and must not wake the pipeline")
}

// TestCustomCollect_ProviderDiesMidCallWritesNothing is the collect-level half of
// the provider-exits-mid-session arm. The externalcollector sibling pins the
// refusal's text; this pins that NOTHING IS ADMITTED.
//
// IT IS THE ONE ARM WHERE A PARTIAL RESULT IS CONCEIVABLE, which is why it needs
// its own sink assertion rather than riding the schema gate's: by the time the
// provider dies the session has already produced a tool listing and passed the
// schema gate, so a caller reading only "collect failed" cannot tell a crashed
// provider from a refused one. The known positive in the same run is what makes
// the empty sink evidence of the refusal rather than of a sink that never works.
func TestCustomCollect_ProviderDiesMidCallWritesNothing(t *testing.T) {
	goodURL := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t, customDef(goodURL))

	handled, res := callCollect(deps, `{"type":"`+customStubFamily+`","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	require.NotNil(t, deps.sink.last(), "known positive: a live provider does reach the sink")
	writesBefore := len(deps.sink.results)

	dyingDeps := newCustomDeps(t, customDef(startCustomProviderHangupOnCall(t)))
	handled, res = callCollect(dyingDeps, `{"type":"`+customStubFamily+`","id":"board"}`)
	require.True(t, handled)
	require.True(t, res.IsError, "a provider that dies mid-call must be refused: %s", resultText(res))
	assert.Contains(t, resultText(res), customStubTool, "the refusal must name the tool it was calling")
	assert.Nil(t, dyingDeps.sink.last(), "a provider that died with the call in flight must have written NOTHING")

	assert.Len(t, deps.sink.results, writesBefore, "and the live provider's sink is untouched by it")
}

// startCustomProviderHangupOnCall answers initialize and tools/list and then
// hangs up on tools/call, by hijacking the connection and closing it. A status
// code would be a response; this arm is a provider that stops talking.
func startCustomProviderHangupOnCall(t *testing.T) string {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "ful1776-hangup-stub", Version: "v1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:         customStubTool,
		Description:  "stub custom collector",
		InputSchema:  contractSchema(t, externalcollector.InputContractJSON()),
		OutputSchema: contractSchema(t, externalcollector.OutputContractJSON()),
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: conformingCustomPayload()}, nil
	})
	// The describe tool is REQUIRED of every provider on every dial, so every stub
	// in this package serves it: a stub without it would fail each row on the
	// missing tool rather than on the property the row is about. The declaration
	// is the smallest conforming one — what a row needs from it, it sets itself.
	addCustomDescribeTool(server, nil)
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
