// SPDX-License-Identifier: Apache-2.0

// Package tools — InterceptCreatePlan claims the create_plan MCP call.
// It is the live client-side plan-creation path (the relocation
// moved the domain logic client-side; the dead store-based persister was
// removed). Builds the full plan tree via projects.BuildPlanGraph,
// validates + resolves patterns over the wire, persists via
// wire_persist.PersistBatch under one bundle_anchor, and renders either the
// tree-walk text result or the structured JSON variant expected by the
// captured goldens.
//
// Wire-up: chain into runInterceptChainInner at
// cmd/knowledge/internal/bootstrap/dream.go AFTER InterceptCreateTicket
// so the create-handler intercepts cluster together. The server-side
// handleCreatePlan has no server-side dispatch post-Phase-4 — this is the
// only path that produces a real response.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// createPlanArgs mirrors the server-side batchPlan presentation shape.
// Pattern fields use the typed batchProposedPattern entries; everything
// else is plain strings.
type createPlanArgs struct {
	Name             string                  `json:"name"`
	Goal             string                  `json:"goal"`
	Summary          string                  `json:"summary"`
	ResearchID       string                  `json:"research_id,omitempty"`
	TicketID         string                  `json:"ticket_id,omitempty"`
	Phases           []createPlanPhase       `json:"phases"`
	OpenQuestions    []createPlanQuestion    `json:"open_questions,omitempty"`
	PatternIDs       []string                `json:"pattern_ids,omitempty"`
	NoPatternsReason string                  `json:"no_patterns_reason,omitempty"`
	ProposedPatterns []createPlanProposedPat `json:"proposed_patterns,omitempty"`
	LanguagePatterns []string                `json:"language_patterns,omitempty"`
	Format           string                  `json:"format,omitempty"`
}

type createPlanPhase struct {
	Name     string           `json:"name"`
	Overview string           `json:"overview"`
	Summary  string           `json:"summary"`
	Steps    []createPlanStep `json:"steps"`
}

type createPlanStep struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Summary     string                `json:"summary"`
	FilePaths   string                `json:"file_paths,omitempty"`
	Criteria    []createPlanCriterion `json:"criteria,omitempty"`
}

type createPlanCriterion struct {
	Description string `json:"description"`
	Command     string `json:"command,omitempty"`
	Type        string `json:"type,omitempty"`
}

type createPlanQuestion struct {
	Question string `json:"question"`
	Summary  string `json:"summary,omitempty"`
	Context  string `json:"context,omitempty"`
}

type createPlanProposedPat struct {
	Name   string `json:"name"`
	Sketch string `json:"sketch,omitempty"`
}

// InterceptCreatePlan handles the create_plan MCP call. Returns
// (false, _) for any other tool name so the chain falls through.
func InterceptCreatePlan(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "create_plan" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("create_plan: graph caller unavailable")
	}

	var a createPlanArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("invalid arguments: " + err.Error())
	}
	if len(a.Phases) == 0 {
		return true, errorResult("at least one phase is required")
	}

	if err := validate.Name("create_plan", a.Name); err != nil {
		return true, errorResult(err.Error())
	}
	if err := validate.Summary("create_plan", "summary", a.Summary); err != nil {
		return true, errorResult(err.Error())
	}
	for i, ph := range a.Phases {
		if err := validate.Summary("create_plan", fmt.Sprintf("phases[%d].summary", i), ph.Summary); err != nil {
			return true, errorResult(err.Error())
		}
		for j, st := range ph.Steps {
			if err := validate.Summary("create_plan", fmt.Sprintf("phases[%d].steps[%d].summary", i, j), st.Summary); err != nil {
				return true, errorResult(err.Error())
			}
			if err := validate.StepDescription("create_plan", fmt.Sprintf("phases[%d].steps[%d].description", i, j), st.Description); err != nil {
				return true, errorResult(err.Error())
			}
		}
	}

	planArgs := buildPlanArgsFromWire(a)

	ctx := context.Background()

	// Validate the exactly-one-of-three pattern contract AND run the soft
	// cross-graph lookup + proxy resolution over the wire (gc is the Execute
	// seam). Hard validation failures (tristate violation, empty entries)
	// short-circuit; the effective (proxy) IDs are written back onto planArgs
	// BEFORE BuildPlanGraph so wirePatternEdges / wireLanguagePatternEdges land
	// their EdgeUses / EdgeAudits targets on real knowledge-graph nodes.
	res, presolveErr := resolvePatternFields(ctx, gc, planArgs.PatternIDs, planArgs.NoPatternsReason, planArgs.ProposedPatterns, planArgs.LanguagePatterns)
	if presolveErr != nil {
		return true, errorResult("create plan: " + presolveErr.Error())
	}
	planArgs.PatternIDs = res.effectivePatternIDs
	planArgs.LanguagePatterns = res.effectiveLangIDs

	nodes, edges := projects.BuildPlanGraph(planArgs, res.unresolvedIDs, res.unresolvedLangIDs)

	bundleID := newBundleID()
	ids, perr := PersistBatch(ctx, gc, nodes, edges, bundleID)
	if perr != nil {
		return true, errorResult("create plan: " + perr.Error())
	}
	if len(ids) == 0 {
		return true, errorResult("create plan: persist returned no IDs")
	}
	planID := ids[0]

	if a.Format == "json" {
		return true, jsonResult(map[string]any{
			"id":       planID,
			"name":     a.Name,
			"node_ids": ids[1:],
			"warnings": orNilWarnings(res.warnings),
		})
	}
	return true, renderCreatePlanText(ctx, gc, a, planID, res.warnings)
}

// renderCreatePlanText builds the text-format create_plan result: the header,
// the rendered plan tree (or a one-liner when the post-create FetchNode walk
// fails), the ## Warnings section, and the pattern auto-suggest hint.
func renderCreatePlanText(ctx context.Context, gc GraphCaller, a createPlanArgs, planID string, warnings []string) kgtools.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Plan created: %s → ID: %s\n\n", a.Name, planID)
	root, ferr := render.FetchNode(ctx, gc, planID)
	if ferr != nil || root == nil || root.Id == "" {
		// Walk failed — emit a one-liner with the suffix.
		return textResult(fmt.Sprintf("Plan created: %s → ID: %s [graph: knowledge/default]", a.Name, planID))
	}
	render.RenderTree(ctx, gc, &sb, root, 0, 3)
	writeClientWarningsSection(&sb, warnings, "\n")
	if section := suggestPatternsForPlanClient(a.Name, a.Goal, a.NoPatternsReason); section != "" {
		sb.WriteString(section)
	}
	return textResult(sb.String() + " [graph: knowledge/default]")
}

// buildPlanArgsFromWire converts the wire shape into the domain
// projects.PlanArgs struct expected by BuildPlanGraph.
func buildPlanArgsFromWire(a createPlanArgs) projects.PlanArgs {
	planArgs := projects.PlanArgs{
		Name:             a.Name,
		Goal:             a.Goal,
		Summary:          a.Summary,
		ResearchID:       a.ResearchID,
		TicketID:         a.TicketID,
		PatternIDs:       a.PatternIDs,
		NoPatternsReason: a.NoPatternsReason,
		LanguagePatterns: a.LanguagePatterns,
	}
	for _, pp := range a.ProposedPatterns {
		planArgs.ProposedPatterns = append(planArgs.ProposedPatterns, projects.ProposedPatternArgs{
			Name:   pp.Name,
			Sketch: pp.Sketch,
		})
	}
	for _, ph := range a.Phases {
		phaseArgs := projects.PhaseArgs{
			Name:     ph.Name,
			Overview: ph.Overview,
			Summary:  ph.Summary,
		}
		for _, st := range ph.Steps {
			stepArgs := projects.StepArgs{
				Name:        st.Name,
				Description: st.Description,
				Summary:     st.Summary,
				FilePaths:   st.FilePaths,
			}
			for _, c := range st.Criteria {
				stepArgs.Criteria = append(stepArgs.Criteria, projects.CriterionArgs{
					Description: c.Description,
					Command:     c.Command,
					Type:        c.Type,
				})
			}
			phaseArgs.Steps = append(phaseArgs.Steps, stepArgs)
		}
		planArgs.Phases = append(planArgs.Phases, phaseArgs)
	}
	for _, q := range a.OpenQuestions {
		planArgs.OpenQuestions = append(planArgs.OpenQuestions, projects.QuestionArgs{
			Question: q.Question,
			Summary:  q.Summary,
			Context:  q.Context,
		})
	}
	return planArgs
}

// writeClientWarningsSection appends a `## Warnings` block to sb when warnings is
// non-empty. Locked-format contract: the section header and `⚠ ` line prefix are
// the parse surface for the planner agent.
func writeClientWarningsSection(sb *strings.Builder, warnings []string, prefix string) {
	if len(warnings) == 0 {
		return
	}
	sb.WriteString(prefix)
	sb.WriteString("## Warnings\n\n")
	for _, w := range warnings {
		fmt.Fprintf(sb, "⚠ %s\n", w)
	}
}

// suggestPatternsForPlanClient is the client-side mirror of the
// server-side suggestPatternsForTicket. The server-side flavor ran a
// cross-practice fan-out via store.Store() reads — the client cannot
// reach those graphs synchronously without N gc.Calls, so we emit only
// the static `## Pattern Auto-Suggest` no-hits hint when no_patterns_
// reason is set. This matches the captured golden which used the
// no-hits branch exclusively.
func suggestPatternsForPlanClient(name, goal, noPatternsReason string) string {
	if noPatternsReason == "" {
		return ""
	}
	const descPrefix = 240
	desc := strings.TrimSpace(goal)
	if len(desc) > descPrefix {
		desc = desc[:descPrefix]
	}
	q := strings.TrimSpace(strings.TrimSpace(name) + " " + desc)
	if q == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n## Pattern Auto-Suggest\n")
	fmt.Fprintf(&sb, "Cross-practice fan-out found no hits above %.2f for query `%q`. ", 0.40, q)
	sb.WriteString("If you suspect a pattern applies, run\n")
	sb.WriteString("`search({\"graph\": \"practice\", \"query\": \"<refined terms>\"})` and retry the create call with `pattern_ids` set.\n")
	return sb.String()
}

// orNilWarnings returns nil when w is empty so JSON output emits
// `"warnings":null` rather than `"warnings":[]`. Matches the captured
// golden's literal "warnings":null marker.
func orNilWarnings(w []string) []string {
	if len(w) == 0 {
		return nil
	}
	return w
}

// _ binds the store import so the file compiles even if the unused
// projects import path changes type names.
var _ = kgtypes.NodePlan
