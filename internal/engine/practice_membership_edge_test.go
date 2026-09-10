// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestPracticeMembershipEdge_RefusedOnTheCanonicalisedRelationship drives the
// SECOND position of the membership rule, which is the one the tools layer
// cannot reach.
//
// WHY IT IS TESTED HERE AND NOT THERE. The tools gate sees the caller's raw
// spelling and compares exactly, because folding an edge type merges two stored
// families. The relationship is canonicalised afterwards — resolveArgsEdgeTypes
// adopts the graph's stored spelling on a unique case-insensitive match — so a
// caller's `SOURCED-FROM` becomes `sourced-from` on its way to the write. This
// position runs on those canonicalised args, and it is the only place the
// variant can be refused without a fold anywhere.
func TestPracticeMembershipEdge_RefusedOnTheCanonicalisedRelationship(t *testing.T) {
	for _, op := range []string{"link", "unlink"} {
		t.Run(op+"/the canonical relationship is refused", func(t *testing.T) {
			err := refusePracticeMembershipEdgeArgs(json.RawMessage(
				`{"operation":"` + op + `","graph":"practice","from":"m-1","to":"hub-a",` +
					`"relationship":"` + string(kgtypes.EdgeSourcedFrom) + `"}`))
			require.Error(t, err, "the canonicalised membership relation must be refused at this position")
			assert.Contains(t, err.Error(), "not written by hand")
			assert.Contains(t, err.Error(), string(kgtypes.EdgeSourcedFrom))
		})

		t.Run(op+"/CONTROL: an ordinary relation passes", func(t *testing.T) {
			assert.NoError(t, refusePracticeMembershipEdgeArgs(json.RawMessage(
				`{"operation":"`+op+`","graph":"practice","from":"m-1","to":"m-2","relationship":"relates-to"}`)),
				"any other relationship is untouched by this rule")
		})
	}

	t.Run("CONTROL: another family's link is not this rule's business", func(t *testing.T) {
		assert.NoError(t, refusePracticeMembershipEdgeArgs(json.RawMessage(
			`{"operation":"link","from":"a","to":"b","relationship":"`+string(kgtypes.EdgeSourcedFrom)+`"}`)),
			"sourced-from means a practice membership only where practice hubs exist")
	})

	t.Run("CONTROL: a non-edge operation is not this rule's business", func(t *testing.T) {
		assert.NoError(t, refusePracticeMembershipEdgeArgs(json.RawMessage(
			`{"operation":"create","graph":"practice","type":"pattern","relationship":"`+
				string(kgtypes.EdgeSourcedFrom)+`"}`)),
			"only the two edge arms write an edge for this rule to refuse")
	})

	t.Run("the comparison is EXACT, and the case variant is this position's INPUT", func(t *testing.T) {
		// A raw variant never reaches here as a variant: the resolve above this
		// position rewrites it to the graph's stored spelling first. Asserting the
		// exactness directly is what keeps that reading honest — this gate does
		// not fold, and it does not need to.
		assert.False(t, IsPracticeMembershipEdge("SOURCED-FROM"),
			"this position compares exactly; the variant is canonicalised before it arrives")
		assert.True(t, IsPracticeMembershipEdge(string(kgtypes.EdgeSourcedFrom)))
	})
}

// TestPracticeMembershipEdge_DispatchRefusesBeforeAnyExecute drives the CALL
// SITE, not the predicate: the rule is stated in Dispatch after the edge-type
// resolve, and a test that only called the predicate would leave the wiring
// unobserved — holing the call site would keep every other row green.
func TestPracticeMembershipEdge_DispatchRefusesBeforeAnyExecute(t *testing.T) {
	calls := 0
	var exec ExecuteFn = func(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		calls++
		return &knowledgev1.ExecuteResponse{}, nil
	}
	var stats StatsFn = func(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
		// The graph already stores the canonical spelling, which is what makes the
		// resolve above the gate a resolve rather than an admission.
		return &knowledgev1.StatsResponse{GraphStats: &knowledgev1.GraphStats{
			EdgesByType: map[string]int64{string(kgtypes.EdgeSourcedFrom): 1, "relates-to": 1},
		}}, nil
	}

	res, err := Dispatch(context.Background(), exec, stats, "mutate", json.RawMessage(
		`{"operation":"unlink","graph":"practice","from":"m-1","to":"hub-a","relationship":"SOURCED-FROM"}`))
	require.NoError(t, err)
	require.True(t, res.IsError, "the canonicalised membership relation must be refused by the dispatch")
	require.NotEmpty(t, res.Content, "the refusal must carry its message")
	assert.Contains(t, res.Content[0].Text, "not written by hand")
	assert.Zero(t, calls, "and refused BEFORE any Execute — nothing may be written")

	t.Run("CONTROL: an ordinary relation reaches the write", func(t *testing.T) {
		calls = 0
		_, cerr := Dispatch(context.Background(), exec, stats, "mutate", json.RawMessage(
			`{"operation":"unlink","graph":"practice","from":"m-1","to":"m-2","relationship":"relates-to"}`))
		require.NoError(t, cerr)
		assert.Positive(t, calls, "any other relationship still reaches the Execute")
	})
}
