// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// handleClientMutateCreateFinding handles mutate(create, type:finding).
// Mirrors projects.RecordFinding: builds a finding node with metadata,
// resolves the question_id link (if any) + supports edge + reference
// nodes/edges, all in one PersistBatch.
func handleClientMutateCreateFinding(ctx context.Context, deps ClientDeps, a mutateArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if err := validate.Name("mutate(create, type=finding)", a.Name); err != nil {
		return errorResult(err.Error())
	}
	if err := validate.Summary("mutate(create, type=finding)", "summary", a.Summary); err != nil {
		return errorResult(err.Error())
	}
	node := buildFindingNode(a)
	nodes := []*knowledgev1.Node{node}
	edges := buildFindingFixedEdges(a)
	nodes, edges = appendFindingReferenceEdges(nodes, edges, a.References)

	bundleID := newBundleID()
	ids, perr := PersistBatch(ctx, gc, nodes, edges, bundleID)
	if perr != nil {
		return errorResult("record finding: " + perr.Error())
	}
	if len(ids) == 0 {
		return errorResult("record finding: persist returned no IDs")
	}
	if a.Concludes && a.QuestionID != "" {
		_ = UpdateBatchStatus(ctx, gc, []string{a.QuestionID}, "answered", bundleID)
	}
	return textResult(fmt.Sprintf("Finding recorded: %s → ID: %s (%d references) [graph: knowledge/default]", a.Name, ids[0], len(a.References)))
}

// buildFindingNode constructs the finding node with metadata.
func buildFindingNode(a mutateArgs) *knowledgev1.Node {
	summary := a.Summary
	if summary == "" {
		summary = a.Description
		if a.Evidence != "" {
			summary += ". Evidence: " + a.Evidence
		}
	}
	node := &knowledgev1.Node{
		Type:        string(kgtypes.NodeFinding),
		Source:      "llm:claude",
		SymbolName:  a.Name,
		Description: a.Description,
		Summary:     summary,
	}
	if a.Evidence != "" {
		kgtypes.SetValue(node, "evidence", a.Evidence)
	}
	if a.Source != "" {
		kgtypes.SetValue(node, "source", a.Source)
	}
	return node
}

// buildFindingFixedEdges constructs the non-reference edges (supports,
// question_id contains+answers).
func buildFindingFixedEdges(a mutateArgs) []kgwire.BatchEdge {
	var edges []kgwire.BatchEdge
	if a.Supports != "" {
		edges = append(edges, kgwire.BatchEdge{FromIdx: 0, ToIdx: -1, ToID: a.Supports, Type: kgtypes.EdgeSupports})
	}
	if a.QuestionID != "" {
		edges = append(edges, kgwire.BatchEdge{FromIdx: 0, ToIdx: -1, ToID: a.QuestionID, Type: kgtypes.EdgeAnswers})
		edges = append(edges, kgwire.BatchEdge{FromIdx: -1, FromID: a.QuestionID, ToIdx: 0, Type: kgtypes.EdgeKGContains})
	}
	return edges
}

// appendFindingReferenceEdges appends reference nodes + edges to the
// in-flight nodes/edges slices. URL/File entries create a reference
// node; NodeID entries link directly to an existing node.
func appendFindingReferenceEdges(nodes []*knowledgev1.Node, edges []kgwire.BatchEdge, refs []findingReference) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	refSlots := make([]int, 0, len(refs))
	for _, ref := range refs {
		switch {
		case ref.URL != "":
			rn := &knowledgev1.Node{
				Type:        string(kgtypes.NodeReference),
				Source:      "llm:claude",
				SymbolName:  ref.Title,
				Description: ref.URL,
				Summary:     ref.Title + " — " + ref.URL,
			}
			kgtypes.SetValue(rn, "type", "url")
			kgtypes.SetValue(rn, "url", ref.URL)
			refSlots = append(refSlots, len(nodes))
			nodes = append(nodes, rn)
		case ref.File != "":
			rn := &knowledgev1.Node{
				Type:        string(kgtypes.NodeReference),
				Source:      "llm:claude",
				SymbolName:  ref.Title,
				Description: ref.File,
				Summary:     ref.Title + " — " + ref.File,
			}
			kgtypes.SetValue(rn, "type", "file")
			kgtypes.SetValue(rn, "file", ref.File)
			refSlots = append(refSlots, len(nodes))
			nodes = append(nodes, rn)
		case ref.NodeID != "":
			edges = append(edges, kgwire.BatchEdge{FromIdx: 0, ToIdx: -1, ToID: ref.NodeID, Type: kgtypes.EdgeReferences})
		}
	}
	for _, slot := range refSlots {
		edges = append(edges, kgwire.BatchEdge{FromIdx: 0, ToIdx: slot, Type: kgtypes.EdgeReferences})
	}
	return nodes, edges
}

// handleClientMutateCreateResearch handles mutate(create, type:research).
// Standalone research question; no questions array (that's create_research).
func handleClientMutateCreateResearch(ctx context.Context, deps ClientDeps, a mutateArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if err := validate.Name("mutate(create, type=research)", a.Name); err != nil {
		return errorResult(err.Error())
	}
	if err := validate.Summary("mutate(create, type=research)", "summary", a.Summary); err != nil {
		return errorResult(err.Error())
	}
	question := a.Name
	bgContext := a.Content
	summary := a.Summary
	if summary == "" {
		summary = "Research question: " + question
		if bgContext != "" {
			summary += ". Context: " + bgContext
		}
	}
	node := knowledgev1.Node{
		Type:        string(kgtypes.NodeResearch),
		Source:      "llm:claude",
		SymbolName:  question,
		Description: question,
		Summary:     summary,
		Content:     bgContext,
		Status:      "open",
	}
	bundleID := newBundleID()
	ids, perr := PersistBatch(ctx, gc, []*knowledgev1.Node{&node}, nil, bundleID)
	if perr != nil {
		return errorResult("record research: " + perr.Error())
	}
	if len(ids) == 0 {
		return errorResult("record research: persist returned no IDs")
	}
	return textResult(fmt.Sprintf("Research question recorded: %s → ID: %s [graph: knowledge/default]", question, ids[0]))
}

// handleClientMutateCreateRule handles mutate(create, type:rule).
func handleClientMutateCreateRule(ctx context.Context, deps ClientDeps, a mutateArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if err := validate.Name("mutate(create, type=rule)", a.Name); err != nil {
		return errorResult(err.Error())
	}
	if err := validate.Summary("mutate(create, type=rule)", "summary", a.Summary); err != nil {
		return errorResult(err.Error())
	}
	summary := a.Summary
	if summary == "" {
		summary = "Rule: " + a.Name
		if a.Scope != "" {
			summary += " (scope: " + a.Scope + ")"
		}
	}
	node := knowledgev1.Node{
		Type:        string(kgtypes.NodeRule),
		Source:      "llm:claude",
		SymbolName:  a.Name,
		Description: a.Description,
		Summary:     summary,
	}
	if a.Scope != "" {
		kgtypes.SetValue(&node, "scope", a.Scope)
	}
	if a.Enforcement != "" {
		kgtypes.SetValue(&node, "enforcement", a.Enforcement)
	}
	bundleID := newBundleID()
	ids, perr := PersistBatch(ctx, gc, []*knowledgev1.Node{&node}, nil, bundleID)
	if perr != nil {
		return errorResult("add rule: " + perr.Error())
	}
	if len(ids) == 0 {
		return errorResult("add rule: persist returned no IDs")
	}
	return textResult(fmt.Sprintf("Rule added: %s → ID: %s [graph: knowledge/default]", a.Name, ids[0]))
}

// handleClientMutateAnswer handles mutate(answer): mark a research
// question as answered with a conclusion + link findings.
func handleClientMutateAnswer(ctx context.Context, deps ClientDeps, a mutateArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	id := a.ID
	if id == "" {
		id = a.QuestionID
	}
	if strings.TrimSpace(id) == "" {
		return errorResult("mutate(answer): id (or question_id) is required")
	}
	node, lerr := LookupNode(ctx, gc, id)
	if lerr != nil || node == nil {
		return errorResult(fmt.Sprintf("research not found: %s", id))
	}
	updatedSummary := "Research question: " + node.SymbolName + ". Conclusion: " + a.Conclusion
	updateArgs, merr := json.Marshal(struct {
		Operation string            `json:"operation"`
		ID        string            `json:"id"`
		Status    string            `json:"status"`
		Summary   string            `json:"summary"`
		Metadata  map[string]string `json:"metadata"`
	}{
		Operation: "update",
		ID:        id,
		Status:    "answered",
		Summary:   updatedSummary,
		Metadata:  map[string]string{"conclusion": a.Conclusion},
	})
	if merr != nil {
		return errorResult("mutate(answer): marshal: " + merr.Error())
	}
	if _, uerr := executeMutate(ctx, gc, updateArgs); uerr != nil {
		return errorResult("mutate(answer): update: " + uerr.Error())
	}
	if a.Findings != "" {
		for fid := range strings.SplitSeq(a.Findings, ",") {
			fid = strings.TrimSpace(fid)
			if fid == "" {
				continue
			}
			_ = LinkOne(ctx, gc, fid, id, kgtypes.EdgeAnswers)
		}
	}
	return textResult(fmt.Sprintf("Research answered: %s [graph: knowledge/default]", node.SymbolName))
}

// isClientRollupContainer returns true for node types that participate
// in the closure rollup: project, ticket, plan, phase, step.
// Mirrors projects.closure.go behavior — any container whose
// descendants should be marked completed when the container is.
func isClientRollupContainer(t kgtypes.NodeType) bool {
	switch t {
	case kgtypes.NodeProject, kgtypes.NodeTicket, kgtypes.NodePlan, kgtypes.NodePhase, kgtypes.NodeStep:
		return true
	}
	return false
}

// isTerminalForClientRollup mirrors projects.isTerminalForRollup.
func isTerminalForClientRollup(status string) bool {
	switch status {
	case kgtypes.StatusCompleted, kgtypes.StatusClosed, kgtypes.StatusArchived, kgtypes.StatusSkipped,
		"failed", "superseded":
		return true
	}
	return false
}

// handleClientUpdateStatusRollup walks the contains tree under a.ID,
// collects every non-terminal descendant, and issues ONE
// mutate(update_batch) with status=completed for all of them PLUS the
// root. Exactly 2 RPCs total (one traverse + one update_batch)
// regardless of descendant count.
func handleClientUpdateStatusRollup(ctx context.Context, gc GraphCaller, a mutateArgs, _ *knowledgev1.Node) kgtools.ToolResult {
	descs, terr := TraverseDescendants(ctx, gc, a.ID, kgtypes.EdgeKGContains, 16)
	if terr != nil {
		return errorResult("mutate(update): rollup traverse: " + terr.Error())
	}
	ids := []string{a.ID}
	for _, d := range descs {
		if isTerminalForClientRollup(d.Status) {
			continue
		}
		ids = append(ids, d.Id)
	}
	bundleID := newBundleID()
	if uerr := UpdateBatchStatus(ctx, gc, ids, kgtypes.StatusCompleted, bundleID); uerr != nil {
		return errorResult("mutate(update): " + uerr.Error())
	}
	return textResult(fmt.Sprintf("Status updated: %d/%d → %s [graph: knowledge/default]", len(ids), len(ids), kgtypes.StatusCompleted))
}
