// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_plan_annotation_link.go closes the one severity-coherence path that
// cannot be decided from the payload: mutate(link) attaching an EXISTING
// annotation to a section.
//
// WHY IT NEEDS A READ WHERE THE OTHERS DO NOT. Every other write that could
// separate the two carriers names the annotation's type or its metadata in the
// payload itself. A link names two ids and a relationship, and nothing in it says
// whether the FROM node is a plan_annotation or what severity it carries — so the
// guard has to ask. One read on a link call is the price of covering the path;
// leaving it uncovered would leave the invariant with a hole and the requirement
// says there is no such sequence.
//
// IT REFUSES RATHER THAN STAMPS, and the refusal is ACTIONABLE: it prints the
// exact method and evidence the caller should send, read from the node they are
// linking. Stamping the caller's edge silently would rewrite an edge set they
// stated, which is the coerce-and-continue this seam's own doctrine forbids —
// and here the caller has the two params in hand, so naming the values costs them
// one retry and keeps the write theirs.

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// guardAnnotationLinkSeverity refuses a relates-to link FROM a plan_annotation
// whose method and evidence do not carry that annotation's own kind and tier.
//
// A READ FAILURE FAILS CLOSED, and an earlier draft of this function got that
// wrong. It treated an unreadable node the same as a non-annotation and stood
// aside, on the reasoning that a read error is no reason to refuse a write the
// caller may be entitled to make. That reasoning is wrong HERE specifically: if
// the unreadable node IS an annotation, standing aside admits an unstamped edge
// and the invariant this guard exists to hold — that no supported write leaves
// the two carriers different — has a hole exactly the width of a flaky read. An
// invariant with a failure mode that opens it is not an invariant. The linter
// flagged the swallowed error and it was right to.
//
// A NOT-FOUND STILL STANDS ASIDE, and the distinction is in LookupNode's own
// contract: it returns (nil, nil) for a node that does not exist and surfaces
// transport errors verbatim. A from-id that resolves to nothing has no severity
// to disagree with and its link will fail on its own merits a moment later.
func guardAnnotationLinkSeverity(ctx context.Context, gc GraphCaller, a mutateArgs) error {
	if a.Operation != "link" || a.Relationship != string(kgtypes.EdgeRelatesTo) || a.From == "" {
		return nil
	}
	n, err := LookupNode(ctx, gc, a.From)
	if err != nil {
		return fmt.Errorf(
			"mutate(link): could not read %s to check whether it is a plan_annotation, so this link is refused rather "+
				"than written unchecked: %w. An annotation's kind and tier must match on its node and on its edge, and "+
				"admitting an unchecked link is the one way that can silently stop being true",
			a.From, err)
	}
	if n == nil || kgtypes.NodeType(n.GetType()) != kgtypes.NodePlanAnnotation {
		return nil
	}
	want, merr := kgtypes.MarshalAnnotationEdgeSeverity(
		kgtypes.Value(n, kgtypes.AnnotationKindKey), kgtypes.Value(n, kgtypes.AnnotationTierKey))
	if merr != nil {
		// THE ANNOTATION ITSELF IS MALFORMED, so there is no coherent edge to ask
		// for and the refusal must say that rather than print a payload. An
		// earlier version relayed the marshal error bare, and before the severity
		// rule existed it did not fail at all — it offered
		// edge_evidence:{"annotation_kind":""} as the value to send, a refusal
		// instructing the caller to write a severity that asserts nothing.
		return fmt.Errorf(
			"mutate(link): %s is a plan_annotation that cannot be attached because its own severity is malformed: %w. "+
				"Fix the annotation first — its kind and tier are what a section read reports, and an edge can carry no "+
				"more meaning than the node it comes from",
			a.From, merr)
	}
	if a.Method == kgtypes.AnnotationEdgeMethod && a.EdgeEvidence == want {
		return nil
	}
	return fmt.Errorf(
		"mutate(link): %s is a plan_annotation, so its %q edge must carry the same kind and tier its node carries — "+
			"a section read answers from the edge, and an edge that disagrees with its node reports a severity nobody "+
			"wrote. Re-send with method:%q and edge_evidence:%s. "+
			"An annotation created with mutate(create, type:\"plan_annotation\", links:[\"<section id>\"], metadata:{...}) "+
			"gets both carriers written together and needs no link call at all",
		a.From, kgtypes.EdgeRelatesTo, kgtypes.AnnotationEdgeMethod, want)
}
