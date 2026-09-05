// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// run_recipe_integration_test.go drives RunRecipe end to end over the fake
// caller in harness_test.go.
//
// EVERY RUN HERE IS AN INLINE EXTRACT, because that is the only shape RunRecipe
// admits. The tests that drove a SAVED recipe — loading a node out of the
// transformers graph, reading its target metadata, refusing a collector-owned or
// self-referential target — went with the family that stored it. What survived
// is every test whose subject is a mechanism this package KEPT and which merely
// happened to reach it through the saved path: manifest parsing, body parsing,
// source-vocabulary validation, and the orphan-edge fail-soft. Those are
// re-pointed, not deleted.

// TestRunRecipe_SavedRecipeNameIsRefused pins the retirement itself: a bodyless
// call is a loud error naming the removed family and the surviving parameter,
// never a nil dereference or a silent empty result.
func TestRunRecipe_SavedRecipeNameIsRefused(t *testing.T) {
	caller := fullRecipeCaller()
	opts := Options{SourceManifest: FormatSourceManifest("hohpe-eip", "eip")}

	res, err := RunRecipe(context.Background(), caller, "src-graph", kgtypes.GraphWebRaw, opts)

	require.Error(t, err, "there is no saved recipe to run")
	assert.Nil(t, res, "a refused run returns no result")
	assert.Contains(t, err.Error(), "transformers", "the refusal names the family that was removed")
	assert.Contains(t, err.Error(), "removed", "and says it was removed")
	assert.Contains(t, err.Error(), "recipe_body", "and names the parameter that still works")
	assert.Contains(t, err.Error(), "eip", "and names the recipe the caller asked for")
	assert.Zero(t, caller.calls,
		"the refusal fires before any read: there is no bucket left to look in")
}

// TestRunRecipe_InlineRun_ReturnsRows is the known-positive for every zero and
// every refusal in this file: the same harness, the same caller shape, a body
// that runs.
func TestRunRecipe_InlineRun_ReturnsRows(t *testing.T) {
	caller := fullRecipeCaller()

	res, err := runInline(t, caller, fullRecipeBody)

	require.NoError(t, err)
	require.NotNil(t, res.Extract, "an inline run returns its rows on Extract")
	assert.NotEmpty(t, res.Extract.Rows, "the fixture matches sections, so rows come back")
	assert.Positive(t, caller.calls, "the source graph WAS read")
}

// TestRunRecipe_MalformedManifest pins the manifest-parse seam: a SourceManifest
// with no key=value structure errors out of ParseSourceManifest before any
// source read.
func TestRunRecipe_MalformedManifest(t *testing.T) {
	caller := fullRecipeCaller()
	opts := Options{SourceManifest: "garbage-no-equals", Body: fullRecipeBody, Extract: true}

	_, err := RunRecipe(context.Background(), caller, "src-graph", kgtypes.GraphWebRaw, opts)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest")
	assert.Zero(t, caller.calls, "a malformed manifest is refused before the source read")
}

// TestRunRecipe_BodyParseError pins the parse-error seam: an unparseable body
// makes RunRecipe surface the parse error and read nothing.
//
// The body is distinct from every other fixture in the package, which is what
// keeps a previously-cached AST from masking the failure — parseInlineWithCache
// keys on a hash of the body's CONTENT, so a body no other test uses cannot
// collide with a cached entry.
func TestRunRecipe_BodyParseError(t *testing.T) {
	caller := fullRecipeCaller()

	// `select` with no node type is rejected by parseSelect.
	_, err := runInline(t, caller, "select")

	require.Error(t, err, "an unparseable body must surface a parse error")
	assert.Contains(t, err.Error(), "parse")
	assert.Zero(t, caller.calls, "a parse error is reached before the source read")
}

// TestRunRecipe_EmptySourceGraph_Refused pins the empty-source path.
//
// THIS IS THE MEASURED SILENT CASE, not an edge of it. rows=0/0 with no error
// was indistinguishable from a recipe that matched nothing on a populated graph;
// the message now says the graph carries no node types at all, which is the one
// fact that tells an operator to look at the collect rather than at the recipe.
func TestRunRecipe_EmptySourceGraph_Refused(t *testing.T) {
	caller := fullRecipeCaller()
	// Drop every source node so loadSourceView materializes an empty graph.
	caller.nodesByGraph[string(kgtypes.GraphWebRaw)] = nil
	caller.edgesByGraph[string(kgtypes.GraphWebRaw)] = nil

	_, err := runInline(t, caller, fullRecipeBody)

	require.Error(t, err, "an empty source graph carries none of the vocabulary the recipe names")
	assert.Contains(t, err.Error(), `"section"`, "the offending select type is named")
	assert.Contains(t, err.Error(), "(none)", "and the observed vocabulary, which is empty")
	assert.Contains(t, err.Error(), "web/src-graph", "and the graph it was checked against")
}

// TestRunRecipe_UnknownEdgeAndField_Refuse proves the source-vocabulary contract
// end to end. Both halves used to degrade to an empty result with no error; both
// are now refused before the walk, naming the offending value and the vocabulary
// the source graph actually carries.
//
// The third subtest is the fail-soft that SURVIVES, and it survives for a
// reason: an edge whose target node is missing from the view is a property of a
// MALFORMED SOURCE GRAPH, not of the recipe, and nothing the author writes can
// repair it.
func TestRunRecipe_UnknownEdgeAndField_Refuse(t *testing.T) {
	t.Run("NonExistentEdge", func(t *testing.T) {
		caller := fullRecipeCaller()

		_, err := runInline(t, caller, `select section
traverse no_such_edge out as $t
emit pattern {
    name := $t
}`)

		require.Error(t, err, "an edge type the graph does not carry is refused, not degraded")
		assert.Contains(t, err.Error(), "no_such_edge", "the offending edge type is named")
		assert.Contains(t, err.Error(), `"relates-to"`, "and the observed edge types")
	})

	t.Run("AbsentField", func(t *testing.T) {
		caller := fullRecipeCaller()

		// The read is an EMIT field, which is why the validator censuses
		// expression field paths and not only where-tree `of` values.
		_, err := runInline(t, caller, `select section
emit pattern {
    name := node.metadata.absent
}`)

		require.Error(t, err, "a metadata key the graph does not carry is refused, not read as empty")
		assert.Contains(t, err.Error(), "absent", "the offending key is named")
		assert.Contains(t, err.Error(), "(none)", "and the observed key vocabulary, which this fixture leaves empty")
	})

	t.Run("orphan_edge_target_is_still_skipped", func(t *testing.T) {
		caller := fullRecipeCaller()
		// A SECOND relates-to edge pointing at a node the graph does not carry.
		// The edge type itself is legal, so the validator admits the recipe and
		// the walk reaches the orphan.
		caller.edgesByGraph[string(kgtypes.GraphWebRaw)] = append(
			caller.edgesByGraph[string(kgtypes.GraphWebRaw)],
			&knowledgev1.Edge{FromId: "s1", ToId: "ghost", Type: "relates-to"},
		)

		res, err := runInline(t, caller, `select section
traverse relates-to out as $t
emit pattern {
    name := $t.symbol_name
}`)

		require.NoError(t, err, "a malformed source graph is not the recipe's fault")
		require.NotNil(t, res.Extract)
		assert.Len(t, res.Extract.Rows, 1,
			"the real target emits and the orphan is skipped rather than erroring")
	})
}
