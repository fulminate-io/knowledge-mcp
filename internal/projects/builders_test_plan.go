// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// BuildTestPlanGraph assembles the full node/edge graph for a test plan
// (test_plan → test_steps → criteria).
func BuildTestPlanGraph(plan TestPlanArgs) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	var nodes []*knowledgev1.Node
	var edges []kgwire.BatchEdge

	// TestPlan root node (index 0) — no status, it is a reusable template.
	planIdx := len(nodes)
	nodes = append(nodes, &knowledgev1.Node{
		Type:        string(kgtypes.NodeTestPlan),
		Source:      "llm:claude",
		SymbolName:  plan.Name,
		Description: plan.Goal,
		Summary:     plan.Summary,
	})

	prevStepIdx := -1
	for _, step := range plan.Steps {
		stepIdx := len(nodes)
		nodes = append(nodes, &knowledgev1.Node{
			Type:        string(kgtypes.NodeTestStep),
			Source:      "llm:claude",
			SymbolName:  step.Name,
			Description: step.Description,
			Summary:     step.Summary,
		})
		edges = append(edges, kgwire.BatchEdge{FromIdx: planIdx, ToIdx: stepIdx, Type: kgtypes.EdgeKGContains})
		if prevStepIdx >= 0 {
			edges = append(edges, kgwire.BatchEdge{FromIdx: stepIdx, ToIdx: prevStepIdx, Type: kgtypes.EdgeDependsOn})
		}
		prevStepIdx = stepIdx

		for _, c := range step.Criteria {
			cIdx := len(nodes)
			nodes = append(nodes, BuildCriterionNode(c))
			edges = append(edges, kgwire.BatchEdge{FromIdx: cIdx, ToIdx: stepIdx, Type: kgtypes.EdgeVerifies})
			edges = append(edges, kgwire.BatchEdge{FromIdx: stepIdx, ToIdx: cIdx, Type: kgtypes.EdgeKGContains})
		}
	}

	return nodes, edges
}
