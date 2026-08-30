// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// collectedGraphCaution is the substring of the accepted trade the shipped
// schemas must state on every graph-instance selector: collected graphs become
// hand-mutable over MCP, with collect reconciliation unchanged as the corrective
// force. It is a DOCUMENTATION duty — no guard, no refusal, no code path.
const collectedGraphCaution = "a later collect reconciles that graph from its source"

// TestMutateSurface_DeclaresInstanceSelectors asserts the wire declaration half
// of the uniform instance-selector shape: both tools declare repo and account,
// the arm that actually routes a non-knowledge mutate consumes both, and both
// tools carry the collected-graph caution on all four new descriptions.
//
// EVERY ASSERTION IS PER TOOL AND PER PARAM, never an aggregate. A single
// combined assertion is satisfiable by declaring all four entries on one tool and
// none on the other, and "the phrase appears four times" is satisfiable by four
// occurrences in one description.
func TestMutateSurface_DeclaresInstanceSelectors(t *testing.T) {
	t.Run("mutate schema declares repo and account", func(t *testing.T) {
		props := mutateProperties()
		for _, param := range []string{"repo", "account"} {
			p, ok := props[param]
			require.Truef(t, ok, "the mutate schema must declare %q", param)
			assert.Equalf(t, "string", p.Type, "mutate.%s must be a string param", param)
			assert.NotEmptyf(t, p.Description, "mutate.%s must carry a description", param)
		}
	})

	t.Run("delete schema declares repo and account", func(t *testing.T) {
		props := DeleteToolDef().InputSchema.Properties
		for _, param := range []string{"repo", "account"} {
			p, ok := props[param]
			require.Truef(t, ok, "the delete schema must declare %q", param)
			assert.Equalf(t, "string", p.Type, "delete.%s must be a string param", param)
			assert.NotEmptyf(t, p.Description, "delete.%s must carry a description", param)
		}
	})

	// THE FALSIFIABLE HALF of the registry work. A table where repo is rejected on
	// every arm INCLUDING the fallthrough satisfies the structural partition gates
	// and leaves the tool exactly as broken as it was: this asserts the specific
	// cell that makes the feature work.
	t.Run("the non-knowledge fallthrough consumes both", func(t *testing.T) {
		spec, ok := mutateArmRegistry[armNonKnowledgeFallthrough]
		require.True(t, ok, "the non-knowledge fallthrough arm must be registered")
		for _, param := range []string{"repo", "account"} {
			assert.Truef(t, spec.consumed[param],
				"armNonKnowledgeFallthrough must CONSUME %q — it is the arm that routes a mutate at a non-knowledge graph", param)
			assert.Falsef(t, spec.rejected[param],
				"armNonKnowledgeFallthrough must not reject %q", param)
		}
	})

	t.Run("both schemas carry the collected-graph caution on both selectors", func(t *testing.T) {
		mutateProps := mutateProperties()
		deleteProps := DeleteToolDef().InputSchema.Properties
		for _, tc := range []struct {
			tool  string
			props map[string]kgtools.Property
			param string
		}{
			{"mutate", mutateProps, "repo"},
			{"mutate", mutateProps, "account"},
			{"delete", deleteProps, "repo"},
			{"delete", deleteProps, "account"},
		} {
			p, ok := tc.props[tc.param]
			require.Truef(t, ok, "%s.%s must be declared before its caution can be asserted", tc.tool, tc.param)
			assert.Containsf(t, p.Description, collectedGraphCaution,
				"%s.%s must state the accepted trade: writes to a collected graph are caller-owned and %s",
				tc.tool, tc.param, collectedGraphCaution)
		}
	})
}

// TestMutateInstanceSelector_RefusalNamesValueAndVocabulary asserts the refusal
// on MESSAGE CONTENT and in BOTH directions, not merely that it errors.
//
// A message that names the value but drops the vocabulary fails: the standing
// rule for bad input here is that the error names the offending value AND the
// vocabulary that would have worked. The second subtest is the leg that stops
// the check refusing correct work, and the third pins that a singleton family is
// never asked for an instance selector it has no way to supply.
func TestMutateInstanceSelector_RefusalNamesValueAndVocabulary(t *testing.T) {
	t.Run("code delete without repo is refused naming the value and the vocabulary", func(t *testing.T) {
		err := requireGraphInstanceSelector(mutateArgs{Operation: "delete", Graph: "code"})
		require.Error(t, err, "a code mutate with no repo must be refused")
		msg := err.Error()
		assert.Contains(t, msg, `graph="code"`, "the refusal must quote the offending graph value")
		assert.Contains(t, msg, "requires repo", "the refusal must name the param the caller owes")
		assert.Contains(t, msg, "accepted graph-instance selectors",
			"the refusal must lead into the vocabulary, not merely name the missing param")
		assert.Contains(t, msg, "account (cloud, cicd)", "the vocabulary must carry the cloud/cicd spelling")
		assert.Contains(t, msg, "language (practice)", "the vocabulary must carry the practice spelling")
	})

	t.Run("the same call with repo declines to the engine", func(t *testing.T) {
		err := requireGraphInstanceSelector(mutateArgs{Operation: "delete", Graph: "code", Repo: "knowledge"})
		assert.NoError(t, err, "a code mutate carrying repo is correct work and must not be claimed here")
	})

	t.Run("checks delete needs no selector", func(t *testing.T) {
		err := requireGraphInstanceSelector(mutateArgs{Operation: "delete", Graph: "checks"})
		assert.NoError(t, err, "checks is a singleton family — it has no instance to select")
	})

	// THE WIRING LEG. The three subtests above call the check directly, which
	// proves what it decides and NOT that anything asks it. MEASURED: with the
	// call site deleted from InterceptMutate's non-knowledge branch the entire
	// tools package still exits 0, so without this leg the check is a
	// declared-but-inert lever no gate can see. This drives the real dispatch
	// entry and asserts the refusal reaches a caller, with zero mutations issued.
	t.Run("the refusal is reachable through the mutate dispatch entry", func(t *testing.T) {
		fc := &fakeGraphCaller{}
		handled, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name:      "mutate",
			Arguments: json.RawMessage(`{"operation":"delete","graph":"code","ids":["a.go:Alpha"]}`),
		})
		require.True(t, handled, "the refusal is a claim — the call must not fall through to the engine")
		require.True(t, res.IsError, "a code mutate with no repo must refuse: %s", toolResultText(res))
		msg := toolResultText(res)
		assert.Contains(t, msg, `graph="code"`, "the refusal reaching the caller must quote the offending value")
		assert.Contains(t, msg, "accepted graph-instance selectors",
			"the refusal reaching the caller must carry the vocabulary")
		assert.Empty(t, fc.execMutations, "a pre-write refusal issues ZERO mutations")
	})
}
