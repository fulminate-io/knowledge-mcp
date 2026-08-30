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
	sink := &captureSink{}
	res, err := RunRecipe(context.Background(), caller, sink, "doc", kgtypes.GraphWebRaw, extractOpts(extractBody))
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

	res, err := RunRecipe(context.Background(), caller, &captureSink{}, "doc", kgtypes.GraphWebRaw, opts)
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

// TestRunRecipe_ExtractInline_NoWrite proves the inline path neither writes nor
// deletes, and — because the fake serves no recipe bucket — that the inline
// preamble supplies its own recipe key and source type.
func TestRunRecipe_ExtractInline_NoWrite(t *testing.T) {
	caller := extractSourceCaller(2)
	sink := &captureSink{}
	res, err := RunRecipe(context.Background(), caller, sink, "doc", kgtypes.GraphWebRaw, extractOpts(extractBody))
	require.NoError(t, err)
	require.NotNil(t, res.Extract)

	assert.Zero(t, sink.calls, "extract must not ship a result through the sink")
	assert.Empty(t, caller.mutations, "extract must issue no mutation, delete included")
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

	resA, err := RunRecipe(context.Background(), extractSourceCaller(1), &captureSink{},
		"doc", kgtypes.GraphWebRaw, extractOpts(bodyA))
	require.NoError(t, err)
	resB, err := RunRecipe(context.Background(), extractSourceCaller(1), &captureSink{},
		"doc", kgtypes.GraphWebRaw, extractOpts(bodyB))
	require.NoError(t, err)

	require.Len(t, resA.Extract.Rows, 1)
	require.Len(t, resB.Extract.Rows, 1)
	assert.NotContains(t, resA.Extract.Rows[0].Fields, "marker_b",
		"body A does not declare this field")
	assert.Equal(t, "only-in-b", resB.Extract.Rows[0].Fields["marker_b"],
		"body B executed body A's rules — the AST cache key is not content-derived")
}

// TestRunRecipe_ExtractRefusesForce asserts the refusal, and that no delete was
// issued on the refused path.
func TestRunRecipe_ExtractRefusesForce(t *testing.T) {
	caller := extractSourceCaller(2)
	opts := extractOpts(extractBody)
	opts.Force = true

	_, err := RunRecipe(context.Background(), caller, &captureSink{}, "doc", kgtypes.GraphWebRaw, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "force")
	assert.Contains(t, err.Error(), "extract")
	assert.Empty(t, caller.mutations, "the refusal must precede any delete")
}

// TestRunRecipe_ExtractInline_NeedsExtract asserts an inline body without
// extract mode is refused, and that the error names the freeze path so the
// caller learns what to do instead.
func TestRunRecipe_ExtractInline_NeedsExtract(t *testing.T) {
	caller := extractSourceCaller(2)
	opts := extractOpts(extractBody)
	opts.Extract = false

	_, err := RunRecipe(context.Background(), caller, &captureSink{}, "doc", kgtypes.GraphWebRaw, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extract mode")
	assert.Contains(t, err.Error(), "save", "the error must name the save-to-freeze path")
	assert.Empty(t, caller.mutations)
}

// TestRunRecipe_ExtractInline_NeedsSourceType asserts the other inline
// precondition: without a source graph type there is no document to read, and
// guessing one would read the wrong graph.
func TestRunRecipe_ExtractInline_NeedsSourceType(t *testing.T) {
	_, err := RunRecipe(context.Background(), extractSourceCaller(2), &captureSink{},
		"doc", "", extractOpts(extractBody))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type")
}
