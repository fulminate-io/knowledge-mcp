// SPDX-License-Identifier: Apache-2.0

// layout_jsx_test.go — the catchers for the anonymous-token whitespace trim:
// what it must NOT widen, which grammars may declare it, and what the write side
// must carry through unchanged.
//
// THE LANDED JSX GUARDS DO NOT COVER THIS SEAM, and the distinction matters
// enough to name. anon_token_test.go's tsx_bare_div_still_matches and
// regression_guards_test.go's two JSX rows all fail on CHILD COUNT or on NODE
// KIND — an attributed element carries an extra named child, and an element and
// a self-closing element are different kinds. Trimming touches neither, so none
// of them can fire on a trim mistake. They remain regression fences; the rows
// here are the discriminating ones.
//
// WHY THE GATING ROWS CALL THE HELPER DIRECTLY. Whether the config flag is
// honored is a property of the comparison, not of any grammar's tokenization.
// Asserting it through a parse would only prove that the grammars which declare
// the trim behave as though they declare it — which is what the reproductions
// already show — and would leave "the flag is ignored and every grammar trims"
// indistinguishable from correct.

package ast

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// jsxMultiLineFile is a formatted multi-line JSX file — the shape whose child
// elements carry the absorbed newline in their leading token.
const jsxMultiLineFile = "export function App() {\n" +
	"  return <div>\n" +
	"    <CodeBlock code={first} />\n" +
	"    <CodeBlock code={second} />\n" +
	"  </div>;\n" +
	"}\n"

func TestJSXLayoutTrimStaysNarrow(t *testing.T) {
	// FIRES ON: a trim applied beyond anonymous tokens. The leading space sits
	// inside a NAMED string fragment, so a comparison that trims named leaves
	// too makes these two match.
	t.Run("named_leaf_whitespace_still_discriminates", func(t *testing.T) {
		matches := runLongTailWalker(t, tsxLangConfig,
			`<CodeBlock code={"x"} />`, "const a = <CodeBlock code={\" x\"} />;\n")
		assert.Empty(t, matches,
			"whitespace inside a string literal is content, not layout — trimming must not reach a named leaf")

		positive := runLongTailWalker(t, tsxLangConfig,
			`<CodeBlock code={"x"} />`, "const b = <CodeBlock code={\"x\"} />;\n")
		require.Len(t, positive, 1,
			"known positive: the same pattern over the untrimmed spelling matches, so the zero above is a refusal and not a dead probe")
	})

	// FIRES ON: a trim that collapses to emptiness, or compares lengths rather
	// than trimmed content. Both spellings are the same node kind with the same
	// child count, differing only in one anonymous operator token, so nothing
	// upstream of the token comparison can reject them.
	t.Run("distinct_anonymous_tokens_still_discriminate", func(t *testing.T) {
		const src = "const strict = lhs === rhs;\nconst loose = lhs == rhs;\n"
		for _, g := range jsxLayoutGrammars {
			strict := runLongTailWalker(t, g.cfg, "lhs === rhs", src)
			require.Len(t, strict, 1, "%s: known positive — the strict operator matches its own spelling", g.label)
			loose := runLongTailWalker(t, g.cfg, "lhs == rhs", src)
			require.Len(t, loose, 1, "%s: known positive — the loose operator matches its own spelling", g.label)
		}

		// At the token level, where a length- or emptiness-based comparison
		// would give itself away regardless of what the grammars emit.
		assert.False(t, tokenTextMatches([]byte("/>"), []byte("\n>"), true),
			"trimming compares trimmed CONTENT: two different operators must stay different")
		assert.True(t, tokenTextMatches([]byte(">"), []byte("\n>"), true),
			"known positive: the same operator across a layout difference must match")
	})

	// FIRES ON: the config gate being ignored, so every grammar trims. The pair
	// is the measured one — a pattern's "<" against a target's absorbed "\n<".
	t.Run("trim_applies_only_when_the_grammar_declares_it", func(t *testing.T) {
		assert.True(t, tokenTextMatches([]byte("<"), []byte("\n<"), true),
			"a declaring grammar must reach the absorbed spelling")
		assert.False(t, tokenTextMatches([]byte("<"), []byte("\n<"), false),
			"a non-declaring grammar must keep byte-exact comparison, or the flag is decorative")
	})

	// FIRES ON: the flag spreading to a third grammar. Read from the live
	// registry rather than a hardcoded list, so a newly registered grammar that
	// declares the trim has to be justified here rather than landing unnoticed.
	t.Run("exactly_two_grammars_declare_the_trim", func(t *testing.T) {
		var declaring []string
		for _, name := range registeredLangs() {
			cfg, ok := langConfigFor(treesitter.Language(name))
			require.True(t, ok, "no LangConfig registered for %s", name)
			if cfg.TrimsAnonTokenWhitespace {
				declaring = append(declaring, name)
			}
		}
		sort.Strings(declaring)
		assert.Equal(t, []string{"javascript", "tsx"}, declaring,
			"only the JSX-bearing grammars absorb layout whitespace into a token; plain typescript has no JSX")
	})
}

// TestJSXLayoutSpliceRoundTrip is the write-side half. A token compared trimmed
// still ALIGNS at its real byte range, which includes the absorbed whitespace —
// so the splice has to carry those bytes through untouched. Without these rows a
// fix that matches correctly while mis-aligning the absorbed span would silently
// reflow every multi-line JSX identity replace onto one line.
func TestJSXLayoutSpliceRoundTrip(t *testing.T) {
	t.Run("identity_over_multi_line_jsx_is_byte_identical", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{"app.tsx": jsxMultiLineFile})
		res, matches := runSplice(t, dir, treesitter.LangTSX,
			jsxChildPattern, jsxChildPattern, true)
		require.Equal(t, 2, matches, "an identity replace over zero matches proves nothing")
		requireNoDiff(t, res, "JSX child pattern over a multi-line target")
	})

	// The preservation half an identity gate cannot prove: a real rewrite must
	// change its one token and leave the surrounding line structure alone.
	t.Run("one_token_rewrite_preserves_surrounding_bytes", func(t *testing.T) {
		got, matches := spliceRewrite(t, treesitter.LangTSX, "app.tsx", jsxMultiLineFile,
			jsxChildPattern, "<CodeBlock source={$C} />")
		require.Equal(t, 2, matches, "a rewrite over zero matches preserves every byte for the wrong reason")
		want := "export function App() {\n" +
			"  return <div>\n" +
			"    <CodeBlock source={first} />\n" +
			"    <CodeBlock source={second} />\n" +
			"  </div>;\n" +
			"}\n"
		assert.Equal(t, want, got, "the element's indentation and line breaks must survive the rewrite")
	})
}
