// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCalleeNameabilityPredicate is the KNOWN-POSITIVE CONTROL for the corpus
// census's widened invariant, and it is required rather than nice: an absence
// assertion over a predicate is worthless if the predicate can return true for
// everything. The census asserts a ZERO; these six subtests are what make that
// zero mean something, by driving the predicate in BOTH directions.
func TestCalleeNameabilityPredicate(t *testing.T) {
	t.Run("mangled_shapes_are_not_nameable", func(t *testing.T) {
		// Taken verbatim from the corpora — these are the real emitted strings
		// this work removes, not invented ones.
		for _, s := range []string{
			"Format{}.Build",
			`pgx.Identifier{"a","b"}.Sanitize`,
			`",".join`,
			`/^\s*$/.test`,
			"$/.test",
			"/.test",
			"this._options?.onDidRemoveLastListener",
			"items.enum!.join",
			"new(c.X.String",
			".hasOwnProperty",
		} {
			assert.False(t, calleeIsNameable(s, ""), "%q must not be nameable", s)
		}
	})

	t.Run("elixir_and_ruby_predicate_names_are_nameable", func(t *testing.T) {
		names := []string{"Map.has_key?", "File.read!", "x.empty?", "x.save!"}
		for _, s := range names {
			assert.True(t, calleeIsNameable(s, "?!"), "%q must be nameable with extra ?!", s)
		}
		// THE REJECT HALF IS WHAT PROVES NameExtra IS ACTUALLY READ rather than
		// the base set being over-permissive: with a different extra set, the
		// same four strings must come back rejected.
		for _, s := range names {
			assert.False(t, calleeIsNameable(s, `""`), "%q must be rejected without extra ?!", s)
		}
	})

	t.Run("bash_command_words_are_declined_from_the_assertion", func(t *testing.T) {
		// The catcher for a gated knob added to a shell by inattention. A shell
		// callee is a command word, so it must never reach the census assertion.
		require.False(t, calleeProfileFor(LangBash).DeclineNonName,
			"a shell must take no decline knob: its callees are command words, not names")
		for _, s := range []string{"${CMD}", `"$BIN"`, "/usr/bin/env"} {
			assert.False(t, calleeIsNameable(s, ""),
				"%q is not a name, which is exactly why a shell is logged and not asserted", s)
		}
		assert.True(t, calleeIsNameable("cmd-with-dash", ""),
			"a dashed command name is nameable, which is why `-` is in the base set")
	})

	t.Run("quote_aware_scan_keeps_a_brace_inside_a_string_literal", func(t *testing.T) {
		// THE MEASURED REGRESSION'S CATCHER. A brace-blind scan reads this span
		// as unbalanced and destroys the legitimate callee `match`; an earlier
		// candidate lost `match`, `some` and `reduce` across a whole corpus for
		// exactly this reason.
		sc := scanCalleeSpan(`rule.split('{')[0].match`)
		assert.True(t, sc.Balanced, "a brace inside a string literal must not unbalance the span")
		assert.Empty(t, sc.BraceRuns, "a brace inside a string literal is not a literal body")

		sc = scanCalleeSpan("T{a:(1)}.M")
		require.Len(t, sc.BraceRuns, 1, "one balanced top-level brace run")
		assert.False(t, sc.HasOpenAtDepth0,
			"a paren INSIDE a literal body is not a cut point")

		// ITS LIMIT, stated so nobody reads more into it than it proves: this
		// drives the scan DIRECTLY, so it cannot tell whether the composition
		// used the scan's result or fell through to the retained legacy branch.
		// The end-to-end gate for that is the fourth call in the fixture
		// go_composite_literal_receiver.
	})

	t.Run("quote_aware_scan_ignores_escapes_in_a_raw_string", func(t *testing.T) {
		// THE TWO HALVES ARE THE WHOLE POINT. A backslash inside a BACKTICK
		// string is an ordinary byte — a Go raw literal takes no escapes — so
		// consuming the byte after it swallows the closing delimiter. A
		// backslash inside `'` or `"` genuinely escapes. A scan that treats all
		// three delimiters alike fails one half or the other.
		sc := scanCalleeSpan("T{p:`C:\\`}.M")
		assert.True(t, sc.Balanced, "a backslash inside a raw string is an ordinary byte")
		assert.Len(t, sc.BraceRuns, 1, "the literal body is still one balanced run")

		sc = scanCalleeSpan(`a['x\'y'].b`)
		assert.True(t, sc.Balanced, "an escaped quote inside a single-quoted string does not close it")
	})

	t.Run("bare_name_predicate_separates_qualified_from_bare", func(t *testing.T) {
		for _, s := range []string{"size", "plain", "getAttribute"} {
			assert.True(t, calleeIsBareName(s), "%q carries no qualifier", s)
		}
		// WITHOUT THE REJECT HALF nothing in the repository fails when
		// calleeIsBareName is implemented as "always true" — and that
		// implementation deletes groovy's `o?.a.b()` emission, the shape the
		// profile table deliberately leaves alone, with every other gate green.
		for _, s := range []string{"a.b", "o.size", "Bar::stat", "this.x.y.deep", "$o->a->b"} {
			assert.False(t, calleeIsBareName(s), "%q carries a qualifier", s)
		}
	})
}
