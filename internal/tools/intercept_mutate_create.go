// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// handleClientMutateCreateFinding handles mutate(create, type:finding).
// Mirrors projects.RecordFinding: builds a finding node with metadata,
// resolves the question_id link (if any) + supports edge + reference
// nodes/edges, all in one PersistBatch.
func handleClientMutateCreateFinding(ctx context.Context, deps ClientDeps, a mutateArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	// A check belongs in a per-language CHECKS graph, alongside the fixture
	// example nodes that validate it, so this path refuses one outright rather
	// than admitting it: there are no fixtures to resolve in the knowledge graph,
	// and the correct answer is a refusal rather than a validation.
	if c, isCheck, cerr := corpus.ParseCheck(&knowledgev1.Node{Metadata: a.Metadata}); cerr != nil {
		return errorResult("mutate(create, type=finding): " + cerr.Error())
	} else if isCheck {
		return errorResult(fmt.Sprintf("mutate(create, type=finding): %s=%q makes this node a check, and a check lives in the checks graph with the fixtures that validate it — re-issue with graph:%q (no language: it is a single graph, and the check's own language metadata scopes it)", corpus.MetaCheckType, c.Type, checksGraphSelector))
	}
	if err := validate.Name("mutate(create, type=finding)", a.Name); err != nil {
		return errorResult(err.Error())
	}
	clamped, clampWarn, serr := validate.ClampSummary("mutate(create, type=finding)", "summary", a.Summary)
	if serr != nil {
		return errorResult(serr.Error())
	}
	a.Summary = clamped
	refWarnings, rerr := clampFindingReferenceSummaries(a.References)
	if rerr != nil {
		return errorResult(rerr.Error())
	}
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
	warnings = append(warnings, refWarnings...)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Finding recorded: %s → ID: %s (%d references) [graph: knowledge/default]", a.Name, ids[0], len(a.References))
	writeClientWarningsSection(&sb, warnings)
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
	node := &knowledgev1.Node{
		Type:        string(kgtypes.NodeFinding),
		Source:      "llm:claude",
		SymbolName:  a.Name,
		Description: a.Description,
		Summary:     a.Summary,
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

// clampFindingReferenceSummaries validates the author summary on every
// NODE-CREATING reference in place, under the indexed path
// references[i].summary. A node_id entry is SKIPPED: it creates no node, so it
// needs no summary, and requiring one there would be a blanket rule rather than
// the per-kind one the schema states.
//
// ITERATION IS BY INDEX so the clamped value is assigned back into the slice
// element the node builder later reads — a range-value loop would clamp a copy
// and ship the unclamped text onward.
func clampFindingReferenceSummaries(refs []findingReference) (warnings []string, err error) {
	for i := range refs {
		if refs[i].URL == "" && refs[i].File == "" {
			continue
		}
		clamped, w, cerr := validate.ClampSummary("mutate(create, type=finding)", fmt.Sprintf("references[%d].summary", i), refs[i].Summary)
		if cerr != nil {
			return nil, cerr
		}
		refs[i].Summary = clamped
		if w != "" {
			warnings = append(warnings, w)
		}
	}
	return warnings, nil
}

// appendFindingReferenceEdges appends reference nodes + edges to the
// in-flight nodes/edges slices. URL/File entries create a reference
// node carrying the author's summary; NodeID entries link directly to an
// existing node and create none.
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
				Summary:     ref.Summary,
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
				Summary:     ref.Summary,
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
		Summary:     a.Summary,
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
	writeClientWarningsSection(&sb, warnings)
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
	// Caller metadata is seeded FIRST so the derived scope/enforcement keys below
	// win on a key collision.
	node := knowledgev1.Node{
		Type:        string(kgtypes.NodeRule),
		Source:      "llm:claude",
		SymbolName:  a.Name,
		Description: a.Description,
		Summary:     a.Summary,
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
	writeClientWarningsSection(&sb, warnings)
	return textResult(sb.String())
}

// isTerminalForClientRollup reports whether a status means the node's own work is
// over, so the cascade leaves it alone. It is the NARROW reading, and nothing on
// the cascade path reads it directly: isSettledForCascade is the single settled
// authority — the unevaluated-criterion hold, the partitioner's container branch
// and its announce branch all test that one — and this predicate is its first
// disjunct. Widenings land there, never here.
func isTerminalForClientRollup(status string) bool {
	switch status {
	case kgtypes.StatusCompleted, kgtypes.StatusClosed, kgtypes.StatusArchived, kgtypes.StatusSkipped,
		"failed", "superseded":
		return true
	}
	return false
}

// handleClientUpdateStatusRollup walks the contains tree under a.ID, collects
// the unsettled descendants the cascade may move — only those whose own type
// is one of the seven container types AND, when the cascade writes completed,
// which own no criterion still waiting to be evaluated, since
// partitionRollupTargets holds every other type back and holds those too — and
// writes cascadeStatus to all of them plus the caller's own status to the root.
// The traversal enumerates the held nodes, and carries the contains EDGES the
// partition needs to attribute a criterion to the node that owns it, so the
// success line can name them.
//
// cascadeStatus is the mapped descendant status the shared claim predicate
// returned; it is NOT necessarily a.Status. When the two are equal the write is
// a single batch over the root and its descendants together, which keeps a
// status-only completed rollup at exactly 2 RPCs (one traverse + one
// update_batch) regardless of descendant count. When they differ — a "Done"
// container whose descendants take "completed" — it costs one more UPDATE.
//
// A caller may send body fields alongside the status. Those apply to the NAMED
// node only — cascading a description down a contains tree would be nonsense —
// so they ride their own by-id UPDATE carrying no status, issued BEFORE the
// rollup so a failure there is a clean zero-write reject. A combined one costs
// one RPC more again.
func handleClientUpdateStatusRollup(
	ctx context.Context,
	gc GraphCaller,
	a mutateArgs,
	_ *knowledgev1.Node,
	cascadeStatus string,
) kgtools.ToolResult {
	fields := rollupNamedNodeFields(a)
	if len(fields) > 0 {
		if ferr := applyRollupNamedNodeFields(ctx, gc, a); ferr != nil {
			// Nothing has been written yet — a plain reject, no partial language.
			return errorResult("mutate(update): rollup field update: " + ferr.Error())
		}
	}

	descs, structureEdges, truncated, terr := render.TraverseDescendantsWithEdges(ctx, gc, a.ID, kgtypes.EdgeKGContains, 16)
	if terr != nil {
		return rollupFailureResult(a.ID, fields, "rollup traverse", terr)
	}
	// A clamped walk can drop a criterion out of the descendant set, which makes
	// the node that owns it look criterion-free and cascades it. Refusing is the
	// only correct disposition; cascading anyway would be a lane that fires
	// forever on the same cause.
	if truncated {
		return rollupFailureResult(a.ID, fields, "rollup traverse", errRollupTraverseTruncated)
	}
	ids, heldCriteria, heldQuestions, heldUnevaluated, heldOther := partitionRollupTargets(a.ID, descs, structureEdges, cascadeStatus)
	rootWritten, uerr := writeCascadeStatuses(ctx, gc, ids, a.Status, cascadeStatus, newBundleID())
	if uerr != nil {
		return cascadeWriteFailureResult(a.ID, fields, rootWritten, uerr)
	}
	msg := rollupStatusMessage(a.ID, a.Status, cascadeStatus, ids, heldCriteria, heldQuestions, heldUnevaluated, heldOther)
	if len(fields) > 0 {
		// Silent success about the second write would be the same defect in
		// miniature, so name what else landed.
		msg += fmt.Sprintf(" — also applied %s to %s", strings.Join(fields, ", "), a.ID)
	}
	return textResult(msg)
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

// rollupFailureResult builds the rollup's failure result for the paths that run
// AFTER the named-node field write. BOTH branches name the id and say that status
// reached neither it nor its descendants: a refusal the caller cannot attribute
// to a node is not actionable, and the truncation refusal is issued before any
// status write and, when body fields were supplied, after they landed — which is
// why it takes the same fields-aware branch as the other two failure paths. Once
// the fields HAVE landed the message additionally names exactly which of them
// persisted, because a bare "traverse failed" there would itself be a
// silent-partial report.
func rollupFailureResult(id string, fields []string, stage string, err error) kgtools.ToolResult {
	if len(fields) == 0 {
		msg := fmt.Sprintf("mutate(update): %v; status reached neither %s nor its descendants", err, id)
		if stage != "" {
			msg = fmt.Sprintf("mutate(update): %s: %v; status reached neither %s nor its descendants", stage, err, id)
		}
		return errorResult(msg)
	}
	return errorResult(fmt.Sprintf(
		"mutate(update): %s applied to %s, but the status rollup failed: %v; "+
			"status reached neither %s nor its descendants — re-run the status update once the cause is cleared",
		strings.Join(fields, ", "), id, err, id,
	))
}
