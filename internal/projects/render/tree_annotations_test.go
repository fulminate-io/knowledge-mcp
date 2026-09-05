// SPDX-License-Identifier: Apache-2.0

package render

// tree_annotations_test.go covers the per-node annotation line the tree renderer
// emits for a section carrying reviewer annotations.
//
// THE ZERO CASE IS THE LOAD-BEARING ONE. An unconditional "0 annotations" line
// would change the rendered bytes of every existing phase-and-step plan, which
// the one-version-back requirement forbids. "Zero annotations" versus "the read
// did not reach them" is carried by the read's own completeness disclosure, not
// by a per-node zero.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func renderWithAnnotations(t *testing.T, annotations map[string]string) string {
	t.Helper()
	root := node("root")
	sec := node("sec-0")
	childIndex := map[string][]*knowledgev1.Node{"root": {sec}}
	var sb strings.Builder
	RenderTreeFromIndex(&sb, root, 0, 3, childIndex, map[string]string{}, annotations)
	return sb.String()
}

// R3-e: one annotation.
func TestRenderTreeFromIndex_AnnotationLine_One(t *testing.T) {
	out := renderWithAnnotations(t, map[string]string{"sec-0": "annotations: 1 (finding 1)"})
	assert.Contains(t, out, "annotations: 1 (finding 1)")
}

// R3-e: many, mixed kinds — rendered in a fixed order so one graph renders one
// way across runs.
func TestRenderTreeFromIndex_AnnotationLine_MixedKinds(t *testing.T) {
	out := renderWithAnnotations(t, map[string]string{"sec-0": "annotations: 4 (correct 1, finding 2, needed change 1)"})
	assert.Contains(t, out, "annotations: 4 (correct 1, finding 2, needed change 1)")
}

// R3-e, the ZERO case: NO LINE IS EMITTED AT ALL, not a zero line. Asserted on
// the rendered bytes against the same tree rendered with a nil map, so the two
// are byte-identical.
func TestRenderTreeFromIndex_AnnotationLine_OmittedWhenNone(t *testing.T) {
	withEmpty := renderWithAnnotations(t, map[string]string{})
	withNil := renderWithAnnotations(t, nil)
	withAbsentKey := renderWithAnnotations(t, map[string]string{"some-other-node": "annotations: 3 (finding 3)"})

	assert.NotContains(t, withEmpty, "annotations")
	assert.Equal(t, withNil, withEmpty, "an empty map renders exactly as no map at all")
	assert.Equal(t, withNil, withAbsentKey, "a map with no entry for this node renders exactly as no map at all")

	// CONTROL through the same instrument: the identical tree WITH an entry does
	// render the line, so the absences above are a real omission rather than a
	// renderer that never emits the line.
	withOne := renderWithAnnotations(t, map[string]string{"sec-0": "annotations: 1 (correct 1)"})
	assert.Contains(t, withOne, "annotations: 1 (correct 1)")
	assert.NotEqual(t, withNil, withOne)
}

// The line's PLACEMENT: after the description truncate and before the ID line,
// so a reader scanning a tree sees the annotation state beside the section it
// belongs to.
func TestRenderTreeFromIndex_AnnotationLinePlacement(t *testing.T) {
	root := node("root")
	sec := node("sec-0")
	sec.Description = "the section body"
	childIndex := map[string][]*knowledgev1.Node{"root": {sec}}
	var sb strings.Builder
	RenderTreeFromIndex(&sb, root, 0, 3, childIndex, map[string]string{},
		map[string]string{"sec-0": "annotations: 2 (finding 2)"})

	lines := strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")
	require.Len(t, lines, 6, "root line, root ID, section line, description, annotations, section ID — got %v", lines)
	assert.Contains(t, lines[3], "the section body")
	assert.Contains(t, lines[4], "annotations: 2 (finding 2)")
	assert.Contains(t, lines[5], "ID: sec-0", "the annotation line sits between the description and the ID line")
}

// AnnotationLine composes the rendered text from a kind census, in a FIXED kind
// order, so one annotation set renders one way whatever order the read returned.
func TestAnnotationLine(t *testing.T) {
	assert.Equal(t, "annotations: 1 (correct 1)", AnnotationLine([]string{"correct"}))
	assert.Equal(t, "annotations: 3 (correct 1, finding 1, needed change 1)",
		AnnotationLine([]string{"needed change", "correct", "finding"}))
	assert.Equal(t, "annotations: 2 (finding 2)", AnnotationLine([]string{"finding", "finding"}))
	assert.Empty(t, AnnotationLine(nil), "no annotations composes no line, which is what keeps the line omitted")
	assert.Empty(t, AnnotationLine([]string{}))
	// An unrecognized kind is SHOWN rather than dropped: the read found an
	// annotation, and hiding it because its kind is unfamiliar would under-report
	// the review state. Unrecognized kinds sort after the three known ones.
	assert.Equal(t, "annotations: 2 (correct 1, mystery 1)", AnnotationLine([]string{"mystery", "correct"}))
}
