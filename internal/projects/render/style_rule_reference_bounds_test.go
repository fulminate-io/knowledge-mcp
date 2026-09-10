// SPDX-License-Identifier: Apache-2.0

// style_rule_reference_bounds_test.go — the reference render's WIDTH axis:
// every variable-width column, and the block bound stated in literals.
//
// WHY THE COLUMN TABLE IS DRIVEN RATHER THAN DECLARED. style_rule_reference.go
// carries a comment table naming each column capped or uncapped-by-design. A
// table proves only that its own rows agree with each other; this file is the
// paired check that fails when the code does the opposite of a row — each capped
// row driven with a 4000-byte value and asserted bounded, the uncapped row
// driven with a 4000-byte id and asserted whole.
//
// WHY THE BLOCK BOUND IS A LITERAL HERE. The suite's previous bound assertion
// asked styleRefBlockIsWithinBound, whose answer key is styleRefBlockBound — the
// production helper. Multiplying styleRefInstructionBound by a thousand left it
// green, because both sides of the comparison moved together. The figures below
// are transcribed from the constants' DOCUMENTED values, so a bound loosened in
// production goes red against the documentation rather than against itself.

package render

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The documented figures, transcribed as literals from the constants' own
// comments. NOT read from the constants: a bound checked against itself is not
// checked. kgtypes' own test pins these same numbers to the same literals, so a
// constant edited in one place and not the other is red in one of the two.
const (
	wantStyleRefInstructionBound = 220 // styleRefInstructionBound: the instruction's fixed prose
	wantStyleRefIDSeparatorBytes = 2   // styleRefInstructionIDSeparatorBytes: the `, ` between two ids
	wantStyleIndexColumnCap      = 60  // kgtypes.StyleIndexColumnCap: one variable-width column
	wantStyleIndexCappedColumns  = 4   // kgtypes.StyleIndexCappedColumns
	wantStyleIndexSeparatorBytes = 5   // kgtypes.StyleIndexRowSeparatorBytes

	// wantStyleRefIDQuoteDelimiters is the two delimiting quote bytes
	// strconv.Quote wraps EVERY id in. It is stated apart from the escaping
	// those quotes surround, because the escaping is a measurement of the id's
	// bytes and these two are not: an id of any alphabet spends exactly these.
	wantStyleRefIDQuoteDelimiters = 2
)

// wantStyleIndexRowBound is kgtypes.StyleIndexRowBound's DOCUMENTED arithmetic,
// written out here with literals rather than read from kgtypes: the id whole,
// plus four capped columns, plus the separator bytes.
//
// IT TAKES THE ID BECAUSE THE BOUND DOES. It was a flat 280 that budgeted 32
// bytes for the id — the one term nothing caps and nothing validates — so it was
// false for every wider id the corpus admits.
func wantStyleIndexRowBound(id string) int {
	return len(id) + wantStyleIndexCappedColumns*wantStyleIndexColumnCap + wantStyleIndexSeparatorBytes
}

// styleRefColumns splits a rendered reference line into its columns. The
// separator is spelled as a LITERAL rather than taken from
// styleRefColumnSeparator: this file's whole subject is not letting the render
// supply its own answer key.
func styleRefColumns(t *testing.T, line string) []string {
	t.Helper()
	require.True(t, strings.HasSuffix(line, "\n"), "a reference line ends in a newline")
	return strings.Split(strings.TrimSuffix(line, "\n"), " — ")
}

// TestStyleRuleReferenceEveryVariableWidthColumnIsCapped drives each column of
// the render's declared table at 4000 bytes — the width the corpus can actually
// hold, since a practice node accepts arbitrary metadata and neither compact
// render validates severity against the ladder.
//
// THE MUTATION IS PER COLUMN: drop that one column's kgtypes.CapStyleColumn call
// in styleRuleReferenceLine and this subtest goes red while the others stay
// green.
func TestStyleRuleReferenceEveryVariableWidthColumnIsCapped(t *testing.T) {
	const wide = 4000

	t.Run("severity", func(t *testing.T) {
		n := newStyleRuleNode("sr-wide-sev", "wide")
		kgtypes.SetValue(n, corpus.MetaSeverity, strings.Repeat("W", wide))
		cols := styleRefColumns(t, styleRuleReferenceLine(n))
		require.Len(t, cols, 3, "id, severity and summary — this rule carries no scope")
		sev := strings.TrimPrefix(cols[1], "severity=")
		assert.LessOrEqual(t, len(sev), wantStyleIndexColumnCap,
			"an off-ladder severity is rendered AS STORED but its WIDTH is capped: "+
				"a read that admits arbitrary values cannot assume a bounded width")
		assert.True(t, strings.HasSuffix(sev, "..."),
			"a capped column says so rather than truncating silently")
	})

	t.Run("scope", func(t *testing.T) {
		n := newStyleRuleNode("sr-wide-scope", "wide")
		kgtypes.SetValue(n, kgtypes.MetaKeyStyleScopeRepo, strings.Repeat("r", wide))
		cols := styleRefColumns(t, styleRuleReferenceLine(n))
		require.Len(t, cols, 4, "id, severity, scope and summary")
		assert.LessOrEqual(t, len(cols[2]), wantStyleIndexColumnCap)
		assert.True(t, strings.HasSuffix(cols[2], "..."))
	})

	t.Run("summary", func(t *testing.T) {
		n := newStyleRuleNode("sr-wide-summary", "wide")
		n.Summary = strings.Repeat("s", wide)
		cols := styleRefColumns(t, styleRuleReferenceLine(n))
		require.Len(t, cols, 3)
		assert.LessOrEqual(t, len(cols[2]), wantStyleIndexColumnCap)
		assert.True(t, strings.HasSuffix(cols[2], "..."))
	})

	// THE UNCAPPED-BY-DESIGN ROW, and its paired check. A capped id does not
	// resolve, and resolving it is the whole purpose of a reference. A cap
	// wrongly applied here would pass every assertion above.
	t.Run("the id is uncapped by design", func(t *testing.T) {
		id := "style-rule-" + strings.Repeat("z", wide)
		n := newStyleRuleNode(id, "wide")
		cols := styleRefColumns(t, styleRuleReferenceLine(n))
		assert.Equal(t, "- "+id, cols[0], "the id is rendered WHOLE")
		assert.NotContains(t, cols[0], "...")
	})
}

// TestStyleRuleReferenceBlockBoundIsTheDocumentedArithmetic states the bound in
// literals and checks BOTH directions: the production helper must compute the
// documented figure, and the rendered block must fit inside it.
//
// THE TWO MUTATIONS THIS CATCHES. Loosen styleRefBlockBound (multiply
// styleRefInstructionBound by 1000) and the first assertion goes red — the old
// assertion, which asked the helper whether the block fitted the helper's own
// answer, stayed green. Grow the render past the documented budget and the
// second goes red.
func TestStyleRuleReferenceBlockBoundIsTheDocumentedArithmetic(t *testing.T) {
	for _, n := range []int{1, 2, 7} {
		var sb strings.Builder
		ids := make([]string, 0, n)
		want := wantStyleRefInstructionBound
		for i := range n {
			id := "style-rule-" + strings.Repeat("z", wantStyleIndexColumnCap) + string(rune('a'+i))
			rule := newStyleRuleNode(id, "wide-rule")
			rule.Summary = strings.Repeat("界", 500)
			// THE MAXIMAL SEVERITY. The bound test used to hold severity at
			// "warning", seven bytes, while calling itself "the widest inputs
			// the render admits" — so the one column that broke the bound was
			// the one column it never varied.
			kgtypes.SetValue(rule, corpus.MetaSeverity, strings.Repeat("W", 4000))
			kgtypes.SetValue(rule, kgtypes.MetaKeyStyleScopeRepo, strings.Repeat("r", 80))
			kgtypes.SetValue(rule, kgtypes.MetaKeyStyleScopePaths, `["`+strings.Repeat("p", 200)+`"]`)
			sb.WriteString(styleRuleReferenceLine(rule))
			ids = append(ids, id)
			// EVERY ID IN THIS TEST IS PLAIN ASCII, so strconv.Quote spends the
			// two delimiters and nothing more. The alphabet matrix below is
			// where the escaping is driven.
			want += wantStyleIndexRowBound(id) + len(id) + wantStyleRefIDQuoteDelimiters + wantStyleRefIDSeparatorBytes
		}
		renderStyleRuleInstruction(&sb, ids)
		block := sb.String()

		assert.Equal(t, want, styleRefBlockBound(ids),
			"%d rules: the helper must compute the arithmetic its comment documents — "+
				"%d prose + per rule (the row bound for that id + that id QUOTED + %d separator)",
			n, wantStyleRefInstructionBound, wantStyleRefIDSeparatorBytes)
		assert.LessOrEqual(t, len(block), want,
			"%d rules: the rendered block is %d bytes against a documented bound of %d",
			n, len(block), want)
		// And the production helper agrees with the literal, which is what ties
		// the two together rather than letting either drift alone.
		assert.True(t, styleRefBlockIsWithinBound(ids, block))
	}
}

// TestStyleRefBlockBoundIsZeroForNoRules keeps the base case honest: an assembly
// with no style rule emits neither a reference line nor an instruction, so its
// honest bound is zero rather than the instruction's prose.
func TestStyleRefBlockBoundIsZeroForNoRules(t *testing.T) {
	assert.Equal(t, 0, styleRefBlockBound(nil))
	assert.Equal(t, 0, styleRefBlockBound([]string{}))
	// The known positive on the same helper: a bound over a real id is not zero.
	assert.Equal(t, wantStyleRefInstructionBound+wantStyleIndexRowBound("abc")+
		len("abc")+wantStyleRefIDQuoteDelimiters+wantStyleRefIDSeparatorBytes,
		styleRefBlockBound([]string{"abc"}))
}

// TestStyleRefBlockBoundHoldsOverMaximalInputsAtEveryIDWidth drives EVERY column
// of the reference line at its widest SIMULTANEOUSLY, across the id width axis.
//
// WHY THE ID IS AN AXIS. The suite's maximal-input test held its ids at a fixed
// width while driving every other column at 4000 bytes, and the block bound
// budgeted a nominal id — so the one term no cap protects was the one term
// nothing drove, on both renders. A style rule's id is written verbatim from the
// imported rule list, validated for name, summary, text, severity and scope and
// never for width, so every width below is expressible through the shipped
// import.
//
// THE MUTATIONS. Budget the id at a fixed 32 in kgtypes.StyleIndexRowBound and
// the bound assertion goes red at every width but 32. Cap the id in
// styleRuleReferenceLine and the whole-id assertion goes red — the id is never
// capped, by design, on either render.
func TestStyleRefBlockBoundHoldsOverMaximalInputsAtEveryIDWidth(t *testing.T) {
	const wide = 4000
	for _, idWidth := range []int{32, 64, 200, 4000} {
		t.Run(fmt.Sprintf("id of %d bytes", idWidth), func(t *testing.T) {
			id := strings.Repeat("z", idWidth)
			rule := newStyleRuleNode(id, "wide-rule")
			rule.Summary = strings.Repeat("界", wide)
			kgtypes.SetValue(rule, corpus.MetaSeverity, strings.Repeat("W", wide))
			kgtypes.SetValue(rule, kgtypes.MetaKeyStyleScopeRepo, strings.Repeat("r", wide))
			kgtypes.SetValue(rule, kgtypes.MetaKeyStyleScopePaths, `["`+strings.Repeat("p", wide)+`"]`)

			var sb strings.Builder
			line := styleRuleReferenceLine(rule)
			sb.WriteString(line)
			renderStyleRuleInstruction(&sb, []string{id})
			block := sb.String()

			// THE ID IS RENDERED WHOLE at every width, which is what keeps the
			// bound assertions below a statement about a bound rather than about
			// a cap quietly added to satisfy them.
			assert.Contains(t, line, "- "+id+" — ", "the id is rendered WHOLE at every width")

			want := wantStyleRefInstructionBound + wantStyleIndexRowBound(id) +
				len(id) + wantStyleRefIDQuoteDelimiters + wantStyleRefIDSeparatorBytes
			assert.Equal(t, want, styleRefBlockBound([]string{id}),
				"the helper must compute the documented arithmetic for an id of %d bytes", idWidth)
			assert.LessOrEqual(t, len(block), want,
				"a block whose every capped column is maximal must fit the bound for ITS id: "+
					"%d bytes against %d", len(block), want)
		})
	}
}

// styleRefIDAlphabet is one row of the id-ALPHABET axis: a unit of source bytes
// an id is built by repeating, and the DOCUMENTED number of bytes strconv.Quote
// renders that unit at.
//
// THE QUOTED WIDTH IS A LITERAL HERE, transcribed from Go's quoting rules, and
// the matrix asserts strconv.Quote against it before using it. The alternative —
// calling strconv.Quote and comparing the answer to itself — is the shape three
// rounds of this suite have been red for.
type styleRefIDAlphabet struct {
	name string
	// unit is repeated to build the id.
	unit string
	// quotedUnit is how many bytes strconv.Quote spends on one unit, DELIMITERS
	// EXCLUDED: a printable ASCII byte costs itself, a double quote and a
	// backslash cost two, a control byte costs four (\x00), and a printable
	// multibyte rune is written through verbatim.
	quotedUnit int
}

// styleRefIDAlphabets is the axis every earlier bound test held at one letter.
// A style rule's id is written verbatim from the imported rule list, which
// validates name, summary, text, severity and scope and neither the id's width
// nor its characters, so every row below is expressible through the shipped
// import.
var styleRefIDAlphabets = []styleRefIDAlphabet{
	{name: "plain ascii", unit: "z", quotedUnit: 1},
	{name: "double quotes", unit: `"`, quotedUnit: 2},
	{name: "backslashes", unit: `\`, quotedUnit: 2},
	{name: "NUL bytes", unit: "\x00", quotedUnit: 4},
	{name: "multibyte runes", unit: "界", quotedUnit: 3},
	{name: "a mix", unit: "a\"\\\x00界", quotedUnit: 1 + 2 + 2 + 4 + 3},
}

// TestStyleRefBlockBoundHoldsOverEveryIDAlphabetAtEveryIDWidth drives the block
// bound across the id ALPHABET at each id width, with every capped column
// maximal.
//
// WHY THE ALPHABET IS AN AXIS. The instruction line spends strconv.Quote(id),
// not the id: a double quote and a backslash render at two bytes and a control
// byte at four, so an id's RENDERED width is not its stored width. A bound that
// paid the stored width plus a fixed quoting allowance was false above that
// allowance — an id of 200 double quotes measured 997 against 869, and 60 NUL
// bytes measured 697 against 589 — while every bound test in the tree drove the
// id WIDTH at 32/64/200/4000 and held its alphabet at one letter.
//
// THE MUTATIONS. Restore the fixed four-byte per-id overhead (len(id) + 4) in
// styleRefBlockBound and the double-quote, backslash, NUL and mix rows go red at
// every width. Pay len(id) instead of len(strconv.Quote(id)) for the quoted
// token and the same rows go red. The plain-ascii and multibyte rows stay green
// under both, which is what makes them this matrix's controls: they are the
// alphabets Go quoting does not expand.
func TestStyleRefBlockBoundHoldsOverEveryIDAlphabetAtEveryIDWidth(t *testing.T) {
	const wide = 4000
	for _, alpha := range styleRefIDAlphabets {
		for _, idWidth := range []int{32, 64, 200, 4000} {
			t.Run(fmt.Sprintf("%s at about %d bytes", alpha.name, idWidth), func(t *testing.T) {
				units := idWidth / len(alpha.unit)
				require.Positive(t, units, "the width axis must fit at least one unit")
				id := strings.Repeat(alpha.unit, units)

				// THE QUOTED WIDTH FROM THE LITERAL TABLE, asserted against the
				// library before it is spent. This is the external expectation:
				// a Go quoting rule stated here and checked against strconv,
				// rather than strconv checked against itself.
				wantQuoted := wantStyleRefIDQuoteDelimiters + units*alpha.quotedUnit
				require.Len(t, strconv.Quote(id), wantQuoted,
					"strconv.Quote spends %d bytes on one %q plus %d delimiters",
					alpha.quotedUnit, alpha.unit, wantStyleRefIDQuoteDelimiters)

				rule := newStyleRuleNode(id, "wide-rule")
				rule.Summary = strings.Repeat("界", wide)
				kgtypes.SetValue(rule, corpus.MetaSeverity, strings.Repeat("W", wide))
				kgtypes.SetValue(rule, kgtypes.MetaKeyStyleScopeRepo, strings.Repeat("r", wide))
				kgtypes.SetValue(rule, kgtypes.MetaKeyStyleScopePaths, `["`+strings.Repeat("p", wide)+`"]`)

				var sb strings.Builder
				line := styleRuleReferenceLine(rule)
				sb.WriteString(line)
				renderStyleRuleInstruction(&sb, []string{id})
				block := sb.String()

				// The id is rendered WHOLE on the reference line for every
				// alphabet, so the bound assertions stay statements about a
				// bound rather than about a cap added to satisfy them.
				assert.Contains(t, line, "- "+id+" — ",
					"the id is rendered WHOLE, unquoted, on the reference line")
				// And the instruction spends its QUOTED form, which is the term
				// the bound now measures.
				assert.Contains(t, block, strconv.Quote(id),
					"the instruction line names the id in its quoted form")

				want := wantStyleRefInstructionBound + wantStyleIndexRowBound(id) +
					wantQuoted + wantStyleRefIDSeparatorBytes
				assert.Equal(t, want, styleRefBlockBound([]string{id}),
					"the helper must compute the documented arithmetic for a %s id of %d bytes",
					alpha.name, len(id))
				assert.LessOrEqual(t, len(block), want,
					"a block whose every capped column is maximal must fit the bound for ITS id: "+
						"%s at %d bytes is %d rendered against %d",
					alpha.name, len(id), len(block), want)
				// AND THE PRODUCTION HELPER'S OWN ANSWER BOUNDS THE BLOCK. This
				// is the assertion the escaping defect fails directly: the
				// helper paid the id's STORED width plus a fixed allowance, so
				// a 60-NUL id rendered 697 bytes against its answer of 589.
				assert.True(t, styleRefBlockIsWithinBound([]string{id}, block),
					"the render must fit the bound the production helper declares for it: "+
						"%s at %d bytes is %d rendered against %d",
					alpha.name, len(id), len(block), styleRefBlockBound([]string{id}))
			})
		}
	}
}

// TestStyleRefBlockBoundPaysEveryRenderedReferenceLine drives the block bound's
// SUMMATION rather than its terms: how many reference lines one block can hold
// for a given set of ids.
//
// WHY THE SUMMATION IS ITS OWN AXIS. Three earlier rounds classified the TERMS
// of one reference line — a capped column, a budgeted width, a budgeted
// expansion — and each closed the term it found. A bound over a BLOCK has a term
// list AND an item count, and the item count is a term too. styleRefBlockBound
// paid one row bound per UNIQUE id while the sections write one reference line
// per COLLECTED id: ticket.go walks one outgoing-edge list and dispatches
// EdgeUses into renderTicketPatterns and EdgeAudits into
// renderLanguagePatternsSection, so a rule carrying BOTH edges lands in both
// slices, renders in both sections, and is named once on the instruction line
// the caller concatenates. Two lines, one payment.
//
// THE INPUT CLASS IS THE ONE THE SUITE ALREADY CONSTRUCTS.
// TestStyleRuleInstructionDeduplicatesIDs builds exactly this ticket and asks
// only that the instruction name the rule once; it never asks the bound. Here
// the same shape is driven at a 200-byte id with every capped column maximal,
// which is the input every other bound test in this file drives.
//
// THE CONTROL IS THE SAME RUN'S SOLE-EDGE RENDER of the identical rule: one
// edge, one line, within bound both before and after the fix. It is what makes
// the duplicate row's red a statement about the line count rather than about the
// rule's width.
//
// THE THREE-LINE CLASS, and why it is not a hypothetical. An edge's identity in
// the server's edge stores is FOUR parts, not three: from, to, type and the
// EVIDENCE group key. The file store spells it per membership as
// edgeDst{id, groupKey: edge.Evidence} in
// cmd/knowledge-server/internal/store/graph_edges.go, and the hosted store keys
// and uniquely indexes the same four. mutate(link) takes edge_evidence from the
// caller, so two evidence-distinct `uses` edges from one ticket to one style
// rule are an ordinary write. ticket.go appends the edge's TARGET per edge with
// no dedupe, so such a ticket collects the rule twice in the uses slice and once
// more from audits: three rendered lines, one instruction token. Nothing caps
// that count from above, which is exactly why the bound sums the collected slice
// instead of assuming a ceiling.
//
// THE MUTATION: restore the dedupe on the ROW term (sum
// kgtypes.StyleIndexRowBound over the deduped ids again) and BOTH driven
// subtests — two lines and three lines — go red while the sole-edge control
// stays green.
func TestStyleRefBlockBoundPaysEveryRenderedReferenceLine(t *testing.T) {
	const wide = 4000
	const idWidth = 200

	// ONE rule node, reached by one or both edges — the same pointer ticket.go
	// appends to both slices when one rule carries both edge types.
	id := strings.Repeat("z", idWidth)
	rule := newStyleRuleNode(id, "wide-rule")
	rule.Summary = strings.Repeat("界", wide)
	kgtypes.SetValue(rule, corpus.MetaSeverity, strings.Repeat("W", wide))
	kgtypes.SetValue(rule, kgtypes.MetaKeyStyleScopeRepo, strings.Repeat("r", wide))
	kgtypes.SetValue(rule, kgtypes.MetaKeyStyleScopePaths, `["`+strings.Repeat("p", wide)+`"]`)
	line := styleRuleReferenceLine(rule)

	// render drives ticket.go:100-102's own three statements: the two sections,
	// their id slices concatenated in that order, then the one instruction. The
	// block the bound covers is the reference lines plus that instruction, and
	// the LINE COUNT is read off the sections' own output — the exact rendered
	// line counted in what they wrote — rather than assumed from the id slice.
	render := func(t *testing.T, uses, audits []*knowledgev1.Node) ([]string, string) {
		t.Helper()
		ticket := &knowledgev1.Node{
			Id: "tkt-lines", Type: string(kgtypes.NodeTicket), SymbolName: "t-lines",
			Status: kgtypes.StatusOpen, Description: "lines", Summary: "lines",
		}
		var sections strings.Builder
		ids := renderTicketPatterns(ticket, uses, &sections)
		ids = append(ids, renderLanguagePatternsSection(ticket, audits, &sections)...)
		require.Equal(t, len(ids), strings.Count(sections.String(), line),
			"the sections write one reference line per COLLECTED id, which is what the bound must pay for")

		var block strings.Builder
		for range ids {
			block.WriteString(line)
		}
		renderStyleRuleInstruction(&block, ids)
		return ids, block.String()
	}

	// The documented arithmetic, in literals: the fixed prose once, a row bound
	// per RENDERED LINE, and the quoted id plus its separator per UNIQUE id.
	// Every id here is plain ascii, so strconv.Quote spends the delimiters alone.
	want := func(lines, unique int) int {
		return wantStyleRefInstructionBound +
			lines*wantStyleIndexRowBound(id) +
			unique*(len(id)+wantStyleRefIDQuoteDelimiters+wantStyleRefIDSeparatorBytes)
	}

	t.Run("both pattern edges reach one rule", func(t *testing.T) {
		ids, block := render(t, []*knowledgev1.Node{rule}, []*knowledgev1.Node{rule})
		require.Len(t, ids, 2, "one rule collected by both sections")

		assert.Equal(t, want(2, 1), styleRefBlockBound(ids),
			"two rendered lines and one unique id: the helper must pay the row bound TWICE "+
				"and the instruction's quoted id ONCE")
		assert.LessOrEqual(t, len(block), want(2, 1),
			"a two-line block is %d bytes against a documented bound of %d", len(block), want(2, 1))
		assert.True(t, styleRefBlockIsWithinBound(ids, block),
			"the render must fit the bound the production helper declares for it: "+
				"%d bytes against %d", len(block), styleRefBlockBound(ids))
	})

	// THE THREE-LINE ROW, the class the bound's comment once declared impossible:
	// the same rule reached TWICE through the uses slice (two evidence-distinct
	// edges) and once through audits.
	t.Run("three pattern edges reach one rule", func(t *testing.T) {
		ids, block := render(t, []*knowledgev1.Node{rule, rule}, []*knowledgev1.Node{rule})
		require.Len(t, ids, 3, "one rule collected three times")
		require.Len(t, dedupeStyleRefIDs(ids), 1,
			"and it is ONE unique id, named once on the instruction line")

		assert.Equal(t, want(3, 1), styleRefBlockBound(ids),
			"three rendered lines and one unique id: the helper must pay the row bound THREE "+
				"times and the instruction's quoted id ONCE")
		assert.LessOrEqual(t, len(block), want(3, 1),
			"a three-line block is %d bytes against a documented bound of %d", len(block), want(3, 1))
		assert.True(t, styleRefBlockIsWithinBound(ids, block),
			"the render must fit the bound the production helper declares for it: "+
				"%d bytes against %d", len(block), styleRefBlockBound(ids))
	})

	// THE CONTROL, same run, same rule, one edge: within bound in both states.
	t.Run("one pattern edge reaches the same rule", func(t *testing.T) {
		ids, block := render(t, []*knowledgev1.Node{rule}, nil)
		require.Len(t, ids, 1, "one rule collected by one section")

		assert.Equal(t, want(1, 1), styleRefBlockBound(ids))
		assert.LessOrEqual(t, len(block), want(1, 1),
			"a one-line block is %d bytes against a documented bound of %d", len(block), want(1, 1))
		assert.True(t, styleRefBlockIsWithinBound(ids, block),
			"the sole-edge control: %d bytes against %d", len(block), styleRefBlockBound(ids))
	})
}
