// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// seedLineageFixture seeds the step → phase → plan → ticket →
// project chain via contains edges. Returns the step ID — the
// lineage walk starts there.
func seedLineageFixture() (*parityGraphFixture, string) {
	f := newParityFixture()
	stepID := "00000000000000000000000000000111"
	phaseID := "00000000000000000000000000000222"
	planID := "00000000000000000000000000000333"
	ticketID := "00000000000000000000000000000444"
	projID := "00000000000000000000000000000555"

	f.add(&knowledgev1.Node{Id: stepID, Type: string(kgtypes.NodeStep), SymbolName: "lin-step",
		Status: "pending", Summary: "ls sum", Description: "ls desc"})
	f.add(&knowledgev1.Node{Id: phaseID, Type: string(kgtypes.NodePhase), SymbolName: "lin-phase",
		Status: "pending", Summary: "lp psum", Description: "lp over"})
	f.add(&knowledgev1.Node{Id: planID, Type: string(kgtypes.NodePlan), SymbolName: "lin-plan",
		Status: "active", Summary: "lp sum", Description: "lp goal"})
	f.add(&knowledgev1.Node{Id: ticketID, Type: string(kgtypes.NodeTicket), SymbolName: "lin-ticket",
		Status: "open", Summary: "lt sum", Description: "lt desc"})
	f.add(&knowledgev1.Node{Id: projID, Type: string(kgtypes.NodeProject), SymbolName: "lin-project",
		Status: "active", Summary: "lp sum", Description: "lp desc"})

	// Contains chain (parent → child).
	f.link(phaseID, stepID)
	f.link(planID, phaseID)
	f.link(ticketID, planID)
	f.link(projID, ticketID)

	return f, stepID
}

func TestInterceptQueryLineage_TextFormat_DeepChain_ByteIdentical(t *testing.T) {
	f, stepID := seedLineageFixture()
	deps := &parityDeps{gc: f.gc()}
	args := mustMarshal(t, map[string]any{"mode": "lineage", "id": stepID})

	handled, res := InterceptQueryLineage(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept error: %v", res.Content)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "lineage")
	assert.Equal(t, want, got)
}

func TestInterceptQueryLineage_JSONFormat_ByteIdentical(t *testing.T) {
	f, stepID := seedLineageFixture()
	deps := &parityDeps{gc: f.gc()}
	args := mustMarshal(t, map[string]any{"mode": "lineage", "id": stepID, "format": "json"})

	handled, res := InterceptQueryLineage(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "lineage.json")
	assert.Equal(t, want, got)
}

func TestInterceptQueryLineage_MissingID_Errors(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "lineage"})
	handled, res := InterceptQueryLineage(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, extractText(res), "lineage mode requires 'id' parameter")
}

// Root-node fallthrough: a node with no parents emits the special
// "(no lineage found — this is a root node)" suffix.
func TestInterceptQueryLineage_RootNode_ShowsHint(t *testing.T) {
	f := newParityFixture()
	rootID := "00000000000000000000000000000aaa"
	f.add(&knowledgev1.Node{Id: rootID, Type: string(kgtypes.NodeProject), SymbolName: "root-node",
		Status: "active", Summary: "rsum", Description: "rdesc"})

	deps := &parityDeps{gc: f.gc()}
	args := mustMarshal(t, map[string]any{"mode": "lineage", "id": rootID})

	handled, res := InterceptQueryLineage(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError)
	assert.Contains(t, extractText(res), "(no lineage found — this is a root node)")
}

func TestInterceptQueryLineage_WrongTool_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "lineage", "id": "x"})
	handled, _ := InterceptQueryLineage(deps, kgtools.CallToolParams{Name: "search", Arguments: args})
	assert.False(t, handled)
}

func TestInterceptQueryLineage_WrongMode_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "evidence", "id": "x"})
	handled, _ := InterceptQueryLineage(deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled)
}
