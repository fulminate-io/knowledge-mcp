// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestHandleChargeClient_EmptyThought_Errors(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","polarity":"positive","weight":1.0}`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "charge requires 'thought'")
}

func TestHandleChargeClient_BadPolarity_Errors(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"t-1","polarity":"sideways","weight":1.0}`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "polarity must be 'positive' or 'negative'")
}

func TestHandleChargeClient_NonThoughtTarget_Rejected(t *testing.T) {
	// The parent verify resolves a non-thought node → rejected with the
	// thought-only-target message.
	fc := &fakeGraphCaller{queryResponses: map[string]kgtools.ToolResult{
		"doc-1": nodeResultJSON(t, "doc-1", "document", nil),
	}}
	deps := interceptTestDeps{gc: fc}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"doc-1","polarity":"positive","weight":1.0,"reasoning":"r"}`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), `charge target doc-1 is type "document", must be "thought"`)
}

func TestHandleChargeClient_MissingTarget_NotFound(t *testing.T) {
	fc := &fakeGraphCaller{} // no seeded node → parent verify misses.
	deps := interceptTestDeps{gc: fc}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"missing","polarity":"positive","weight":1.0,"reasoning":"r"}`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "thought missing not found")
}

// TestHandleChargeClient_LowersToCreateBatch covers the composer happy path: a
// valid charge against a NodeThought parent lowers to a CREATE MutationPlan with
// the charge NodeBody (type=charge, SymbolName=truncated reasoning, polarity +
// weight metadata) + EdgeChargedBy (thought→charge) + EdgeEvidencedBy
// (charge→evidence). GraphClient is nil (test affordance) → the bare-ID tail.
func TestHandleChargeClient_LowersToCreateBatch(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"th-1": nodeResultJSON(t, "th-1", "thought", nil),
			// Evidence ev-1 resolves in knowledge → raw id (no proxy).
			"ev-1": nodeResultJSON(t, "ev-1", "finding", nil),
		},
		mutateIDs: []string{"charge-1"},
	}
	deps := interceptTestDeps{gc: fc}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"th-1","polarity":"positive","weight":3.0,"reasoning":"because the test says so","evidence":["ev-1"]}`),
	})
	require.False(t, res.IsError, "charge should succeed: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res), "Charge recorded → ID: charge-1")

	// Exactly one CREATE MutationPlan: charge NodeBody + 2 edges.
	require.Len(t, fc.execMutations, 1)
	m := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_CREATE, m.GetKind())
	require.Len(t, m.GetNodeBodies(), 1)
	body := m.GetNodeBodies()[0]
	assert.Equal(t, "charge", body.GetType())
	assert.Equal(t, "because the test says so", body.GetName(), "SymbolName=reasoning (under maxLen, untruncated)")
	assert.Equal(t, "positive", body.GetMetadata()["polarity"])
	assert.Equal(t, "3.00", body.GetMetadata()["weight"])
	assert.Equal(t, "because the test says so", body.GetContent())

	// Edge directions: EdgeChargedBy th-1→charge(slot0); EdgeEvidencedBy charge(slot0)→ev-1.
	edges := m.GetEdges()
	require.Len(t, edges, 2)
	assert.Equal(t, "th-1", edges[0].GetFromId())
	assert.Equal(t, int32(0), edges[0].GetToIdx())
	assert.Equal(t, string(kgtypes.EdgeChargedBy), edges[0].GetType())
	assert.Equal(t, int32(0), edges[1].GetFromIdx())
	assert.Equal(t, "ev-1", edges[1].GetToId())
	assert.Equal(t, string(kgtypes.EdgeEvidencedBy), edges[1].GetType())
}

// TestHandleChargeClient_NoHitEvidence_RawIDPreserved covers ResolveOrProxy
// outcome (c): an evidence id found in NEITHER knowledge NOR any foreign graph
// is emitted as the raw id on the EdgeEvidencedBy edge (NOT dropped).
func TestHandleChargeClient_NoHitEvidence_RawIDPreserved(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			"th-1": nodeResultJSON(t, "th-1", "thought", nil),
			// dangling-ev is NOT seeded anywhere → no-hit → raw id preserved.
		},
		mutateIDs: []string{"charge-2"},
	}
	deps := interceptTestDeps{gc: fc}
	res := handleChargeClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"charge","thought":"th-1","polarity":"negative","weight":1.0,"reasoning":"r","evidence":["dangling-ev"]}`),
	})
	require.False(t, res.IsError, "charge should succeed: %s", toolResultText(res))
	require.Len(t, fc.execMutations, 1)
	edges := fc.execMutations[0].GetEdges()
	require.Len(t, edges, 2, "EdgeChargedBy + the dangling EdgeEvidencedBy (no-hit ID NOT dropped)")
	assert.Equal(t, "dangling-ev", edges[1].GetToId(), "no-hit evidence id preserved as raw (outcome c)")
}
