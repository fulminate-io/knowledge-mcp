// SPDX-License-Identifier: Apache-2.0

// layout_token_test.go — the per-grammar layout-token skip: what it fixes, what
// it must not reopen, and what it must not disturb on the write side.
//
// THE DEFECT IT CLOSES. Go's grammar emits an anonymous newline terminator
// inside a block, so a multi-line body's child list carries a child a one-line
// body's does not. The matcher compares ALL children, which is what makes
// operators and keywords constrain a match — but it read that newline as a
// constraint too, and every Go pattern shaped `<construct> { <literal
// statement> }` stopped matching multi-line source. Silently: zero matches is
// a valid answer.
//
// THE ROWS BELOW ARE MEASURED RED. Against the tree before the skip landed,
// every multi-line row here reported 0 matches and the two-file differential
// reported 1 of 2 — the same number the finding recorded through the live tool.
//
// WHAT THE ROWS MUST NOT LET THROUGH. The scoped rule is "an anonymous token
// whose source text this grammar declares as pure layout"; the forbidden
// blanket rule is "any anonymous token". Every row here would pass under the
// blanket one except the operator row, which is why it is here: `==` and `!=`
// are anonymous tokens, and a blanket skip makes a pattern checking one match
// source doing the other.
//
// THE CLASSIFICATION ROWS ARE THE OTHER HALF. Python's newline never reaches a
// child list, so no parse-tree fixture can distinguish a correct empty Python
// layout set from a wrong non-empty one — the only available catcher is a
// declaration-level assertion, which is what TestLayoutTokenClassification is.
// C's `;` and Java's likewise are MEANING rather than layout, and the
// contract table's c_body_semicolon rows plus the DroppedSpans machinery both
// depend on that staying true.

package ast

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// layoutMultiLineBody is the multi-line guard shape — the CEO fixture that
// matched at the ticket-start commit and stopped matching after Phase 1.
const layoutMultiLineBody = "package main\n\n" +
	"func f(conn *T) {\n" +
	"\tif conn != nil {\n" +
	"\t\tconn.Close()\n" +
	"\t}\n" +
	"}\n"

// layoutSingleLineBody is the same construct on one line — the shape that kept
// matching, and the known-positive control for every multi-line row.
const layoutSingleLineBody = "package main\n\n" +
	"func g(conn *T) {\n" +
	"\tif conn != nil { conn.Close() }\n" +
	"}\n"

// layoutMultiLinePattern is the mirror direction: the PATTERN carries the
// layout token and the target does not.
const layoutMultiLinePattern = "if $A != nil {\n\t$B.Close()\n}"

func TestLayoutTokenMatching(t *testing.T) {
	t.Run("one_line_pattern_matches_a_multi_line_body", func(t *testing.T) {
		matches := runWalker(t, "if $A != nil { $B.Close() }", layoutMultiLineBody)
		require.Len(t, matches, 1,
			"a one-line pattern must reach a multi-line body — the layout newline is not a constraint")
	})

	t.Run("one_line_pattern_matches_a_one_line_body", func(t *testing.T) {
		matches := runWalker(t, "if $A != nil { $B.Close() }", layoutSingleLineBody)
		require.Len(t, matches, 1,
			"known positive: this direction worked before the skip and must keep working")
	})

	// The finding's sharpest evidence: a byte-identical construct failing to
	// match itself is not arguably a pattern-authoring subtlety.
	t.Run("fully_literal_pattern_matches_a_multi_line_body", func(t *testing.T) {
		matches := runWalker(t, "if conn != nil { conn.Close() }", layoutMultiLineBody)
		require.Len(t, matches, 1, "a pattern with no placeholders must match the source it is spelled from")
	})

	t.Run("fully_literal_pattern_matches_a_one_line_body", func(t *testing.T) {
		matches := runWalker(t, "if conn != nil { conn.Close() }", layoutSingleLineBody)
		require.Len(t, matches, 1, "known positive for the literal row above")
	})

	t.Run("multi_line_pattern_matches_a_one_line_target", func(t *testing.T) {
		matches := runWalker(t, layoutMultiLinePattern, layoutSingleLineBody)
		require.Len(t, matches, 1,
			"the skip applies on the PATTERN side too, or the mirror direction stays broken")
	})

	t.Run("multi_line_pattern_matches_a_multi_line_target", func(t *testing.T) {
		matches := runWalker(t, layoutMultiLinePattern, layoutMultiLineBody)
		require.Len(t, matches, 1, "known positive for the mirror row above")
	})

	// The finding's differential payload, run through the real Match pipeline
	// over a two-file corpus. Measured 2 at ticket-start 7c19bd2c and 1 after
	// Phase 1; 2 is the number this fix restores.
	t.Run("both_spellings_match_across_a_two_file_corpus", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{
			"a.go": layoutMultiLineBody,
			"b.go": layoutSingleLineBody,
		})
		_, matches := runSplice(t, dir, treesitter.LangGo,
			"if $A != nil { $B.Close() }", "if $A != nil { $B.Close() }", true)
		require.Equal(t, 2, matches,
			"the multi-line file and the one-line file must both match; 1 is the regression's signature")
	})

	// THE DISCRIMINATING ROW. Every other row here passes under a blanket
	// "skip any anonymous token" rule; this one does not. `==` and `!=` are
	// anonymous operator tokens, and a blanket skip would let a pattern
	// checking one match source doing the other.
	t.Run("operators_remain_constraints_under_the_skip", func(t *testing.T) {
		matches := runWalker(t, "if $A == nil { $B.Close() }", layoutMultiLineBody)
		require.Empty(t, matches,
			"an operator is meaning, not layout — a blanket anonymous-token skip would match here")
		positive := runWalker(t, "if $A != nil { $B.Close() }", layoutMultiLineBody)
		require.Len(t, positive, 1,
			"known positive: the same fixture with the right operator does match, so the zero above is a refusal and not a dead probe")
	})
}

func TestLayoutTokenClassification(t *testing.T) {
	// The mandated empty sets. C's `;` is a statement terminator that MEANS
	// something — the contract table's c_body_semicolon rows and the
	// DroppedSpans machinery both depend on it never being classified as
	// layout. Java's is the same token in a different grammar. Python's
	// newline is the offside rule itself.
	for _, lang := range []treesitter.Language{
		treesitter.LangPython,
		treesitter.LangC,
		treesitter.LangJava,
	} {
		t.Run("empty_layout_set_"+string(lang), func(t *testing.T) {
			cfg, ok := langConfigFor(lang)
			require.True(t, ok, "no LangConfig registered for %s", lang)
			assert.Empty(t, cfg.LayoutTokens,
				"%s declares no pure-layout token: its terminator is meaning, not layout", lang)
		})
	}

	// The known positive. Without it, every assertion above would also pass
	// against a field nothing ever populates.
	t.Run("go_declares_its_intra_block_newline", func(t *testing.T) {
		cfg, ok := langConfigFor(treesitter.LangGo)
		require.True(t, ok, "no LangConfig registered for go")
		assert.Equal(t, []string{"\n"}, cfg.LayoutTokens,
			"go is the one grammar the census measured as surfacing a pure-layout child")
	})

	// THE CONFIG MUST AGREE WITH THE MEASUREMENT. A declaration that drifts
	// from the census is a classification nobody measured — which is the state
	// this phase exists to leave behind.
	t.Run("every_declaration_agrees_with_the_census", func(t *testing.T) {
		for _, probe := range layoutProbes {
			cfg, ok := langConfigFor(probe.lang)
			require.True(t, ok, "no LangConfig registered for %s", probe.lang)
			v := measureLayout(t, probe)
			if v.skip != "" {
				continue
			}
			assert.Equal(t, v.layout, len(cfg.LayoutTokens) > 0,
				"%s: census measured layout=%v but LayoutTokens=%q — declaration and measurement must agree",
				probe.lang, v.layout, cfg.LayoutTokens)
		}
	})
}

func TestLayoutTokenSpliceRoundTrip(t *testing.T) {
	// A skipped layout token earns no alignment entry, so it becomes an
	// unaligned region inside the matched span. The contiguous-slice splice
	// should carry it through as source bytes because it lies between two
	// anchors — these rows are that "should" turned into a gate.
	t.Run("identity_over_a_multi_line_target_is_byte_identical", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"main.go": layoutMultiLineBody})
		res, matches := runSplice(t, dir, treesitter.LangGo,
			"if $A != nil { $B.Close() }", "if $A != nil { $B.Close() }", true)
		require.Equal(t, 1, matches, "an identity replace over zero matches proves nothing")
		requireNoDiff(t, res, "one-line pattern over a multi-line target")
	})

	// THE MIRROR. Without it the write side is gated in only one direction,
	// and a skip applied asymmetrically between pattern and target would pass
	// the row above while corrupting this one.
	t.Run("identity_with_a_multi_line_pattern_over_a_one_line_target_is_byte_identical", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"main.go": layoutSingleLineBody})
		res, matches := runSplice(t, dir, treesitter.LangGo,
			layoutMultiLinePattern, layoutMultiLinePattern, true)
		require.Equal(t, 1, matches, "an identity replace over zero matches proves nothing")
		requireNoDiff(t, res, "multi-line pattern over a one-line target")
	})

	// The preservation half an identity gate cannot prove: a real rewrite must
	// change its one token and leave the newline and tabs around it alone.
	t.Run("one_token_rewrite_preserves_every_other_byte", func(t *testing.T) {
		got, matches := spliceRewrite(t, treesitter.LangGo, "main.go", layoutMultiLineBody,
			"if $A != nil { $B.Close() }", "if $A != nil { $B.Shutdown() }")
		require.Equal(t, 1, matches, "a rewrite over zero matches preserves every byte for the wrong reason")
		want := "package main\n\n" +
			"func f(conn *T) {\n" +
			"\tif conn != nil {\n" +
			"\t\tconn.Shutdown()\n" +
			"\t}\n" +
			"}\n"
		assert.Equal(t, want, got, "the body's line structure and tabs must survive the rewrite")
	})
}
