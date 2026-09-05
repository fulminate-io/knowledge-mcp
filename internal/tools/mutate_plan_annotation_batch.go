// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_plan_annotation_batch.go holds the severity-coherence guard on
// create_batch's edges[]: a relates-to edge FROM a plan_annotation must carry
// that annotation's own kind and tier.
//
// IT REPLACES A GUARD BUILT ON A FALSE PREMISE, and the correction is worth
// stating because the premise was the reasoning, not a detail. The first version
// refused every such edge outright, on the claim that create_batch's edges[]
// entry "declares only from_idx, to_idx, from_id, to_id and type, so it cannot
// carry the annotation's kind and tier". The SCHEMA is closed; the RUNTIME is
// not. engine.edgeBody declares Weight, Confidence, Method, Evidence and
// LastValidated, and the repository's own pre-existing
// TestCompileMutate_CreateBatchEdgeMetadata has always asserted they land on the
// compiled edge. So the old guard refused the one spelling that COULD be written
// coherently — and, because it tested only from_idx, admitted the from_id
// spelling that carried nothing at all. Two errors pointing opposite ways, from
// one cause: it reasoned about the declared schema instead of what the arm
// accepts. The schema now declares those carriers too, so the two agree.
//
// THE RULE IS NOW THE SAME ONE mutate(link) APPLIES, which is the point: both
// operations attach an annotation to a section, so both answer the question the
// same way. Carry the matching method and evidence and the write lands; carry
// the wrong ones or none and it is refused, naming the exact values to send.
//
// TWO SPELLINGS, TWO COSTS. An edge whose source is a from_idx slot names a node
// in the SAME payload, so its kind and tier are readable without touching the
// graph. An edge whose source is a from_id names an EXISTING annotation, which
// has to be read — the same read, for the same reason, that the link guard pays.
// A batch attaching only new annotations therefore costs no reads at all.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// batchEdgeShape is what this guard reads off one edges[] entry.
//
// FromIdx IS A POINTER so an ABSENT slot is distinguishable from slot 0. The
// engine's own decoder treats an absent from_idx as the -1 "use the string id"
// sentinel; reading it as a plain int here would make every from_id edge look
// like an attachment from nodes[0], which is how a guard starts refusing writes
// nobody made.
type batchEdgeShape struct {
	FromIdx  *int   `json:"from_idx"`
	FromID   string `json:"from_id"`
	Type     string `json:"type"`
	Method   string `json:"method"`
	Evidence string `json:"evidence"`
}

// guardBatchAnnotationEdges requires every create_batch relates-to edge whose
// source is a plan_annotation to carry that annotation's kind and tier.
//
// A READ FAILURE FAILS CLOSED, on the rule the link guard states: if the
// unreadable node IS an annotation, waving the edge through admits an unstamped
// one, and an invariant with a failure mode that opens it is not an invariant.
func guardBatchAnnotationEdges(ctx context.Context, gc GraphCaller, raw json.RawMessage) error {
	var payload struct {
		Nodes []batchAnnotationNode `json:"nodes"`
		Edges []batchEdgeShape      `json:"edges"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil //nolint:nilerr // a payload that does not parse is the dispatcher's error to report
	}
	// The caller's own order, so a payload with two offenders always names the
	// same one first.
	for i, e := range payload.Edges {
		if e.Type != string(kgtypes.EdgeRelatesTo) {
			continue
		}
		metadata, where, isAnnotation, err := batchEdgeSourceAnnotation(ctx, gc, payload.Nodes, e)
		if err != nil {
			return err
		}
		if !isAnnotation {
			continue
		}
		if err := requireCoherentAnnotationEdge(i, where, metadata, e); err != nil {
			return err
		}
	}
	return nil
}

// batchEdgeSourceAnnotation resolves an edge's source and reports whether it is a
// plan_annotation, with the metadata carrying its severity and the caller's own
// spelling of that endpoint for the message.
//
// THE TWO SPELLINGS ARE RESOLVED DIFFERENTLY BY NECESSITY, not by preference: a
// slot index names a node in this payload and a string id names one in the graph.
func batchEdgeSourceAnnotation(
	ctx context.Context, gc GraphCaller, nodes []batchAnnotationNode, e batchEdgeShape,
) (metadata map[string]string, where string, isAnnotation bool, err error) {
	if e.FromIdx != nil && *e.FromIdx >= 0 {
		if *e.FromIdx >= len(nodes) {
			// An out-of-range slot is the dispatcher's error to report, not this
			// guard's: pre-empting it with a duplicate would hide the real one.
			return nil, "", false, nil
		}
		n := nodes[*e.FromIdx]
		if kgtypes.NodeType(n.Type) != kgtypes.NodePlanAnnotation {
			return nil, "", false, nil
		}
		return n.Metadata, fmt.Sprintf("nodes[%d]", *e.FromIdx), true, nil
	}
	if e.FromID == "" {
		return nil, "", false, nil
	}
	node, lerr := LookupNode(ctx, gc, e.FromID)
	if lerr != nil {
		return nil, "", false, fmt.Errorf(
			"mutate(create_batch): could not read %s to check whether it is a plan_annotation, so this batch is refused "+
				"rather than written unchecked: %w. An annotation's kind and tier must match on its node and on its edge, "+
				"and admitting an unchecked attachment is the one way that can silently stop being true",
			e.FromID, lerr)
	}
	if node == nil || kgtypes.NodeType(node.GetType()) != kgtypes.NodePlanAnnotation {
		return nil, "", false, nil
	}
	return map[string]string{
		kgtypes.AnnotationKindKey: kgtypes.Value(node, kgtypes.AnnotationKindKey),
		kgtypes.AnnotationTierKey: kgtypes.Value(node, kgtypes.AnnotationTierKey),
	}, e.FromID, true, nil
}

// requireCoherentAnnotationEdge refuses an annotation attachment whose edge
// metadata does not match the annotation's own, naming the exact values to send.
//
// THE MESSAGE PRINTS THE PAYLOAD rather than describing it, for the reason the
// link refusal does: nothing else exposes the required evidence string, so a
// refusal that only said "wrong" would leave the caller no way to be right.
func requireCoherentAnnotationEdge(idx int, where string, metadata map[string]string, e batchEdgeShape) error {
	want, err := kgtypes.MarshalAnnotationEdgeSeverity(
		metadata[kgtypes.AnnotationKindKey], metadata[kgtypes.AnnotationTierKey])
	if err != nil {
		// Same rule as the link guard: a malformed annotation has no coherent
		// edge to ask for, so the refusal names the annotation rather than
		// printing a payload that would assert nothing.
		return fmt.Errorf(
			"mutate(create_batch): edges[%d] attaches %s, a plan_annotation whose own severity is malformed: %w. "+
				"Fix the annotation first — an edge can carry no more meaning than the node it comes from",
			idx, where, err)
	}
	if e.Method == kgtypes.AnnotationEdgeMethod && e.Evidence == want {
		return nil
	}
	return fmt.Errorf(
		"mutate(create_batch): edges[%d] attaches %s, a plan_annotation, with a %q edge that does not carry its kind "+
			"and tier — a section read answers from the edge, and an edge that disagrees with its node reports a "+
			"severity nobody wrote. Re-send that edge with \"method\":%q and \"evidence\":%s. "+
			"mutate(create, type:\"plan_annotation\", links:[\"<section id>\"], metadata:{...}) writes both carriers for "+
			"you and needs no edges[] entry at all",
		idx, where, kgtypes.EdgeRelatesTo, kgtypes.AnnotationEdgeMethod, want)
}

// claimIncoherentAnnotationBatch is the InterceptMutate-shaped wrapper: it owns
// the operation test so the caller carries one unnested branch, and returns
// (true, refusal) only when it actually refuses.
func claimIncoherentAnnotationBatch(ctx context.Context, gc GraphCaller, a mutateArgs) (bool, kgtools.ToolResult) {
	if a.Operation != "create_batch" {
		return false, kgtools.ToolResult{}
	}
	if err := guardBatchAnnotationEdges(ctx, gc, a.raw); err != nil {
		return true, errorResult(err.Error())
	}
	return false, kgtools.ToolResult{}
}
