// SPDX-License-Identifier: Apache-2.0

package render_test

// section_annotation_reads_test.go is the ANNOTATION half of the chunked-plan
// seam suite, split out of section_reads_test.go for the repository's 500-line
// per-file cap. It shares that file's wire fixture and helpers: both sides of
// this seam are production code — projects.BuildPlanGraph writes and
// render.Handle reads — and the only stand-in is the wire itself.
//
// WHAT THIS HALF COVERS: where an annotation ATTACHES (the section, never only
// the root), what counts as one (a typed plan_annotation, not every relates-to
// peer), and the two state transitions — a hole left by a deleted section, and
// an annotation arriving between a section read and a section re-write.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// R3-a / R3-c. An annotation joins the SECTION, never only the root, and the
// section's own body is unchanged by it.
func TestSectionedPlan_AnnotationAttachesToTheSectionNotTheRoot(t *testing.T) {
	w := newWireFixture()
	ids := seedFromBuilder(t, w, tenSectionPlan(), "plan-7")
	sectionID := ids[1]
	bodyBefore := w.nodes[sectionID].Description

	ann := &knowledgev1.Node{
		Id: "ann-s", SymbolName: "a finding on section 0",
		Type: string(kgtypes.NodePlanAnnotation), Summary: "the touch points miss a caller",
	}
	kgtypes.SetValue(ann, kgtypes.AnnotationKindKey, kgtypes.AnnotationKindFinding)
	kgtypes.SetValue(ann, kgtypes.AnnotationTierKey, "T2")
	w.addNode(ann)
	w.addEdge("ann-s", sectionID, string(kgtypes.EdgeRelatesTo), "", "")

	// The SECTION's annotation read finds it.
	bySection, _, err := render.FetchSectionAnnotations(context.Background(), w, []string{sectionID, "plan-7"})
	require.NoError(t, err)
	require.Len(t, bySection[sectionID], 1, "the annotation is joined to the section")
	assert.Equal(t, "ann-s", bySection[sectionID][0].ID)

	// The ROOT's is empty. SAME-RUN CONTROL through the same instrument: an
	// annotation deliberately attached AT THE ROOT shows the opposite, so the
	// emptiness above is a real absence rather than a read that finds nothing.
	assert.Empty(t, bySection["plan-7"], "the annotation is NOT joined to the root")
	rootAnn := &knowledgev1.Node{
		Id: "ann-root", SymbolName: "a plan-level note",
		Type: string(kgtypes.NodePlanAnnotation), Summary: "a note on the whole plan",
	}
	kgtypes.SetValue(rootAnn, kgtypes.AnnotationKindKey, kgtypes.AnnotationKindCorrect)
	w.addNode(rootAnn)
	w.addEdge("ann-root", "plan-7", string(kgtypes.EdgeRelatesTo), "", "")
	bySection2, _, err := render.FetchSectionAnnotations(context.Background(), w, []string{sectionID, "plan-7"})
	require.NoError(t, err)
	assert.Len(t, bySection2["plan-7"], 1, "control: a root-attached annotation IS found at the root")

	// R3-c: the section body is untouched by either attachment.
	assert.Equal(t, bodyBefore, w.nodes[sectionID].Description,
		"attaching an annotation writes nothing to the section it annotates")
}

// A relates-to peer that is NOT a plan_annotation is not counted as one.
// relates-to is the graph's most common edge and anything at all may point at a
// section with one; a reader that treated every peer as review state would report
// annotations nobody wrote.
func TestSectionedPlan_NonAnnotationPeersAreNotAnnotations(t *testing.T) {
	w := newWireFixture()
	ids := seedFromBuilder(t, w, tenSectionPlan(), "plan-8")
	sectionID := ids[1]
	w.addNode(&knowledgev1.Node{
		Id: "finding-1", SymbolName: "an unrelated finding",
		Type: string(kgtypes.NodeFinding), Summary: "not review state on this section",
	})
	w.addEdge("finding-1", sectionID, string(kgtypes.EdgeRelatesTo), "", "")

	bySection, _, err := render.FetchSectionAnnotations(context.Background(), w, []string{sectionID})
	require.NoError(t, err)
	assert.Empty(t, bySection[sectionID], "a relates-to peer of another type is not an annotation")
	assert.NotContains(t, toolText(handleAssemble(w, `{"id":"plan-8"}`)), "annotations:")
}

// T2. THE RULING ON A HOLE, pinned: a gap in the position sequence is LEGAL and
// ordering stays ascending.
//
// WHY LEGAL RATHER THAN REFUSED. Deleting a section leaves a hole, and closing it
// would mean rewriting every later section's position — the whole-plan rewrite
// the chunked shape exists to remove. Ordering is ascending by key, which a gap
// does not disturb. The cost is that a position is not an index into the section
// list; the section RANGE indexes the sequence, not the position values, so a
// hole does not shift a caller's pages either.
func TestSectionedPlan_AHoleInThePositionSequenceIsLegal(t *testing.T) {
	pos := func(i int) *int { return &i }
	args := projects.PlanArgs{Name: "holed", Goal: "g", Summary: "s", Sections: []projects.SectionArgs{
		{Name: "kept first", Body: "BODY-A", Summary: "a", Position: pos(0)},
		{Name: "kept third", Body: "BODY-C", Summary: "c", Position: pos(2)},
		{Name: "kept fourth", Body: "BODY-D", Summary: "d", Position: pos(3)},
	}}
	w := newWireFixture()
	seedFromBuilder(t, w, args, "plan-9")

	body := toolText(handleAssemble(w, `{"id":"plan-9"}`))
	assert.Equal(t, []string{"kept first", "kept third", "kept fourth"},
		orderOfNames(sliceFrom(t, body, "## Sections"), []string{"kept first", "kept third", "kept fourth"}),
		"a hole leaves the surviving sections in ascending position order")
	assert.Contains(t, body, "- [0] kept first")
	assert.Contains(t, body, "- [2] kept third", "the positions are reported verbatim, not compacted")
	assert.Contains(t, body, "- [3] kept fourth")

	// And the RANGE indexes the section SEQUENCE, so a hole does not move a
	// caller's pages: [0,1] is the first two surviving sections.
	page := toolText(handleAssemble(w, `{"id":"plan-9","section_start":0,"section_end":1}`))
	rangeBlock := sliceFrom(t, page, "\n## Section ")
	assert.Contains(t, rangeBlock, "BODY-A")
	assert.Contains(t, rangeBlock, "BODY-C")
	assert.NotContains(t, rangeBlock, "BODY-D")
}

// T3. An annotation added between a section READ and a section RE-WRITE: the
// re-write does not disturb it, and the next read shows it.
func TestSectionedPlan_AnnotationAddedBetweenAReadAndAReWrite(t *testing.T) {
	w := newWireFixture()
	ids := seedFromBuilder(t, w, tenSectionPlan(), "plan-10")
	sectionID := ids[1]

	// READ: no annotations yet, and therefore no line.
	first := toolText(handleAssemble(w, `{"id":"`+sectionID+`"}`))
	assert.NotContains(t, first, "## Annotations")

	// A reviewer attaches one.
	ann := &knowledgev1.Node{
		Id: "ann-t3", SymbolName: "mid-flight", Type: string(kgtypes.NodePlanAnnotation),
		Summary: "attached between the read and the re-write",
	}
	kgtypes.SetValue(ann, kgtypes.AnnotationKindKey, kgtypes.AnnotationKindCorrect)
	w.addNode(ann)
	w.addEdge("ann-t3", sectionID, string(kgtypes.EdgeRelatesTo), "", "")

	// RE-WRITE the section body, the way a planner applying a settlement does.
	w.nodes[sectionID].Description = "BODY-0 REVISED"

	// The annotation survives the body write and the next read shows it: the two
	// are different nodes, which is why a review round no longer races a rewrite.
	second := toolText(handleAssemble(w, `{"id":"`+sectionID+`"}`))
	assert.Contains(t, second, "BODY-0 REVISED")
	assert.Contains(t, second, "## Annotations (1)")
	assert.Contains(t, second, "attached between the read and the re-write")
}
