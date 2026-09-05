// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/testdata/fixturelib"
)

// rotation_reading_order_test.go — the gate on the page's own frame reaching
// the structure-tree path.
//
// WHAT IS PINNED HERE. structtree.newPageRunIndex accepts a layout.PageInfo —
// the page's media box and its /Rotate value — and STORES it, and the
// structure-tree walk's layout.LinesFromRuns call reads it back. The parameter
// was once accepted and then dropped, which left every index the constructor
// built carrying the zero PageInfo: no media box and no rotation. A tagged
// page with a non-zero /Rotate then laid its element out in raw content-stream
// geometry instead of reading order, and nothing in the suite said so. This
// file is what says so.
//
// WHY THE UNTAGGED PAIR. Asserting the tagged path's order alone would be an
// identity check supplying its own answer key — whatever the walk emits is
// what the assertion would learn. So the SAME three runs also go through the
// UNTAGGED path. That path never consults the structure tree, so its reading
// cannot be moved by the assignment under test: it is the control that stays
// correct when the repair is reverted, and the two paths must agree on
// identical content.
//
// AND AN EXTERNAL EXPECTATION ON TOP OF THE PAIR, because two paths can agree
// by both being wrong. Both orders are pinned literally and both are derived
// from the transform documented at layout/coords.go, not read off a run: for
// /Rotate 90 the normalizer maps (x, y) to (y, mediaBoxWidth - x), so three
// runs sharing an x and descending in y land on ONE line whose x ascends in
// the opposite order. The rotated page therefore reads the three markers
// exactly reversed from the unrotated one.
//
// WHY THREE RUNS AND NOT TWO. The page-scale line grouper short-circuits below
// three runs (layout/lines.go, the len(runs) < 3 guard): each run becomes its
// own line in content-stream order with no banding and no sort, so a two-run
// untagged fixture reports the content-stream order at EVERY rotation and is
// no control at all. Three runs is the smallest fixture that puts both paths
// through the real banding. The three markers are equal-length on purpose —
// a 90 degree normalize swaps each run's width and height, so unequal words
// would perturb the line-band tolerance the fixture depends on.

var rotationMarkers = []string{"ALPHA", "BRAVO", "DELTA"}

// rotationBody renders the three marker words as separate text-showing
// operations 100 points apart on the page's vertical axis, in descending y so
// content-stream order and unrotated reading order coincide. tagged wraps all
// three in ONE marked-content region so the structure tree can carry a single
// <P> over them — the shape that sends them through the structure-tree walk as
// one element.
func rotationBody(tagged bool) string {
	var b strings.Builder
	for i, word := range rotationMarkers {
		y := 700 - i*100
		b.WriteString("BT /F1 12 Tf 100 ")
		b.WriteString(itoaSmall(y))
		b.WriteString(" Td (")
		b.WriteString(word)
		b.WriteString(") Tj ET\n")
	}
	if !tagged {
		return b.String()
	}
	return "/P << /MCID 1 >> BDC\n" + b.String() + "EMC\n"
}

// itoaSmall renders a small non-negative int without pulling strconv into a
// content-stream builder that only ever emits three y coordinates.
func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// writeRotationFixture emits one of the four fixtures this file needs: the
// tagged or untagged rendering of identical content, at the given page
// /Rotate. Rotation 0 writes no /Rotate entry at all, matching an ordinary
// page.
func writeRotationFixture(t *testing.T, tagged bool, rotation int) string {
	t.Helper()
	kind := "untagged"
	if tagged {
		kind = "tagged"
	}
	dst := filepath.Join(t.TempDir(), kind+".pdf")
	fonts := fixturelib.SimpleFontSpecMap(map[string]string{"F1": "Helvetica"})

	if !tagged {
		spec := fixturelib.PageSpec{Fonts: fonts, Body: rotationBody(false), Rotation: rotation}
		if err := fixturelib.WritePDF(dst, []fixturelib.PageSpec{spec}); err != nil {
			t.Fatalf("WritePDF(rotation=%d): %v", rotation, err)
		}
		return dst
	}

	spec := fixturelib.TaggedPageSpec{
		Fonts:    fonts,
		Body:     rotationBody(true),
		Rotation: rotation,
		StructTree: fixturelib.StructElemSpec{
			Type:     "Document",
			Children: []fixturelib.StructElemSpec{{Type: "P", MCIDs: []int{1}}},
		},
	}
	if err := fixturelib.WriteTaggedPDF(dst, spec); err != nil {
		t.Fatalf("WriteTaggedPDF(rotation=%d): %v", rotation, err)
	}
	return dst
}

// collectMarkerOrder drives the fixture through the REAL collector — not
// layout.LinesFromRuns directly — and reduces the emitted graph to the order
// the markers appear in across every non-root node's Content, in emission
// order. The marker set is a parameter rather than a package-level constant so
// a second rotated-order fixture with its own words can share this reducer
// instead of copying it.
//
// Reducing to the marker sequence rather than to a node count is deliberate:
// the property under test is READING ORDER, and it must stay observable
// whether the chunker lands the three runs on one node or on three.
func collectMarkerOrder(t *testing.T, path string, markers []string) []string {
	t.Helper()
	c := &PDFCollector{}
	res, err := c.Collect(context.Background(), path, collector.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect(%s): %v", path, err)
	}

	var b strings.Builder
	for _, n := range res.Nodes {
		if n.GetType() == "document" {
			continue
		}
		b.WriteString(n.GetContent())
		b.WriteString("\n")
	}
	text := b.String()

	order := make([]string, 0, len(markers))
	for i := 0; i < len(text); {
		matched := false
		for _, word := range markers {
			if strings.HasPrefix(text[i:], word) {
				order = append(order, word)
				i += len(word)
				matched = true
				break
			}
		}
		if !matched {
			i++
		}
	}
	if len(order) != len(markers) {
		t.Fatalf("fixture %s emitted marker sequence %v from content %q; the order assertion needs exactly the %d markers",
			filepath.Base(path), order, text, len(markers))
	}
	return order
}

// reversed returns a copy of words in reverse order.
func reversed(words []string) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[len(words)-1-i] = w
	}
	return out
}

// TestRotatedTaggedPage_ReadingOrderMatchesUntaggedPath is the regression gate
// on structtree.newPageRunIndex storing its pageInfo argument.
//
// Reverting that one assignment leaves the rest of the PDF suite green and
// turns THIS test red on its rotated tagged leg alone, with the unrotated
// tagged leg and both untagged legs still passing — which is what makes the
// failure point at the page frame rather than at reading order in general.
func TestRotatedTaggedPage_ReadingOrderMatchesUntaggedPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		rotation int
		want     []string
	}{
		{
			// The markers descend in y and the page frame is the identity,
			// so reading order is top to bottom as written.
			name:     "unrotated",
			rotation: 0,
			want:     rotationMarkers,
		},
		{
			// /Rotate 90 is clockwise display rotation. The normalizer's
			// inverse maps (x, y) to (y, width - x), so runs sharing an x
			// and descending in y become one line whose x ascends in the
			// opposite order: the markers read exactly reversed.
			name:     "rot90",
			rotation: 90,
			want:     reversed(rotationMarkers),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			untagged := collectMarkerOrder(t, writeRotationFixture(t, false, tc.rotation), rotationMarkers)
			tagged := collectMarkerOrder(t, writeRotationFixture(t, true, tc.rotation), rotationMarkers)
			t.Logf("rotation=%d untagged=%v tagged=%v", tc.rotation, untagged, tagged)

			// THE CONTROL LEG. The untagged path does not consult the
			// structure tree, so this is the reading the repair under test
			// cannot move. It failing means the fixture or the layout stage
			// is wrong, not the structure-tree walk.
			if strings.Join(untagged, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("control: untagged path at rotation %d read %v, want %v — the fixture does not establish the state this gate observes",
					tc.rotation, untagged, tc.want)
			}

			// THE SUBJECT. Identical content through the tagged path must
			// read in the same order.
			if strings.Join(tagged, " ") != strings.Join(untagged, " ") {
				t.Errorf("tagged path at rotation %d read %v, untagged path read %v on identical content: the page frame is not reaching the structure-tree walk",
					tc.rotation, tagged, untagged)
			}
			if strings.Join(tagged, " ") != strings.Join(tc.want, " ") {
				t.Errorf("tagged path at rotation %d read %v, want %v", tc.rotation, tagged, tc.want)
			}
		})
	}
}
