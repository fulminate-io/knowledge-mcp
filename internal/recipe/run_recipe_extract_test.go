// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// run_recipe_extract_test.go holds every extract-mode run test. It is separate
// from the integration suite because all six together would push that file past
// this package's file-length ceiling; the helpers stay reachable because both
// files are package recipe.

// extractBody is the inline body the tests below run: one row per section,
// carrying the section's own name.
const extractBody = `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
} as $p`

// extractSourceCaller builds a caller serving n sections in the web graph and
// no recipe bucket at all — an inline run must never read one.
func extractSourceCaller(n int) *routingCaller {
	nodes := make([]*knowledgev1.Node, 0, n)
	for i := range n {
		nodes = append(nodes, &knowledgev1.Node{
			Id: "s" + strconv.Itoa(i), Type: "section",
			SymbolName: "Section " + strconv.Itoa(i),
		})
	}
	return &routingCaller{nodesByGraph: map[string][]*knowledgev1.Node{"web": nodes}}
}

func extractOpts(body string) Options {
	return Options{
		SourceManifest: FormatSourceManifest("doc-slug", "inline"),
		Extract:        true,
		Body:           body,
	}
}

// TestRunRecipe_ExtractRows asserts the captured rows carry the emitted type,
// the source anchor and the evaluated fields.
func TestRunRecipe_ExtractRows(t *testing.T) {
	caller := extractSourceCaller(3)
	res, err := RunRecipe(context.Background(), caller, "doc", kgtypes.GraphWebRaw, extractOpts(extractBody))
	require.NoError(t, err)
	require.NotNil(t, res.Extract, "extract mode must populate Extract")

	require.Len(t, res.Extract.Rows, 3)
	assert.Equal(t, 3, res.Extract.RowsMatched)
	assert.Equal(t, 3, res.Extract.RowsReturned)
	assert.False(t, res.Extract.Truncated, "three rows under the default cap is not truncated")
	assert.Empty(t, res.Extract.TruncatedBy)

	row := res.Extract.Rows[0]
	assert.Equal(t, "pattern", row.Type, "the row names the EMITTED type")
	assert.Equal(t, "s0", row.SourceNodeID, "the row names the source node the lineage edge would anchor to")
	assert.Equal(t, "Section 0", row.Fields["name"], "the row carries the evaluated emit fields")

	// The byte-cap fields are renderer-populated, so a Result that never went
	// through a renderer reports them as explicitly zero rather than computed.
	assert.Zero(t, res.Extract.BytesReturned)
}

// TestRunRecipe_ExtractRowCapTruncates is the ONLY test that goes red if the cap
// is never applied — the rows test above passes whether or not a cap exists. The
// fixture therefore supplies MORE rows than the cap it sets.
func TestRunRecipe_ExtractRowCapTruncates(t *testing.T) {
	const sourceRows, cap = 7, 3
	caller := extractSourceCaller(sourceRows)
	opts := extractOpts(extractBody)
	opts.MaxRows = cap

	res, err := RunRecipe(context.Background(), caller, "doc", kgtypes.GraphWebRaw, opts)
	require.NoError(t, err)
	require.NotNil(t, res.Extract)

	assert.Len(t, res.Extract.Rows, cap)
	assert.Equal(t, cap, res.Extract.RowsReturned)
	// The whole point of the disclosure: matched counts the FULL population, so
	// a caller reads "3 of 7" rather than a silently short list.
	assert.Equal(t, sourceRows, res.Extract.RowsMatched)
	assert.True(t, res.Extract.Truncated)
	assert.Equal(t, "max_rows", res.Extract.TruncatedBy)

	// Emit semantics are unchanged by extract: every matched row still emitted.
	assert.Equal(t, sourceRows, res.Stats.NodesEmitted)
	assert.Len(t, res.Nodes, sourceRows)
}

// TestRunRecipe_ExtractInline_NoWrite proves the run neither writes nor deletes,
// and — because the fake serves no recipe bucket — that the inline preamble
// supplies its own recipe key and source type.
//
// THE MUTATION ASSERTION IS THE WHOLE WRITE CHECK NOW. The sink is gone with the
// write path, so the only channel that could still reach a graph is a mutation
// on the caller, and that is what the fake records.
func TestRunRecipe_ExtractInline_NoWrite(t *testing.T) {
	caller := extractSourceCaller(2)
	res, err := RunRecipe(context.Background(), caller, "doc", kgtypes.GraphWebRaw, extractOpts(extractBody))
	require.NoError(t, err)
	require.NotNil(t, res.Extract)

	assert.Empty(t, caller.mutations, "a recipe run must issue no mutation, delete included")
	assert.NotEmpty(t, res.Extract.Rows, "control: the run really did produce rows")
}

// TestRunRecipe_ExtractInline_CacheByContent is the direct regression for the
// shared AST cache. Two DIFFERENT bodies run in ONE process must produce
// different rows; under a synthetic constant key the second would execute the
// first's rules. Every other inline test is an error path, a zero-write
// assertion, or a single body — each would pass with a colliding key.
func TestRunRecipe_ExtractInline_CacheByContent(t *testing.T) {
	bodyA := extractBody
	bodyB := `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
    marker_b := "only-in-b"
} as $p`

	resA, err := RunRecipe(context.Background(), extractSourceCaller(1), "doc", kgtypes.GraphWebRaw, extractOpts(bodyA))
	require.NoError(t, err)
	resB, err := RunRecipe(context.Background(), extractSourceCaller(1), "doc", kgtypes.GraphWebRaw, extractOpts(bodyB))
	require.NoError(t, err)

	require.Len(t, resA.Extract.Rows, 1)
	require.Len(t, resB.Extract.Rows, 1)
	assert.NotContains(t, resA.Extract.Rows[0].Fields, "marker_b",
		"body A does not declare this field")
	assert.Equal(t, "only-in-b", resB.Extract.Rows[0].Fields["marker_b"],
		"body B executed body A's rules — the AST cache key is not content-derived")
}

// TestRunRecipe_ExtractInline_NeedsAMode asserts an inline body asking for
// NEITHER mode is refused, and that the error names BOTH modes that do work — so
// the caller learns what to do instead rather than only what it did wrong.
//
// THE SECOND MODE IS WHY THIS TEST CHANGED. The refusal used to say a run "writes
// nothing", which was the mechanical truth while extract was the only mode; a
// landing writes, so that sentence would now be a false reason attached to a true
// refusal. The NotContains leg on "freeze" is the older ratchet and stays: the
// refusal must not prescribe the retired save-the-body-to-freeze-it workflow.
//
// THE LANDING LEG IS THE CONTROL that keeps the refusal from being unconditional:
// the same body with a LandTarget and no extract must be ADMITTED.
func TestRunRecipe_ExtractInline_NeedsAMode(t *testing.T) {
	caller := extractSourceCaller(2)
	opts := extractOpts(extractBody)
	opts.Extract = false

	_, err := RunRecipe(context.Background(), caller, "doc", kgtypes.GraphWebRaw, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extract:true",
		"the refusal names the parameter that reads the rows back")
	assert.Contains(t, err.Error(), "land:true",
		"and the parameter that writes them, so a caller who wanted the write is not sent to the reader")
	assert.NotContains(t, err.Error(), "writes nothing",
		"the reason must not claim a run writes nothing: a landing run writes")
	assert.NotContains(t, err.Error(), "freeze",
		"the refusal must not prescribe the retired freeze-by-saving workflow")
	assert.Empty(t, caller.mutations)

	// THE CONTROL: extract unset but a landing target supplied is a legal run, so
	// the refusal above is the ABSENCE OF BOTH modes rather than the absence of
	// extract.
	landing := extractOpts(extractBody)
	landing.Extract = false
	landing.LandTarget = TargetSpec{GraphType: kgtypes.GraphPractice, Name: "default"}
	res, lerr := RunRecipe(context.Background(), extractSourceCaller(2), "doc", kgtypes.GraphWebRaw, landing)
	require.NoError(t, lerr, "a landing run with no extract must be admitted")
	assert.NotEmpty(t, res.Nodes, "and it must emit the nodes the landing will write")
}

// TestRunRecipe_ExtractInline_NeedsSourceType asserts the other inline
// precondition: without a source graph type there is no document to read, and
// guessing one would read the wrong graph.
func TestRunRecipe_ExtractInline_NeedsSourceType(t *testing.T) {
	_, err := RunRecipe(context.Background(), extractSourceCaller(2), "doc", "", extractOpts(extractBody))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type")
}
