// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// agentNode builds a NodeAgent fixture with a given id + SymbolName (the role
// name a user-authored agent node carries, e.g. "planner").
func agentNode(id, name string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeAgent), SymbolName: name}
}

// TestResolveOriginAgentID asserts a known role resolves to its agent node, an
// unresolvable value resolves to "", and duplicate SymbolNames resolve to the
// LOWEST id deterministically regardless of browse order (with the collision
// surfaced by buildAgentNameToID).
func TestResolveOriginAgentID(t *testing.T) {
	// Two "planner" agent nodes stored in NON-sorted browse order ("bbb" before
	// "aaa") — the deterministic tie-break must still pick "aaa".
	fc := &backfillFakeCaller{
		agents: []*knowledgev1.Node{
			agentNode("bbb", "planner"),
			agentNode("aaa", "planner"),
			agentNode("imp-1", "implementer"),
		},
	}
	ctx := context.Background()

	// (1) Known role resolves to the lowest id on a SymbolName collision.
	got := resolveOriginAgentID(ctx, fc, "planner")
	assert.Equal(t, "aaa", got, "duplicate planner SymbolNames must resolve to the lowest id deterministically")

	// (2) Determinism across runs: a second call returns the same id (a
	// map[name]=lastSeenId build with no sort would flap with browse order).
	again := resolveOriginAgentID(ctx, fc, "planner")
	assert.Equal(t, "aaa", again, "resolution must be stable across runs")

	// (3) A non-duplicate role resolves to its single agent node.
	assert.Equal(t, "imp-1", resolveOriginAgentID(ctx, fc, "implementer"))

	// (4) Unresolvable values (no agent node of that name) degrade to "" — metadata
	// only, no hub edge.
	assert.Empty(t, resolveOriginAgentID(ctx, fc, "main"), "main has no agent node — degrade to empty")
	assert.Empty(t, resolveOriginAgentID(ctx, fc, "orchestrator"))
	assert.Empty(t, resolveOriginAgentID(ctx, fc, "custom-role"))

	// (5) Empty origin normalizes to "main", which has no agent node → "".
	assert.Empty(t, resolveOriginAgentID(ctx, fc, ""))

	// (6) buildAgentNameToID surfaces the collision count: exactly one name
	// ("planner") has >1 id.
	nameToID, collisions, err := buildAgentNameToID(ctx, fc)
	require.NoError(t, err)
	assert.Equal(t, 1, collisions, "exactly one SymbolName (planner) collides")
	assert.Equal(t, "aaa", nameToID["planner"])
	assert.Equal(t, "imp-1", nameToID["implementer"])
}
