// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptCreateTestPlan claims the create_test_plan
// MCP call after the relocation. The server has no create_test_plan handler
// has no server-side dispatch post-Phase-4.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/projects"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// createTestPlanArgs mirrors the server-side batchTestPlan shape.
type createTestPlanArgs struct {
	Name    string               `json:"name"`
	Goal    string               `json:"goal"`
	Summary string               `json:"summary"`
	Steps   []createTestPlanStep `json:"steps"`
	Format  string               `json:"format,omitempty"`
}

type createTestPlanStep struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Summary     string                    `json:"summary"`
	Criteria    []createTestPlanCriterion `json:"criteria,omitempty"`
}

type createTestPlanCriterion struct {
	Description string `json:"description"`
	Command     string `json:"command,omitempty"`
	Type        string `json:"type,omitempty"`
}

// InterceptCreateTestPlan handles the create_test_plan MCP call.
func InterceptCreateTestPlan(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "create_test_plan" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("create_test_plan: graph caller unavailable")
	}
	var a createTestPlanArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("invalid arguments: " + err.Error())
	}
	if len(a.Steps) == 0 {
		return true, errorResult("at least one step is required")
	}
	if err := validate.Name("create_test_plan", a.Name); err != nil {
		return true, errorResult(err.Error())
	}
	if err := validate.Summary("create_test_plan", "summary", a.Summary); err != nil {
		return true, errorResult(err.Error())
	}
	for i, st := range a.Steps {
		if err := validate.Summary("create_test_plan", fmt.Sprintf("steps[%d].summary", i), st.Summary); err != nil {
			return true, errorResult(err.Error())
		}
	}

	planArgs := projects.TestPlanArgs{
		Name:    a.Name,
		Goal:    a.Goal,
		Summary: a.Summary,
	}
	for _, st := range a.Steps {
		stepArgs := projects.TestStepArgs{
			Name:        st.Name,
			Description: st.Description,
			Summary:     st.Summary,
		}
		for _, c := range st.Criteria {
			stepArgs.Criteria = append(stepArgs.Criteria, projects.CriterionArgs{
				Description: c.Description,
				Command:     c.Command,
				Type:        c.Type,
			})
		}
		planArgs.Steps = append(planArgs.Steps, stepArgs)
	}

	ctx := context.Background()
	nodes, edges := projects.BuildTestPlanGraph(planArgs)
	bundleID := newBundleID()
	ids, perr := PersistBatch(ctx, gc, nodes, edges, bundleID)
	if perr != nil {
		return true, errorResult("create test plan: " + perr.Error())
	}
	if len(ids) == 0 {
		return true, errorResult("create test plan: persist returned no IDs")
	}
	planID := ids[0]
	if a.Format == "json" {
		return true, jsonResult(map[string]any{
			"id":       planID,
			"name":     a.Name,
			"step_ids": ids[1:],
		})
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Test plan created: %s → ID: %s\n\n", a.Name, planID)
	root, ferr := render.FetchNode(ctx, gc, planID)
	if ferr != nil || root == nil || root.Id == "" {
		return true, textResult(fmt.Sprintf("Test plan created: %s → ID: %s [graph: knowledge/default]", a.Name, planID))
	}
	render.RenderTree(ctx, gc, &sb, root, 0, 3)
	return true, textResult(sb.String() + " [graph: knowledge/default]")
}
