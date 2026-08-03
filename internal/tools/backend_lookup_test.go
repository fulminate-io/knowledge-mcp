// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// backend_lookup_test.go covers the backend-resolution helpers: metadata
// stripping, the batch backend-backed guard, node lookup and priority parsing.
// The scripted GraphCaller these drive lives in fake_graph_caller_test.go.

func TestStripBackendPrivateMetadata_StripsExpected(t *testing.T) {
	in := map[string]string{
		"backend":      "linear",
		"external_url": "https://example.invalid",
		"external_id":  "ABC-1",
		"linear_id":    "uuid",
		"linear_dirty": "true",
		"priority":     "high",
		"labels":       "bug",
	}
	out := stripBackendPrivateMetadata(in, "linear")
	assert.Equal(t, map[string]string{"priority": "high", "labels": "bug"}, out)
	// Caller's input must NOT have been mutated.
	_, hadBackend := in["backend"]
	assert.True(t, hadBackend, "input map must not be mutated in place")
}

func TestStripBackendPrivateMetadata_EmptyInput(t *testing.T) {
	assert.Nil(t, stripBackendPrivateMetadata(nil, "linear"))
	assert.Nil(t, stripBackendPrivateMetadata(map[string]string{}, "linear"))
}

func TestStripBackendPrivateMetadata_AllPrivate(t *testing.T) {
	in := map[string]string{"backend": "linear", "linear_id": "x", "external_url": "y"}
	out := stripBackendPrivateMetadata(in, "linear")
	assert.Nil(t, out, "all-private input should yield nil")
}

func TestGuardBatchHasNoBackendBacked_SingleID_PassesThrough(t *testing.T) {
	fc := &fakeGraphCaller{}
	err := guardBatchHasNoBackendBacked(context.Background(), fc, []string{"id-1"})
	require.NoError(t, err)
	assert.Empty(t, fc.calls, "single-id batches must not trigger any lookup")
}

func TestGuardBatchHasNoBackendBacked_NilCaller(t *testing.T) {
	err := guardBatchHasNoBackendBacked(context.Background(), nil, []string{"a", "b"})
	require.NoError(t, err)
}

func TestGuardBatchHasNoBackendBacked_AllLocal(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"a": nodeResultJSON(t, "a", "ticket", map[string]string{}),
		"b": nodeResultJSON(t, "b", "ticket", map[string]string{}),
	}}
	err := guardBatchHasNoBackendBacked(context.Background(), fc, []string{"a", "b"})
	require.NoError(t, err)
}

func TestGuardBatchHasNoBackendBacked_RejectsMixed(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"local-1":   nodeResultJSON(t, "local-1", "ticket", map[string]string{}),
		"backend-1": nodeResultJSON(t, "backend-1", "ticket", map[string]string{"backend": "linear"}),
		"local-2":   nodeResultJSON(t, "local-2", "ticket", map[string]string{}),
	}}
	err := guardBatchHasNoBackendBacked(context.Background(), fc, []string{"local-1", "backend-1", "local-2"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backend-1")
	assert.Contains(t, err.Error(), "mixed")
}

func TestGuardBatchHasNoBackendBacked_AllBackend_Permitted(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"backend-1": nodeResultJSON(t, "backend-1", "ticket", map[string]string{"backend": "linear"}),
		"backend-2": nodeResultJSON(t, "backend-2", "ticket", map[string]string{"backend": "linear"}),
	}}
	err := guardBatchHasNoBackendBacked(context.Background(), fc, []string{"backend-1", "backend-2"})
	require.NoError(t, err, "all-backend batches are safely retryable")
}

func TestLookupNodeBackend_Tombstoned_RoundTripsWireFlag(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"tomb-1": nodeResultJSON(t, "tomb-1", "ticket", map[string]string{
			"backend":      "linear",
			"linear_id":    "uuid-1",
			"external_url": "https://example.invalid/tomb-1",
		}),
	}}
	node, backendName, externalURL, backendID, meta, err := lookupNodeBackend(context.Background(), fc, "tomb-1")
	require.NoError(t, err)
	assert.Equal(t, "linear", backendName)
	assert.Equal(t, "uuid-1", backendID)
	assert.Equal(t, "https://example.invalid/tomb-1", externalURL)
	assert.Equal(t, "tomb-1", node.Id)
	assert.NotNil(t, meta)
	// Verify the compiled by-id QueryPlan carried include_tombstones:true (the
	// render.FetchNode Execute path threads it onto the plan, not the JSON args).
	require.Len(t, fc.execRequests, 1)
	q := fc.execRequests[0].GetQuery()
	require.NotNil(t, q, "lookup compiles a by-id QueryPlan")
	assert.Equal(t, "tomb-1", q.GetById())
	assert.True(t, q.GetIncludeTombstones(), "lookup must request include_tombstones:true")
}

func TestLookupNodeBackend_LocalOnly_ReturnsEmpty(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"local-1": nodeResultJSON(t, "local-1", "ticket", map[string]string{}),
	}}
	_, backendName, externalURL, backendID, _, err := lookupNodeBackend(context.Background(), fc, "local-1")
	require.NoError(t, err)
	assert.Empty(t, backendName)
	assert.Empty(t, backendID)
	assert.Empty(t, externalURL)
}

func TestLookupNodeBackend_NotFound_NotError(t *testing.T) {
	fc := &fakeGraphCaller{}
	_, backendName, _, _, _, err := lookupNodeBackend(context.Background(), fc, "missing")
	require.NoError(t, err)
	assert.Empty(t, backendName)
}

func TestLookupNodeBackend_TransportError_Surfaced(t *testing.T) {
	wantErr := errors.New("connect: refused")
	fc := &fakeGraphCaller{queryErrors: map[string]error{"id-1": wantErr}}
	_, _, _, _, _, err := lookupNodeBackend(context.Background(), fc, "id-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect: refused", "wrap must surface transport error")
}

func TestParsePriority(t *testing.T) {
	cases := map[string]int{
		"":       0,
		"none":   0,
		"urgent": 1,
		"High":   2,
		"medium": 3,
		"NORMAL": 3,
		"low":    4,
		"bogus":  0,
		"3":      3,
	}
	for in, want := range cases {
		assert.Equal(t, want, parsePriority(in), "input %q", in)
	}
}
