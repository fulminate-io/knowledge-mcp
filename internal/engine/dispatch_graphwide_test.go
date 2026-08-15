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
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// TestDispatchGraphWide_PivotPagedEdgePlan asserts the start-less traverse
// composes TWO Executes — the node enumeration, then a BOUNDED edges read that
// pivots on the enumerated ids under an explicit positive limit. Sending the ids
// upstream is the whole point: the match-all plan they used to spell as an absent
// pivot was unbounded, and cost that scales with the entire edge table is exactly
// what a user-reachable read must not offer.
func TestDispatchGraphWide_PivotPagedEdgePlan(t *testing.T) {
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

	assert.Equal(t, 2, s.calls, "node enumeration + ONE bounded edge page")
	require.Len(t, s.reqs, 2)
	edgesQ := s.reqs[1].GetQuery()
	assert.Equal(t, knowledgev1.ReturnMode_RETURN_MODE_EDGES, edgesQ.GetReturnMode())
	assert.ElementsMatch(t, []string{"n1", "n2"}, edgesQ.GetIds(),
		"the edge page pivots on the enumerated node ids")
	assert.Positive(t, edgesQ.GetLimit(), "the edge page carries an explicit positive limit")
	assert.Contains(t, out.Content[0].Text, `"n1"`)
	assert.Contains(t, out.Content[0].Text, `"target":"n2"`)
}

// TestDispatchGraphWide_DanglingSourceEdgeNotRendered pins the renderer's
// source-membership filter directly. A dangling edge — one whose source has no
// node row (hard-deleted node, no cascade on the edges table) — can no longer
// arrive from a real backend now that the read pivots on the enumerated ids,
// because a vanished endpoint is never in the pivot set. The fake serves one
// anyway: the filter is belt-and-braces rather than load-bearing, and this keeps
// it honest independent of the read shape.
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
		assert.Equal(t, int32(paging.BrowsePageSize), nodesQ.GetLimit(), "a bounded page, not limit 0")
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

// TestDispatchGraphWide_CandidateGroups pins the graph-wide JSON arm's group
// collapse: one edge_groups entry per group, member edges withheld from the flat
// array, and the arm's own dangling-source rule applied to groups as well.
func TestDispatchGraphWide_CandidateGroups(t *testing.T) {
	groupEdges := func(from string) []knowledgev1.Edge {
		key := from + ":42:CALLS:Run"
		return []knowledgev1.Edge{
			{FromId: from, ToId: "p/a.go:Run", Type: "CALLS", Method: kgtypes.EdgeMethodAmbiguousName, Evidence: key, Confidence: 1.0 / 3.0},
			{FromId: from, ToId: "p/b.go:Run", Type: "CALLS", Method: kgtypes.EdgeMethodAmbiguousName, Evidence: key, Confidence: 1.0 / 3.0},
			{FromId: from, ToId: "p/c.go:Run", Type: "CALLS", Method: kgtypes.EdgeMethodAmbiguousName, Evidence: key, Confidence: 1.0 / 3.0},
		}
	}
	nodes := []*knowledgev1.Node{
		{Id: "n1", SymbolName: "One", Type: "function"},
		{Id: "n2", SymbolName: "Two", Type: "function"},
		{Id: "p/a.go:Run", SymbolName: "Run", Type: "function", FilePath: "p/a.go", StartLine: 10},
		{Id: "p/b.go:Run", SymbolName: "Run", Type: "function", FilePath: "p/b.go", StartLine: 20},
		{Id: "p/c.go:Run", SymbolName: "Run", Type: "function", FilePath: "p/c.go", StartLine: 30},
	}

	renderPayload := func(t *testing.T, edges []knowledgev1.Edge) map[string]any {
		t.Helper()
		s := &seqExec{responses: []*knowledgev1.ExecuteResponse{
			enginetest.ResponseWithNodes(nodes...),
			{Edges: edgesToProtoForTest(edges)},
		}}
		out, err := Dispatch(context.Background(), s.fn(),
			"traverse", json.RawMessage(`{"graph":"knowledge","format":"json"}`))
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		return payload
	}

	t.Run("group_emitted_and_members_excluded", func(t *testing.T) {
		// BOTH HALVES REQUIRED: asserting only the exclusion is satisfied by an
		// implementation that drops every edge.
		edges := append(groupEdges("n1"), knowledgev1.Edge{FromId: "n1", ToId: "n2", Type: "contains"})
		payload := renderPayload(t, edges)

		rows, ok := payload["edge_groups"].([]any)
		require.True(t, ok, "edge_groups must be present")
		require.Len(t, rows, 1)
		assert.Len(t, rows[0].(map[string]any)["candidates"].([]any), 3)

		flat := payload["edges"].([]any)
		require.Len(t, flat, 1, "only the bound edge stays in the flat array")
		assert.Equal(t, "n2", flat[0].(map[string]any)["target"])
	})

	t.Run("dangling_source_group_skipped", func(t *testing.T) {
		// The group's source has no node row, so the arm's existing dangling rule
		// drops it — while the bound edge with a known source still renders, so
		// this leg cannot pass by emitting nothing at all.
		edges := append(groupEdges("ghost"), knowledgev1.Edge{FromId: "n1", ToId: "n2", Type: "contains"})
		payload := renderPayload(t, edges)

		_, hasGroups := payload["edge_groups"]
		assert.False(t, hasGroups, "a group whose source is not enumerated is skipped entirely")
		flat := payload["edges"].([]any)
		require.Len(t, flat, 1)
		assert.Equal(t, "n2", flat[0].(map[string]any)["target"])
	})
}
