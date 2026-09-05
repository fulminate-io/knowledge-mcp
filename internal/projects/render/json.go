// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// assembleNode is a recursive tree node for JSON assemble output.
// Ported from cmd/knowledge-server/tools/tools_assemble_json.go:15,
// plus UpdatedAt: the JSON counterpart of the text renders'
// updatedSuffix, following the by-id convention (raw int64 unix nanos,
// key omitted when zero — cmd/knowledge/internal/tools/
// intercept_query_examine.go:299-304).
type assembleNode struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Status      string            `json:"status,omitempty"`
	Description string            `json:"description,omitempty"`
	UpdatedAt   int64             `json:"updated_at,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Children    []assembleNode    `json:"children,omitempty"`

	// THE THREE CHUNKED-PLAN KEYS, every one omitempty so a node that is not a
	// plan section — which is every node in every plan written before sections
	// existed — emits exactly the bytes it always did.
	//
	// BodyBytes is the section's size, so a json reader can choose its pages the
	// way the text render's `## Sections` index lets a prose reader choose them.
	// BodyOmitted says the body was dropped because it fell OUTSIDE the requested
	// range — the one thing a reader must not have to infer from an absent
	// description, which is also what an empty section looks like.
	BodyBytes   int                      `json:"body_bytes,omitempty"`
	BodyOmitted bool                     `json:"body_omitted,omitempty"`
	Annotations *SectionAnnotationCounts `json:"annotations,omitempty"`
}

// annotationsJSONFor projects one section's annotations into the shape a json
// row carries, returning nil when it has none so the key is omitted rather than
// rendered as a zero — the same omit-when-none rule the tree's text line follows,
// and for the same reason: an unconditional key changes the bytes of every plan
// written before annotations existed.
//
// THE SHAPE IS SectionAnnotationCounts, shared with the plan_tree json arm rather
// than declared per arm, so the two formats of the same read cannot report the
// same section differently.
func annotationsJSONFor(annotations []SectionAnnotation) *SectionAnnotationCounts {
	if len(annotations) == 0 {
		return nil
	}
	kinds := make(map[string]int, len(annotations))
	for _, a := range annotations {
		kinds[a.Kind]++
	}
	return &SectionAnnotationCounts{Count: len(annotations), Kinds: kinds}
}

// assembleJSON builds a recursive tree from any node and returns it
// as JSON. Universal JSON path for assemble — walks EdgeKGContains
// children recursively (same hierarchy as the text renders) and includes
// linked research/decisions as extra top-level fields.
//
// Ported from cmd/knowledge-server/tools/tools_assemble_json.go:29
// with the store reads swapped for wire-shape calls; the whole subtree now
// arrives from one AssembleSubtree traversal, so the recursion below issues no
// wire call at all.
//
// THE ARM DISCLOSES TRUNCATION TWICE, AND BOTH ARE INTENTIONAL. The `truncated`
// key goes on the ENVELOPE ROOT, unconditionally for both true and false,
// because truncation is a property of the READ and not of a node — a per-row
// key would say nothing and would inflate exactly the large payloads where
// truncation matters most, and an absent key is indistinguishable from an old
// binary. The prose notice rides alongside as its own block. The key is what a
// machine reads; the block is what a caller reads.
func assembleJSON(
	ctx context.Context, gc GraphCaller, node *knowledgev1.Node, sectionStart, sectionEnd *int,
) kgtools.ToolResult {
	childIndex, byID, _, truncated := AssembleSubtree(ctx, gc, node.Id, 5)

	// THE SECTION RANGE IS HONORED HERE, and its absence was a SILENT DROP: this
	// arm used to take no range at all, so assemble(id:<plan>, format:"json",
	// section_start, section_end) returned the whole plan — bytes identical to the
	// same call with no range, and on a real plan above the point where a result
	// spills. The caller was told nothing. A range that cannot be honored is
	// refused by resolveSectionRange below, naming the bound; a range that can be
	// is applied. Neither is a drop.
	annotations, annotationsTruncated, annotationErr := assembleJSONAnnotations(ctx, gc, node, byID)
	// THE ANNOTATION READ'S VERDICT IS ORed IN, exactly as the two text arms and
	// the json plan_tree arm already do. Discarding it here made this the one read
	// that could not distinguish "no annotations" from "the hydrate was clamped" —
	// and because `truncated` rides the envelope unconditionally, discarding it did
	// not leave the caller uninformed, it told them affirmatively that a plan under
	// review carried no review state.
	truncated = truncated || annotationsTruncated
	tree := buildAssembleTree(node, 0, 5, childIndex)
	if err := applySectionRangeJSON(&tree, sectionStart, sectionEnd, annotations); err != nil {
		return kgtools.ErrorResult(err.Error())
	}
	result := map[string]any{"root": tree, "truncated": truncated}

	research, decisions, linkedTruncated := collectLinkedNodes(ctx, gc, node.Id)
	truncated = truncated || linkedTruncated
	result["truncated"] = truncated
	if len(research) > 0 {
		result["research"] = research
	}
	if len(decisions) > 0 {
		result["decisions"] = decisions
	}

	b, err := json.Marshal(result)
	if err != nil {
		return kgtools.ErrorResult("json marshal: " + err.Error())
	}
	out := AppendTruncationNotice(kgtools.TextResult(string(b)), truncated, len(byID))
	return AppendAnnotationReadFailureNotice(out, annotationErr)
}

// assembleJSONAnnotations reads the annotations on a hydrated subtree's sections,
// degrading to none and returning BOTH disclosures the caller owes: the read's
// truncation verdict and its error.
//
// TWO CAUSES, TWO CARRIERS. An error means the read failed outright and gets its
// own notice naming it; a truncation verdict means the bulk hydrate was clamped
// and rows were dropped, which is the ordinary row-ceiling disclosure. A helper
// that returned only the error left the clamped case indistinguishable from a
// plan nobody has reviewed.
func assembleJSONAnnotations(
	ctx context.Context, gc GraphCaller, root *knowledgev1.Node, byID map[string]*knowledgev1.Node,
) (map[string][]SectionAnnotation, bool, error) {
	sectionIDs := SectionIDsOf(byID)
	// THE ROOT COUNTS TOO, and leaving it out was the whole reason a section read
	// in json carried no review state. assemble of a plan_section id makes that
	// section the ROOT, and a root is not among its own descendants — so a helper
	// that only walked the hydrated descendants found no sections at all and
	// returned nothing, on exactly the read whose text form leads with an
	// annotations block.
	if kgtypes.NodeType(root.GetType()) == kgtypes.NodePlanSection {
		sectionIDs = append(sectionIDs, root.GetId())
	}
	if len(sectionIDs) == 0 {
		return nil, false, nil
	}
	annotations, truncated, err := FetchSectionAnnotations(ctx, gc, sectionIDs)
	if err != nil {
		slog.Warn("assemble json: annotation read failed; rendering without annotation state", "error", err)
		return nil, false, err
	}
	return annotations, truncated, nil
}

// applySectionRangeJSON stamps every plan_section child of the root with its size
// and its review state, and drops the bodies OUTSIDE the caller's range.
//
// THE RANGE INDEXES THE SECTION SEQUENCE, not the child list, exactly as the text
// path's does — a plan's children are its sections plus its open questions, and a
// range that counted questions would shift under a plan that has them. The two
// paths call the same resolveSectionRange, so an invalid range is refused with
// the same message in both formats.
//
// A SECTION OUTSIDE THE RANGE KEEPS ITS ROW, with its id, name, size and review
// state and no body. That is what makes the result a PAGE rather than a subset: a
// reader can see what they did not ask for and ask for it next.
func applySectionRangeJSON(
	root *assembleNode, start, end *int, annotations map[string][]SectionAnnotation,
) error {
	// A SECTION READ MAKES THE SECTION THE ROOT, and its own review state belongs
	// on it. The text form of that read leads with an `## Annotations` block; the
	// json form carried nothing until this line.
	if kgtypes.NodeType(root.Type) == kgtypes.NodePlanSection {
		root.BodyBytes = len(root.Description)
		root.Annotations = annotationsJSONFor(annotations[root.ID])
	}
	sections := make([]*assembleNode, 0, len(root.Children))
	for i := range root.Children {
		if kgtypes.NodeType(root.Children[i].Type) == kgtypes.NodePlanSection {
			sections = append(sections, &root.Children[i])
		}
	}
	// A PLAN WITH NO SECTIONS AND NO RANGE has nothing to do here. WITH a range it
	// does: resolveSectionRange's own count==0 arm refuses, naming the plan shape,
	// and reaching it is exactly why this guard is conditional rather than flat.
	//
	// A FLAT RETURN HERE WAS THE DEFECT. It sat above the resolver, so a caller who
	// asked for a page of a phase-and-step plan in json received the whole plan,
	// byte-identical to the no-range call and carrying no error — while the text
	// arm, which calls resolveSectionRange from writeSectionRange with no such
	// guard, refused the identical request. Every plan on this project is a
	// phase-and-step plan, so that was the input class the arm actually met.
	if len(sections) == 0 && start == nil && end == nil {
		return nil // not a sectioned plan and no page asked for; nothing here applies.
	}
	for _, sec := range sections {
		sec.BodyBytes = len(sec.Description)
		sec.Annotations = annotationsJSONFor(annotations[sec.ID])
	}

	// NO RANGE MEANS THE INDEX AND THE TREE, NOT EVERY BODY — the default the
	// schema, help(assemble) and the generated guide all document, stated without
	// a format qualifier because it is meant to hold for both. It did not: this
	// arm returned every section body, so a caller following the documentation got
	// 76,093 bytes on a ten-section plan of realistic size against 2,458 for the
	// identical text call, and the json figure is ABOVE the point where a result
	// spills. That is precisely the outcome the paging requirement exists to
	// prevent, reached by doing what the tool says to do.
	//
	// THE BODIES ARE DROPPED AS OMITTED rather than simply absent, the same way an
	// out-of-range body is, because an absent description is also what an EMPTY
	// section looks like and a reader must not have to guess which they have.
	if start == nil && end == nil {
		for _, sec := range sections {
			sec.Description = ""
			sec.BodyOmitted = true
		}
		return nil
	}
	lo, hi, err := resolveSectionRange(start, end, len(sections))
	if err != nil {
		return err
	}
	for i, sec := range sections {
		if i < lo || i > hi {
			sec.Description = ""
			sec.BodyOmitted = true
		}
	}
	return nil
}

// buildAssembleTree recursively builds a tree of assembleNode from the
// prefetched parent→child index. It takes no graph caller, which is the
// structural guarantee that the recursion cannot issue a wire call.
//
// CHILDREN-KEY CONTRACT: Children is `omitempty`, so a node with no children
// emits NO children key at all. The index path preserves that naturally —
// childIndex holds no entry for a childless parent, so nothing is appended and
// omitempty drops the key.
func buildAssembleTree(
	node *knowledgev1.Node,
	depth, maxDepth int,
	childIndex map[string][]*knowledgev1.Node,
) assembleNode {
	an := assembleNode{
		ID:          node.Id,
		Name:        node.SymbolName,
		Type:        node.Type,
		Status:      node.Status,
		Description: node.Description,
		UpdatedAt:   node.UpdatedAt,
		Metadata:    nonEmptyMeta(node),
	}

	if depth >= maxDepth {
		return an
	}

	for _, cn := range childIndex[node.Id] {
		an.Children = append(an.Children, buildAssembleTree(cn, depth+1, maxDepth, childIndex))
	}

	return an
}

// collectLinkedNodes finds research and decision nodes linked to
// nodeID via EdgeInformedBy (outgoing for research, incoming for
// decisions). Ported from tools_assemble_json.go:79; both sides share ONE bulk
// hydrate, and each renders by walking its own EDGE slice so the emitted arrays
// keep edge order rather than the hydrated map's undefined one. The third
// return is the hydrate's truncation verdict.
func collectLinkedNodes(ctx context.Context, gc GraphCaller, nodeID string) ([]assembleNode, []assembleNode, bool) {
	var research, decisions []assembleNode

	outEdges, _ := IterEdges(ctx, gc, nodeID, kgwire.OutgoingEdges, kgtypes.EdgeInformedBy)
	inEdges, _ := IterEdges(ctx, gc, nodeID, kgwire.IncomingEdges, kgtypes.EdgeInformedBy)

	peerIDs := make([]string, 0, len(outEdges)+len(inEdges))
	for _, e := range outEdges {
		peerIDs = append(peerIDs, e.ToId)
	}
	for _, e := range inEdges {
		peerIDs = append(peerIDs, e.FromId)
	}
	peers, truncated, _ := foundation.FetchNodesByIDs(ctx, gc, "", "", peerIDs, foundation.IncludeTombstones)

	for _, e := range outEdges {
		if n, ok := peers[e.ToId]; ok && kgtypes.NodeType(n.Type) == kgtypes.NodeResearch {
			research = append(research, nodeToAssembleNode(n))
		}
	}
	for _, e := range inEdges {
		if n, ok := peers[e.FromId]; ok && kgtypes.NodeType(n.Type) == kgtypes.NodeDecision {
			decisions = append(decisions, nodeToAssembleNode(n))
		}
	}

	return research, decisions, truncated
}

// nodeToAssembleNode converts a wire node to a flat assembleNode
// (no children). Ported from tools_assemble_json.go:120, plus the
// UpdatedAt carry that buildAssembleTree also performs.
func nodeToAssembleNode(n *knowledgev1.Node) assembleNode {
	return assembleNode{
		ID:          n.Id,
		Name:        n.SymbolName,
		Type:        n.Type,
		Status:      n.Status,
		Description: n.Description,
		UpdatedAt:   n.UpdatedAt,
		Metadata:    nonEmptyMeta(n),
	}
}

// nonEmptyMeta returns the node's metadata map only if it has
// entries. Verbatim port of tools_assemble_json.go:132.
func nonEmptyMeta(n *knowledgev1.Node) map[string]string {
	if len(n.Metadata) == 0 {
		return nil
	}
	return n.Metadata
}
