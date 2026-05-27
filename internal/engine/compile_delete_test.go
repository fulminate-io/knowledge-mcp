// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestCompileMutate_PracticeTransformers asserts the Phase-1 guard narrowing:
// a practice/transformers create/update/delete with NO link_graph compiles to a
// Target-routed MutationPlan (Target.Graph == the requested graph,
// Target.Language == a.Language); a link_graph!="" op still falls through to
// legacy (T-GTB5). This is the positive complement to the deny cases moved out
// of TestCompileMutate_DenyCases / TestDefaultDeny_SpecializedShapes.
func TestCompileMutate_PracticeTransformers(t *testing.T) {
	t.Run("practice create → CREATE, Target practice+language", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"create","graph":"practice","language":"go","type":"finding","name":"P","summary":"s"}`))
		require.True(t, ok, "practice create (no link_graph) must compile")
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, req.GetMutation().GetKind())
		assert.Equal(t, "practice", req.GetTarget().GetGraph())
		assert.Equal(t, "go", req.GetTarget().GetLanguage())
	})

	t.Run("practice update → UPDATE, Target practice", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"update","graph":"practice","language":"go","id":"x","status":"y"}`))
		require.True(t, ok, "practice by-id update must compile")
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, req.GetMutation().GetKind())
		assert.Equal(t, "practice", req.GetTarget().GetGraph())
		assert.Equal(t, "go", req.GetTarget().GetLanguage())
	})

	t.Run("practice delete-by-ids → DELETE, Target practice", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"delete","graph":"practice","language":"go","ids":["a","b"]}`))
		require.True(t, ok, "practice delete-by-ids must compile")
		m := req.GetMutation()
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, m.GetKind())
		assert.Equal(t, []string{"a", "b"}, m.GetSelection().GetIds())
		assert.Equal(t, "practice", req.GetTarget().GetGraph())
	})

	t.Run("transformers create → CREATE, Target transformers", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"create","graph":"transformers","type":"recipe","name":"r","summary":"s"}`))
		require.True(t, ok, "transformers create (no link_graph) must compile")
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, req.GetMutation().GetKind())
		assert.Equal(t, "transformers", req.GetTarget().GetGraph())
	})

	t.Run("link_graph linkage → ok=false (legacy/T-GTB5)", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"link","link_graph":"linkage","from":"x","to":"y","relationship":"r"}`))
		assert.False(t, ok, "the cross-graph link_graph case stays denied")
		assert.Nil(t, req)
	})
}

// TestCompileDelete_PruneByAge asserts the standalone `delete` tool + the id-less
// mutate(operation:delete) BOTH lower a prune-by-age delete onto the same
// MUTATION_KIND_DELETE plan: Selection.NodeType=session + a created_at OP_LT
// FieldPredicate whose Value is the duration-parsed cutoff (~Now-7d, RFC3339).
// The server-side semantics (created_at OP_LT selects older-than-cutoff) are
// proven by engine_fieldpredicate_test.go:47-58; this asserts the COMPILE
// produces that exact plan shape.
func TestCompileDelete_PruneByAge(t *testing.T) {
	assertPrunePlan := func(t *testing.T, req *knowledgev1.ExecuteRequest, ok bool) {
		t.Helper()
		require.True(t, ok, "prune-by-age delete must compile")
		m := req.GetMutation()
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, m.GetKind())
		sel := m.GetSelection()
		assert.Equal(t, "session", sel.GetNodeType(), "NodeType = session (the prune alias)")
		assert.Empty(t, sel.GetIds(), "prune-by-age carries NO by-id selector")
		preds := sel.GetFieldPredicates()
		require.Len(t, preds, 1, "exactly one created_at FieldPredicate")
		assert.Equal(t, "created_at", preds[0].GetField())
		assert.Equal(t, knowledgev1.MetadataPredicate_OP_LT, preds[0].GetOp())
		// The cutoff Value parses as RFC3339 and sits ~7d in the past.
		cutoff, err := time.Parse(time.RFC3339, preds[0].GetValue())
		require.NoError(t, err, "FieldPredicate.Value must be RFC3339")
		want := time.Now().Add(-7 * 24 * time.Hour)
		assert.WithinDuration(t, want, cutoff, time.Minute, "cutoff ≈ Now-7d")
	}

	t.Run("standalone delete tool", func(t *testing.T) {
		req, ok := Compile("delete", json.RawMessage(`{"older_than":"7d","type":"session"}`))
		assertPrunePlan(t, req, ok)
	})

	t.Run("id-less mutate(operation:delete) lowers identically", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(`{"operation":"delete","older_than":"7d","type":"session"}`))
		assertPrunePlan(t, req, ok)
	})
}

// TestCompileDelete_ByIDs asserts the by-ids delete shape is unchanged: a
// {ids:[...]} delete → Selection.Ids with NO FieldPredicate.
func TestCompileDelete_ByIDs(t *testing.T) {
	req, ok := Compile("delete", json.RawMessage(`{"ids":["a","b"]}`))
	require.True(t, ok, "by-ids delete must compile")
	m := req.GetMutation()
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, m.GetKind())
	assert.Equal(t, []string{"a", "b"}, m.GetSelection().GetIds())
	assert.Empty(t, m.GetSelection().GetFieldPredicates(), "by-ids delete carries NO FieldPredicate")
}

// TestCompileDelete_SessionID asserts that when session_id is set on a
// prune-by-age delete, a {session_id, OP_EQ} MetadataPredicate is added so only
// that session's nodes match (mirroring handlePruneHistory's session_id metadata
// == SessionID guard).
func TestCompileDelete_SessionID(t *testing.T) {
	req, ok := Compile("delete", json.RawMessage(`{"older_than":"7d","type":"session","session_id":"sess-1"}`))
	require.True(t, ok)
	preds := req.GetMutation().GetSelection().GetMetadataPredicates()
	require.Len(t, preds, 1, "session_id adds exactly one metadata predicate")
	assert.Equal(t, "session_id", preds[0].GetKey())
	assert.Equal(t, knowledgev1.MetadataPredicate_OP_EQ, preds[0].GetOp())
	assert.Equal(t, "sess-1", preds[0].GetValue())
}

// TestCompileDelete_DryRunFallsThrough asserts a dry_run:true prune-by-age delete
// returns ok=false (the legacy count-only path is preserved — the engine has no
// dry-run mode).
func TestCompileDelete_DryRunFallsThrough(t *testing.T) {
	req, ok := Compile("delete", json.RawMessage(`{"older_than":"7d","type":"session","dry_run":true}`))
	assert.False(t, ok, "dry_run:true must fall through to the legacy count path")
	assert.Nil(t, req)
}

// TestCompileDelete_UnknownTypeFallsThrough asserts an unknown prune type
// (not in pruneTypeAliases) returns ok=false so the legacy handler surfaces the
// error.
func TestCompileDelete_UnknownTypeFallsThrough(t *testing.T) {
	req, ok := Compile("delete", json.RawMessage(`{"older_than":"7d","type":"thought"}`))
	assert.False(t, ok, "unknown prune type must fall through to legacy")
	assert.Nil(t, req)
}
