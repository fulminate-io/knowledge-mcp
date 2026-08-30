// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestAccountMutateParams_UnknownTopLevelParamRejected pins the unknown-key
// class the param-accounting gate previously could not see. The gate iterates
// each arm's REJECTED set, and every rejected set is authored from schema keys
// only, so a supplied key that is not a schema param at all has no cell on any
// arm and was classified by nobody — neither routed nor rejected.
//
// The check keys on the SCHEMA rather than on the arm, which is why one subtest
// covering one arm establishes the contract for all of them: an undeclared key
// is undeclared for every arm.
//
// Three of the four subtests are characterization guards that pass before and
// after the fix. They are the false-block fences: the schema-derived check must
// not start rejecting declared params, must not descend into batch sub-objects,
// and must not report a key the caller left empty.
func TestAccountMutateParams_UnknownTopLevelParamRejected(t *testing.T) {
	t.Run("unknown_key_is_rejected_with_the_canonical_form", func(t *testing.T) {
		// The probe key must be genuinely absent from the schema or this case
		// asserts nothing — the same known-positive discipline the sibling
		// reader-side test applies to its synthetic key.
		_, declared := mutateProperties()["file_paths"]
		require.False(t, declared, "file_paths must be absent from the mutate schema for this case to mean anything")

		err := accountMutateParams(armUpdateFallthrough, withRawArgs(
			mutateArgs{Operation: "update", ID: "n-1"},
			`{"operation":"update","id":"n-1","file_paths":"a.go,b.go"}`,
		))
		require.Error(t, err, "a key the mutate schema does not declare must be rejected pre-write")
		msg := err.Error()
		assert.Contains(t, msg, `unknown parameter "file_paths"`, "the offending key is named and quoted")
		assert.Contains(t, msg, `did you mean metadata:{"file_paths": ...}`,
			"the message points at metadata, the flex-open carrier an undeclared key usually belongs in")
		assert.Contains(t, msg, "Valid top-level parameters:", "the valid set is enumerated for the caller")
		// The enumeration is derived from the schema, so a real declared param
		// must appear in it — a hardcoded list that rotted would not.
		assert.Contains(t, msg, "metadata", "the enumerated set is the live schema's own key set")
	})

	t.Run("schema_declared_key_is_untouched", func(t *testing.T) {
		err := accountMutateParams(armUpdateFallthrough, withRawArgs(
			mutateArgs{Operation: "update", ID: "n-1", Name: "renamed", Status: "active"},
			`{"operation":"update","id":"n-1","name":"renamed","status":"active","metadata":{"k":"v"}}`,
		))
		assert.NoError(t, err, "a payload of only declared params must pass — this fences all 22 arms against a false block")
	})

	t.Run("batch_sub_object_keys_are_not_top_level", func(t *testing.T) {
		// nodes[] entries carry type/name/summary, which ARE top-level schema
		// params on other operations but are SUB-OBJECT keys here. A check that
		// walked the payload recursively would still pass this; one that walked
		// it recursively AND compared sub-object keys against the top-level
		// schema would break every batch call that names a key the schema does
		// not declare at top level.
		err := accountMutateParams(armCreateBatch, withRawArgs(
			mutateArgs{Operation: "create_batch"},
			`{"operation":"create_batch","nodes":[{"type":"finding","name":"n","summary":"s","not_a_top_level_key":"v"}]}`,
		))
		assert.NoError(t, err, "batch sub-object keys are not top-level params and must not be swept")
	})

	t.Run("empty_unknown_key_is_not_reported", func(t *testing.T) {
		// suppliedMutateParams' empty-is-absent rule is what this preserves: an
		// empty value cannot be a silent drop, so reporting it would turn a
		// no-op key into a hard failure. Every empty spelling is covered.
		err := accountMutateParams(armUpdateFallthrough, withRawArgs(
			mutateArgs{Operation: "update", ID: "n-1"},
			`{"operation":"update","id":"n-1","unknown_str":"","unknown_null":null,"unknown_num":0,`+
				`"unknown_bool":false,"unknown_obj":{},"unknown_arr":[]}`,
		))
		assert.NoError(t, err, "an unknown key whose value is semantically empty is not a dropped write")
	})
}

// TestInterceptMutate_UnknownTopLevelParamRejected is the incident replay. A
// top-level file_paths on mutate(update) — a real call this program made — routed
// nowhere and errored never: the update succeeded, the field vanished, and only
// an auditor's disjointness re-derivation caught it.
//
// It drives the production dispatch head rather than the gate directly, so it
// fails if the gate is correct but unreachable from the arm the live call took.
// The node is a plain local ticket, which the rollup and typed-router arms both
// decline, so the call lands on armUpdateFallthrough — the same arm the live
// incident took.
func TestInterceptMutate_UnknownTopLevelParamRejected(t *testing.T) {
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"local-1": nodeResultJSON(t, "local-1", "ticket", map[string]string{}),
	}}
	deps := interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}

	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(
			`{"operation":"update","id":"local-1","name":"new name","file_paths":"cmd/a.go,cmd/b.go"}`),
	})

	require.True(t, handled, "an undeclared top-level param must be answered here, not dropped on the way to the engine")
	require.True(t, res.IsError, "the call fails rather than succeeding with the field silently discarded")
	assert.Contains(t, toolResultText(res), `unknown parameter "file_paths"`,
		"the error names the param that would otherwise have vanished")
	assert.Empty(t, fc.execMutations, "a pre-write rejection issues ZERO mutations")
}
