// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
)

// TestDispatch_DeleteByIDsDryRun_PreviewsNeverDeletes is the data-loss footgun
// regression guard. Pre-fix, delete(ids, dry_run:true)
// compiled UNCONDITIONALLY to a MUTATION_KIND_DELETE and really deleted the
// nodes. This drives the standalone delete tool END-TO-END through Dispatch and
// asserts the dry-run:
//
//   - issues a READ (the single Execute carries a QueryPlan, NOT a MutationPlan),
//   - so the engine NEVER sees a delete — nothing is deleted,
//   - renders a "would delete" preview (NOT "Deleted N node(s)").
func TestDispatch_DeleteByIDsDryRun_PreviewsNeverDeletes(t *testing.T) {
	s := &seqExec{responses: []*knowledgev1.ExecuteResponse{
		enginetest.ResponseWithNodes(
			&knowledgev1.Node{Id: "a", SymbolName: "Alpha", Type: "finding"},
			&knowledgev1.Node{Id: "b", SymbolName: "Beta", Type: "decision"},
		),
	}}

	out, err := Dispatch(context.Background(),
		s.fn(),
		nil,
		"delete", json.RawMessage(`{"ids":["a","b"],"dry_run":true}`))
	require.NoError(t, err)
	require.False(t, out.IsError, "dry-run preview renders cleanly: %s", out.Content[0].Text)

	// EXACTLY one Execute, and it is a READ — a QueryPlan, never a MutationPlan.
	// A MutationPlan here would mean the dry-run deleted (the footgun).
	require.Equal(t, 1, s.calls, "dry-run issues EXACTLY one Execute (the read)")
	require.Len(t, s.reqs, 1)
	assert.NotNil(t, s.reqs[0].GetQuery(), "dry-run Execute MUST be a QueryPlan (a read)")
	assert.Nil(t, s.reqs[0].GetMutation(), "dry-run Execute MUST NOT be a MutationPlan — that is the delete footgun")
	assert.ElementsMatch(t, []string{"a", "b"}, s.reqs[0].GetQuery().GetIds(),
		"the read targets exactly the ids that would be deleted")

	// The render is a "would delete" preview, NOT the "Deleted N" lie.
	text := out.Content[0].Text
	assert.Contains(t, strings.ToLower(text), "would delete", "preview says what WOULD be deleted")
	assert.Contains(t, text, "Alpha", "preview lists the would-be-deleted node names")
	assert.Contains(t, text, "Beta")
	assert.NotContains(t, text, "Deleted 2 node(s)", "dry-run must NOT claim a completed deletion")
}

// TestDispatch_DeleteByIDsRealDelete_StillDeletes is the complement guard: a
// NON-dry-run by-ids delete still flows to the real DELETE — the preview seam
// does NOT intercept it. exec runs once with a MutationPlan and the render is the
// "Deleted N node(s)" affected-count line.
func TestDispatch_DeleteByIDsRealDelete_StillDeletes(t *testing.T) {
	s := &seqExec{responses: []*knowledgev1.ExecuteResponse{
		{AffectedCount: 2},
	}}

	out, err := Dispatch(context.Background(),
		s.fn(),
		nil,
		"delete", json.RawMessage(`{"ids":["a","b"]}`))
	require.NoError(t, err)
	require.False(t, out.IsError)

	require.Equal(t, 1, s.calls, "real delete issues EXACTLY one Execute")
	require.Len(t, s.reqs, 1)
	assert.NotNil(t, s.reqs[0].GetMutation(), "a real delete Execute IS a MutationPlan")
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, s.reqs[0].GetMutation().GetKind())
	assert.ElementsMatch(t, []string{"a", "b"}, s.reqs[0].GetMutation().GetSelection().GetIds())
	assert.Equal(t, "Deleted 2 node(s)", out.Content[0].Text)
}

// TestDispatch_DeleteDryRunJSON_ReportsWouldDelete asserts the JSON-format dry-run
// reports the read-only would_delete count + nodes, NOT an affected-count delete.
func TestDispatch_DeleteDryRunJSON_ReportsWouldDelete(t *testing.T) {
	s := &seqExec{responses: []*knowledgev1.ExecuteResponse{
		enginetest.ResponseWithNodes(
			&knowledgev1.Node{Id: "a", SymbolName: "Alpha", Type: "finding"},
		),
	}}

	out, err := Dispatch(context.Background(),
		s.fn(),
		nil,
		"delete", json.RawMessage(`{"ids":["a"],"dry_run":true,"format":"json"}`))
	require.NoError(t, err)
	require.False(t, out.IsError)

	var decoded struct {
		DryRun      bool `json:"dry_run"`
		WouldDelete int  `json:"would_delete"`
		Nodes       []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &decoded))
	assert.True(t, decoded.DryRun)
	assert.Equal(t, 1, decoded.WouldDelete)
	require.Len(t, decoded.Nodes, 1)
	assert.Equal(t, "a", decoded.Nodes[0].ID)
	assert.Equal(t, "Alpha", decoded.Nodes[0].Name)

	// One READ Execute, never a Mutation.
	require.Len(t, s.reqs, 1)
	assert.NotNil(t, s.reqs[0].GetQuery())
	assert.Nil(t, s.reqs[0].GetMutation())
}
