// SPDX-License-Identifier: Apache-2.0

// Package projects builds the node/edge graphs for project/ticket/plan/
// test-plan creation (the Build* helpers) plus the ticket/plan pattern
// validation used by the MCP create handlers.

package projects

import (
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// BuildPlanGraph assembles the full node/edge graph for a plan
// (project → phases → steps → criteria + open questions + patterns).
//
// unresolvedPatternIDs is the list of pattern IDs that ValidatePatternFields
// flagged as missing or wrong-type. They are persisted on the plan node as
// the unresolved_pattern_ids metadata key so a later query(id: planID)
// surfaces "this plan references X unresolved pattern IDs" without
// re-validating.
//
// unresolvedLanguagePatternIDs is the language-pattern equivalent. IDs that
// fail ValidateLanguagePatterns are persisted on the plan node as the
// unresolved_language_patterns metadata key. Language-pattern wiring is
// independent of architectural pattern wiring — both can co-exist on the
// same plan.
func BuildPlanGraph(plan PlanArgs, unresolvedPatternIDs []string, unresolvedLanguagePatternIDs []string) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	var nodes []*knowledgev1.Node
	var edges []kgwire.BatchEdge

	// Plan node (index 0). Pattern-shaped metadata (no_patterns_reason,
	// unresolved_pattern_ids) is set here so the plan node carries it from
	// creation onward — a later query(id: planID) surfaces the warning
	// without re-validating.
	projectIdx := len(nodes)
	planNode := &knowledgev1.Node{
		Type:        string(kgtypes.NodePlan),
		Source:      "llm:claude",
		SymbolName:  plan.Name,
		Description: plan.Goal,
		Summary:     plan.Summary,
		Status:      kgtypes.StatusActive,
	}
	if plan.NoPatternsReason != "" {
		kgtypes.SetValue(planNode, "no_patterns_reason", plan.NoPatternsReason)
	}
	if len(unresolvedPatternIDs) > 0 {
		kgtypes.SetValue(planNode, "unresolved_pattern_ids", strings.Join(unresolvedPatternIDs, ","))
	}
	if len(unresolvedLanguagePatternIDs) > 0 {
		kgtypes.SetValue(planNode, "unresolved_language_patterns", strings.Join(unresolvedLanguagePatternIDs, ","))
	}
	nodes = append(nodes, planNode)

	if plan.ResearchID != "" {
		edges = append(edges, kgwire.BatchEdge{FromIdx: projectIdx, ToIdx: -1, ToID: plan.ResearchID, Type: kgtypes.EdgeInformedBy})
	}
	if plan.TicketID != "" {
		edges = append(edges, kgwire.BatchEdge{FromIdx: -1, FromID: plan.TicketID, ToIdx: projectIdx, Type: kgtypes.EdgeKGContains})
	}

	// Pattern wiring: plan → existing pattern uses edges, plus eager creation
	// of "emerging" pattern nodes for proposed_patterns.
	nodes, edges = wirePatternEdges(projectIdx, plan, nodes, edges)

	// Language-pattern wiring: plan → language_pattern audits edges. No
	// proposed-language-pattern counterpart — language_patterns has no
	// eager-create variant.
	edges = wireLanguagePatternEdges(projectIdx, plan.LanguagePatterns, edges)

	prevPhaseIdx := -1
	for _, phase := range plan.Phases {
		nodes, edges, prevPhaseIdx = appendPhaseSubtree(projectIdx, prevPhaseIdx, phase, nodes, edges)
	}

	for _, q := range plan.OpenQuestions {
		qIdx := len(nodes)
		summary := q.Summary
		if summary == "" {
			summary = DeriveQuestionSummary(q.Question, q.Context)
		}
		nodes = append(nodes, &knowledgev1.Node{
			Type:        string(kgtypes.NodeQuestion),
			Source:      "llm:claude",
			SymbolName:  q.Question,
			Description: q.Context,
			Summary:     summary,
			Status:      kgtypes.StatusOpen,
		})
		edges = append(edges, kgwire.BatchEdge{FromIdx: projectIdx, ToIdx: qIdx, Type: kgtypes.EdgeKGContains})
	}
	return nodes, edges
}

// wirePatternEdges adds plan→pattern EdgeUses edges for each PatternID and
// eagerly creates a NodePattern (status=emerging) for each ProposedPattern,
// linking plan→new-pattern via EdgeUses. Returns the augmented node and edge
// slices. PatternID lookups + cross-graph resolution happen earlier (the
// create_plan interceptor's ValidatePatternFields + ResolvePatternIDsToEffectiveIDs);
// this helper only wires graph structure, using plan.PatternIDs AS-IS as edge targets.
func wirePatternEdges(planIdx int, plan PlanArgs, nodes []*knowledgev1.Node, edges []kgwire.BatchEdge) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	for _, pid := range plan.PatternIDs {
		edges = append(edges, kgwire.BatchEdge{FromIdx: planIdx, ToIdx: -1, ToID: pid, Type: kgtypes.EdgeUses})
	}
	for _, p := range plan.ProposedPatterns {
		patIdx := len(nodes)
		patNode := &knowledgev1.Node{
			Type:        string(kgtypes.NodePattern),
			Source:      "llm:claude",
			SymbolName:  p.Name,
			Description: p.Sketch,
			Summary:     DerivePatternSummary(p.Name, p.Sketch),
			Status:      "emerging",
		}
		if p.Sketch != "" {
			kgtypes.SetValue(patNode, "shape", p.Sketch)
		}
		// The plan→pattern EdgeUses edge below IS the relationship —
		// no proposed_in_plan metadata is written, since the plan's ID
		// is not known until after the create-batch wire call and the
		// mutate(create_batch) seam lacks a metadata-update primitive.
		nodes = append(nodes, patNode)
		edges = append(edges, kgwire.BatchEdge{FromIdx: planIdx, ToIdx: patIdx, Type: kgtypes.EdgeUses})
	}
	return nodes, edges
}

// appendPhaseSubtree appends a phase + its steps + criteria, returning the
// updated node/edge slices and the new prevPhaseIdx for depends-on chaining.
// Extracted from BuildPlanGraph to keep the parent under the 80-line cap.
func appendPhaseSubtree(planIdx, prevPhaseIdx int, phase PhaseArgs, nodes []*knowledgev1.Node, edges []kgwire.BatchEdge) ([]*knowledgev1.Node, []kgwire.BatchEdge, int) {
	phaseIdx := len(nodes)
	nodes = append(nodes, &knowledgev1.Node{
		Type:        string(kgtypes.NodePhase),
		Source:      "llm:claude",
		SymbolName:  phase.Name,
		Description: phase.Overview,
		Summary:     phase.Summary,
		Status:      kgtypes.StatusPending,
	})
	edges = append(edges, kgwire.BatchEdge{FromIdx: planIdx, ToIdx: phaseIdx, Type: kgtypes.EdgeKGContains})
	if prevPhaseIdx >= 0 {
		edges = append(edges, kgwire.BatchEdge{FromIdx: phaseIdx, ToIdx: prevPhaseIdx, Type: kgtypes.EdgeDependsOn})
	}

	prevStepIdx := -1
	for _, step := range phase.Steps {
		nodes, edges, prevStepIdx = appendStepSubtree(phaseIdx, prevStepIdx, step, nodes, edges)
	}
	return nodes, edges, phaseIdx
}

// appendStepSubtree appends a step + its file-path implements edges + its
// criteria, returning the updated node/edge slices and the new prevStepIdx.
func appendStepSubtree(phaseIdx, prevStepIdx int, step StepArgs, nodes []*knowledgev1.Node, edges []kgwire.BatchEdge) ([]*knowledgev1.Node, []kgwire.BatchEdge, int) {
	stepIdx := len(nodes)
	stepNode := &knowledgev1.Node{
		Type:        string(kgtypes.NodeStep),
		Source:      "llm:claude",
		SymbolName:  step.Name,
		Description: step.Description,
		Summary:     step.Summary,
		Status:      kgtypes.StatusPending,
	}
	if step.FilePaths != "" {
		kgtypes.SetValue(stepNode, "file_paths", step.FilePaths)
	}
	nodes = append(nodes, stepNode)
	edges = append(edges, kgwire.BatchEdge{FromIdx: phaseIdx, ToIdx: stepIdx, Type: kgtypes.EdgeKGContains})
	if prevStepIdx >= 0 {
		edges = append(edges, kgwire.BatchEdge{FromIdx: stepIdx, ToIdx: prevStepIdx, Type: kgtypes.EdgeDependsOn})
	}

	// NO step->file edge is emitted here, deliberately.
	//
	// This loop used to emit ToID: "file:"+fp. That identity is a PHANTOM — the
	// literal "file:" prefix appears nowhere else in the tree, so no such node is
	// ever created, for any path, in any graph. Nor would the bare path work: file
	// nodes live in the CODE graph while create_plan writes to KNOWLEDGE, and
	// resolveWriteID only consults the graph being written.
	//
	// The edge therefore never once produced a working link, and failed in two
	// different ways depending on path LENGTH — resolveWriteID returns unchecked at
	// len(id) >= 32, so a long path silently persisted a dangling edge
	// (peer_type ""), while a short one aborted the ENTIRE create_plan batch with
	// "not_found: node file:... not found" and zero nodes written. That length
	// dependence is why it read as intermittent.
	//
	// The paths are not lost: step.FilePaths is already persisted as file_paths
	// metadata immediately above. Genuine cross-graph step->file linking goes
	// through the linkage graph (mutate link_graph:"linkage" with the bare code
	// path), which resolves correctly and is the supported mechanism.
	for _, c := range step.Criteria {
		cIdx := len(nodes)
		nodes = append(nodes, BuildCriterionNode(c))
		edges = append(edges, kgwire.BatchEdge{FromIdx: cIdx, ToIdx: stepIdx, Type: kgtypes.EdgeVerifies})
		edges = append(edges, kgwire.BatchEdge{FromIdx: stepIdx, ToIdx: cIdx, Type: kgtypes.EdgeKGContains})
	}
	return nodes, edges, stepIdx
}

// BuildProjectNode assembles a single project node (no edges — project is a top-level container).
//
// When backendName != "", populates backend write-through metadata on the node:
//   - backend-agnostic: "backend", "external_url", "external_archived"="false",
//     "external_id" (set to ref.URL — Linear projects have no human Identifier,
//     so the deeplink doubles as the external identifier)
//   - backend-private (namespaced): "<backend>_id", "<backend>_group_id",
//     "<backend>_group_key"
//
// When ref.State != "", overrides node.Status with the verbatim backend state
// (e.g. "started", "in-review"). Otherwise the KG-canonical default
// kgtypes.StatusActive is preserved.
//
// All three backend params (backendName, ref, group) zero-valued together
// is the local-only path: no metadata is written, default Status sticks.
func BuildProjectNode(args ProjectArgs, backendName string, ref backends.RemoteRef, group backends.Group) (*knowledgev1.Node, []kgwire.BatchEdge) {
	node := &knowledgev1.Node{
		Type:        string(kgtypes.NodeProject),
		Source:      "llm:claude",
		SymbolName:  args.Name,
		Description: args.Description,
		Summary:     args.Summary,
		Status:      kgtypes.StatusActive,
	}
	if backendName != "" {
		kgtypes.SetValue(node, "backend", backendName)
		kgtypes.SetValue(node, "external_url", ref.URL)
		kgtypes.SetValue(node, "external_archived", "false")
		// Linear projects have no human-readable identifier; URL doubles as
		// external_id so collapsed renders show one line. Tickets diverge
		// (external_id = Identifier, external_url = URL).
		if ref.URL != "" {
			kgtypes.SetValue(node, "external_id", ref.URL)
		}
		kgtypes.SetValue(node, backendName+"_id", ref.ID)
		kgtypes.SetValue(node, backendName+"_group_id", group.ID)
		kgtypes.SetValue(node, backendName+"_group_key", group.Key)
	}
	if ref.State != "" {
		// Status round-trip: backend-assigned state name verbatim,
		// no normalization. Direct string assignment to Node.Status.
		node.Status = ref.State
	}
	return node, nil
}

// BuildTicketNode assembles the full ticket graph (ticket node + contains edge
// from parent project + pattern wiring). Returns node + edge slices
// compatible with the mutate(create_batch) wire call.
//
// unresolvedPatternIDs is the list of pattern IDs that ValidatePatternFields
// flagged as missing or wrong-type. They are persisted on the ticket node as
// the unresolved_pattern_ids metadata key so a later query(id: ticketID)
// surfaces "this ticket references X unresolved pattern IDs" without
// re-validating.
//
// When backendName != "", populates backend write-through metadata on the
// ticket node:
//   - backend-agnostic: "backend", "external_url" (=ref.URL),
//     "external_archived"="false", "external_id" (=ref.Identifier;
//     unlike projects, tickets have a human-readable identifier like
//     "ABC-42" distinct from the deeplink URL).
//   - backend-private (namespaced): "<backend>_id", "<backend>_group_id",
//     "<backend>_group_key".
//
// External ID precedence: when backendName != "" AND args.ExternalID == "",
// external_id is populated from ref.Identifier (the backend's human key).
// When args.ExternalID != "", that local-supplied value wins — preserves the
// pre-T2 local-only path and lets callers override the backend's identifier
// if they have a domain reason to.
//
// When ref.State != "", overrides ticketNode.Status with the verbatim backend
// state (e.g. Linear's "In Review"). Otherwise the KG-canonical default
// kgtypes.StatusOpen is preserved.
//
// parentBackendID is the parent project's "<backend>_id" metadata value. When
// non-empty AND backendName != "", it is written as "<backendName>_project_id"
// metadata so the ticket carries an explicit backend-side parent pointer.
// (Empty in the local-only path; supplied by CreateTicket's caller in the
// backend path — see Phase 3's handler resolution.)
func BuildTicketNode(args TicketArgs, unresolvedPatternIDs []string, unresolvedLanguagePatternIDs []string, backendName string, ref backends.RemoteRef, group backends.Group, parentBackendID string) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	// Ticket node (index 0). Pattern-shaped metadata (no_patterns_reason,
	// unresolved_pattern_ids) is set here so the ticket node carries it from
	// creation onward — a later query(id: ticketID) surfaces the warning
	// without re-validating.
	ticketIdx := 0
	ticketNode := &knowledgev1.Node{
		Type:        string(kgtypes.NodeTicket),
		Source:      "llm:claude",
		SymbolName:  args.Name,
		Description: args.Description,
		Summary:     args.Summary,
		Status:      kgtypes.StatusOpen,
	}
	// External ID precedence: local-supplied args.ExternalID wins. Only
	// fall back to ref.Identifier when args.ExternalID is empty AND a
	// backend is wired.
	switch {
	case args.ExternalID != "":
		kgtypes.SetValue(ticketNode, "external_id", args.ExternalID)
	case backendName != "" && ref.Identifier != "":
		kgtypes.SetValue(ticketNode, "external_id", ref.Identifier)
	}
	if args.Priority != "" {
		kgtypes.SetValue(ticketNode, "priority", args.Priority)
	}
	if args.Labels != "" {
		kgtypes.SetValue(ticketNode, "labels", args.Labels)
	}
	if args.NoPatternsReason != "" {
		kgtypes.SetValue(ticketNode, "no_patterns_reason", args.NoPatternsReason)
	}
	if len(unresolvedPatternIDs) > 0 {
		kgtypes.SetValue(ticketNode, "unresolved_pattern_ids", strings.Join(unresolvedPatternIDs, ","))
	}
	if len(unresolvedLanguagePatternIDs) > 0 {
		kgtypes.SetValue(ticketNode, "unresolved_language_patterns", strings.Join(unresolvedLanguagePatternIDs, ","))
	}
	if backendName != "" {
		kgtypes.SetValue(ticketNode, "backend", backendName)
		kgtypes.SetValue(ticketNode, "external_url", ref.URL)
		kgtypes.SetValue(ticketNode, "external_archived", "false")
		kgtypes.SetValue(ticketNode, backendName+"_id", ref.ID)
		kgtypes.SetValue(ticketNode, backendName+"_group_id", group.ID)
		kgtypes.SetValue(ticketNode, backendName+"_group_key", group.Key)
		if parentBackendID != "" {
			kgtypes.SetValue(ticketNode, backendName+"_project_id", parentBackendID)
		}
	}
	if ref.State != "" {
		// Status round-trip: backend-assigned state name verbatim,
		// no normalization. Direct string assignment to Node.Status.
		ticketNode.Status = ref.State
	}
	nodes := []*knowledgev1.Node{ticketNode}

	var edges []kgwire.BatchEdge
	if args.ProjectID != "" {
		// Edge from existing project node (FromIdx=-1, FromID=ProjectID) to the new ticket (ToIdx=0).
		edges = append(edges, kgwire.BatchEdge{FromIdx: -1, FromID: args.ProjectID, ToIdx: ticketIdx, Type: kgtypes.EdgeKGContains})
	}

	// Pattern wiring: ticket → existing pattern uses edges, plus eager
	// creation of "emerging" pattern nodes for proposed_patterns.
	nodes, edges = wireTicketPatternEdges(ticketIdx, args, nodes, edges)

	// Language-pattern wiring: ticket → language_pattern audits edges. No
	// proposed-language-pattern counterpart — language_patterns has no
	// eager-create variant.
	edges = wireLanguagePatternEdges(ticketIdx, args.LanguagePatterns, edges)
	return nodes, edges
}

// wireLanguagePatternEdges adds <from>→<lang_pattern> EdgeAudits edges per ID.
// Mirrors wirePatternEdges but uses EdgeAudits and has no proposed-pattern
// counterpart (language_patterns has no eager-create variant).
func wireLanguagePatternEdges(fromIdx int, ids []string, edges []kgwire.BatchEdge) []kgwire.BatchEdge {
	for _, pid := range ids {
		edges = append(edges, kgwire.BatchEdge{FromIdx: fromIdx, ToIdx: -1, ToID: pid, Type: kgtypes.EdgeAudits})
	}
	return edges
}

// wireTicketPatternEdges adds ticket→pattern EdgeUses edges for each PatternID
// and eagerly creates a NodePattern (status=emerging) for each ProposedPattern,
// linking ticket→new-pattern via EdgeUses. Returns the augmented node and
// edge slices. PatternID lookups + cross-graph resolution happen earlier (the
// create_ticket interceptor's ValidatePatternFields + ResolvePatternIDsToEffectiveIDs);
// this helper only wires graph structure, using args.PatternIDs AS-IS as edge
// targets. Mirrors wirePatternEdges for plans.
func wireTicketPatternEdges(ticketIdx int, args TicketArgs, nodes []*knowledgev1.Node, edges []kgwire.BatchEdge) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	for _, pid := range args.PatternIDs {
		edges = append(edges, kgwire.BatchEdge{FromIdx: ticketIdx, ToIdx: -1, ToID: pid, Type: kgtypes.EdgeUses})
	}
	for _, p := range args.ProposedPatterns {
		patIdx := len(nodes)
		patNode := &knowledgev1.Node{
			Type:        string(kgtypes.NodePattern),
			Source:      "llm:claude",
			SymbolName:  p.Name,
			Description: p.Sketch,
			Summary:     DerivePatternSummary(p.Name, p.Sketch),
			Status:      "emerging",
		}
		if p.Sketch != "" {
			kgtypes.SetValue(patNode, "shape", p.Sketch)
		}
		// The ticket→pattern EdgeUses edge below IS the relationship —
		// no proposed_in_ticket metadata is written, since the ticket's ID
		// is not known until after the create-batch wire call and the
		// mutate(create_batch) seam lacks a metadata-update primitive (same
		// reasoning as the plan-side wirePatternEdges).
		nodes = append(nodes, patNode)
		edges = append(edges, kgwire.BatchEdge{FromIdx: ticketIdx, ToIdx: patIdx, Type: kgtypes.EdgeUses})
	}
	return nodes, edges
}

// BuildCriterionNode constructs a criterion node from a CriterionArgs.
func BuildCriterionNode(c CriterionArgs) *knowledgev1.Node {
	cType := c.Type
	if cType == "" {
		cType = "manual"
	}
	node := &knowledgev1.Node{
		Type:        string(kgtypes.NodeCriterion),
		Source:      "llm:claude",
		SymbolName:  c.Description,
		Description: c.Description,
		Summary:     DeriveCriterionSummary(cType, c.Description, c.Command),
	}
	if c.Command != "" {
		kgtypes.SetValue(node, "command", c.Command)
	}
	kgtypes.SetValue(node, "type", cType)
	return node
}
