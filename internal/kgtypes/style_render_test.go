// SPDX-License-Identifier: Apache-2.0

// style_render_test.go — the shared width vocabulary, pinned by LITERALS.
//
// WHY LITERALS AND NOT THE CONSTANTS. Both compact style-rule renders read this
// package for their column cap, their ellipsis and their row bound, and both
// suites assert against those same constants. That makes the two renders agree
// with each other by construction — and agree with NOTHING ELSE. Changing
// StyleIndexEllipsis from "..." to any other string left the render suite, the
// tools suite and this package all green, because every assertion in the tree
// took its expected value from the constant it was checking. The expectations
// below are written out; a constant edited without a decision to edit it is red
// here, and a decision to edit it is one line to change with the reason in the
// commit.
//
// THE CAP'S BOUNDARY TABLE is the second half. The tree exercised one over-cap
// input and one short one; four classes between them — one under, exactly at,
// one over, and a cut landing inside a three- or four-byte rune — went
// unobserved while production was correct on all of them.

package kgtypes

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStyleRenderConstantsAreTheDocumentedValues pins the three shared figures.
//
// THE MUTATION: change any one of them and this is red — and for the ellipsis
// the render package's uses-edge golden goes red beside it, which is the second,
// independent instrument.
func TestStyleRenderConstantsAreTheDocumentedValues(t *testing.T) {
	assert.Equal(t, 60, StyleIndexColumnCap,
		"the column cap is 60 bytes; both compact renders and both bound arithmetics assume it")
	assert.Equal(t, "...", StyleIndexEllipsis,
		"THE ELLIPSIS SPELLING. Three ASCII periods, not the single-rune character: "+
			"it is rendered into markdown and into tab-separated rows a reader parses back, "+
			"and nothing else in the tree pinned it")
	assert.Equal(t, 4, StyleIndexCappedColumns,
		"four columns of a compact line pass through the cap: severity, scope, "+
			"summary and the sister-check id. The row's own id is not among them")
	assert.Equal(t, 5, StyleIndexRowSeparatorBytes,
		"a row's fixed non-column overhead: four tabs joining five columns, plus one newline")
}

// TestStyleIndexRowBoundIsAFunctionOfTheID drives the row bound over the id
// width axis with EVERY expected value written out as a literal.
//
// WHY THIS TEST EXISTS. The row bound was a single figure, 280, whose documented
// arithmetic budgeted 32 bytes for the row's own id — and nothing bounds that
// id: a style rule's id is written verbatim from the imported rule list and
// validated for name, summary, text, severity and scope, never for width. Every
// bound test in the tree held the id at exactly 32 bytes, the budgeted figure,
// so the one term no cap protected was the one term nothing drove. A bound is
// only as true as its BUDGETED terms; this one now has none.
//
// THE MUTATION: budget the id at a fixed 32 again — make the body
// `32 + StyleIndexCappedColumns*StyleIndexColumnCap + StyleIndexRowSeparatorBytes` —
// and every row below except the 32-byte one goes red, as does the
// two-different-ids arm.
func TestStyleIndexRowBoundIsAFunctionOfTheID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		idWidth int
		want    int
	}{
		// 4×60 + 5 = 245 is the whole bound when there is no id at all.
		{"an empty id costs only the capped columns and the separators", 0, 245},
		{"the 32-byte width the superseded constant budgeted", 32, 277},
		{"64 bytes, the first width at which the superseded constant was false", 64, 309},
		{"200 bytes", 200, 445},
		{"4000 bytes, a width a practice node's metadata admits", 4000, 4245},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, StyleIndexRowBound(strings.Repeat("z", tc.idWidth)),
				"the documented arithmetic is len(id) + 4×60 + 5")
		})
	}

	// THE ARM THAT CANNOT BE SATISFIED BY A BUDGET. A bound that pays the id at
	// its measured width answers differently for two ids of different widths; a
	// bound that budgets one answers the same for both, whatever the figure.
	assert.NotEqual(t, StyleIndexRowBound("a"), StyleIndexRowBound("aa"),
		"the bound is a FUNCTION of the id, so two ids of different widths cannot share one bound")
}

// TestCapStyleColumnBoundaryTable drives the cap at every boundary class, each
// expected value written out rather than derived from the cap.
//
// THE OVER-CAP CASE IS THE CONTROL: without a class that DOES truncate, "the
// value came back unchanged" is satisfied just as well by a function that never
// caps anything.
func TestCapStyleColumnBoundaryTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty is empty",
			in:   "",
			want: "",
		},
		{
			name: "one byte under the cap is untouched",
			in:   strings.Repeat("a", 59),
			want: strings.Repeat("a", 59),
		},
		{
			name: "exactly at the cap is untouched and is NOT marked truncated",
			in:   strings.Repeat("a", 60),
			want: strings.Repeat("a", 60),
		},
		{
			// THE CONTROL. 61 bytes in, 60 out: 57 kept plus three periods.
			name: "one byte over the cap truncates visibly to exactly 60 bytes",
			in:   strings.Repeat("a", 61),
			want: strings.Repeat("a", 57) + "...",
		},
		{
			// 61 bytes in. The naive cut at 57 lands inside the 19th three-byte
			// rune, so it walks back to 55 and the result is 58 bytes — UNDER
			// the cap, which is the correct answer rather than a miss.
			name: "a cut landing inside a three-byte rune walks back to the boundary",
			in:   "a" + strings.Repeat("界", 20),
			want: "a" + strings.Repeat("界", 18) + "...",
		},
		{
			// 64 bytes in. The cut at 57 lands inside the 15th four-byte rune,
			// walks back to 56, and the result is 59 bytes.
			name: "a cut landing inside a four-byte rune walks back to the boundary",
			in:   strings.Repeat("𝄞", 16),
			want: strings.Repeat("𝄞", 14) + "...",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CapStyleColumn(tc.in)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, len(got), 60, "no class may exceed the 60-byte cap")
			assert.Equal(t, got, strings.ToValidUTF8(got, "�"),
				"every cut lands on a rune boundary — a split rune is invalid UTF-8")
		})
	}
}

// TestCapStyleColumnMarksOnlyWhatItTruncated separates the two halves the table
// above could satisfy jointly: a marker appears exactly when a cut happened.
func TestCapStyleColumnMarksOnlyWhatItTruncated(t *testing.T) {
	assert.NotContains(t, CapStyleColumn(strings.Repeat("a", 60)), "...",
		"a value AT the cap is not a truncation and must not be marked as one")
	assert.Contains(t, CapStyleColumn(strings.Repeat("a", 61)), "...",
		"a value OVER the cap is a truncation and must say so")
}
