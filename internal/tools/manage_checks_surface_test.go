// SPDX-License-Identifier: Apache-2.0

package tools

// manage_checks_surface_test.go pins the tool's ADVERTISED surface and its
// bad-input behavior — the two properties a caller meets before any operation
// runs.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestAllToolSchemas_AdvertisesManageChecks asserts the catalog publishes the
// tool under its ADVERTISED Name literal.
//
// The Name literal rather than the Go identifier is the whole point: what
// tools/list publishes is the Name inside the ToolDef, and the two do not always
// correspond — the graph-type ToolDef advertises "custom_collector". A test
// spelled from the identifier would pass while the wire name was anything at all.
func TestAllToolSchemas_AdvertisesManageChecks(t *testing.T) {
	var found *kgtools.MCPTool
	for i, tool := range AllToolSchemas() {
		if tool.Name == "manage_checks" {
			found = &AllToolSchemas()[i]
			break
		}
	}
	require.NotNil(t, found, "the advertised catalog must contain manage_checks")

	// The operation enum is what a strict client validates a call against, so it
	// must carry the whole admitted vocabulary and nothing else.
	op, ok := found.InputSchema.Properties["operation"]
	require.True(t, ok, "manage_checks must declare an operation param")
	assert.ElementsMatch(t, manageChecksOperations, op.Enum,
		"the advertised enum and the dispatch vocabulary must be the same set")
	assert.Equal(t, []string{"operation"}, found.InputSchema.Required,
		"operation is the single unconditional key; a conditional one in root Required breaks strict validation")

	// The catalog-wide strict-validity guard runs over EVERY tool including this
	// one, so its invariants are already asserted for manage_checks by
	// TestAllToolSchemas. Re-run one of them here as a known positive that this
	// tool is genuinely inside that walk rather than merely present in the slice.
	for name, prop := range found.InputSchema.Properties {
		assert.NotEmpty(t, prop.Description, "manage_checks.%s must carry a description", name)
	}
}

// TestInterceptManageChecks_UnknownOperationNamesTheVocabulary asserts the
// bad-input rule on BEHAVIOR: an unadmitted operation is refused naming both the
// offending value and the admitted set.
//
// The known-positive control is the same call shape with an ADMITTED operation,
// which must be claimed and not refused for the operation's sake — without it, a
// tool that refused everything would satisfy every assertion below.
func TestInterceptManageChecks_UnknownOperationNamesTheVocabulary(t *testing.T) {
	deps := &repoTestDeps{rootDir: t.TempDir(), gc: newChecksGraphFake()}

	handled, res := InterceptManageChecks(context.Background(), deps,
		kgtools.CallToolParams{Name: "manage_checks", Arguments: json.RawMessage(`{"operation":"destroy"}`)})
	require.True(t, handled, "manage_checks is claimed client-side, refusal included")
	require.True(t, res.IsError)
	body := res.Content[0].Text
	assert.Contains(t, body, `"destroy"`, "the refusal must name the offending value")
	for _, op := range manageChecksOperations {
		assert.Contains(t, body, op, "the refusal must enumerate the admitted vocabulary, missing %q", op)
	}

	// CONTROL: an admitted operation is served rather than refused.
	handled, res = InterceptManageChecks(context.Background(), deps,
		kgtools.CallToolParams{Name: "manage_checks", Arguments: json.RawMessage(`{"operation":"` + OpChecksList + `"}`)})
	require.True(t, handled)
	require.False(t, res.IsError, "an admitted operation must be served: %s", res.Content[0].Text)

	// A call for another tool is declined outright, not answered.
	handled, _ = InterceptManageChecks(context.Background(), deps,
		kgtools.CallToolParams{Name: "manage", Arguments: json.RawMessage(`{"operation":"status"}`)})
	assert.False(t, handled, "manage_checks must not claim another tool's call")
}
