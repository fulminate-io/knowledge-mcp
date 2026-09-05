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
	Sections         []createPlanSection     `json:"sections,omitempty"`
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
	Summary     string `json:"summary"`
	Command     string `json:"command,omitempty"`
	Type        string `json:"type,omitempty"`
}

type createPlanQuestion struct {
	Question string `json:"question"`
	Summary  string `json:"summary,omitempty"`
	Context  string `json:"context,omitempty"`
}

type createPlanProposedPat struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Sketch  string `json:"sketch,omitempty"`
}

// InterceptCreatePlan handles the create_plan MCP call. Returns
// (false, _) for any other tool name so the chain falls through.
func InterceptCreatePlan(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "create_plan" {
		return false, kgtools.ToolResult{}
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("create_plan: graph caller unavailable")
	}

	var a createPlanArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("invalid arguments: " + decodeArgsError(params.Arguments, err))
	}
	// Ahead of every validation and every write: the decode above discards any
	// top-level key createPlanArgs has no field for, so an undeclared param would
	// otherwise vanish into a successful create. TOP-LEVEL ONLY — the nested
	// phases[]/steps[]/criteria[] keys (file_paths among them) are out of scope.
	if err := rejectSwallowedParamValues("create_plan", params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	if err := rejectUndeclaredParams("create_plan", "", CreatePlanToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	// The nested sections[] scan, beside the top-level one above: the decode
	// discards any nested key createPlanSection has no field for, and
	// rejectUndeclaredParams is TOP-LEVEL ONLY, so an undeclared section key
	// would otherwise vanish into a successful create.
	if err := rejectUndeclaredSectionKeys(params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	if err := validatePlanShape(&a); err != nil {
		return true, errorResult(err.Error())
	}

	clampWarnings, err := validatePlanSummaries(&a)
	if err != nil {
		return true, errorResult(err.Error())
	}

	planArgs := buildPlanArgsFromWire(a)

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
	res.warnings = append(res.warnings, clampWarnings...)

	nodes, edges, berr := projects.BuildPlanGraph(planArgs, res.unresolvedIDs, res.unresolvedLangIDs)
	if berr != nil {
		// PRE-WRITE and LOUD: the builder failed to encode a section's position,
		// and an edge without it is indistinguishable from an unpositioned one —
		// so the plan would persist and render its sections in arrival order with
		// nothing reporting the loss. Nothing has been written at this point.
		return true, errorResult("create plan: " + berr.Error())
	}

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

// validatePlanSummaries runs the indexed summary/description validation over
// the plan tree. EVERY summary it sees is the author's — plan, phase, step,
// criterion, proposed pattern and open question alike — and each is CLAMPED at a
// word boundary (with a non-fatal warning) via validate.ClampSummary. Step
// descriptions are validated via validate.StepDescription.
//
// Nothing composes a summary on this path any more, so there is no derived text
// to validate separately: an omitted one is refused under its indexed field
// path rather than filled in.
//
// Author clamps mutate a in place (the receiver is a pointer) so the clamped
// summaries flow into buildPlanArgsFromWire; emptiness still hard-rejects.
// Returns the accumulated clamp warnings plus the first hard validation error
// encountered.
func validatePlanSummaries(a *createPlanArgs) (warnings []string, err error) {
	if err := validate.Name("create_plan", a.Name); err != nil {
		return nil, err
	}
	clamped, w, cerr := validate.ClampSummary("create_plan", "summary", a.Summary)
	if cerr != nil {
		return nil, cerr
	}
	a.Summary = clamped
	if w != "" {
		warnings = append(warnings, w)
	}
	for i := range a.Phases {
		clamped, w, cerr := validate.ClampSummary("create_plan", fmt.Sprintf("phases[%d].summary", i), a.Phases[i].Summary)
		if cerr != nil {
			return nil, cerr
		}
		a.Phases[i].Summary = clamped
		if w != "" {
			warnings = append(warnings, w)
		}
		for j := range a.Phases[i].Steps {
			clamped, w, cerr := validate.ClampSummary("create_plan", fmt.Sprintf("phases[%d].steps[%d].summary", i, j), a.Phases[i].Steps[j].Summary)
			if cerr != nil {
				return nil, cerr
			}
			a.Phases[i].Steps[j].Summary = clamped
			if w != "" {
				warnings = append(warnings, w)
			}
			if err := validate.StepDescription("create_plan", fmt.Sprintf("phases[%d].steps[%d].description", i, j), a.Phases[i].Steps[j].Description); err != nil {
				return nil, err
			}
			cw, cerr := validateCriteria(i, j, a.Phases[i].Steps[j].Criteria)
			if cerr != nil {
				return nil, cerr
			}
			warnings = append(warnings, cw...)
		}
	}
	sw, serr := validatePlanSections(a.Sections)
	if serr != nil {
		return nil, serr
	}
	warnings = append(warnings, sw...)
	pw, perr := validateProposedPatternSummaries(a.ProposedPatterns)
	if perr != nil {
		return nil, perr
	}
	warnings = append(warnings, pw...)
	qw, qerr := validateOpenQuestionSummaries(a.OpenQuestions)
	if qerr != nil {
		return nil, qerr
	}
	warnings = append(warnings, qw...)
	return warnings, nil
}

// validateProposedPatternSummaries clamps each proposed pattern's author summary
// in place under its indexed field path. Split out of validatePlanSummaries to
// keep that function under the cognitive-complexity gate.
//
// ITERATION IS BY INDEX for the reason validateCriteria states: a range-value
// loop clamps a COPY and ships the unclamped summary into PersistBatch, passing
// every local assertion on the way.
func validateProposedPatternSummaries(pats []createPlanProposedPat) (warnings []string, err error) {
	for i := range pats {
		clamped, w, cerr := validate.ClampSummary("create_plan", fmt.Sprintf("proposed_patterns[%d].summary", i), pats[i].Summary)
		if cerr != nil {
			return nil, cerr
		}
		pats[i].Summary = clamped
		if w != "" {
			warnings = append(warnings, w)
		}
	}
	return warnings, nil
}

// validateOpenQuestionSummaries clamps each open question's author summary in
// place under its indexed field path. Split out for the same reason, and it
// iterates by index for the same reason.
func validateOpenQuestionSummaries(questions []createPlanQuestion) (warnings []string, err error) {
	for i := range questions {
		clamped, w, cerr := validate.ClampSummary("create_plan", fmt.Sprintf("open_questions[%d].summary", i), questions[i].Summary)
		if cerr != nil {
			return nil, cerr
		}
		questions[i].Summary = clamped
		if w != "" {
			warnings = append(warnings, w)
		}
	}
	return warnings, nil
}

// validateCriteria validates one step's criteria on two axes, both under the
// indexed criteria path so an author can find the offender in a tree of dozens.
// First the AUTHOR-SUPPLIED summary, clamped through validate.ClampSummary —
// required non-empty, word-boundary clamped over the cap with a non-fatal
// warning. Then the COMMAND's shape, gated through validate.RunSelectorGuard,
// which rejects a `go test` selector carrying no assertion that the selector
// matched anything.
//
// ITERATION IS BY INDEX, deliberately: `for k, c := range` would assign the
// clamp to a COPY, passing any assertion on the local value while shipping the
// unclamped summary into PersistBatch, where the server hard-refuses the whole
// create.
func validateCriteria(phaseIdx, stepIdx int, criteria []createPlanCriterion) ([]string, error) {
	var warnings []string
	for k := range criteria {
		clamped, w, cerr := validate.ClampSummary("create_plan", fmt.Sprintf("phases[%d].steps[%d].criteria[%d].summary", phaseIdx, stepIdx, k), criteria[k].Summary)
		if cerr != nil {
			return nil, cerr
		}
		criteria[k].Summary = clamped
		if w != "" {
			warnings = append(warnings, w)
		}
		if err := validate.RunSelectorGuard("create_plan", fmt.Sprintf("phases[%d].steps[%d].criteria[%d].command", phaseIdx, stepIdx, k), criteria[k].Command); err != nil {
			return nil, err
		}
	}
	return warnings, nil
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
	childIndex, byID, dependsOn, truncated := render.AssembleSubtree(ctx, gc, root.Id, 3)
	render.RenderTreeFromIndex(&sb, root, 0, 3, childIndex, dependsOn, nil)
	writeClientWarningsSection(&sb, warnings)
	if section := suggestPatternsForPlanClient(a.Name, a.Goal, a.NoPatternsReason); section != "" {
		sb.WriteString(section)
	}
	return render.AppendTruncationNotice(
		textResult(sb.String()+" [graph: knowledge/default]"), truncated, len(byID))
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
			Name:    pp.Name,
			Summary: pp.Summary,
			Sketch:  pp.Sketch,
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
					Summary:     c.Summary,
					Command:     c.Command,
					Type:        c.Type,
				})
			}
			phaseArgs.Steps = append(phaseArgs.Steps, stepArgs)
		}
		planArgs.Phases = append(planArgs.Phases, phaseArgs)
	}
	for _, sec := range a.Sections {
		planArgs.Sections = append(planArgs.Sections, projects.SectionArgs{
			Name:     sec.Name,
			Body:     sec.Body,
			Summary:  sec.Summary,
			Position: sec.Position,
		})
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

// writeClientWarningsSection HOISTS a `## Warnings` block to the TOP of sb when
// warnings is non-empty, above whatever the caller has written so far. Locked-
// format contract: the section header and `⚠ ` line prefix are the parse surface
// for the planner agent, and both are unchanged.
//
// IT PREPENDS RATHER THAN APPENDING, and the position is the whole point. Every
// warning this renderer carries qualifies the success the caller has already
// written — a summary the tool clamped on their behalf, a pattern id that
// resolved nowhere, text that will never be interpreted as parameters. Rendered
// below that success line it arrives after the reader has their answer, and in
// practice goes unread; a clamp that silently shortened authored text is exactly
// the kind of thing a caller finds out about later, from the stored node.
//
// The `prefix` parameter is GONE rather than repurposed. It used to let each
// call site choose the blank-line gap between its body and the section BELOW it;
// with the section on top there is one gap, in one place, and a per-site knob for
// it would be a lever that no longer selects anything.
//
// Prepending to a strings.Builder means reading its contents back and rewriting
// them — Builder has no insert. That is why the body is captured before Reset:
// the alternative is threading the warnings into fourteen call sites that each
// assemble their body differently, several of which append a trailing graph tag
// after this call and would have to be re-ordered individually.
func writeClientWarningsSection(sb *strings.Builder, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	body := sb.String()
	sb.Reset()
	sb.WriteString("## Warnings\n\n")
	for _, w := range warnings {
		fmt.Fprintf(sb, "⚠ %s\n", w)
	}
	sb.WriteString("\n")
	sb.WriteString(body)
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
