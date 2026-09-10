// SPDX-License-Identifier: Apache-2.0

// style_render.go — the WIDTH vocabulary a style rule's compact renders share.
//
// WHY IT LIVES HERE AND NOT BESIDE EITHER RENDERER. Two renders in two packages
// present a style rule compactly: the index arm's tab-separated row (one layer
// down, in the tools package) and the assembly reference line (in the project
// renderer). They cap the same variable-width columns to the same width for the
// same reason, and the assembly renderer's block bound is stated in terms of the
// index row's bound — so the two have to agree by CONSTRUCTION rather than by a
// pair of literals that happen to match today. The project renderer cannot
// import the tools package (tools imports the renderer, so the edge only runs
// one way), which leaves this package — already the home of the style rule's
// metadata vocabulary — as the one place both sides can read.
//
// The cap is a property of the RENDERER, never of the corpus: the server admits
// a 500-rune summary, which is up to 2000 bytes in one column, so an uncapped
// column would be unbounded in practice. Every capped value is recoverable in
// full from the by-id lookup, which is what makes capping it cost nothing.

package kgtypes

import "unicode/utf8"

const (
	// StyleIndexColumnCap bounds a variable-width style-rule column in BYTES,
	// cut on a rune boundary with a visible ellipsis. Bytes rather than runes is
	// what makes the cap a WIDTH bound: a 60-rune summary can be 240 bytes.
	StyleIndexColumnCap = 60

	// StyleIndexEllipsis marks a capped column so a truncation is visible rather
	// than silent.
	StyleIndexEllipsis = "..."

	// StyleIndexCappedColumns is how many of a compact style-rule line's
	// columns pass through CapStyleColumn: severity, scope, summary and the
	// sister-check id. The row's own id is not among them.
	StyleIndexCappedColumns = 4

	// StyleIndexRowSeparatorBytes is a row's fixed non-column overhead: the
	// four tabs that join its five columns, plus the newline that joins it to
	// the next row.
	StyleIndexRowSeparatorBytes = 5
)

// StyleIndexRowBound is the per-line bound for one compact style-rule line, AS
// A FUNCTION OF THE ID THAT LINE CARRIES.
//
// The arithmetic: the id whole, then StyleIndexCappedColumns columns each at
// most StyleIndexColumnCap, then StyleIndexRowSeparatorBytes of separators —
// len(id) + 4×60 + 5.
//
// THE TERM TABLE, and every term is MEASURED, CAPPED or FIXED — never BUDGETED:
//
//	term                                          kind      why
//	len(id)                                       MEASURED  the id is rendered raw and nothing bounds it, so the bound reads it
//	StyleIndexCappedColumns × StyleIndexColumnCap CAPPED    every other column passes through CapStyleColumn on the way out
//	StyleIndexRowSeparatorBytes                   FIXED     four tabs and a newline, a literal of this render's own shape
//
// A BUDGETED TERM is a width the bound assumes and nothing constrains, and this
// bound has none. That is the whole content of the rule: a bound is only as true
// as its budgeted terms, so a bound with none is true over every input the
// render admits. Note what MEASURED costs the caller — it makes the bound a
// function rather than a figure, and every assertion that reads it has to name
// the ids it rendered.
//
// WHY IT IS A FUNCTION AND NOT A FIGURE, and this is the lesson the two rounds
// before it paid for. A declared bound over a render has two kinds of term: the
// CAPPED, which this render controls, and the BUDGETED, which it does not. This
// bound was a single figure that budgeted 32 bytes for the id — and nothing
// bounds a style rule's id, which is written verbatim from the rule list the
// author imports and validated for name, summary, text, severity and scope but
// never for width. At id widths of 64, 200 and 4000 bytes the rendered rows
// measured 308, 444 and 4244 against a declared 280, so the figure was false
// everywhere above its own budget while every test held the id at exactly it. A
// bound is only as true as its budgeted terms, so this one has none: every
// uncapped input it carries is a parameter.
//
// WHY SEVERITY COSTS A CAPPED COLUMN RATHER THAN EIGHT BYTES. This bound used
// to budget severity at the width of `critical`, the longest word on the check
// contract's ladder. Neither compact render validates severity against that
// ladder — deliberately, because the corpus already holds off-ladder
// practice-side values and a READ must not refuse stored data — and a practice
// node accepts arbitrary metadata, so the stored width is corpus data. A render
// that admits arbitrary VALUES cannot assume a bounded WIDTH: rendered
// uncapped, a 4000-byte severity and a 4000-byte sister-check id put a single
// index row at 8159 bytes against the 200 this bound once declared. Both
// columns are capped now and the arithmetic above states what capping them
// costs.
func StyleIndexRowBound(id string) int {
	return len(id) + StyleIndexCappedColumns*StyleIndexColumnCap + StyleIndexRowSeparatorBytes
}

// CapStyleColumn bounds one variable-width column, cut on a rune boundary.
//
// IT NEVER CAPS THE LINE'S OWN ID, and no caller passes that one: a truncated id
// does not resolve, and that id is the whole point of a compact line — every
// other column exists to help a reader decide whether to fetch it.
//
// THE SISTER-CHECK ID IS THE ONE ID THAT DOES PASS THROUGH HERE, and the
// distinction is which read recovers it. A check id admitted through the mutate
// entry point is caller-supplied and namespaced `<language>:`, so its width is
// corpus data; and it is not what the line exists to resolve — the by-id read of
// the RULE returns it in full, which is the same bargain the summary and the
// scope columns take.
//
// It mirrors corpusscan's capDisplayName rather than calling it: that constant
// and that function belong to a topology analyzer's render, and a shared
// vocabulary reaching into an analyzer for a string helper would tie two
// unrelated renders together. The behaviour is deliberately identical.
func CapStyleColumn(s string) string {
	if len(s) <= StyleIndexColumnCap {
		return s
	}
	cut := StyleIndexColumnCap - len(StyleIndexEllipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + StyleIndexEllipsis
}
