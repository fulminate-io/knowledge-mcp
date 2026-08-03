// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
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
	clamped, clampWarn, serr := validate.ClampSummary("mutate(create, type=finding)", "summary", a.Summary)
	if serr != nil {
		return errorResult(serr.Error())
	}
	a.Summary = clamped
	node := buildFindingNode(a)
	nodes := []*knowledgev1.Node{node}
	edges := buildFindingFixedEdges(a)
	nodes, edges = appendFindingReferenceEdges(nodes, edges, a.References)

	// Context links: pre-validated ticket/session/knowledge-link edges
	// ride the same atomic create_batch; code-target links + warnings are handled
	// after the create. Node is slot 0.
	cl := buildContextLinks(ctx, gc, a.TicketID, a.Session, a.Links)
	edges = append(edges, cl.batchEdges...)

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
	warnings := append(cl.warnings, applyCodeLinks(ctx, gc, ids[0], cl.codeLinks)...)
	if clampWarn != "" {
		warnings = append(warnings, clampWarn)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Finding recorded: %s → ID: %s (%d references) [graph: knowledge/default]", a.Name, ids[0], len(a.References))
	writeClientWarningsSection(&sb, warnings, "\n\n")
	return textResult(sb.String())
}

// copyCallerMetadata returns a FRESH copy of the caller's metadata map, or nil
// when there is nothing to carry. Copied, never aliased: each builder stamps its
// derived keys on top, and mutating the caller's map in place would leak those
// derived keys back into the caller's own arguments — breaking retry
// idempotency for anyone who reuses the map. Same rule mergeUpdateMetadata
// states for the update path.
func copyCallerMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

// buildFindingNode constructs the finding node with metadata. The caller's
// metadata is seeded FIRST so the derived evidence/source keys below win on a
// key collision.
func buildFindingNode(a mutateArgs) *knowledgev1.Node {
	summary := a.Summary
	if summary == "" {
		summary = projects.DeriveFindingSummary(a.Description, a.Evidence)
	}
	node := &knowledgev1.Node{
		Type:        string(kgtypes.NodeFinding),
		Source:      "llm:claude",
		SymbolName:  a.Name,
		Description: a.Description,
		Summary:     summary,
		Content:     a.Content,
		Status:      a.Status,
		Metadata:    copyCallerMetadata(a.Metadata),
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
	clamped, clampWarn, serr := validate.ClampSummary("mutate(create, type=research)", "summary", a.Summary)
	if serr != nil {
		return errorResult(serr.Error())
	}
	a.Summary = clamped
	question := a.Name
	bgContext := a.Content
	summary := a.Summary
	if summary == "" {
		summary = "Research question: " + question
		if bgContext != "" {
			summary += ". Context: " + bgContext
		}
	}
	// A caller-supplied description wins; absent one, the question text is the
	// description (the long-standing default). SymbolName stays the question
	// unconditionally — a research node's name IS its question.
	description := a.Description
	if description == "" {
		description = question
	}
	status := a.Status
	if status == "" {
		status = "open"
	}
	node := knowledgev1.Node{
		Type:        string(kgtypes.NodeResearch),
		Source:      "llm:claude",
		SymbolName:  question,
		Description: description,
		Summary:     summary,
		Content:     bgContext,
		Status:      status,
		Metadata:    copyCallerMetadata(a.Metadata),
	}
	// Context links: pre-validated ticket/session/knowledge-link edges
	// ride the create_batch; code links + warnings handled after. Node is slot 0.
	cl := buildContextLinks(ctx, gc, a.TicketID, a.Session, a.Links)
	bundleID := newBundleID()
	ids, perr := PersistBatch(ctx, gc, []*knowledgev1.Node{&node}, cl.batchEdges, bundleID)
	if perr != nil {
		return errorResult("record research: " + perr.Error())
	}
	if len(ids) == 0 {
		return errorResult("record research: persist returned no IDs")
	}
	warnings := append(cl.warnings, applyCodeLinks(ctx, gc, ids[0], cl.codeLinks)...)
	if clampWarn != "" {
		warnings = append(warnings, clampWarn)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Research question recorded: %s → ID: %s [graph: knowledge/default]", question, ids[0])
	writeClientWarningsSection(&sb, warnings, "\n\n")
	return textResult(sb.String())
}

// handleClientMutateCreateRule handles mutate(create, type:rule).
func handleClientMutateCreateRule(ctx context.Context, deps ClientDeps, a mutateArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if err := validate.Name("mutate(create, type=rule)", a.Name); err != nil {
		return errorResult(err.Error())
	}
	clamped, clampWarn, serr := validate.ClampSummary("mutate(create, type=rule)", "summary", a.Summary)
	if serr != nil {
		return errorResult(serr.Error())
	}
	a.Summary = clamped
	summary := a.Summary
	if summary == "" {
		summary = projects.DeriveRuleSummary(a.Name, a.Scope)
	}
	// Caller metadata is seeded FIRST so the derived scope/enforcement keys below
	// win on a key collision.
	node := knowledgev1.Node{
		Type:        string(kgtypes.NodeRule),
		Source:      "llm:claude",
		SymbolName:  a.Name,
		Description: a.Description,
		Summary:     summary,
		Content:     a.Content,
		Status:      a.Status,
		Metadata:    copyCallerMetadata(a.Metadata),
	}
	if a.Scope != "" {
		kgtypes.SetValue(&node, "scope", a.Scope)
	}
	if a.Enforcement != "" {
		kgtypes.SetValue(&node, "enforcement", a.Enforcement)
	}
	// Context links: pre-validated ticket/session/knowledge-link edges
	// ride the create_batch; code links + warnings handled after. Node is slot 0.
	cl := buildContextLinks(ctx, gc, a.TicketID, a.Session, a.Links)
	bundleID := newBundleID()
	ids, perr := PersistBatch(ctx, gc, []*knowledgev1.Node{&node}, cl.batchEdges, bundleID)
	if perr != nil {
		return errorResult("add rule: " + perr.Error())
	}
	if len(ids) == 0 {
		return errorResult("add rule: persist returned no IDs")
	}
	warnings := append(cl.warnings, applyCodeLinks(ctx, gc, ids[0], cl.codeLinks)...)
	if clampWarn != "" {
		warnings = append(warnings, clampWarn)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Rule added: %s → ID: %s [graph: knowledge/default]", a.Name, ids[0])
	writeClientWarningsSection(&sb, warnings, "\n\n")
	return textResult(sb.String())
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
	// Caller metadata is seeded FIRST so the derived conclusion key wins on a
	// key collision. Copied, never aliased — see copyCallerMetadata.
	metadata := copyCallerMetadata(a.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["conclusion"] = a.Conclusion
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
		Metadata:  metadata,
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

// handleClientUpdateStatusRollup walks the contains tree under a.ID, collects
// the non-terminal descendants the cascade may move — only those whose own type
// is one of the five container types, since partitionRollupTargets holds every
// other type back — and issues ONE mutate(update_batch) with status=completed
// for all of them PLUS the root. The traversal still enumerates the held nodes
// so the success line can name them.
//
// A caller may send body fields alongside the status. Those apply to the NAMED
// node only — cascading a description down a contains tree would be nonsense —
// so they ride their own by-id UPDATE carrying no status, issued BEFORE the
// rollup so a failure there is a clean zero-write reject. A status-only rollup
// skips that write entirely and stays exactly 2 RPCs (one traverse + one
// update_batch) regardless of descendant count; a combined one costs 3.
func handleClientUpdateStatusRollup(ctx context.Context, gc GraphCaller, a mutateArgs, _ *knowledgev1.Node) kgtools.ToolResult {
	fields := rollupNamedNodeFields(a)
	if len(fields) > 0 {
		if ferr := applyRollupNamedNodeFields(ctx, gc, a); ferr != nil {
			// Nothing has been written yet — a plain reject, no partial language.
			return errorResult("mutate(update): rollup field update: " + ferr.Error())
		}
	}

	descs, terr := TraverseDescendants(ctx, gc, a.ID, kgtypes.EdgeKGContains, 16)
	if terr != nil {
		return rollupFailureResult(a.ID, fields, "rollup traverse", terr)
	}
	ids, heldCriteria, heldQuestions, heldOther := partitionRollupTargets(a.ID, descs)
	bundleID := newBundleID()
	if uerr := UpdateBatchStatus(ctx, gc, ids, kgtypes.StatusCompleted, bundleID); uerr != nil {
		return rollupFailureResult(a.ID, fields, "", uerr)
	}
	msg := rollupStatusMessage(a.ID, ids, heldCriteria, heldQuestions, heldOther)
	if len(fields) > 0 {
		// Silent success about the second write would be the same defect in
		// miniature, so name what else landed.
		msg += fmt.Sprintf(" — also applied %s to %s", strings.Join(fields, ", "), a.ID)
	}
	return textResult(msg)
}

// rollupNamedNodeFields returns, in a stable order, the names of the body fields
// the caller supplied alongside the status. An empty result is the status-only
// rollup, which must stay byte-for-byte the original two-RPC path.
func rollupNamedNodeFields(a mutateArgs) []string {
	var names []string
	if a.Name != "" {
		names = append(names, "name")
	}
	if a.Description != "" {
		names = append(names, "description")
	}
	if a.Summary != "" {
		names = append(names, "summary")
	}
	if a.Content != "" {
		names = append(names, "content")
	}
	if a.Keywords != "" {
		names = append(names, "keywords")
	}
	if a.Source != "" {
		names = append(names, "source")
	}
	if len(a.Metadata) > 0 {
		names = append(names, "metadata")
	}
	return names
}

// applyRollupNamedNodeFields writes the caller's body fields to the named
// container via the full-fidelity by-id forward: executeMutate re-compiles it so
// name/description/summary/content/keywords/source route as top-level set_fields
// and metadata as set_metadata.
//
// Status is deliberately LEFT UNSET — the rollup batch owns it, and emitting it
// here would write the same field twice through two plans. Now that the field
// is a POINTER, nil MEANS "untouched" by construction rather than by accident
// of omitempty: an unset pointer omits the key, where an empty string would
// have become a clear-to-blank write.
func applyRollupNamedNodeFields(ctx context.Context, gc GraphCaller, a mutateArgs) error {
	args, merr := json.Marshal(forwardedTypedUpdatePayload{
		Operation:   "update",
		ID:          a.ID,
		Name:        a.Name,
		Description: a.Description,
		Summary:     a.Summary,
		Content:     a.Content,
		Keywords:    a.Keywords,
		Source:      a.Source,
		Metadata:    a.Metadata,
	})
	if merr != nil {
		return fmt.Errorf("marshal forward: %w", merr)
	}
	if _, uerr := executeMutate(ctx, gc, args); uerr != nil {
		return uerr
	}
	return nil
}

// rollupFailureResult builds the rollup's failure result for the two paths that
// run AFTER the named-node field write. When no field write was issued there is
// nothing partial to report and the original messages stand verbatim; once the
// fields HAVE landed, a bare "traverse failed" would itself be a silent-partial
// report, so both paths name the id, exactly which fields persisted, and that
// status reached neither the node nor its descendants.
func rollupFailureResult(id string, fields []string, stage string, err error) kgtools.ToolResult {
	if len(fields) == 0 {
		if stage != "" {
			return errorResult("mutate(update): " + stage + ": " + err.Error())
		}
		return errorResult("mutate(update): " + err.Error())
	}
	return errorResult(fmt.Sprintf(
		"mutate(update): %s applied to %s, but the status rollup failed: %v; "+
			"status reached neither %s nor its descendants — re-run the status update once the cause is cleared",
		strings.Join(fields, ", "), id, err, id,
	))
}
