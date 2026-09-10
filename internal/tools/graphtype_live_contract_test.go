// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// graphtype_live_contract_test.go — the collect validates its arguments against
// the schema the provider ADVERTISED, before the call is made.
//
// ITS SIBLING MOVED WITH THE WRITE PATH. The register-time half of this file
// pinned that custom_collector(register|update) dialed the provider before
// storing anything; those operations are gone, and the property now belongs to
// `knowledge collector add`, which dials before writing the entry — see the
// bootstrap package. Deleting the pre-call argument validation still leaves both
// packages green without the test below, which is why it stays here.

// startProviderWithSchemas stands a provider up whose tool advertises the given
// schemas, and counts the tool calls it receives. The COUNT is what makes the
// pre-call refusal observable: "refused" and "refused after calling the
// provider" are different behaviors, and only the counter separates them.
func startProviderWithSchemas(t *testing.T, in, out any) (url string, calls *callCounter) {
	t.Helper()
	calls = &callCounter{}
	server := mcp.NewServer(&mcp.Implementation{Name: "ful1776-contract-stub", Version: "v1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:         customStubTool,
		Description:  "stub custom collector",
		InputSchema:  in,
		OutputSchema: out,
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.inc()
		return &mcp.CallToolResult{StructuredContent: conformingCustomPayload()}, nil
	})
	// The describe tool is REQUIRED of every provider on every dial, so every stub
	// in this package serves it: a stub without it would fail each row on the
	// missing tool rather than on the property the row is about. The declaration
	// is the smallest conforming one — what a row needs from it, it sets itself.
	addCustomDescribeTool(server, nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(func() {
		for ss := range server.Sessions() {
			_ = ss.Close()
		}
		srv.Close()
	})
	return srv.URL, calls
}

type callCounter struct {
	mu sync.Mutex
	n  int
}

func (c *callCounter) inc()       { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *callCounter) count() int { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

// contractSchemaMapT decodes a checked-in contract schema into a mutable map.
func contractSchemaMapT(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

// --- R2: the collect validates its arguments against the ADVERTISED schema ---

// TestCustomCollect_ArgumentsAreValidatedBeforeTheCall pins the promise the
// collect tool's schema and collect.md both make: the params object is checked
// against the schema the PROVIDER advertised, and a violation is refused BEFORE
// the call rather than inside the provider's own handler.
//
// THE CALL COUNTER IS THE POINT. Without it a refusal after a wasted call would
// read identically, and "validated before the call" is precisely the claim.
func TestCustomCollect_ArgumentsAreValidatedBeforeTheCall(t *testing.T) {
	// The provider demands a params object carrying a string `repo`.
	strictIn := contractSchemaMapT(t, externalcollector.InputContractJSON())
	props := strictIn["properties"].(map[string]any)
	props["params"] = map[string]any{
		"type":                 "object",
		"required":             []any{"repo"},
		"properties":           map[string]any{"repo": map[string]any{"type": "string"}},
		"additionalProperties": false,
	}
	strictIn["required"] = []any{"id", "params"}

	url, calls := startProviderWithSchemas(t, strictIn, contractSchemaMapT(t, externalcollector.OutputContractJSON()))
	deps := newCustomDeps(t, customDef(url))

	handled, res := callCollect(deps, `{"type":"`+customStubFamily+`","id":"board","params":{"wrong_key":"x"}}`)
	require.True(t, handled)
	require.True(t, res.IsError, "params violating the provider's advertised inputSchema must be refused: %s", resultText(res))
	body := resultText(res)
	assert.Contains(t, body, customStubTool, "the refusal must name the tool")
	assert.Contains(t, body, "call arguments", "the refusal must say which side failed")
	assert.Equal(t, 0, calls.count(), "the provider must NOT have been called — the refusal is a PRE-call check")
	assert.Nil(t, deps.sink.last(), "a refused collect must write nothing")

	// KNOWN POSITIVE, same provider, same run: conforming params reach it.
	handled, res = callCollect(deps, `{"type":"`+customStubFamily+`","id":"board","params":{"repo":"acme"}}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	assert.Equal(t, 1, calls.count(), "conforming params must reach the provider")
	assert.NotNil(t, deps.sink.last())
}
