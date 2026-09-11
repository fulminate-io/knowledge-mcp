// SPDX-License-Identifier: Apache-2.0

// collect_recipe_unenrolled_type_test.go — a recipe whose emit names a type the
// combined practice graph does not enroll never reaches a landing, never
// previews a row, and leaves nothing behind on either path.
//
// THE TWO MODES ARE ONE ROW, not two tests that happen to agree. A landing and
// an extract run the SAME Interpret and differ only in what the caller does with
// the Result — and in the TARGET they hash ids under: a landing carries the
// caller's practice target, an extract carries a sentinel graph type. An
// emit-type check keyed on that target would pass every landing cell here and
// fail only the extract one, which is why the extract row is not optional.

package tools

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// unenrolledEmitBody is landingBody with ONE change: the emitted type. Every
// other line — the select, the fields, the summary — is the shipped fixture's,
// so a refusal can only be the type.
const unenrolledEmitBody = `select section
emit widget_of_nowhere {
    type := "widget_of_nowhere"
    name := section.symbol_name
    summary := section.symbol_name
}`

// collectParamsFor builds a collect payload over a recipe body in one mode.
func collectParamsFor(t *testing.T, body string, mode map[string]any) kgtools.CallToolParams {
	t.Helper()
	args := map[string]any{
		"type": "web", "id": "hohpe-eip", "transformer": "recipe", "recipe_body": body,
	}
	maps.Copy(args, mode)
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: "collect", Arguments: raw}
}

func TestInterceptCollect_UnenrolledEmitType_LandsNothing(t *testing.T) {
	c := newLandingCaller()
	sink := &recipeCaptureSink{}
	deps := &recipeDeps{sink: sink, gc: c}

	handled, res := InterceptCollect(opCtx(), deps,
		collectParamsFor(t, unenrolledEmitBody, map[string]any{"land": true}))

	require.True(t, handled)
	require.True(t, res.IsError, "a landing over a refused recipe must reach the caller as an error")
	msg := resultText(res)
	assert.Contains(t, msg, "widget_of_nowhere", "the tool call's error names the offending emit's type")
	assert.Contains(t, msg, "combined practice graph", "and what it was checked against")

	// A LANDING IS NEVER PARTIAL. The refusal happens inside Interpret, before the
	// landing is composed at all, so there is no batch to be atomic about — and
	// that is the strongest form of the invariant rather than a weaker one.
	//
	// THIS ROW IS GUARDED TWICE, and it says so rather than implying otherwise:
	// with the recipe validator's emit-type check removed it still passes, because
	// the client write guard refuses the composed batch one layer down. It asserts
	// the OUTCOME requirement 7 names — nothing of a refused run lands — which is
	// exactly the property that must hold however the refusal is reached. The row
	// that observes the validator alone is the extract one below, which no write
	// guard can save.
	assert.Empty(t, c.mutations, "no batch was ever sent")
	assert.Empty(t, c.practiceByID, "and no row of this run is in the practice graph")
	assert.Empty(t, sink.results, "and nothing reached any sink")
}

func TestInterceptCollect_UnenrolledEmitType_ExtractIsRefusedTheSameWay(t *testing.T) {
	c := newLandingCaller()
	sink := &recipeCaptureSink{}
	deps := &recipeDeps{sink: sink, gc: c}

	handled, res := InterceptCollect(opCtx(), deps,
		collectParamsFor(t, unenrolledEmitBody, map[string]any{"extract": true}))

	require.True(t, handled)
	require.True(t, res.IsError,
		"an extract that previewed rows a landing could not write would be a preview of a lie")
	msg := resultText(res)
	assert.Contains(t, msg, "widget_of_nowhere", "the same refusal, named the same way")
	assert.Contains(t, msg, "combined practice graph")
	assert.NotContains(t, msg, "Message Router",
		"and no row is rendered: the refusal is before the walk, so there is nothing to preview")
	assert.Empty(t, c.mutations)
	assert.Empty(t, sink.results)
}

// TestInterceptCollect_UnenrolledEmitType_ControlsInBothModes is the near-miss
// control for both rows above: the SAME payloads with an ENROLLED emit type run
// exactly as they did before — the landing writes its batch, the extract renders
// its rows. Without these, a check that refused every recipe would satisfy both
// refusal rows.
func TestInterceptCollect_UnenrolledEmitType_ControlsInBothModes(t *testing.T) {
	t.Run("the landing still writes", func(t *testing.T) {
		c := newLandingCaller()
		deps := &recipeDeps{sink: &recipeCaptureSink{}, gc: c}
		handled, res := InterceptCollect(opCtx(), deps,
			collectParamsFor(t, landingBody, map[string]any{"land": true}))
		require.True(t, handled)
		require.False(t, res.IsError, "the enrolled-type landing must still land: %s", resultText(res))
		require.Len(t, c.mutations, 1, "and it is ONE create_batch")
	})

	t.Run("the extract still renders its rows", func(t *testing.T) {
		c := newLandingCaller()
		deps := &recipeDeps{sink: &recipeCaptureSink{}, gc: c}
		handled, res := InterceptCollect(opCtx(), deps,
			collectParamsFor(t, landingBody, map[string]any{"extract": true}))
		require.True(t, handled)
		require.False(t, res.IsError, resultText(res))
		assert.Contains(t, resultText(res), "Message Router",
			"the known positive: the extract really does render rows, so the refused run's silence is a decision")
		assert.Empty(t, c.mutations, "and an extract still writes nothing")
	})
}

// TestInterceptCollect_UnenrolledEmitType_TheCheckIsNotKeyedOnTheRunTarget is the
// MUTATION this pair exists to catch, stated as its own row so a reader does not
// have to infer it from the two above.
//
// An extract run's TargetSpec carries the sentinel `extract` graph type, because
// it writes nothing and a real graph key would claim ids in a graph it never
// touches; a landing's carries the practice target. An implementation that asked
// "is this run's target the practice graph" before checking the emit type would
// be green on every landing row in this package and would let an extract preview
// rows no landing could ever write. The assertion is that BOTH modes refuse the
// same body — and that the extract mode really did take the sentinel path, which
// the enrolled-type control above establishes by rendering rows on it.
func TestInterceptCollect_UnenrolledEmitType_TheCheckIsNotKeyedOnTheRunTarget(t *testing.T) {
	for _, mode := range []map[string]any{{"land": true}, {"extract": true}} {
		c := newLandingCaller()
		deps := &recipeDeps{sink: &recipeCaptureSink{}, gc: c}
		handled, res := InterceptCollect(opCtx(), deps, collectParamsFor(t, unenrolledEmitBody, mode))
		require.True(t, handled)
		require.True(t, res.IsError, "mode %v must refuse the same body", mode)
		assert.Contains(t, resultText(res), "widget_of_nowhere")
		assert.Empty(t, c.mutations)
	}

	// The practice graph is the ONLY target a landing writes into, which is what
	// makes a fixed vocabulary the right thing to check against in both modes.
	require.Equal(t, kgtypes.GraphPractice, landingTarget().GraphType,
		"a landing's target is the combined practice graph — if that ever changes, the fixed vocabulary above "+
			"is no longer the right answer key and this pair has to be revisited")
}
