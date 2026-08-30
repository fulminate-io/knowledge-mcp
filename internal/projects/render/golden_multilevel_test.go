// SPDX-License-Identifier: Apache-2.0

// Package render — the structurally adequate golden.
//
// WHY THIS FILE EXISTS. Measured across the fifteen goldens in golden_test.go,
// nine contain exactly one ID line — a bare root with no tree at all — and the
// largest has four. That corpus cannot carry an equivalence claim about a tree
// renderer: sibling reordering is barely exercisable when almost no fixture has
// two children under one parent, and a node reached by two contains edges cannot
// be expressed in a four-node chain at all.
//
// The fixture below carries all three shapes at once: four levels
// (plan -> phase -> step -> criterion), THREE siblings under one parent so
// ordering is observable, and ONE criterion reached from TWO different steps so
// the diamond dedup is observable.
//
// It lives beside golden_test.go rather than inside it because that file sits
// at 450 lines against the repo's hard 500-line cap on a source file.

package render

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestGoldenPlanMultilevel(t *testing.T) {
	node := func(id, name string, typ kgtypes.NodeType, status string) *knowledgev1.Node {
		return &knowledgev1.Node{
			Id: id, Type: string(typ), SymbolName: name, Status: status,
			Description: name + " desc", Summary: name + " summary",
		}
	}

	f := newGraphFixture().
		addKnowledgeNode(node("ml-plan", "multilevel-plan", kgtypes.NodePlan, kgtypes.StatusActive)).
		addKnowledgeNode(node("ml-ph-1", "phase-one", kgtypes.NodePhase, kgtypes.StatusCompleted)).
		addKnowledgeNode(node("ml-ph-2", "phase-two", kgtypes.NodePhase, kgtypes.StatusActive)).
		addKnowledgeNode(node("ml-ph-3", "phase-three", kgtypes.NodePhase, kgtypes.StatusPending)).
		addKnowledgeNode(node("ml-st-a", "step-alpha", kgtypes.NodeStep, kgtypes.StatusPending)).
		addKnowledgeNode(node("ml-st-b", "step-beta", kgtypes.NodeStep, kgtypes.StatusPending)).
		addKnowledgeNode(node("ml-st-c", "step-gamma", kgtypes.NodeStep, kgtypes.StatusPending)).
		addKnowledgeNode(node("ml-crit", "shared-criterion", kgtypes.NodeCriterion, kgtypes.StatusPending)).
		// Three siblings under one parent — the ordering shape.
		link("ml-plan", "ml-ph-1").
		link("ml-plan", "ml-ph-2").
		link("ml-plan", "ml-ph-3").
		link("ml-ph-2", "ml-st-a").
		link("ml-ph-2", "ml-st-b").
		link("ml-ph-3", "ml-st-c").
		// THE DIAMOND: one criterion contained by two different steps. The
		// batched render attaches it under the first contains edge that reaches
		// it and skips the second, so it appears ONCE. The per-node walk it
		// replaced rendered it under both parents.
		link("ml-st-a", "ml-crit").
		link("ml-st-b", "ml-crit")

	text, err := callRender(context.Background(), f, map[string]any{"id": "ml-plan"})
	require.NoError(t, err)
	runGolden(t, "plan_multilevel", text,
		"ml-plan", "ml-ph-1", "ml-ph-2", "ml-ph-3",
		"ml-st-a", "ml-st-b", "ml-st-c", "ml-crit")
}
