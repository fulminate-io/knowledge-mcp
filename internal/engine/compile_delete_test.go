// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestCompileMutate_PracticeTransformers asserts the Phase-1 guard narrowing:
// a practice/transformers create/update/delete with NO link_graph compiles to a
// Target-routed MutationPlan (Target.Graph == the requested graph,
// Target.Language == a.Language); a link_graph!="" op still falls through to
// legacy. This is the positive complement to the deny cases moved out
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

	t.Run("link_graph linkage → ok=false (legacy)", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"link","link_graph":"linkage","from":"x","to":"y","relationship":"r"}`))
		assert.False(t, ok, "the cross-graph link_graph case stays denied")
		assert.Nil(t, req)
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

// TestCompileDelete_DryRunNeverCompilesToDelete asserts a dry_run:true by-ids
// delete NEVER lowers to a MUTATION_KIND_DELETE — Compile returns ok=false.
// This is the compile-side half of the data-loss footgun fix: the dispatcher
// claims the dry-run upstream (dispatchDeletePreview) and renders a read-only
// preview; a dry-run that somehow reached the compiler denies rather than
// deletes (safe direction).
func TestCompileDelete_DryRunNeverCompilesToDelete(t *testing.T) {
	t.Run("by-ids dry_run → ok=false (the footgun: was unconditionally DELETE)", func(t *testing.T) {
		req, ok := Compile("delete", json.RawMessage(`{"ids":["a","b"],"dry_run":true}`))
		assert.False(t, ok, "by-ids dry_run:true must NOT compile to a DELETE (the data-loss footgun)")
		assert.Nil(t, req)
	})
}

// TestCompileDelete_UnknownTypeFallsThrough asserts a prune type that is not in
// pruneTypeAliases (now empty — no type is retention-eligible) returns ok=false
// so the legacy handler surfaces the error.
func TestCompileDelete_UnknownTypeFallsThrough(t *testing.T) {
	req, ok := Compile("delete", json.RawMessage(`{"older_than":"7d","type":"thought"}`))
	assert.False(t, ok, "unknown prune type must fall through to legacy")
	assert.Nil(t, req)
}

// TestCompileDelete_HardFlag (FAILS-WHEN-ABSENT for the soft-default seam) pins
// the `hard` opt-in semantics at compile: deletes are SOFT by default
// (plan.HardDelete=false when the flag is absent or false), hard:true sets the
// plan flag, the string form "true" parses (stale MCP schemas coerce unknown
// params to strings — the force_full lesson), and a MALFORMED value DENIES the
// compile rather than guessing in either direction on a destructive op.
func TestCompileDelete_HardFlag(t *testing.T) {
	t.Run("absent → soft (HardDelete=false)", func(t *testing.T) {
		req, ok := Compile("delete", json.RawMessage(`{"ids":["a"]}`))
		require.True(t, ok)
		assert.False(t, req.GetMutation().GetHardDelete(), "default delete must be SOFT")
	})

	t.Run("hard:false → soft", func(t *testing.T) {
		req, ok := Compile("delete", json.RawMessage(`{"ids":["a"],"hard":false}`))
		require.True(t, ok)
		assert.False(t, req.GetMutation().GetHardDelete())
	})

	t.Run("hard:true → HardDelete=true", func(t *testing.T) {
		req, ok := Compile("delete", json.RawMessage(`{"ids":["a"],"hard":true}`))
		require.True(t, ok)
		assert.True(t, req.GetMutation().GetHardDelete(), "explicit hard:true opts into permanent removal")
	})

	t.Run(`string "true" → HardDelete=true (lenient coercion)`, func(t *testing.T) {
		req, ok := Compile("delete", json.RawMessage(`{"ids":["a"],"hard":"true"}`))
		require.True(t, ok)
		assert.True(t, req.GetMutation().GetHardDelete())
	})

	t.Run(`string "false" → soft`, func(t *testing.T) {
		req, ok := Compile("delete", json.RawMessage(`{"ids":["a"],"hard":"false"}`))
		require.True(t, ok)
		assert.False(t, req.GetMutation().GetHardDelete())
	})

	t.Run("malformed → DENY (never guess on a destructive flag)", func(t *testing.T) {
		for _, raw := range []string{
			`{"ids":["a"],"hard":"yes"}`,
			`{"ids":["a"],"hard":1}`,
			`{"ids":["a"],"hard":{"v":true}}`,
		} {
			req, ok := Compile("delete", json.RawMessage(raw))
			assert.False(t, ok, "malformed hard flag must deny the compile: %s", raw)
			assert.Nil(t, req)
		}
	})
}
