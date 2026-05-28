// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// TestLogState_NodeByID_RoundTrips seeds a logState with 3 templates /
// 2 streams / 5 chunks and asserts NodeByID returns each seeded node
// and (zero, false) for an unseeded ID. Mirrors the criterion shape.
func TestLogState_NodeByID_RoundTrips(t *testing.T) {
	templates := []*knowledgev1.Node{
		{Id: "tpl-1", Type: string(kgtypes.NodeLogTemplate), SymbolName: "a"},
		{Id: "tpl-2", Type: string(kgtypes.NodeLogTemplate), SymbolName: "b"},
		{Id: "tpl-3", Type: string(kgtypes.NodeLogTemplate), SymbolName: "c"},
	}
	streams := []*knowledgev1.Node{
		{Id: "stream-1", Type: string(kgtypes.NodeLogStream)},
		{Id: "stream-2", Type: string(kgtypes.NodeLogStream)},
	}
	chunks := []*knowledgev1.Node{
		{Id: "chunk-1", Type: string(kgtypes.NodeLogChunk)},
		{Id: "chunk-2", Type: string(kgtypes.NodeLogChunk)},
		{Id: "chunk-3", Type: string(kgtypes.NodeLogChunk)},
		{Id: "chunk-4", Type: string(kgtypes.NodeLogChunk)},
		{Id: "chunk-5", Type: string(kgtypes.NodeLogChunk)},
	}
	st := newLogState(templates, streams, chunks, nil, nil, nil)

	for _, n := range append(append([]*knowledgev1.Node{}, templates...), append(streams, chunks...)...) {
		got, ok := st.NodeByID(n.Id)
		assert.True(t, ok, "seeded id %q must be present", n.Id)
		assert.Equal(t, n.Id, got.Id)
	}
	_, ok := st.NodeByID("does-not-exist")
	assert.False(t, ok, "unseeded id must return (zero, false)")
}

// TestLogState_EdgesOf_FiltersByType seeds mixed edge types and asserts
// EdgesOf narrows to the requested types only. Mirrors the EdgeIterRequest
// call shape at tools_logs_query_explain.go:120 and
// tools_logs_query_ranking.go:93.
func TestLogState_EdgesOf_FiltersByType(t *testing.T) {
	edges := []knowledgev1.Edge{
		{FromId: "tpl-1", ToId: "tpl-2", Type: string(kgtypes.EdgeCorrelatesWith)},
		{FromId: "tpl-1", ToId: "tpl-3", Type: string(kgtypes.EdgeCorrelatesWith)},
		{FromId: "tpl-1", ToId: "chunk-1", Type: string(kgtypes.EdgeType("EMITTED_BY"))},
		{FromId: "tpl-2", ToId: "tpl-1", Type: string(kgtypes.EdgeCorrelatesWith)},
	}
	st := newLogState(nil, nil, nil, nil, nil, edges)

	// Outgoing CORRELATES_WITH only — should match the first two edges.
	out := st.EdgesOf("tpl-1", kgwire.OutgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeCorrelatesWith})
	assert.Len(t, out, 2)
	for i := range out {
		assert.Equal(t, string(kgtypes.EdgeCorrelatesWith), out[i].Type)
	}

	// Outgoing without filter — both CORRELATES_WITH and EMITTED_BY surface.
	allOut := st.EdgesOf("tpl-1", kgwire.OutgoingEdges, nil)
	assert.Len(t, allOut, 3)

	// Incoming filter — the tpl-2 → tpl-1 edge.
	in := st.EdgesOf("tpl-1", kgwire.IncomingEdges, []kgtypes.EdgeType{kgtypes.EdgeCorrelatesWith})
	assert.Len(t, in, 1)
	assert.Equal(t, "tpl-2", in[0].FromId)

	// Unseeded node — empty result, not panic.
	none := st.EdgesOf("does-not-exist", kgwire.OutgoingEdges, nil)
	assert.Empty(t, none)
}

// TestLogState_NilSafe asserts nil-receiver invocations don't panic.
// Defensive — tools_logs_handler.go's resolveGraphDB previously
// returned nil DB when the local store wasn't initialized; with the
// logState replacement, callers may still hit the nil-state path during
// degraded operation.
func TestLogState_NilSafe(t *testing.T) {
	var st *logState
	_, ok := st.NodeByID("x")
	assert.False(t, ok)
	assert.Nil(t, st.EdgesOf("x", kgwire.OutgoingEdges, nil))
}
