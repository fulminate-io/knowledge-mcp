// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// typedUpdateResponse runs a typed update and returns the RESPONSE TEXT
// alongside the fake caller. It exists because runTypedUpdate discards the
// ToolResult, and every assertion in this file is about what the response SAYS —
// the receipt lines and the warnings section. It reuses typedUpdateRaw so the
// payload seeding matches the production path exactly.
func typedUpdateResponse(t *testing.T, node *knowledgev1.Node, a mutateArgs) (string, *fakeGraphCaller) {
	t.Helper()
	fc := &fakeGraphCaller{}
	deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}
	handled, res := handleClientMutateUpdateTyped(context.Background(), deps, withRawArgs(a, typedUpdateRaw(t, a)), node)
	require.True(t, handled, "a criterion/rule/finding update is claimed by the typed router")
	require.False(t, res.IsError, "unexpected error result: %s", toolResultText(res))
	return toolResultText(res), fc
}

// typedUpdateRefusal runs a typed update that is expected to be REFUSED and
// returns the error text alongside the fake caller, so a test can assert both
// what the refusal says and that it issued no writes. The sibling of
// typedUpdateResponse, which requires success.
func typedUpdateRefusal(t *testing.T, node *knowledgev1.Node, a mutateArgs) (string, *fakeGraphCaller) {
	t.Helper()
	fc := &fakeGraphCaller{}
	deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}
	handled, res := handleClientMutateUpdateTyped(context.Background(), deps, withRawArgs(a, typedUpdateRaw(t, a)), node)
	require.True(t, handled, "a criterion/rule/finding update is claimed by the typed router")
	require.True(t, res.IsError, "expected a refusal, got success: %s", toolResultText(res))
	return toolResultText(res), fc
}

// leakySummaryParamDialect builds a summary whose tail carries the NAMESPACED
// parameter-tag dialect. Assembled from fragments rather than pasted as a live
// tag block, so this test file does not itself become a specimen the repo's leak
// censuses have to except.
func leakySummaryParamDialect() string {
	return "the sweep remainder is zero</" + "summary><" + "parameter name=" + `"metadata">{"evaluate_at": "SHOULD_NOT_LAND"}`
}

// ambiguousSummaryParamMention builds a summary that mentions parameter markup
// WITHOUT carrying the summary field's own closing tag. This is the shape the
// swallowed-parameter gate deliberately does NOT refuse — nothing in it proves a
// mis-serialization — and it is therefore the advisory's remaining domain.
func ambiguousSummaryParamMention() string {
	return "the grammar's parameter form is <" + "parameter name=" + `"session">`
}

// leakySummaryBareDialect builds a summary whose tail carries the BARE dialect —
// a closing field tag followed by a bare parameter tag, with NO `parameter
// name=` wrapper anywhere in it. This is the shape the defect report carried.
func leakySummaryBareDialect() string {
	return "the sweep remainder is zero</" + "summary><" + "metadata>" +
		`{"evaluate_at": "SHOULD_NOT_LAND"}` + "</" + "metadata>"
}

// leakyDescriptionParamDialect is the description-field counterpart of the
// parameter-tag dialect fixture.
func leakyDescriptionParamDialect() string {
	return "the gate re-runs byte-for-byte</" + "description><" + "parameter name=" + `"metadata">{"gate": "SHOULD_NOT_LAND"}`
}

// TestTypedUpdate_ParamShapedTail_RefusesOrWarnsPerField pins the mutate(update)
// write receipt and the two mechanisms that now split the parameter-shaped-tail
// surface between them.
//
// THE SPLIT, and why the earlier "warns and still writes" contract on subtests 1,
// 2 and 5 was superseded. A warning did not stop the damage: the write landed,
// the tag soup was stored in a field a human authored, and the swallowed
// parameters still routed nowhere. Those three subtests now assert a REFUSAL,
// issued before any write, for the one shape a correct call cannot produce — a
// value carrying its own closing tag with the remainder running to the end of the
// value (swallowed_param_gate.go). Subtest 6 keeps the advisory's remaining
// domain live: parameter markup with NO anchored closing tag is genuinely
// ambiguous text, so it still warns and still writes.
//
// Subtest 5 remains the catcher for a fix that parameterized only a MESSAGE and
// left detection hardcoded to one field: against such an implementation the bare
// dialect goes unrefused, which is the defect's own reported shape. Subtest 3 is
// the known-negative that makes the whole test non-vacuous — without it, an
// implementation that refused or warned unconditionally would be
// indistinguishable from a correct one.
func TestTypedUpdate_ParamShapedTail_RefusesOrWarnsPerField(t *testing.T) {
	t.Run("parameter-tag dialect in summary is refused before any write", func(t *testing.T) {
		node := nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green",
			map[string]string{"type": "manual"})
		body, fc := typedUpdateRefusal(t, node, mutateArgs{
			Operation: "update", ID: "c1",
			Summary:  leakySummaryParamDialect(),
			Metadata: map[string]string{"evaluate_at": "phase-2-boundary"},
		})

		// The refusal must name the field the caller actually sent. `content` is
		// what a half-done field substitution emits.
		assert.Contains(t, body, "the summary parameter's value")
		assert.NotContains(t, body, "the content parameter's value")
		// And it must quote the malformed input rather than merely describing it.
		assert.Contains(t, body, "SHOULD_NOT_LAND")
		// ZERO writes: the metadata key the caller DID send correctly must not have
		// landed either, because a mis-serialized call is refused whole rather than
		// applied in part.
		assert.Empty(t, fc.execMutations, "a refused call issues no writes at all")
	})

	t.Run("tail in description is refused and names description", func(t *testing.T) {
		node := nodeOf(t, "c2", "criterion", "the suite is green", "the suite is green",
			map[string]string{"type": "manual"})
		body, fc := typedUpdateRefusal(t, node, mutateArgs{
			Operation: "update", ID: "c2",
			Description: leakyDescriptionParamDialect(),
		})

		assert.Contains(t, body, "the description parameter's value")
		assert.NotContains(t, body, "the content parameter's value")
		assert.Empty(t, fc.execMutations)
	})

	t.Run("a clean update produces no warnings section", func(t *testing.T) {
		node := nodeOf(t, "f3", "finding", "leak", "leak in handler",
			map[string]string{"evidence": "store.go:42"})
		body, _ := typedUpdateResponse(t, node, mutateArgs{
			Operation: "update", ID: "f3",
			Description: "the sweep remainder is zero",
			Metadata:    map[string]string{"evaluate_at": "phase-2-boundary"},
		})

		assert.NotContains(t, body, "## Warnings",
			"a correct call must not be warned about; without this the advisory could fire unconditionally")
		// The receipt still renders on a clean call. No summary rides the forward:
		// the call supplied none, and nothing composes one.
		assert.Contains(t, body, "Fields forwarded by this call: description")
		assert.NotContains(t, body, "Fields forwarded by this call: description, summary")
	})

	t.Run("the receipt reports the summary disposition", func(t *testing.T) {
		node := nodeOf(t, "f4", "finding", "leak", "leak in handler",
			map[string]string{"evidence": "store.go:42"})

		explicit, _ := typedUpdateResponse(t, node, mutateArgs{
			Operation: "update", ID: "f4", Summary: "a short caller-authored summary",
		})
		assert.Contains(t, explicit, "Summary: caller-supplied")

		// The second literal is produced by every call that names no summary,
		// whatever else it changes — a body edit and a status-only edit alike.
		edited, _ := typedUpdateResponse(t, node, mutateArgs{
			Operation: "update", ID: "f4", Description: "the sweep remainder is zero",
		})
		assert.Contains(t, edited, "Summary: "+summaryDispositionUnchanged)

		statusOnly, _ := typedUpdateResponse(t, node, mutateArgs{
			Operation: "update", ID: "f4", Status: "completed",
		})
		assert.Contains(t, statusOnly, "Summary: "+summaryDispositionUnchanged)
		assert.Contains(t, statusOnly, "Fields forwarded by this call: status")
	})

	t.Run("bare dialect in summary is refused", func(t *testing.T) {
		node := nodeOf(t, "c5", "criterion", "the suite is green", "the suite is green",
			map[string]string{"type": "manual"})
		summary := leakySummaryBareDialect()
		// The fixture must carry NO parameter-open tag, or it would be caught by the
		// regex leg and this subtest would silently duplicate subtest 1 instead of
		// covering the closing-tag leg.
		require.Empty(t, paramTailNames(summary),
			"the bare-dialect fixture must not contain a parameter-open tag")

		body, fc := typedUpdateRefusal(t, node, mutateArgs{
			Operation: "update", ID: "c5", Summary: summary,
		})

		assert.Contains(t, body, "the summary parameter's value")
		assert.Contains(t, body, "SHOULD_NOT_LAND")
		assert.Empty(t, fc.execMutations)
	})

	// The advisory's remaining domain, kept live so the split between the two
	// mechanisms is asserted rather than described. Parameter markup with no
	// anchored closing tag proves nothing about serialization — a value may
	// legitimately discuss the grammar — so it warns and the write proceeds.
	t.Run("parameter mention without an anchored closing tag still only warns", func(t *testing.T) {
		node := nodeOf(t, "c6", "criterion", "the suite is green", "the suite is green",
			map[string]string{"type": "manual"})
		summary := ambiguousSummaryParamMention()
		require.Empty(t, swallowedParamFragment("summary", summary),
			"this fixture must be OUTSIDE the refusal's predicate, or the subtest asserts nothing about the advisory")

		body, fc := typedUpdateResponse(t, node, mutateArgs{
			Operation: "update", ID: "c6",
			Summary:  summary,
			Metadata: map[string]string{"evaluate_at": "phase-2-boundary"},
		})

		m := lastUpdatePlan(t, fc)
		assert.Contains(t, body, "## Warnings")
		assert.Contains(t, body, "summary ends with parameter-like markup")
		assert.Contains(t, body, "Metadata keys forwarded by this call: evaluate_at")
		assert.Equal(t, "phase-2-boundary", m.GetSetMetadata()["evaluate_at"])
	})
}
