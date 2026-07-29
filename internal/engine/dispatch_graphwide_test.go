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
)

// TestDispatchGraphWide_MatchAllEdgePlan asserts the start-less traverse composes
// TWO Executes — the node enumeration, then a MATCH-ALL edges read carrying no
// pivot discriminant of any kind. The absent pivot is the whole point: the node
// ids no longer make the round trip upstream just to spell "all".
func TestDispatchGraphWide_MatchAllEdgePlan(t *testing.T) {
	s := &seqExec{responses: []*knowledgev1.ExecuteResponse{
		enginetest.ResponseWithNodes(
			&knowledgev1.Node{Id: "n1", SymbolName: "One", Type: "plan"},
			&knowledgev1.Node{Id: "n2", SymbolName: "Two", Type: "step"},
		),
		{Edges: edgesToProtoForTest([]knowledgev1.Edge{
			{FromId: "n1", ToId: "n2", Type: "contains"},
		})},
	}}

	out, err := Dispatch(context.Background(), s.fn(),
		"traverse", json.RawMessage(`{"graph":"knowledge","format":"json"}`))
	require.NoError(t, err)

	assert.Equal(t, 2, s.calls, "node enumeration + ONE match-all edges read")
	require.Len(t, s.reqs, 2)
	edgesQ := s.reqs[1].GetQuery()
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_EDGES, edgesQ.GetReturnMode())
	assert.Empty(t, edgesQ.GetIds(), "match-all plan carries no ids[] pivot")
	assert.Empty(t, edgesQ.GetById(), "match-all plan carries no by_id pivot")
	assert.Empty(t, edgesQ.GetSelection().GetFromId(), "match-all plan carries no from_id pivot")
	assert.Contains(t, out.Content[0].Text, `"n1"`)
	assert.Contains(t, out.Content[0].Text, `"target":"n2"`)
}

// TestDispatchGraphWide_DanglingSourceEdgeNotRendered pins the one place the
// match-all read is BROADER than the all-node-ids pivot read it replaced: a
// dangling edge whose source has no node row (hard-deleted node, no cascade on
// the edges table) comes back from the backend, and the renderer must drop it so
// the rendered list stays exactly what the pivot read produced.
func TestDispatchGraphWide_DanglingSourceEdgeNotRendered(t *testing.T) {
	s := &seqExec{responses: []*knowledgev1.ExecuteResponse{
		enginetest.ResponseWithNodes(
			&knowledgev1.Node{Id: "n1", SymbolName: "One", Type: "plan"},
			&knowledgev1.Node{Id: "n2", SymbolName: "Two", Type: "step"},
		),
		{Edges: edgesToProtoForTest([]knowledgev1.Edge{
			{FromId: "n1", ToId: "n2", Type: "contains"},
			{FromId: "ghost", ToId: "n2", Type: "contains"}, // source has no node row
		})},
	}}

	out, err := Dispatch(context.Background(), s.fn(),
		"traverse", json.RawMessage(`{"graph":"knowledge","format":"json"}`))
	require.NoError(t, err)
	assert.NotContains(t, out.Content[0].Text, "ghost",
		"an edge whose source is not an enumerated node must not be rendered")
	assert.Contains(t, out.Content[0].Text, `"target":"n2"`, "the live edge still renders")

	// The text renderer counts the same filtered set. It enumerates IDS rather
	// than hydrated nodes, so its first response carries the ids carrier — the
	// membership set the dangling filter needs comes from those ids.
	sText := &seqExec{responses: []*knowledgev1.ExecuteResponse{
		{Ids: []string{"n1", "n2"}},
		s.responses[1],
	}}
	outText, err := Dispatch(context.Background(), sText.fn(),
		"traverse", json.RawMessage(`{"graph":"knowledge"}`))
	require.NoError(t, err)
	assert.Contains(t, outText.Content[0].Text, "- edges: 1",
		"the dangling edge is excluded from the count too")
}

// TestDispatchGraphWideEdges_TextFormatUsesIDsOnly pins the format split: the
// text arm enumerates with a bounded RETURN_MODE_IDS keyset browse, while the
// JSON arm keeps the hydrated match-all read it needs to render node rows.
func TestDispatchGraphWideEdges_TextFormatUsesIDsOnly(t *testing.T) {
	t.Run("text arm enumerates ids in a bounded keyset page", func(t *testing.T) {
		s := &seqExec{responses: []*knowledgev1.ExecuteResponse{
			{Ids: []string{"n1", "n2"}},
			{Edges: edgesToProtoForTest([]knowledgev1.Edge{{FromId: "n1", ToId: "n2", Type: "contains"}})},
		}}
		out, err := Dispatch(context.Background(), s.fn(),
			"traverse", json.RawMessage(`{"graph":"knowledge"}`))
		require.NoError(t, err)
		require.Len(t, s.reqs, 2)

		nodesQ := s.reqs[0].GetQuery()
		assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_IDS, nodesQ.GetReturnMode(),
			"the text arm must not hydrate whole nodes to print two counts")
		assert.Equal(t, int32(BrowsePageSize), nodesQ.GetLimit(), "a bounded page, not limit 0")
		require.NotNil(t, nodesQ.AfterId, "after_id must be PRESENT on page 1 — presence selects the keyset browse")
		assert.Empty(t, nodesQ.GetAfterId(), "page 1's cursor is the empty string")
		assert.True(t, nodesQ.GetSkipTotal(), "the drain never reads Total")

		assert.Contains(t, out.Content[0].Text, "- nodes: 2")
		assert.Contains(t, out.Content[0].Text, "- edges: 1")
	})

	t.Run("json arm stays hydrated", func(t *testing.T) {
		s := &seqExec{responses: []*knowledgev1.ExecuteResponse{
			enginetest.ResponseWithNodes(&knowledgev1.Node{Id: "n1", SymbolName: "One", Type: "plan"}),
			{Edges: edgesToProtoForTest(nil)},
		}}
		out, err := Dispatch(context.Background(), s.fn(),
			"traverse", json.RawMessage(`{"graph":"knowledge","format":"json"}`))
		require.NoError(t, err)
		require.NotEmpty(t, s.reqs)

		nodesQ := s.reqs[0].GetQuery()
		assert.NotEqual(t, knowledgev1.ReturnMode_RETURN_MODE_IDS, nodesQ.GetReturnMode(),
			"renderGraphWideJSON needs five fields per node — ids would not serve it")
		assert.Contains(t, out.Content[0].Text, `"One"`, "the hydrated name still renders")
	})
}

// TestDispatchGraphWide_EmptyGraphSkipsEdgeRead pins that an empty node
// enumeration short-circuits before the edge read, so an empty graph costs one
// Execute and renders no edges — identical to the id-pivoted shape it replaced.
func TestDispatchGraphWide_EmptyGraphSkipsEdgeRead(t *testing.T) {
	s := &seqExec{responses: []*knowledgev1.ExecuteResponse{
		enginetest.ResponseWithNodes(),
	}}
	_, err := Dispatch(context.Background(), s.fn(),
		"traverse", json.RawMessage(`{"graph":"knowledge","format":"json"}`))
	require.NoError(t, err)
	assert.Equal(t, 1, s.calls, "no nodes → no edge read")
}
