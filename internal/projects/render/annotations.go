// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"fmt"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// annotations.go reads the reviewer annotations attached to a plan's sections.
//
// AN ANNOTATION IS JOINED TO ITS SECTION BY relates-to, not by a containment
// edge, and that is what keeps it OUT of the tree: every subtree traversal asks
// for `contains` alone, so however many annotations a section carries, the
// rendered tree's shape is unchanged and only the per-section annotation LINE
// moves. It is also why the annotations have to be read separately here — the
// tree traversal genuinely cannot see them.
//
// THE READ RETURNS SUMMARIES, NOT BODIES, and that is a measurement, not a
// preference. On a real plan the annotations concentrate on the largest section:
// eight of nine reviewer annotations named one 13 KB section, and those eight
// weigh 27 KB of body against 3 KB of summary. A section read that inlined full
// bodies fits once and spills as soon as the plan grows; one that returns
// summaries with their ids has room to spare, and a reader who wants the full
// reasoning fetches that one annotation by id.

// SectionAnnotation is one annotation as a section read returns it: enough to
// decide whether to fetch the body, and the id with which to fetch it.
type SectionAnnotation struct {
	ID      string
	Kind    string
	Tier    string
	Lane    string
	Summary string
}

// FetchSectionAnnotations reads the annotations attached to sectionIDs and
// returns them grouped by section, plus the read's truncation verdict.
//
// TWO WIRE CALLS WHATEVER THE SECTION COUNT: one set-form edge drain over every
// section id at once, and ONE bulk ids[] hydrate of the annotation nodes the
// drain named. Never a read per section and never a read per annotation.
//
// THE VERDICT IS THE HYDRATE'S. The edge drain is complete-or-loud — it splits
// and re-reads rather than returning a short union — so it contributes no
// truncation; the bulk hydrate is one unbounded id set the server can clamp, and
// a clamped hydrate drops annotation rows outright. That is the only truncation
// this read can suffer, and it is what distinguishes "this section has no
// annotations" from "the read did not reach them".
func FetchSectionAnnotations(ctx context.Context, gc GraphCaller, sectionIDs []string) (map[string][]SectionAnnotation, bool, error) {
	if gc == nil || len(sectionIDs) == 0 {
		return map[string][]SectionAnnotation{}, false, nil
	}
	edges, err := IterEdgesFor(ctx, gc, sectionIDs, kgwire.IncomingEdges, kgtypes.EdgeRelatesTo)
	if err != nil {
		return nil, false, err
	}
	if len(edges) == 0 {
		return map[string][]SectionAnnotation{}, false, nil
	}

	// The peer of an INCOMING relates-to edge is its FromId — the annotation.
	// Walk the edge slice rather than a set, so a section's annotations come back
	// in a stable order rather than a randomized map one.
	annotationIDs := make([]string, 0, len(edges))
	seen := make(map[string]struct{}, len(edges))
	for _, e := range edges {
		if _, dup := seen[e.FromId]; dup {
			continue
		}
		seen[e.FromId] = struct{}{}
		annotationIDs = append(annotationIDs, e.FromId)
	}

	nodes, truncated, err := foundation.FetchNodesByIDs(ctx, gc, "", "", annotationIDs, foundation.IncludeTombstones)
	if err != nil {
		return nil, false, err
	}

	out := make(map[string][]SectionAnnotation, len(sectionIDs))
	for _, e := range edges {
		n, ok := nodes[e.FromId]
		if !ok {
			// The hydrate did not return this peer — a clamped read, which the
			// truncated verdict above already discloses. Rendering it as an
			// annotation with no kind would report a review state nobody wrote.
			continue
		}
		// ONLY a plan_annotation node counts. relates-to is the graph's most
		// common edge and anything at all may point at a section with one; a
		// reader that treated every relates-to peer as an annotation would report
		// unrelated nodes as review state.
		if kgtypes.NodeType(n.Type) != kgtypes.NodePlanAnnotation {
			continue
		}
		out[e.ToId] = append(out[e.ToId], SectionAnnotation{
			ID:      n.Id,
			Kind:    kgtypes.Value(n, kgtypes.AnnotationKindKey),
			Tier:    kgtypes.Value(n, kgtypes.AnnotationTierKey),
			Lane:    kgtypes.Value(n, kgtypes.AnnotationLaneKey),
			Summary: n.Summary,
		})
	}
	return out, truncated, nil
}

// AnnotationLines turns a per-section annotation set into the per-node line map
// RenderTreeFromIndex takes. A section with no annotations gets NO ENTRY, which
// is what makes the renderer omit its line rather than render a zero.
func AnnotationLines(bySection map[string][]SectionAnnotation) map[string]string {
	if len(bySection) == 0 {
		return nil
	}
	out := make(map[string]string, len(bySection))
	for sectionID, annotations := range bySection {
		kinds := make([]string, 0, len(annotations))
		for _, a := range annotations {
			kinds = append(kinds, a.Kind)
		}
		if line := AnnotationLine(kinds); line != "" {
			out[sectionID] = line
		}
	}
	return out
}

// SectionIDsOf returns the ids of the plan_section children in a hydrated node
// set, so a caller can read their annotations without a second traversal.
func SectionIDsOf(byID map[string]*knowledgev1.Node) []string {
	out := make([]string, 0, len(byID))
	for id, n := range byID {
		if kgtypes.NodeType(n.GetType()) == kgtypes.NodePlanSection {
			out = append(out, id)
		}
	}
	sortStringsStable(out)
	return out
}

// sortStringsStable sorts ids so the annotation read's pivot set — and therefore
// its paging — is the same on every run for one graph. A map range is
// randomized, and an unstable pivot order makes a paged read's page boundaries
// move between runs of the same query.
func sortStringsStable(ids []string) {
	sort.Strings(ids)
}

// AppendAnnotationReadFailureNotice discloses that a read's annotations are
// missing because the annotation read FAILED, naming the error.
//
// WHY IT IS NOT AppendTruncationNotice, which is what both degrade arms used to
// call. That notice says "Showing N rows — the server row ceiling engaged, so
// this subtree may be incomplete. Re-run with a smaller `limit` (the subtree
// depth) for a complete tree at that depth." Every clause of it is wrong here: no
// ceiling engaged, the subtree is complete, the count it prints is the annotation
// count rather than a row count, and `limit` is not a parameter the assemble tool
// accepts at all — so the one remedy it offers cannot be taken. A caller who
// followed it would change nothing and see the same result, and would have been
// told a false cause on the way.
//
// THE DISCLOSURE IS STILL MANDATORY. The whole reason the degrade arms raise a
// verdict is that "this section has no annotations" and "the annotations could
// not be read" are the same bytes without one. This notice says which, names the
// error so an operator can act, and offers no remedy the tool cannot take.
func AppendAnnotationReadFailureNotice(res kgtools.ToolResult, err error) kgtools.ToolResult {
	if err == nil {
		return res
	}
	res.Content = append(res.Content, kgtools.ContentBlock{
		Type: "text",
		Text: fmt.Sprintf(
			"The annotation read failed, so no annotation state is reported here and this render is INCOMPLETE rather "+
				"than showing a plan with no review on it: %v. Re-read to retry; the sections and their bodies above are "+
				"unaffected.", err),
	})
	return res
}

// SectionAnnotationCounts is one section's review state as a json row carries
// it: how many annotations, and how many of each kind.
//
// IT IS SHARED BY THE JSON ARMS rather than re-derived per arm, so assemble's
// json and plan_tree's json cannot report the same section differently.
type SectionAnnotationCounts struct {
	Count int            `json:"count"`
	Kinds map[string]int `json:"kinds"`
}

// AnnotationCounts projects a per-section annotation set onto the json shape,
// with NO ENTRY for a section that has none — which is what lets a renderer omit
// the key rather than emit a zero, the same omit-when-none rule the tree's text
// line follows and for the same reason: an unconditional key changes the bytes of
// every plan written before annotations existed.
func AnnotationCounts(bySection map[string][]SectionAnnotation) map[string]SectionAnnotationCounts {
	if len(bySection) == 0 {
		return nil
	}
	out := make(map[string]SectionAnnotationCounts, len(bySection))
	for sectionID, annotations := range bySection {
		if len(annotations) == 0 {
			continue
		}
		kinds := make(map[string]int, len(annotations))
		for _, a := range annotations {
			kinds[a.Kind]++
		}
		out[sectionID] = SectionAnnotationCounts{Count: len(annotations), Kinds: kinds}
	}
	return out
}
