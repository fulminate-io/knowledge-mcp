// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_plan_annotation.go holds the pre-write guard on a plan_annotation
// write. It is the sibling of the create_batch criterion-pair gate and it lands
// at the same seam, for the same reason that gate's own header gives.
//
// WHAT THE ANNOTATION CONTRACT IS. An annotation carries its KIND — one of
// correct, finding, needed change — plus, where the kind demands it, a TIER (a
// finding without one records a concern with no severity, which is not a review
// finding) and the EXACT REPLACEMENT TEXT (a needed change without one names a
// problem and withholds the fix, which is the one thing that makes it actionable).
// The reviewer LANE is optional: an annotation with no lane is still a complete
// annotation, merely an unattributed one.
//
// REJECT, NEVER AUTO-COMPLETE, quoted from the model this copies: "Growing the
// missing edge for the caller is the coerce-and-continue the house
// BAD-INPUT-ERRORS rule forbids — it silently rewrites the caller's stated edge
// set, and it has no principled stopping point." The same holds for a kind: there
// is no defensible default among three words that mean opposite things, and a
// tier or a replacement text invented on the caller's behalf would be a review
// artifact nobody wrote.
//
// WHY IT IS A PAYLOAD-SHAPE GUARD AT THIS SEAM RATHER THAN AN ACCOUNTING ROW.
// Kind, tier, lane and replacement text are METADATA KEYS on the node body, not
// top-level params, so no param-accounting arm ever sees them — the accounting
// table answers "is this param routed", and a malformed annotation is a
// well-formed params set. And it must NOT add a rejectUndeclaredParams call: the
// standing census counts per-operation-handler calls to that function and fails
// on a new one.
//
// DETERMINISM, both rules carried from the gate this copies: the payload is
// walked in the CALLER'S OWN ORDER so a batch with two offenders always names the
// same one first, and the offender is rendered in the caller's own spelling
// (`nodes[1]`) so the message points at what they wrote.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// batchAnnotationNode is what this guard reads off one nodes[] entry.
type batchAnnotationNode struct {
	Type     string            `json:"type"`
	Metadata map[string]string `json:"metadata"`
}

// guardPlanAnnotationCreate refuses a SINGLE typed create of a malformed
// plan_annotation. Returns nil for every other type, so the guard cannot become a
// tree-wide metadata validator.
//
// UPSERT IS NOT ROUTED HERE: it is refused by type in
// guardAnnotationSeverityCoherence, because upsert writes no edge and so cannot
// produce a coherent annotation at all. That gap was not theoretical —
// mutate(upsert, type:"plan_annotation") with no metadata created an annotation
// carrying NO KIND, which the tree rendered as `annotations: 1 ( 1)`.
func guardPlanAnnotationCreate(a mutateArgs) error {
	if kgtypes.NodeType(a.Type) != kgtypes.NodePlanAnnotation {
		return nil
	}
	return validateAnnotationMetadata("mutate(create)", "", a.Metadata)
}

// guardCreateBatchPlanAnnotations refuses a create_batch carrying a malformed
// plan_annotation, naming the offending slot.
//
// A payload that does not parse is the DISPATCHER'S error to report — the guard
// only inspects what parses and must not preempt the real unmarshal error with a
// duplicate, exactly as the criterion-pair gate states.
func guardCreateBatchPlanAnnotations(raw json.RawMessage) error {
	var payload struct {
		Nodes []batchAnnotationNode `json:"nodes"`
	}
	_ = json.Unmarshal(raw, &payload)
	for i, n := range payload.Nodes {
		if kgtypes.NodeType(n.Type) != kgtypes.NodePlanAnnotation {
			continue
		}
		if err := validateAnnotationMetadata("mutate(create_batch)", fmt.Sprintf("nodes[%d] ", i), n.Metadata); err != nil {
			return err
		}
	}
	return nil
}

// validateAnnotationMetadata is the one contract both arms apply, so the two
// cannot drift into two vocabularies. `where` is the caller's own spelling of the
// offending entry, empty for a single create.
func validateAnnotationMetadata(op, where string, metadata map[string]string) error {
	kind := metadata[kgtypes.AnnotationKindKey]
	if !kgtypes.IsAnnotationKind(kind) {
		return fmt.Errorf(
			"%s: %sis a plan_annotation whose metadata.%s is %q — an annotation's kind must be one of %s. "+
				"The kind is what a section read and the tree's per-section line report; it is never defaulted, "+
				"because the three kinds mean opposite things and there is no defensible choice among them",
			op, where, kgtypes.AnnotationKindKey, kind, quotedKindList())
	}
	// THE TIER RULE IS THE SHARED ONE, not a second copy. kgtypes.Validate-
	// AnnotationSeverity is also what the edge payload runs before it serializes,
	// so a severity the node accepts is exactly a severity the edge can carry.
	// Two copies of this rule is how an annotation ends up acceptable on one
	// carrier and unwritable on the other.
	if serr := kgtypes.ValidateAnnotationSeverity(kind, metadata[kgtypes.AnnotationTierKey]); serr != nil {
		// THE VALIDATOR SPEAKS FOR ITSELF. Binding this error and then asserting a
		// cause of our own was a second copy of the rule wearing the first one's
		// clothes: correct only while the missing-tier arm is the validator's last
		// remaining failure, and silently wrong the day it grows another — an
		// annotation refused for a third reason would be reported as a missing
		// tier, naming a key the caller may have supplied.
		return fmt.Errorf(
			"%s: %sis a plan_annotation of kind %q whose severity is not writable: %w. "+
				"A finding with no tier is a concern with no severity, which a reader cannot act on and this tool will not invent",
			op, where, kind, serr)
	}
	if kind == kgtypes.AnnotationKindNeededChange && metadata[kgtypes.AnnotationReplacementKey] == "" {
		return fmt.Errorf(
			"%s: %sis a plan_annotation of kind %q carrying no metadata.%s — a needed change carries the EXACT replacement text. "+
				"Without it the annotation names a problem and withholds the fix, and the planner applying it would have to retype the section",
			op, where, kgtypes.AnnotationKindNeededChange, kgtypes.AnnotationReplacementKey)
	}
	return nil
}

// quotedKindList renders the valid set for a refusal message, in the vocabulary's
// own fixed order so the message reads the same every time.
func quotedKindList() string {
	quoted := make([]string, 0, len(kgtypes.AnnotationKinds))
	for _, k := range kgtypes.AnnotationKinds {
		quoted = append(quoted, `"`+k+`"`)
	}
	return strings.Join(quoted, ", ")
}

// stampPlanAnnotationEdges puts the annotation's KIND and TIER onto the
// relates-to edges the links param built, so a reader can learn a section's
// review state from the section's own edges without hydrating a single
// annotation node.
//
// IT IS NOT AUTO-COMPLETION, and the distinction matters because the guard above
// forbids exactly that. Nothing is invented and no edge is grown: the caller
// stated the edge with links:[section] and stated the kind and tier in the node's
// metadata, both already validated by validateAnnotationMetadata. This copies two
// facts the caller supplied onto the relation they asked for.
//
// IT TOUCHES ONLY THE ANNOTATION'S OWN OUTGOING relates-to EDGES. A ticket or
// session context edge is `contains` and is left alone, and so is any edge on a
// create whose type is not plan_annotation.
//
// A MARSHAL FAILURE FAILS THE WRITE rather than degrading to an unstamped edge:
// an edge with empty Evidence is indistinguishable from one that never carried a
// severity, so absorbing the failure would persist an annotation whose severity
// is silently unreadable at the edge — the same line the section builder's
// position payload draws.
func stampPlanAnnotationEdges(a mutateArgs, edges []kgwire.BatchEdge) ([]kgwire.BatchEdge, error) {
	if kgtypes.NodeType(a.Type) != kgtypes.NodePlanAnnotation {
		return edges, nil
	}
	evidence, err := kgtypes.MarshalAnnotationEdgeSeverity(
		a.Metadata[kgtypes.AnnotationKindKey], a.Metadata[kgtypes.AnnotationTierKey])
	if err != nil {
		return nil, err
	}
	for i := range edges {
		if edges[i].Type != kgtypes.EdgeRelatesTo || edges[i].FromIdx != contextNodeSlot {
			continue
		}
		edges[i].Method = kgtypes.AnnotationEdgeMethod
		edges[i].Evidence = evidence
	}
	return edges, nil
}

// ─── SEVERITY COHERENCE ──────────────────────────────────────────────────────
//
// An annotation's kind and tier are stored TWICE by design: on the node, where
// they are the annotation's own record, and on its relates-to edge, where they
// answer "which sections carry unresolved findings, and how bad" without
// hydrating a node. Two carriers of one fact can disagree, and this ticket
// exists to remove exactly that class one level up — a prefill stored twice on
// one node with no consistency check.
//
// THE INVARIANT: for every relates-to edge FROM a plan_annotation, the edge's
// kind and tier equal the node's. There is no supported write that can leave
// them different.
//
// HOW IT IS HELD: the severity is written ONCE, by the create-with-links path,
// which builds both carriers from one source inside one batch — they cannot
// disagree because nothing writes them separately. Every other write that could
// separate them is REFUSED pre-write, naming the key and the supported path.
//
// WHY REFUSE RATHER THAN SYNC. Updating both carriers on a node edit is not
// expressible as one write: a node update and an edge write are different
// mutation kinds and cannot ride one MutationPlan, so "update the node then
// re-link" leaves a window in which the two disagree — and a window is a
// violation of an invariant stated as "no sequence of supported writes". The
// alternatives are recorded on the finding this guard is linked to.

// annotationSeverityKeys are the two keys that must not drift between the node
// and its edge. replacement_text and reviewer_lane are NOT here: neither rides
// the edge, so neither can disagree with anything.
var annotationSeverityKeys = []string{kgtypes.AnnotationKindKey, kgtypes.AnnotationTierKey}

// guardAnnotationSeverityCoherence refuses, from the PAYLOAD ALONE, the writes
// that move an annotation's severity on the NODE while leaving its edge behind.
//
// IT NEEDS NO GRAPH READ, which is why it sits above every routing branch. The
// two ATTACHMENT paths — mutate(link) and create_batch's edges[] — are guarded
// separately in mutate_plan_annotation_link.go and
// mutate_plan_annotation_batch.go, because deciding them can require reading the
// annotation being attached.
func guardAnnotationSeverityCoherence(a mutateArgs) error {
	// UPSERT OF AN ANNOTATION IS REFUSED BY TYPE, ahead of the metadata rule, so
	// the caller gets the specific answer rather than a write-once message about a
	// key they may not even have sent.
	//
	// THE TWO RULES COLLIDE ON THIS ARM AND THE COLLISION IS REAL. Upsert is
	// create-or-update by a caller-supplied id, and telling those apart needs a
	// read. As a CREATE it would have to set the severity, which the write-once
	// rule forbids on this operation; as an UPDATE it would move the node's copy
	// and leave any edge behind. Refusing the type outright answers both without a
	// read, and costs nothing real: upsert exists for tool-owned config records
	// with caller-chosen ids, and an annotation is neither — it is created with
	// links, which is what writes its edge.
	if a.Operation == "upsert" && kgtypes.NodeType(a.Type) == kgtypes.NodePlanAnnotation {
		return fmt.Errorf(
			"mutate(upsert): a plan_annotation is not created or updated by upsert — create it with " +
				"mutate(create, type:\"plan_annotation\", links:[\"<section id>\"], metadata:{...}), which writes its kind " +
				"and tier onto the node AND onto its relates-to edge in one batch. Upsert writes no edge, so an annotation " +
				"made this way carries a severity on one carrier and nothing on the other, and an upsert over an EXISTING " +
				"annotation would move the node's copy and leave its edge behind")
	}
	switch a.Operation {
	case "update", "update_batch", "bulk_update_metadata", "upsert":
		return refuseSeverityMetadataWrite(a)
	}
	return nil
}

// refuseSeverityMetadataWrite refuses a metadata write carrying either severity
// key on any of the four operations that write metadata onto a node WITHOUT
// touching its edges.
//
// IT IS TYPE-BLIND, deliberately. update and bulk_update_metadata carry an id
// and no type, so deciding whether the target is a plan_annotation would cost a
// read per id on a batch path — and these two keys are the annotation
// vocabulary, so setting them on any other node type is meaningless input that
// the house rule says errors rather than lands.
func refuseSeverityMetadataWrite(a mutateArgs) error {
	var payload struct {
		Metadata map[string]string `json:"metadata"`
		Items    []struct {
			ID       string            `json:"id"`
			Metadata map[string]string `json:"metadata"`
		} `json:"items"`
		Updates []struct {
			ID       string            `json:"id"`
			Metadata map[string]string `json:"metadata"`
		} `json:"updates"`
	}
	if err := json.Unmarshal(a.raw, &payload); err != nil {
		return nil //nolint:nilerr // a payload that does not parse is the dispatcher's error to report
	}
	// The caller's own order, so a payload with two offenders always names the
	// same one first: the top-level map, then items[], then updates[].
	if key := firstSeverityKey(payload.Metadata); key != "" {
		return severityWriteRefusal(a.Operation, "metadata", key)
	}
	for i, it := range payload.Items {
		if key := firstSeverityKey(it.Metadata); key != "" {
			return severityWriteRefusal(a.Operation, fmt.Sprintf("items[%d].metadata", i), key)
		}
	}
	for i, u := range payload.Updates {
		if key := firstSeverityKey(u.Metadata); key != "" {
			return severityWriteRefusal(a.Operation, fmt.Sprintf("updates[%d].metadata", i), key)
		}
	}
	return nil
}

// firstSeverityKey returns the first severity key present, in the vocabulary's
// own fixed order so the message is the same on every run for one payload.
func firstSeverityKey(md map[string]string) string {
	for _, k := range annotationSeverityKeys {
		if _, ok := md[k]; ok {
			return k
		}
	}
	return ""
}

// severityWriteRefusal is the one message both metadata arms produce, so the
// four operations cannot drift into four explanations of one rule.
func severityWriteRefusal(op, where, key string) error {
	return fmt.Errorf(
		"mutate(%s): %s carries %q — an annotation's kind and tier are written ONCE, by "+
			"mutate(create, type:\"plan_annotation\", links:[\"<section id>\"], metadata:{...}), which writes them onto the "+
			"node AND onto its relates-to edge in one batch. Editing them here would move the node's copy and leave the "+
			"edge's behind, and a section read answers from the edge — so the plan would report a severity nobody wrote. "+
			"To change an annotation's severity, create the replacement annotation and delete this one: a different kind "+
			"or tier is a different assertion, not an edit to the same one",
		op, where, key)
}
