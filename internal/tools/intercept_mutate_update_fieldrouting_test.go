// SPDX-License-Identifier: Apache-2.0

// intercept_mutate_update_fieldrouting_test.go — the VERDICT TEST for the
// reported "mutate(update) applies summary and status but silently drops
// description and metadata" defect on the criterion update path.
//
// WHY THIS TEST EXISTS AS ITS OWN FILE. The report was filed against the live
// tool and read back twice, and it named a class of silent narrowing rather than
// one call. Answering it needs a probe that enters at the SAME door the reported
// call entered — InterceptMutate, with the node resolved by a by-id query —
// rather than at the internal typed-update handler, because a drop could equally
// live in the routing above the handler as inside it. The sibling tests in
// intercept_mutate_update_test.go all call handleClientMutateUpdateTyped
// directly, so none of them could have settled the report either way.
//
// WHAT IT ASSERTS. One criterion update carrying description, metadata AND
// status together lands all three on the forwarded MutationPlan: description in
// set_fields, the metadata key in set_metadata, status in set_fields. The three
// ride ONE call on purpose — the report's shape is "the fields I sent alongside
// the ones that worked went nowhere", which a per-field test cannot reproduce.
//
// THE FIXTURE'S metadata KEY IS THE REPORTED ONE. evaluate_at is the key the
// ticket names as the observed casualty (a gate criterion that could not carry
// it, so boundary sweeps selecting on metadata.evaluate_at silently excluded
// it). Using the reported key rather than a generic "k" keeps the test tied to
// the artifact it answers.

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestInterceptMutate_CriterionUpdate_DescriptionAndMetadataBothRoute is the
// verdict probe described in this file's header. It goes RED if either half of
// the reported drop is real: remove `Description: a.Description` or
// `Metadata: meta` from the forward handleClientMutateUpdateTyped builds and one
// of the two assertions below fails.
func TestInterceptMutate_CriterionUpdate_DescriptionAndMetadataBothRoute(t *testing.T) {
	const (
		newDesc  = "the boundary sweep leaves no unevaluated gate criterion"
		metaKey  = "evaluate_at"
		metaVal  = "phase-2-boundary"
		newState = "completed"
	)

	stored, err := json.Marshal(map[string]any{
		"id": "c1", "type": "criterion",
		"symbol_name": "old description", "description": "old description",
		"metadata": map[string]string{"type": "manual"},
	})
	require.NoError(t, err)
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"c1": {Content: []kgtools.ContentBlock{{Type: "text", Text: string(stored)}}},
	}}
	deps := interceptTestDeps{byName: map[string]backends.Backend{}, gc: fc}

	args, err := json.Marshal(map[string]any{
		"operation": "update", "id": "c1",
		"description": newDesc,
		"metadata":    map[string]string{metaKey: metaVal},
		"status":      newState,
		// An explicit summary rides along so the criterion path's over-cap
		// refusal cannot be what decides this test. Without it a long description
		// would re-derive a summary and the assertion set below would be
		// entangled with the (separately owned) derivation rule.
		"summary": "the boundary sweep leaves no unevaluated gate criterion",
	})
	require.NoError(t, err)

	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name: "mutate", Arguments: args,
	})
	require.True(t, handled, "a criterion update must be claimed by the typed router")
	require.False(t, res.IsError, "expected success: %s", toolResultText(res))

	require.GreaterOrEqual(t, len(fc.execMutations), 1, "expected one forwarded mutation")
	m := fc.execMutations[len(fc.execMutations)-1]

	assert.Equal(t, newDesc, m.GetSetFields()["description"],
		"description must ride set_fields — the reported drop's first half")
	assert.Equal(t, metaVal, m.GetSetMetadata()[metaKey],
		"the caller's metadata key must ride set_metadata — the reported drop's second half")
	assert.Equal(t, newState, m.GetSetFields()["status"],
		"status is the field the report says DID land; asserting it here keeps the "+
			"other two honest, since a forward that carried nothing at all would "+
			"otherwise look like the same failure")

	// The receipt is the caller-visible half of the same claim: a caller reading
	// only the response must be able to see that description and the metadata key
	// were forwarded. Assert it here so a regression that keeps the wire correct
	// while under-reporting it is still caught.
	body := toolResultText(res)
	assert.Contains(t, body, "Fields forwarded by this call:")
	assert.Contains(t, body, "description")
	assert.Contains(t, body, "Metadata keys forwarded by this call:")
	assert.Contains(t, body, metaKey)
}
