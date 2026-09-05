// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/testdata/fixturelib"
)

// rotation_cross_element_test.go — the gate on CROSS-ELEMENT reading order
// carrying the page's /Rotate.
//
// WHY A SECOND ROTATION FILE. Its neighbor rotation_reading_order_test.go
// covers the INTRA-element half: it wraps every run in ONE marked-content
// region, so structtree.HybridFallback finds no untagged residue, returns
// early, and structtree.sortReadingOrder never executes. Ordering there is
// settled inside a single element by the layout stage. This file builds the
// shape that actually REACHES the cross-element sort — three tagged structure
// elements plus one untagged run on the same page — because that sort is where
// the page frame was missing.
//
// THE FOURTH RUN IS THE WHOLE POINT. Three markers are each wrapped in their
// own marked-content region and carried by their own structure element; the
// fourth is left outside every region. That fourth run is what makes the
// residue non-empty, which is the only thing that stops HybridFallback
// returning early and puts the merged blocks through sortReadingOrder. A fully
// tagged page passes this test with the defect present.
//
// WHY THE UNTAGGED PAIR. Asserting the tagged path's order alone would be an
// identity check supplying its own answer key. The SAME content therefore also
// goes through the UNTAGGED path, which never consults the structure tree and
// so cannot be moved by the repair under test: it is the control that stays
// correct when the fix is reverted.
//
// AND AN EXTERNAL EXPECTATION ON TOP OF THE PAIR, because two paths can agree
// by both being wrong. Every expected order below is DERIVED from the transform
// documented at layout/coords.go, never read off a run. All four markers share
// x=100 and descend in y, so:
//
//   - rot 0:   identity — reads as written.
//   - rot 90:  (x, y) → (y, mb.X1 - x). The four markers share an x, so they
//     land on ONE line whose x' is y ASCENDING: the LOWEST y reads first, i.e.
//     reversed.
//   - rot 180: (x, y) → (mb.X1 - x, mb.Y1 - y). One column whose y' is
//     mb.Y1 - y, so the LOWEST y reads first: reversed.
//   - rot 270: (x, y) → (mb.Y1 - y, x). One line whose x' is mb.Y1 - y, so the
//     HIGHEST y reads first: as written.
//
// want is therefore forward, reversed, reversed, forward.

var crossElementMarkers = []string{"ALPHA", "BRAVO", "CHARLIE", "DELTA"}

// crossElementBody renders the four marker words as separate text-showing
// operations 30 points apart on the page's vertical axis, in descending y so
// content-stream order and unrotated reading order coincide.
//
// tagged wraps the FIRST THREE runs in their own marked-content regions (MCID
// 1, 2, 3) so the structure tree can carry one element over each, and leaves
// the FOURTH outside every region so it arrives at HybridFallback as untagged
// residue.
func crossElementBody(tagged bool) string {
	var b strings.Builder
	for i, marker := range crossElementMarkers {
		y := 750 - i*30
		run := "BT /F1 12 Tf 100 " + itoaSmall(y) + " Td (" + marker + ") Tj ET\n"
		if tagged && i < len(crossElementMarkers)-1 {
			b.WriteString("/P << /MCID " + itoaSmall(i+1) + " >> BDC\n")
			b.WriteString(run)
			b.WriteString("EMC\n")
			continue
		}
		b.WriteString(run)
	}
	return b.String()
}

// writeCrossElementFixture emits the tagged or the untagged rendering of
// identical content at the given page /Rotate, into t.TempDir(). Nothing is
// checked in. Rotation 0 writes no /Rotate entry at all, matching an ordinary
// page.
//
// The tagged structure tree is three sibling elements under a Document root —
// a heading and two paragraphs — one per marked-content region. Three separate
// elements rather than one is what makes the ordering CROSS-element.
func writeCrossElementFixture(t *testing.T, tagged bool, rotation int) string {
	t.Helper()
	kind := "untagged"
	if tagged {
		kind = "tagged"
	}
	dst := filepath.Join(t.TempDir(), kind+".pdf")
	fonts := fixturelib.SimpleFontSpecMap(map[string]string{"F1": "Helvetica"})

	if !tagged {
		spec := fixturelib.PageSpec{Fonts: fonts, Body: crossElementBody(false), Rotation: rotation}
		if err := fixturelib.WritePDF(dst, []fixturelib.PageSpec{spec}); err != nil {
			t.Fatalf("WritePDF(rotation=%d): %v", rotation, err)
		}
		return dst
	}

	spec := fixturelib.TaggedPageSpec{
		Fonts:    fonts,
		Body:     crossElementBody(true),
		Rotation: rotation,
		StructTree: fixturelib.StructElemSpec{
			Type: "Document",
			Children: []fixturelib.StructElemSpec{
				{Type: "H1", MCIDs: []int{1}},
				{Type: "P", MCIDs: []int{2}},
				{Type: "P", MCIDs: []int{3}},
			},
		},
	}
	if err := fixturelib.WriteTaggedPDF(dst, spec); err != nil {
		t.Fatalf("WriteTaggedPDF(rotation=%d): %v", rotation, err)
	}
	return dst
}

// TestRotatedHybridPage_CrossElementReadingOrderMatchesUntaggedPath is the
// regression gate on structtree.sortReadingOrder keying on a rotation-normalized
// reading anchor rather than on raw page-space coordinates.
//
// Reverting that key to the raw bbox leaves the rest of the PDF suite green and
// turns THIS test red at rot_90 and rot_180, with rot_0 and rot_270 still
// passing — which is what makes the failure point at the missing rotation term
// rather than at reading order in general.
func TestRotatedHybridPage_CrossElementReadingOrderMatchesUntaggedPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		rotation int
		want     []string
	}{
		{name: "rot_0", rotation: 0, want: crossElementMarkers},
		{name: "rot_90", rotation: 90, want: reversed(crossElementMarkers)},
		{name: "rot_180", rotation: 180, want: reversed(crossElementMarkers)},
		{name: "rot_270", rotation: 270, want: crossElementMarkers},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			untagged := collectMarkerOrder(t, writeCrossElementFixture(t, false, tc.rotation), crossElementMarkers)
			tagged := collectMarkerOrder(t, writeCrossElementFixture(t, true, tc.rotation), crossElementMarkers)
			t.Logf("rotation=%d untagged=%v tagged=%v", tc.rotation, untagged, tagged)

			// THE CONTROL LEG. The untagged path does not consult the
			// structure tree, so this is the reading the repair under test
			// cannot move. It failing means the fixture is wrong, not the
			// cross-element merge.
			if strings.Join(untagged, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("control: untagged path at rotation %d read %v, want %v — the fixture does not establish the state this gate observes",
					tc.rotation, untagged, tc.want)
			}

			// THE SUBJECT. Identical content through the partially-tagged
			// path must read in the same order.
			if strings.Join(tagged, " ") != strings.Join(untagged, " ") {
				t.Errorf("tagged path at rotation %d read %v, untagged path read %v on identical content: cross-element ordering is not applying the page rotation",
					tc.rotation, tagged, untagged)
			}
			if strings.Join(tagged, " ") != strings.Join(tc.want, " ") {
				t.Errorf("tagged path at rotation %d read %v, want %v", tc.rotation, tagged, tc.want)
			}
		})
	}
}
