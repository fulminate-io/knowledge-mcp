// SPDX-License-Identifier: Apache-2.0

// client_warnings_ordering_test.go — the ordering contract for the shared
// `## Warnings` renderer.
//
// A warning that arrives AFTER the success line it qualifies is a warning most
// callers never read: the success line answers the question they asked, and the
// text below it reads as trailing detail. Every warning this renderer carries is
// about the call that just succeeded — a summary the tool clamped on the
// caller's behalf, a pattern id that resolved nowhere, text that will never be
// interpreted as parameters — so it has to be visible before the reader stops.
//
// These assertions are deliberately ORDERING-ONLY and are spread across three
// unrelated arms (a typed update, a think, a finding create). writeClientWarningsSection
// is shared by every create and update receipt in this package, so pinning one
// arm would leave a per-call-site regression invisible; pinning the CONTENT again
// here would only duplicate the assertions the per-arm tests already own.

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// assertWarningsPrecede asserts that the warnings section opens before the
// success line it qualifies. Both markers are required to be PRESENT first: an
// absent marker makes an index comparison vacuously true in one direction and
// misleadingly false in the other, and this whole file is index comparisons.
func assertWarningsPrecede(t *testing.T, body, successMarker string) {
	t.Helper()
	warnAt := strings.Index(body, "## Warnings")
	successAt := strings.Index(body, successMarker)
	require.GreaterOrEqual(t, warnAt, 0, "no warnings section in the response:\n%s", body)
	require.GreaterOrEqual(t, successAt, 0, "no success line %q in the response:\n%s", successMarker, body)
	assert.Less(t, warnAt, successAt,
		"the warnings section must open ABOVE the success line it qualifies:\n%s", body)
}

// TestClientWarnings_PrecedeTheSuccessLine_TypedUpdate drives the advisory the
// mutate(update) receipt emits — a description whose tail carries parameter-like
// markup — and pins that it renders above the receipt's success line.
//
// The fixture ends with a bare parameter-open tag and NO closing description
// tag, which is the shape that trips the non-fatal advisory without meeting
// rejectSwallowedParamValues' hard-refusal shape: a refused call renders no
// receipt at all, and there would be nothing to order.
func TestClientWarnings_PrecedeTheSuccessLine_TypedUpdate(t *testing.T) {
	node := nodeOf(t, "f-order", "finding", "orphan rows", "old desc", nil)
	body, _ := typedUpdateResponse(t, node, mutateArgs{
		Operation: "update", ID: "f-order",
		Description: `a new description body <parameter name="status">completed`,
	})
	require.Contains(t, body, "ends with parameter-like markup",
		"this fixture must actually produce the advisory, or the ordering assertion is vacuous")
	assertWarningsPrecede(t, body, "mutate(update): updated f-order")
}

// TestClientWarnings_PrecedeTheSuccessLine_Think covers the second shape the
// renderer serves: a receipt whose success line is its FIRST line rather than
// its only one.
func TestClientWarnings_PrecedeTheSuccessLine_Think(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"th-order"}}
	deps := interceptTestDeps{gc: fc}

	args, err := json.Marshal(map[string]any{
		"operation": "think",
		"content":   "a hypothesis recorded to exercise the clamp warning's placement",
		"summary":   strings.Repeat("an over-long summary that the clamp must cut at a word boundary ", 12),
	})
	require.NoError(t, err)

	handled, res := InterceptThoughts(context.Background(), deps, kgtools.CallToolParams{
		Name: "thoughts", Arguments: args,
	})
	require.True(t, handled)
	require.False(t, res.IsError, "unexpected error: %s", toolResultText(res))
	body := toolResultText(res)
	require.Contains(t, body, "clamped",
		"this fixture must actually produce the clamp warning, or the ordering assertion is vacuous")
	assertWarningsPrecede(t, body, "Thought recorded")
}

// TestClientWarnings_PrecedeTheSuccessLine_FindingCreate covers the CREATE half
// of the shared renderer. The update and think arms above both build their body
// one line at a time; this one is the arm whose success line already carries the
// graph tag, so a renderer that prepended only to single-line bodies is caught.
func TestClientWarnings_PrecedeTheSuccessLine_FindingCreate(t *testing.T) {
	fc := &fakeGraphCaller{mutateIDs: []string{"fnd-order"}}
	deps := interceptTestDeps{gc: fc}

	args, err := json.Marshal(map[string]any{
		"operation": "create", "type": "finding",
		"name":        "an ordering fixture",
		"description": "the body of the finding",
		"summary":     strings.Repeat("an over-long summary that the clamp must cut at a word boundary ", 12),
	})
	require.NoError(t, err)

	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name: "mutate", Arguments: args,
	})
	require.True(t, handled)
	require.False(t, res.IsError, "unexpected error: %s", toolResultText(res))
	body := toolResultText(res)
	require.Contains(t, body, "clamped",
		"this fixture must actually produce the clamp warning, or the ordering assertion is vacuous")
	assertWarningsPrecede(t, body, "Finding recorded")
}

// TestWriteClientWarningsSection_Shape pins the renderer's exact output on a
// body it did not write, which is the contract every call site depends on: the
// header, the `⚠ ` line prefix (the planner agent's parse surface), one blank
// line, then the caller's body VERBATIM and unreordered. The multi-line body is
// deliberate — a renderer that hoisted only the first line, or that dropped
// everything after it, passes every index comparison in this file and fails here.
func TestWriteClientWarningsSection_Shape(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("Thing created: X → ID: y\nSecond line of the body")
	writeClientWarningsSection(&sb, []string{"first warning", "second warning"})

	assert.Equal(t,
		"## Warnings\n\n⚠ first warning\n⚠ second warning\n\nThing created: X → ID: y\nSecond line of the body",
		sb.String())
}

// TestWriteClientWarningsSection_NoWarningsLeavesBodyUntouched is the
// known-negative for the shape test: the empty case must not rewrite the builder
// at all, not even to add a separator.
func TestWriteClientWarningsSection_NoWarningsLeavesBodyUntouched(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("Thing created: X → ID: y")
	writeClientWarningsSection(&sb, nil)
	assert.Equal(t, "Thing created: X → ID: y", sb.String())
}

// TestClientWarnings_CleanCallHasNoSection is the known-negative for all three
// assertions above: without it, a renderer that emitted the header
// unconditionally would satisfy every ordering check in this file.
func TestClientWarnings_CleanCallHasNoSection(t *testing.T) {
	node := nodeOf(t, "f-clean", "finding", "orphan rows", "old desc", nil)
	body, _ := typedUpdateResponse(t, node, mutateArgs{
		Operation: "update", ID: "f-clean", Description: "a short new description",
	})
	assert.NotContains(t, body, "## Warnings")
	assert.Contains(t, body, "mutate(update): updated f-clean")
}
