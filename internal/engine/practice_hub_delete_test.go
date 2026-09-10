// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestPracticeHubDelete_OneSelectionForBothSides is the ENGINE-package observer
// for the by-hub delete, and it exists because this package had none.
//
// WHAT WENT WRONG WITHOUT ONE. The by-hub logic lived here and was asserted only
// from the tools package, through a fake that answered every read with a canned
// two-node response — so the rendered count came from the fake rather than from
// the plan, and a preview that resolved the hub ALONE stayed green while the real
// delete removed three nodes. A row in the package that owns the code, reading
// the plan rather than the answer, is what catches that.
//
// THE PROPERTY IS THAT BOTH SIDES SELECT THE SAME WAY: one metadata predicate on
// the hub key, and NO ids list. `ids` short-circuits the predicate arms on both
// the read and the write path, so a plan carrying both resolves the ids alone —
// which on the preview meant listing the hub and none of its members, and on the
// write would have meant the reverse.
func TestPracticeHubDelete_OneSelectionForBothSides(t *testing.T) {
	const hub = "hub-under-test"

	var plans []*knowledgev1.ExecuteRequest
	var stats StatsFn = func(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
		return &knowledgev1.StatsResponse{GraphStats: &knowledgev1.GraphStats{}}, nil
	}

	// The hub the guard resolves, answered by the same exec: a live `source` node
	// carrying its OWN id, which is the hub contract.
	hubNode := &knowledgev1.Node{
		Id: hub, Type: string(kgtypes.NodeSource),
		Metadata: map[string]string{kgtypes.MetaKeySourceHub: hub},
	}
	var exec ExecuteFn = func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		plans = append(plans, req)
		if q, isQuery := req.GetPlan().(*knowledgev1.ExecuteRequest_Query); isQuery && len(q.Query.GetIds()) > 0 {
			return enginetest.ResponseWithNodes(hubNode), nil
		}
		return &knowledgev1.ExecuteResponse{AffectedCount: 3}, nil
	}

	t.Run("the real delete is ONE write selecting by the hub key alone", func(t *testing.T) {
		plans = nil
		_, err := Dispatch(context.Background(), exec, stats, "delete", json.RawMessage(
			`{"graph":"practice","source":"`+hub+`"}`))
		require.NoError(t, err)

		var writes []*knowledgev1.MutationPlan
		for _, p := range plans {
			if m, isMutation := p.GetPlan().(*knowledgev1.ExecuteRequest_Mutation); isMutation {
				writes = append(writes, m.Mutation)
			}
		}
		require.Len(t, writes, 1, "ONE write: the hub is swept by its own key, not removed by a second call")
		sel := writes[0].GetSelection()
		require.Len(t, sel.GetMetadataPredicates(), 1)
		assert.Equal(t, kgtypes.MetaKeySourceHub, sel.GetMetadataPredicates()[0].GetKey())
		assert.Equal(t, hub, sel.GetMetadataPredicates()[0].GetValue())
		assert.Empty(t, sel.GetIds(), "and no ids list to short-circuit the predicate")
	})

	t.Run("the dry run reads the SAME selection", func(t *testing.T) {
		plans = nil
		_, err := Dispatch(context.Background(), exec, stats, "delete", json.RawMessage(
			`{"graph":"practice","source":"`+hub+`","dry_run":true}`))
		require.NoError(t, err)

		var previews []*knowledgev1.QueryPlan
		for _, p := range plans {
			if q, isQuery := p.GetPlan().(*knowledgev1.ExecuteRequest_Query); isQuery {
				if len(q.Query.GetSelection().GetMetadataPredicates()) > 0 {
					previews = append(previews, q.Query)
				}
			}
		}
		require.Len(t, previews, 1)
		assert.Equal(t, hub, previews[0].GetSelection().GetMetadataPredicates()[0].GetValue(),
			"the preview resolves the set the delete removes")
		assert.Empty(t, previews[0].GetIds(), "and carries no ids list either")

		var writes int
		for _, p := range plans {
			if _, isMutation := p.GetPlan().(*knowledgev1.ExecuteRequest_Mutation); isMutation {
				writes++
			}
		}
		assert.Zero(t, writes, "a dry run writes nothing")
	})
}
