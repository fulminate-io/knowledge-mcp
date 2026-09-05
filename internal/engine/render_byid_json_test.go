// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// render_byid_json_test.go covers the format:"json" arm of the by-id absorption
// read — the arm that used to drop the parameter silently.
//
// EVERY TEST HERE DRIVES Dispatch END-TO-END rather than renderByIDResult
// directly. The defect was not in a renderer; it was that no caller ever handed
// the renderer a format, so a renderer-only test would pass against the very
// tree the ticket describes. The format has to travel from the args map through
// dispatchQueryByID to the shape choice, and only the end-to-end call proves it
// does.

// byIDEnvelope is the decode target for the format:"json" payload. It is a
// LOCAL map decode rather than a typed one wherever key PRESENCE is the
// question: a typed struct decodes an absent `truncated` and a false one to the
// same zero value, which is precisely the distinction these tests exist to make.
func byIDEnvelope(t *testing.T, text string) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &payload),
		"the format:\"json\" by-id read must return a parseable JSON envelope, got: %s", text)
	return payload
}

// byIDEdgeExec builds the 3-response sequence a query(id, include_edges) read
// consumes: (1) the bare node, (2) the raw edges, (3) the ONE bulk peer hydrate.
// hydrateTruncated is the server's verdict on that third call — the only read in
// the composition that can come back clamped.
func byIDEdgeExec(hydrateTruncated bool) *seqExec {
	peers := enginetest.ResponseWithNodes(
		&knowledgev1.Node{Id: "a", SymbolName: "Alpha", Type: "phase"},
	)
	peers.Truncated = hydrateTruncated
	return &seqExec{responses: []*knowledgev1.ExecuteResponse{
		enginetest.ResponseWithNode(&knowledgev1.Node{Id: "n1", SymbolName: "Hub", Type: "plan"}),
		{Edges: edgesToProtoForTest([]knowledgev1.Edge{{FromId: "n1", ToId: "a", Type: "contains"}})},
		peers,
	}}
}

// TestByIDJSON_EnvelopeShape pins the envelope the ticket enumerates —
// node + edges + cross_links + truncated — on a knowledge-graph read that
// requested both sections.
//
// THE CROSS-LINK LEG IS THE LOAD-BEARING ONE. The legacy body concatenates a
// markdown "## Cross-Graph Links" section onto its JSON, so a caller asking for
// cross-links used to receive bytes no JSON parser accepts. json.Unmarshal
// succeeding on a payload that CONTAINS a cross_links row is what refuses that
// implementation; asserting the key alone would pass against a body with the
// markdown still stapled on only if the staple came first, so the parse is the
// assertion and the row count is the confirmation.
func TestByIDJSON_EnvelopeShape(t *testing.T) {
	proxy := &knowledgev1.Node{
		Id:         "proxy:knowledge:n1",
		SymbolName: "Proxy",
		Type:       string(kgtypes.NodeProxy),
		Metadata:   map[string]string{"foreign_id": "n1", "graph_type": string(kgtypes.GraphCode), "name": "knowledge"},
	}
	s := &seqExec{responses: []*knowledgev1.ExecuteResponse{
		enginetest.ResponseWithNode(&knowledgev1.Node{Id: "n1", SymbolName: "Hub", Type: "plan"}),     // (1) bare node
		{Edges: edgesToProtoForTest([]knowledgev1.Edge{{FromId: "n1", ToId: "a", Type: "contains"}})}, // (2) edges
		enginetest.ResponseWithNodes(&knowledgev1.Node{Id: "a", SymbolName: "Alpha", Type: "phase"}),  // (3) peers
		enginetest.ResponseWithNode(proxy), // (4) linkage by-id: the node IS a proxy
		{Edges: edgesToProtoForTest([]knowledgev1.Edge{{FromId: proxy.Id, ToId: "sym1", Type: "BACKS"}})}, // (5) proxy edges
		enginetest.ResponseWithNodes(&knowledgev1.Node{ // (6) proxy peer hydrate
			Id: "sym1", SymbolName: "SomeSymbol", Type: "function",
			Metadata: map[string]string{"graph_type": string(kgtypes.GraphCode), "name": "knowledge"},
		}),
	}}

	out, err := Dispatch(context.Background(), s.fn(), nil, "query",
		json.RawMessage(`{"id":"n1","include_edges":true,"include_cross_links":true,"graph":"knowledge","format":"json"}`))
	require.NoError(t, err)
	require.False(t, out.IsError, "content: %s", out.Content[0].Text)

	payload := byIDEnvelope(t, out.Content[0].Text)
	node, ok := payload["node"].(map[string]any)
	require.True(t, ok, "the envelope carries the node under `node`; keys: %v", keysOf(payload))
	assert.Equal(t, "n1", node["id"])

	edges, ok := payload["edges"].([]any)
	require.True(t, ok, "the envelope carries the edge summary under `edges`; keys: %v", keysOf(payload))
	require.Len(t, edges, 1)

	links, ok := payload["cross_links"].([]any)
	require.True(t, ok,
		"the envelope carries the cross-graph links under `cross_links` — as DATA. The legacy body "+
			"appends them as a markdown section, which is why this payload has to parse at all; keys: %v",
		keysOf(payload))
	require.Len(t, links, 1)

	_, hasTruncated := payload["truncated"]
	require.True(t, hasTruncated, "the envelope carries `truncated` unconditionally; keys: %v", keysOf(payload))
}

// TestByIDJSON_TruncatedField is the two-polarity gate on the key: present and
// FALSE on a whole read, present and TRUE on one whose peer hydrate the server
// clamped. A single polarity cannot tell a wired field from a constant in either
// direction.
func TestByIDJSON_TruncatedField(t *testing.T) {
	render := func(hydrateTruncated bool) map[string]any {
		s := byIDEdgeExec(hydrateTruncated)
		out, err := Dispatch(context.Background(), s.fn(), nil, "query",
			json.RawMessage(`{"id":"n1","include_edges":true,"graph":"knowledge","format":"json"}`))
		require.NoError(t, err)
		require.False(t, out.IsError)
		return byIDEnvelope(t, out.Content[0].Text)
	}

	t.Run("whole result", func(t *testing.T) {
		assertTruncatedKey(t, render(false), false, "by-id (json)")
	})
	t.Run("clamped peer hydrate", func(t *testing.T) {
		assertTruncatedKey(t, render(true), true, "by-id (json)")
	})
}

// TestByIDJSON_ClampAlsoKeepsTheProseNotice pins that the JSON arm does not
// TRADE the prose notice for the key: the payload block stays independently
// parseable in Content[0] and the disclosure rides a SECOND block, which is the
// contract query_schema.go states for every other JSON envelope.
func TestByIDJSON_ClampAlsoKeepsTheProseNotice(t *testing.T) {
	s := byIDEdgeExec(true)
	out, err := Dispatch(context.Background(), s.fn(), nil, "query",
		json.RawMessage(`{"id":"n1","include_edges":true,"graph":"knowledge","format":"json"}`))
	require.NoError(t, err)
	require.Len(t, out.Content, 2, "the notice is a SECOND block, never concatenated into the payload")
	byIDEnvelope(t, out.Content[0].Text) // block 0 still parses as JSON.
	assert.Contains(t, out.Content[1].Text, "the server row ceiling engaged")
}

// TestByID_DefaultFormatKeepsLegacyBodies is the control that stops the format
// branch becoming unconditional. Without it, a renderer rewritten to emit the
// envelope for EVERY by-id read passes every assertion above while silently
// changing the bytes render.FetchNode and every text caller consume.
//
// The knowledge leg asserts the legacy {node, edges} body — a top-level `node`
// key with NO `truncated` beside it — and the generic leg asserts markdown.
func TestByID_DefaultFormatKeepsLegacyBodies(t *testing.T) {
	t.Run("knowledge: {node, edges} with no truncated key", func(t *testing.T) {
		s := byIDEdgeExec(true) // clamped: the key would be TRUE if this body carried one.
		out, err := Dispatch(context.Background(), s.fn(), nil, "query",
			json.RawMessage(`{"id":"n1","include_edges":true,"graph":"knowledge"}`))
		require.NoError(t, err)
		payload := byIDEnvelope(t, out.Content[0].Text)
		_, hasNode := payload["node"]
		require.True(t, hasNode, "the legacy knowledge body is {node, edges}; keys: %v", keysOf(payload))
		_, hasTruncated := payload["truncated"]
		assert.False(t, hasTruncated,
			"the format-unset body is unchanged by this ticket — its truncation disclosure is the "+
				"trailing notice block, and query_schema.go says so")
	})

	t.Run("generic: markdown", func(t *testing.T) {
		s := byIDEdgeExec(false)
		out, err := Dispatch(context.Background(), s.fn(), nil, "query",
			json.RawMessage(`{"id":"n1","include_edges":true,"graph":"practice","language":"go"}`))
		require.NoError(t, err)
		assert.Contains(t, out.Content[0].Text, " node\n\n**Hub**",
			"the format-unset cross-graph body stays markdown")
	})
}

// TestByIDJSON_GenericGraphAlsoHonorsFormat covers the second polarity of the
// graph axis: the cross-graph arm returned markdown for a json request, and a
// fix wired only into the knowledge branch would leave it there.
func TestByIDJSON_GenericGraphAlsoHonorsFormat(t *testing.T) {
	s := byIDEdgeExec(false)
	out, err := Dispatch(context.Background(), s.fn(), nil, "query",
		json.RawMessage(`{"id":"n1","include_edges":true,"graph":"practice","language":"go","format":"json"}`))
	require.NoError(t, err)
	payload := byIDEnvelope(t, out.Content[0].Text)
	assert.NotContains(t, out.Content[0].Text, "## practice", "a json request never returns the markdown body")
	_, hasTruncated := payload["truncated"]
	require.True(t, hasTruncated, "the generic arm carries the key too; keys: %v", keysOf(payload))
}
