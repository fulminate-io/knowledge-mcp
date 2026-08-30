// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// flowStats counts what flow resolution DID over one populate run, ONE COUNTER
// PER OUTCOME.
//
// NOTHING HERE IS EVER SUMMED ACROSS KINDS. An outcome that leaves no edge is
// otherwise indistinguishable from a fact the walk never reached — a graph shows
// the same nothing for every decline — so each decline is counted where it
// happens and reported under its own key.
type flowStats struct {
	// FlowsSeen is every flow edge that entered resolution, whatever became of
	// it. It is the denominator the other counters are read against.
	FlowsSeen int

	// ReturnEdges is the FLOWS_TO_RETURN self-edges emitted.
	ReturnEdges int

	// ArgEdgesBound, ArgEdgesGrouped and ArgEdgesUnresolved split the
	// FLOWS_TO_ARG population by what resolution made of the callee: one exact
	// declaration, a multi-candidate group (counted per MEMBER), or no in-repo
	// declaration at all.
	//
	// ArgEdgesUnresolved IS THE UNRESOLVED-CALLEE COUNTER, and it counts facts
	// the collector used to DISCARD. Every stdlib and third-party sink lands
	// here, which is most of what a security consumer is looking for.
	ArgEdgesBound      int
	ArgEdgesGrouped    int
	ArgEdgesUnresolved int

	// FieldEdges, FieldUnresolvedOwner and FieldAmbiguousOwner split the
	// FLOWS_TO_FIELD population by what resolution made of the field's OWNER.
	FieldEdges           int
	FieldUnresolvedOwner int
	FieldAmbiguousOwner  int
}

// log emits the flow residue picture on its own line, beside the reference
// resolution line rather than folded into it.
func (s flowStats) log() {
	if s.FlowsSeen == 0 {
		// A corpus with no armed language produces no flow edge at all, and a
		// line of zeros there would train a reader to ignore the line.
		return
	}
	slog.Info("collector: flow resolution",
		"flows_seen", s.FlowsSeen,
		"return_edges", s.ReturnEdges,
		"arg_edges_bound", s.ArgEdgesBound,
		"arg_edges_grouped", s.ArgEdgesGrouped,
		"arg_edges_unresolved", s.ArgEdgesUnresolved,
		"field_edges", s.FieldEdges,
		"field_unresolved_owner", s.FieldUnresolvedOwner,
		"field_ambiguous_owner", s.FieldAmbiguousOwner)
}

// resolveFlowSelf passes through a FLOWS_TO_RETURN edge, whose endpoints the
// slot pre-pass already made exact.
//
// THE ENDPOINT CHECK AGAINST nodeIDs IS LOAD-BEARING and must not be dropped,
// for the reason resolveContainment's is: an edge emitted for a declaration the
// populate pass creates no node for would point at an ID no node carries.
//
// EVIDENCE PASSES THROUGH VERBATIM. It is the flow key — source and sink
// positions — and rebuilding or dropping it is exactly what routing this edge
// through resolveReference would have done.
func resolveFlowSelf(e *treesitter.Edge, nodeIDs map[string]bool, stats *flowStats) []*knowledgev1.Edge {
	stats.FlowsSeen++
	if !nodeIDs[e.FromID] || !nodeIDs[e.ToID] {
		return nil
	}
	stats.ReturnEdges++
	return []*knowledgev1.Edge{{
		FromId: e.FromID, ToId: e.ToID, Type: string(e.Type), Weight: e.Weight,
		Evidence: e.Evidence,
	}}
}

// resolveFlowArg resolves a FLOWS_TO_ARG edge's callee, and KEEPS THE FACT when
// the callee resolves to nothing in this repo.
//
// AN UNRESOLVED CALLEE BECOMES A SELF-EDGE carrying its spelling in Evidence,
// rather than being discarded. The argument against enumerating in-repo
// candidates for an unindexed target is that the enumeration would be entirely
// false; a self-edge naming NO candidate asserts nothing false, and it is the
// only way a consumer learns that a parameter reaches `exec.Command` at all.
//
// EVERY READER IDENTIFIES THAT SELF-EDGE STRUCTURALLY — `Type == FLOWS_TO_ARG
// && FromId == ToId` — AND NEVER BY SCANNING Evidence FOR AN @, because a callee
// spelling may legitimately contain one. Ruby composes `@logger.info` as a
// single nameable spelling, C# verbatim identifiers spell `@class.Method`, and
// both are ordinary resolved callees. A consumer applying the textual rule
// misclassifies every one of them.
//
// THE CALLEE IS RESOLVED WITH resolveRef AGAINST THE EDGE'S OWN RefSite — the
// same function and the same site the sibling CALLS edge used, so the answer is
// that edge's answer. That identity holds ONLY because the chunker wrote the
// spelling through normalizeCallee; an arm that hand-derived it would send a
// different string into the same resolver and reach a different declaration.
func resolveFlowArg(
	e *treesitter.Edge, ix *declIndex, nodeIDs map[string]bool, stats *flowStats, ordinals groupOrdinals,
) []*knowledgev1.Edge {
	stats.FlowsSeen++
	// GUARDED FIRST, BEFORE ANY RESOLUTION, and this is the one arm where it
	// could be forgotten: the unresolved branch below uses e.FromID as BOTH
	// endpoints, so an id no node carries would land twice.
	if !nodeIDs[e.FromID] {
		return nil
	}

	res := resolveRef(ix, e.Ref, e.ToID)
	switch res.Status {
	case RefBound:
		stats.ArgEdgesBound++
		return []*knowledgev1.Edge{{
			FromId: e.FromID, ToId: res.Candidates[0].NodeID, Type: string(e.Type), Weight: e.Weight,
			Method: string(res.Rule), Evidence: e.Evidence,
		}}
	case RefAmbiguous:
		return flowGroupEdges(e, res.Candidates, kgtypes.EdgeMethodAmbiguousName, stats, ordinals)
	case RefDynamic:
		if len(res.Candidates) == 0 {
			return flowUnresolvedSelfEdge(e, stats)
		}
		return flowGroupEdges(e, res.Candidates, kgtypes.EdgeMethodDynamic, stats, ordinals)
	default:
		return flowUnresolvedSelfEdge(e, stats)
	}
}

// flowUnresolvedSelfEdge records a callee that names no in-repo declaration.
//
// METHOD IS DELIBERATELY EMPTY: no resolution rung fired, and stamping one would
// name a resolution that did not happen. Confidence is 0 for the same reason —
// this edge makes no claim about WHICH declaration the callee is, only that the
// parameter reached that argument position of that spelling.
func flowUnresolvedSelfEdge(e *treesitter.Edge, stats *flowStats) []*knowledgev1.Edge {
	stats.ArgEdgesUnresolved++
	return []*knowledgev1.Edge{{
		FromId: e.FromID, ToId: e.FromID, Type: string(e.Type), Weight: e.Weight,
		Evidence: e.Evidence + flowCalleeSep + e.ToID,
	}}
}

// flowGroupEdges emits one edge per candidate of a multi-candidate callee or
// field owner, each carrying the COMPOSED Evidence: the flow key, then the
// group key.
//
// groupEdges IS DELIBERATELY NOT REUSED, and the omission is not an oversight:
// it sets Evidence to the BARE group key, which for a flow edge would discard
// the source and sink positions that are the fact itself. Composing here is the
// only shape that keeps both, and the separator is the one character no callee
// spelling and no field name can contain.
func flowGroupEdges(
	e *treesitter.Edge, candidates []*declRec, method string, stats *flowStats, ordinals groupOrdinals,
) []*knowledgev1.Edge {
	// THE DISCRIMINATOR INCLUDES THE FLOW KEY, so a flow group's ordinals are
	// independent of the CALLS group's over the same site. Without it the two
	// surfaces would share a counter and one edge's identity would depend on
	// whether the other had been walked first.
	disc := e.Evidence + ":" + e.ToID + ":" + string(e.Type) + ":" + e.FromID
	key := e.Evidence + flowGroupSep + groupKey(e.ToID, string(e.Type), e.FromID, ordinals.next(disc))

	conf := 1 / float64(len(candidates))
	out := make([]*knowledgev1.Edge, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, &knowledgev1.Edge{
			FromId: e.FromID, ToId: c.NodeID, Type: string(e.Type),
			Confidence: conf, Method: method, Evidence: key,
		})
	}
	switch kgtypes.EdgeType(e.Type) {
	case kgtypes.EdgeFlowsToField:
		stats.FieldAmbiguousOwner++
	default:
		stats.ArgEdgesGrouped += len(candidates)
	}
	return out
}

// resolveFlowField resolves a FLOWS_TO_FIELD edge's OWNER — the declaration the
// written field belongs to.
//
// IT IS THE PARENT-TO-MEMBER CONTAINMENT RESOLUTION REVERSED, and it inherits
// that arm's documented split: a lexically enclosing container is exact by slot
// and passes straight through, while a Go method's receiver is a SIBLING
// declaration that may live in another file, so no slot addresses it and its
// name is resolved against the index at package scope — Go's own rule.
func resolveFlowField(
	e *treesitter.Edge, ix *declIndex, nodeIDs map[string]bool, stats *flowStats, ordinals groupOrdinals,
) []*knowledgev1.Edge {
	stats.FlowsSeen++
	if !nodeIDs[e.FromID] {
		return nil
	}
	if nodeIDs[e.ToID] {
		stats.FieldEdges++
		return []*knowledgev1.Edge{{
			FromId: e.FromID, ToId: e.ToID, Type: string(e.Type), Weight: e.Weight,
			Evidence: e.Evidence,
		}}
	}
	if e.ToID == "" || e.Ref == nil {
		stats.FieldUnresolvedOwner++
		return nil
	}

	candidates := receiverCandidates(ix, e.Ref, receiverTypeName(e.ToID))
	switch len(candidates) {
	case 0:
		// Declared in no file of this package. Nothing is emitted rather than
		// reaching for a same-named declaration somewhere else — but the outcome
		// is COUNTED, because an owner this pass could not bind is a real residue
		// rather than a fact that was never seen.
		stats.FieldUnresolvedOwner++
		return nil
	case 1:
		stats.FieldEdges++
		return []*knowledgev1.Edge{{
			FromId: e.FromID, ToId: candidates[0].NodeID, Type: string(e.Type), Weight: e.Weight,
			Evidence: e.Evidence,
		}}
	default:
		return flowGroupEdges(e, candidates, kgtypes.EdgeMethodAmbiguousName, stats, ordinals)
	}
}

// receiverTypeName reduces a parent-qualified spelling to the receiver TYPE's
// own name by cutting at the last dot.
func receiverTypeName(qualified string) string {
	if i := strings.LastIndexByte(qualified, '.'); i >= 0 {
		return qualified[i+1:]
	}
	return qualified
}

// receiverCandidates looks a receiver TYPE up at the reference's own package
// scope, against the collision-safe index.
//
// IT IS ONE RULE WITH ONE SPELLING, called by the containment arm and by the
// field arm alike. Copying it into the second caller would be a second spelling
// of one rule, which is how the two drift.
func receiverCandidates(ix *declIndex, ref *treesitter.RefSite, receiverName string) []*declRec {
	if ref == nil || receiverName == "" {
		return nil
	}
	return ix.lookup(declKey{Scope: ref.Scope, Name: baseDeclName(receiverName)})
}

// flowCalleeSep opens the callee-spelling component of a flow Evidence string,
// and flowGroupSep opens the group-key component. Both mirror the grammar
// kgtypes.EdgeEvidenceFlowPrefix documents, where the parse is left-to-right and
// "|" is the one separator no spelling can contain.
const (
	flowCalleeSep = "@"
	flowGroupSep  = "|"
)
