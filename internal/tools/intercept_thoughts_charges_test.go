// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// seededChargesFixture returns a ctxCaller seeding thought t1 with one positive
// charge c1, joined by an EdgeChargedBy edge — exactly the two reads
// fetchChargesFor issues (ONE EdgeChargedBy edges read + ONE bulk node hydrate).
func seededChargesFixture() *ctxCaller {
	charge := &knowledgev1.Node{Id: "c1", Type: string(kgtypes.NodeCharge)}
	kgtypes.SetValue(charge, "polarity", "positive")
	kgtypes.SetValue(charge, "weight", "7")
	return &ctxCaller{
		nodesByID: nodesByID(charge),
		chargeEdges: []*knowledgev1.Edge{
			{Type: string(kgtypes.EdgeChargedBy), FromId: "t1", ToId: "c1"},
		},
	}
}

func callChargesFor(t *testing.T, deps ClientDeps, args map[string]any) (bool, kgtools.ToolResult) {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return InterceptThoughts(deps, kgtools.CallToolParams{Name: "thoughts", Arguments: raw})
}

// TestChargesFor_DispatchClaimed is the core QA repro: thoughts(charges_for)
// must be CLAIMED client-side, not fall through to the engine deny.
func TestChargesFor_DispatchClaimed(t *testing.T) {
	t.Parallel()
	deps := ctxPackDeps{gc: seededChargesFixture()}
	handled, res := callChargesFor(t, deps, map[string]any{"operation": "charges_for", "thought_ids": []string{"t1"}})
	require.True(t, handled, "thoughts(charges_for) must be claimed by the client intercept")
	assert.False(t, res.IsError, "a valid charges_for must not error: %s", toolResultText(res))
	assert.NotContains(t, toolResultText(res), "is not a recognized engine-reducible shape",
		"charges_for must not fall through to the engine deny")
}

// TestChargesFor_RequiresThoughtIDs: an empty/absent thought_ids is a loud error.
func TestChargesFor_RequiresThoughtIDs(t *testing.T) {
	t.Parallel()
	deps := ctxPackDeps{gc: seededChargesFixture()}
	handled, res := callChargesFor(t, deps, map[string]any{"operation": "charges_for"})
	require.True(t, handled, "charges_for is still claimed even when thought_ids is missing")
	assert.True(t, res.IsError, "missing thought_ids must be a loud error")
	assert.Contains(t, toolResultText(res), "thought_ids is required")
}

// chargesForJSON is the decoded shape of the json render arm.
type chargesForJSON struct {
	ChargesByThought map[string][]knowledgev1.Node `json:"charges_by_thought"`
}

// TestChargesFor_JSONShape: format=json maps the thought id to its one-element
// charge node array, carrying the charge id and polarity.
func TestChargesFor_JSONShape(t *testing.T) {
	t.Parallel()
	deps := ctxPackDeps{gc: seededChargesFixture()}
	handled, res := callChargesFor(t, deps, map[string]any{
		"operation": "charges_for", "thought_ids": []string{"t1"}, "format": "json",
	})
	require.True(t, handled)
	require.False(t, res.IsError, "json charges_for errored: %s", toolResultText(res))

	var got chargesForJSON
	require.NoError(t, json.Unmarshal([]byte(toolResultText(res)), &got))
	charges := got.ChargesByThought["t1"]
	require.Len(t, charges, 1, "t1 must map to its one seeded charge")
	assert.Equal(t, "c1", charges[0].GetId(), "the charge node id must be carried")
	assert.Equal(t, "positive", kgtypes.Value(&charges[0], "polarity"), "the charge polarity must be carried")
}
