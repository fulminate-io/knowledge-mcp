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
	Summary     string `json:"summary"`
	Command     string `json:"command,omitempty"`
	Type        string `json:"type,omitempty"`
}

// InterceptCreateTestPlan handles the create_test_plan MCP call.
func InterceptCreateTestPlan(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "create_test_plan" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("create_test_plan: graph caller unavailable")
	}
	var a createTestPlanArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("invalid arguments: " + decodeArgsError(params.Arguments, err))
	}
	// Ahead of every validation and every write: the decode above discards any
	// top-level key createTestPlanArgs has no field for, so an undeclared param
	// would otherwise vanish into a successful create.
	if err := rejectSwallowedParamValues("create_test_plan", params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	if err := rejectUndeclaredParams("create_test_plan", "", CreateTestPlanToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	if len(a.Steps) == 0 {
		return true, errorResult("at least one step is required")
	}
	warnings, verr := clampTestPlanSummaries(&a)
	if verr != nil {
		return true, errorResult(verr.Error())
	}

	planArgs := projects.TestPlanArgs{
		Name:    a.Name,
		Goal:    a.Goal,
		Summary: a.Summary,
	}
	for i, st := range a.Steps {
		stepArgs := projects.TestStepArgs{
			Name:        st.Name,
			Description: st.Description,
			Summary:     st.Summary,
		}
		criteria, cerr := buildTestPlanCriteria(i, st.Criteria)
		if cerr != nil {
			return true, errorResult(cerr.Error())
		}
		stepArgs.Criteria = criteria
		planArgs.Steps = append(planArgs.Steps, stepArgs)
	}

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
			"warnings": orNilWarnings(warnings),
		})
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Test plan created: %s → ID: %s\n\n", a.Name, planID)
	root, ferr := render.FetchNode(ctx, gc, planID)
	if ferr != nil || root == nil || root.Id == "" {
		var oneLine strings.Builder
		fmt.Fprintf(&oneLine, "Test plan created: %s → ID: %s", a.Name, planID)
		writeClientWarningsSection(&oneLine, warnings)
		return true, textResult(oneLine.String() + " [graph: knowledge/default]")
	}
	childIndex, byID, dependsOn, truncated := render.AssembleSubtree(ctx, gc, root.Id, 3)
	render.RenderTreeFromIndex(&sb, root, 0, 3, childIndex, dependsOn, nil)
	writeClientWarningsSection(&sb, warnings)
	return true, render.AppendTruncationNotice(
		textResult(sb.String()+" [graph: knowledge/default]"), truncated, len(byID))
}

// buildTestPlanCriteria converts one step's wire criteria into the builder's
// CriterionArgs, guarding each command's shape on the way. These criteria reach
// the graph through the test-plan builder, which runs no criterion validation of
// its own, so the command's shape is checked here — under the indexed path that
// locates the offender in a plan of dozens.
//
// The SUMMARY is already clamped in place by clampTestPlanSummaries before this
// runs, so the value copied here is the clamped one.
func buildTestPlanCriteria(stepIdx int, criteria []createTestPlanCriterion) ([]projects.CriterionArgs, error) {
	// NIL, not an empty slice, for a step with no criteria: the append-into-nil
	// shape this replaced left the field nil, and BuildTestPlanGraph reads it.
	if len(criteria) == 0 {
		return nil, nil
	}
	out := make([]projects.CriterionArgs, 0, len(criteria))
	for k, c := range criteria {
		if gerr := validate.RunSelectorGuard("create_test_plan",
			fmt.Sprintf("steps[%d].criteria[%d].command", stepIdx, k), c.Command); gerr != nil {
			return nil, gerr
		}
		out = append(out, projects.CriterionArgs{
			Description: c.Description,
			Summary:     c.Summary,
			Command:     c.Command,
			Type:        c.Type,
		})
	}
	return out, nil
}

// clampTestPlanSummaries validates the test-plan name and clamps the
// author-supplied plan + step + criterion summaries in place (a is a pointer so
// the clamped values flow into BuildTestPlanGraph). Each author summary is
// clamped at a word boundary with a non-fatal warning rather than hard-rejected;
// emptiness still hard-rejects. Returns the accumulated clamp warnings plus the
// first hard validation error.
//
// The criteria loop ITERATES BY INDEX: `for k, c := range` would clamp a COPY
// and ship the unclamped summary into PersistBatch, where the server refuses the
// whole create.
func clampTestPlanSummaries(a *createTestPlanArgs) (warnings []string, err error) {
	if err := validate.Name("create_test_plan", a.Name); err != nil {
		return nil, err
	}
	clamped, w, cerr := validate.ClampSummary("create_test_plan", "summary", a.Summary)
	if cerr != nil {
		return nil, cerr
	}
	a.Summary = clamped
	if w != "" {
		warnings = append(warnings, w)
	}
	for i := range a.Steps {
		c, sw, scerr := validate.ClampSummary("create_test_plan", fmt.Sprintf("steps[%d].summary", i), a.Steps[i].Summary)
		if scerr != nil {
			return nil, scerr
		}
		a.Steps[i].Summary = c
		if sw != "" {
			warnings = append(warnings, sw)
		}
		for k := range a.Steps[i].Criteria {
			cc, cw, ccerr := validate.ClampSummary("create_test_plan", fmt.Sprintf("steps[%d].criteria[%d].summary", i, k), a.Steps[i].Criteria[k].Summary)
			if ccerr != nil {
				return nil, ccerr
			}
			a.Steps[i].Criteria[k].Summary = cc
			if cw != "" {
				warnings = append(warnings, cw)
			}
		}
	}
	return warnings, nil
}
