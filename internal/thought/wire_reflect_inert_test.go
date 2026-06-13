// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// captureCaller records the last ExecuteRequest it received so a test can inspect
// the compiled MutationPlan (specifically the reflect_inert_writeback flag).
type captureCaller struct {
	last *knowledgev1.ExecuteRequest
}

func (c *captureCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.last = req
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestExecuteReflectInertMutate_SetsFlag proves the
// programmatic flag-set lands on the compiled proto, and ONLY via the inert
// helper: executeReflectInertMutate marks ReflectInertWriteback=true, while the
// plain executeViaEngine path leaves it false.
func TestExecuteReflectInertMutate_SetsFlag(t *testing.T) {
	ctx := context.Background()

	// A bulk_update_metadata call lowers to MUTATION_KIND_UPDATE_ITEMS — the same
	// shape the reflection writeback emits (cluster_id / propagated_*).
	bulkArgs, err := json.Marshal(map[string]any{
		"operation": "bulk_update_metadata",
		"updates": []map[string]any{
			{"id": "th-1", "metadata": map[string]string{"cluster_id": "c-1"}},
		},
	})
	require.NoError(t, err)

	// (1) Inert helper sets the flag on the compiled MutationPlan.
	inert := &captureCaller{}
	err = executeReflectInertMutate(ctx, inert, bulkArgs)
	require.NoError(t, err)
	require.NotNil(t, inert.last)
	require.NotNil(t, inert.last.GetMutation(), "bulk_update_metadata must compile to a MutationPlan")
	assert.True(t, inert.last.GetMutation().GetReflectInertWriteback(),
		"executeReflectInertMutate must set ReflectInertWriteback=true")

	// (2) Plain executeViaEngine leaves the flag false.
	plain := &captureCaller{}
	_, err = executeViaEngine(ctx, plain, "mutate", bulkArgs)
	require.NoError(t, err)
	require.NotNil(t, plain.last)
	require.NotNil(t, plain.last.GetMutation())
	assert.False(t, plain.last.GetMutation().GetReflectInertWriteback(),
		"executeViaEngine must NOT set ReflectInertWriteback")
}
