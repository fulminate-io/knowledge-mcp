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

// TestCompileMutate_TransformersRecipeBucketRouting is the FAILS-WHEN-ABSENT
// regression guard: a transformers recipe mutation must route to the
// canonical "recipes" bucket (Target.Name == "recipes"), NEVER to a per-name
// transformers instance derived from the recipe name. The recipe name must keep
// flowing to the node SymbolName (createPayload maps a.Name → NodeBody.Name) so
// loadRecipeByName can find it. Without the pin, mutationRequest threaded a.Name
// as the Target instance, scattering each new recipe into its own instance and
// making it unrunnable. The pre-existing transformers tests asserted only
// Target.GetGraph() — that gap is why this regressed.
func TestCompileMutate_TransformersRecipeBucketRouting(t *testing.T) {
	t.Run("create with arbitrary recipe name → Target.Name=='recipes', SymbolName keeps the recipe name", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"create","graph":"transformers","type":"recipe","name":"postgres-docs-to-postgres-best-practices","content":"select x emit y","summary":"s"}`))
		require.True(t, ok, "transformers recipe create must compile")
		assert.Equal(t, "transformers", req.GetTarget().GetGraph())
		assert.Equal(t, "recipes", req.GetTarget().GetName(),
			"the Target instance MUST be the 'recipes' bucket, NOT the recipe name")

		bodies := req.GetMutation().GetNodeBodies()
		require.Len(t, bodies, 1, "one recipe node body")
		assert.Equal(t, "postgres-docs-to-postgres-best-practices", bodies[0].GetName(),
			"the recipe name MUST still flow to the node SymbolName so the loader finds it")
	})

	t.Run("by-id update → Target.Name=='recipes' (explicit pin, was empty default)", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"update","graph":"transformers","id":"abc","status":"x"}`))
		require.True(t, ok, "transformers by-id update must compile")
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE, req.GetMutation().GetKind())
		assert.Equal(t, "recipes", req.GetTarget().GetName(),
			"by-id update routes to the 'recipes' bucket")
	})

	t.Run("delete-by-ids → Target.Name=='recipes'", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"delete","graph":"transformers","ids":["abc"]}`))
		require.True(t, ok, "transformers delete-by-ids must compile")
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, req.GetMutation().GetKind())
		assert.Equal(t, "recipes", req.GetTarget().GetName(),
			"delete routes to the 'recipes' bucket")
	})

	// The two batch arms (update_batch/bulk_metadata) build their Target inline
	// (compile_mutate_batch.go), bypassing mutationRequest — these subtests lock
	// the inline-arm pin so a future transformers caller of either arm cannot
	// scatter the write into a per-name instance off an arbitrary name.
	t.Run("update_batch with arbitrary name → Target.Name=='recipes' (inline-arm pin)", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"update_batch","graph":"transformers","name":"some-recipe-name","items":[{"id":"abc","status":"x"}]}`))
		require.True(t, ok, "transformers update_batch must compile")
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS, req.GetMutation().GetKind())
		assert.Equal(t, "recipes", req.GetTarget().GetName(),
			"update_batch routes to the 'recipes' bucket, NOT the supplied name")
	})

	t.Run("bulk_update_metadata with arbitrary name → Target.Name=='recipes' (inline-arm pin)", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"bulk_update_metadata","graph":"transformers","name":"some-recipe-name","updates":[{"id":"abc","metadata":{"k":"v"}}]}`))
		require.True(t, ok, "transformers bulk_update_metadata must compile")
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS, req.GetMutation().GetKind())
		assert.Equal(t, "recipes", req.GetTarget().GetName(),
			"bulk_update_metadata routes to the 'recipes' bucket, NOT the supplied name")
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

// TestCompileDelete_SingularIDAlias covers the spelling trap that made a working
// capability read as a missing one.
//
// Every other single-node mutate op names its target with `id`, so an author
// deleting one node reaches for `id`. Before the alias, that payload carried no
// `ids`, fell through to the prune-by-age branch, failed its selection and denied
// the compile — and the resulting message said `mutate` was "not a recognized
// engine-reducible shape". That names neither the field nor the fix, so it reads
// as "delete is unsupported on this graph" rather than "say ids".
//
// THE LAST ARM IS THE CONTROL that keeps the alias honest: `id` and `ids` must
// select the SAME target set, not route to different arms, so supplying both is
// additive rather than a conflict to adjudicate.
func TestCompileDelete_SingularIDAlias(t *testing.T) {
	t.Run("singular id compiles to the by-ids selection", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"delete","graph":"checks","id":"chk-1"}`))
		require.True(t, ok, "a singular-id delete must compile rather than deny into an opaque message")
		assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_DELETE, req.GetMutation().GetKind())
		assert.Equal(t, []string{"chk-1"}, req.GetMutation().GetSelection().GetIds())
		assert.Equal(t, "checks", req.GetTarget().GetGraph())
	})

	t.Run("plural ids still compiles identically", func(t *testing.T) {
		// The known-positive that predates the alias: without it, a green first
		// arm could not be distinguished from a compiler that now accepts
		// anything.
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"delete","graph":"checks","ids":["chk-1"]}`))
		require.True(t, ok)
		assert.Equal(t, []string{"chk-1"}, req.GetMutation().GetSelection().GetIds())
	})

	t.Run("both spellings union rather than conflict", func(t *testing.T) {
		req, ok := compileMutate(json.RawMessage(
			`{"operation":"delete","graph":"checks","ids":["a"],"id":"b"}`))
		require.True(t, ok)
		assert.ElementsMatch(t, []string{"a", "b"}, req.GetMutation().GetSelection().GetIds(),
			"the alias is a second spelling of the same axis, so the two sets union")
	})

	t.Run("a singular id does not bypass the dry-run deny", func(t *testing.T) {
		// The destructive-safety leg: the alias must not open a path that skips
		// the guard every other delete shape passes through.
		_, ok := compileMutate(json.RawMessage(
			`{"operation":"delete","graph":"checks","id":"chk-1","dry_run":true}`))
		assert.False(t, ok, "a dry-run must never lower to a real DELETE, alias or not")
	})
}
