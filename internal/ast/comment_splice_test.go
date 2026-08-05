// SPDX-License-Identifier: Apache-2.0

// comment_splice_test.go — the splice-fidelity reproduction for comments that
// Phase 3 made matchable.
//
// THE HAZARD. splice.go rebuilds a rewritten site as src[start:left] + body +
// src[right:end], where [left, right] is the source range the template's middle
// overwrites. Before Phase 3 a comment-carrying body never matched, so no
// comment ever landed in that overwritten range. Phase 3 makes those sites
// match — and a comment the walker skipped now sits inside [left, right] with no
// pattern token anchored to it, so the splice replaces it with template text and
// the comment is silently DELETED. That trades Phase 3's fixed false-negative
// for a false rewrite, which is strictly worse.
//
// WHY THIS TEST IS RED-FIRST, AND AGAINST WHICH TREE. It cannot go red until
// Phase 3.2 has landed — before that the comment sites report ZERO MATCHES and
// every case is vacuous. Run it against the Phase-3-only tree: the comment-edge
// cases fail with the comment text missing from otherwise-intact output. A case
// reporting zero matches means Phase 3 is not actually done, not that this gate
// is broken — the assertions say which so the two are distinguishable across a
// context boundary.
//
// EIGHT subtests exactly — the count is pinned by a criterion. Five are
// comment-edge cases that go green in Phase 4.2; three are guards:
// comment_outside_match_is_not_claimed (the rollback/reset catcher, green once
// Phase 3.2's rollbacks are correct), identity_over_a_comment_site_is_a_no_op
// (the identity control), and interior_comment_is_inside_the_caller_rewrite (the
// CHARACTERIZATION guard for the boundary Phase 4.2 deliberately does not cross —
// the one case that asserts a comment is NOT preserved).

package ast

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// requireOneMatch fails with a message that names the Phase-3 dependency, so a
// zero-match result reads as "Phase 3 is not done" rather than as a splice bug.
// Every fidelity fixture is written to carry exactly one matching site.
func requireOneMatch(t *testing.T, matches int) {
	t.Helper()
	require.Equalf(t, 1, matches,
		"expected exactly one match; a ZERO here means Phase 3's comment-transparent alignment is not in this tree, NOT that the splice gate is broken")
}

func TestCommentSpliceFidelity(t *testing.T) {
	t.Run("leading_comment_survives_a_rewrite", func(t *testing.T) {
		got, matches := spliceRewrite(t, treesitter.LangJavaScript, "f.js", `function f(b) {
  if (b) {
    // lead
    doThing();
  }
}
`, "if ($C) { doThing(); }", "if ($C) { doOther(); }")
		requireOneMatch(t, matches)
		assert.Contains(t, got, "// lead", "the leading comment must survive the rewrite")
		assert.Contains(t, got, "doOther", "the rewrite must have landed")
		assert.NotContains(t, got, "doThing", "the rewritten statement must be gone")
	})

	t.Run("trailing_comment_survives_a_rewrite", func(t *testing.T) {
		got, matches := spliceRewrite(t, treesitter.LangJavaScript, "f.js", `function f(b) {
  if (b) {
    doThing();
    // trail
  }
}
`, "if ($C) { doThing(); }", "if ($C) { doOther(); }")
		requireOneMatch(t, matches)
		assert.Contains(t, got, "// trail", "the trailing comment must survive the rewrite")
		assert.Contains(t, got, "doOther", "the rewrite must have landed")
	})

	t.Run("midblock_comment_survives_a_rewrite", func(t *testing.T) {
		// Two-statement pattern, rewrite of the FIRST statement only: the comment
		// sits at the core's TRAILING edge, before the preserved doB().
		got, matches := spliceRewrite(t, treesitter.LangJavaScript, "f.js", `function f(b) {
  if (b) {
    doA();
    // mid
    doB();
  }
}
`, "if ($C) { doA(); doB(); }", "if ($C) { doX(); doB(); }")
		requireOneMatch(t, matches)
		assert.Contains(t, got, "// mid", "the mid-block comment must survive when only the first statement is rewritten")
		assert.Contains(t, got, "doX", "the rewrite must have landed")
		assert.Contains(t, got, "doB", "the untouched second statement must survive")
	})

	t.Run("arglist_comment_survives_a_rewrite", func(t *testing.T) {
		got, matches := spliceRewrite(t, treesitter.LangGo, "f.go", `package p
func f(x int) {
	g(/* c */ x)
}
`, "g($X)", "h($X)")
		requireOneMatch(t, matches)
		assert.Contains(t, got, "/* c */", "the inline argument comment must survive the rewrite")
		assert.Contains(t, got, "h(", "the rewrite must have landed")
	})

	t.Run("template_attaches_with_no_space", func(t *testing.T) {
		// The template's middle carries NO leading whitespace. A comment is
		// CONTENT, not spacing, so it must be re-emitted whatever the template's
		// spacing choice — an implementation that merely widens isSpliceSpace
		// (treating a comment as inter-token whitespace) drops it here, because
		// preferSourceSpace would then honor the template's "no space" decision.
		got, matches := spliceRewrite(t, treesitter.LangJavaScript, "f.js", `function f(b) {
  if (b) {
    // lead
    doThing();
  }
}
`, "if ($C) { doThing(); }", "if ($C) {doOther();}")
		requireOneMatch(t, matches)
		assert.Contains(t, got, "// lead", "a comment is content, not spacing; it survives regardless of the template's whitespace choice")
		assert.Contains(t, got, "doOther", "the rewrite must have landed")
	})

	t.Run("comment_outside_match_is_not_claimed", func(t *testing.T) {
		// Two sites and an interstitial comment belonging to neither's span; only
		// the first site matches the pattern. The catcher for Phase 3.2's
		// matchSeqShadow rollback and Captures.reset truncation: a span leaked
		// from a rejected candidate or an abandoned greedy try would show up here
		// as the interstitial comment or the second site being claimed as edge
		// material of the first match.
		got, matches := spliceRewrite(t, treesitter.LangJavaScript, "f.js", `function f(a, b) {
  if (a) { doThing(); }
  // between
  if (b) { doOther(); }
}
`, "if ($C) { doThing(); }", "if ($C) { doNew(); }")
		requireOneMatch(t, matches)
		assert.Contains(t, got, "doNew", "the first site must be rewritten")
		assert.Contains(t, got, "// between", "the interstitial comment belongs to no match and must be untouched")
		assert.Contains(t, got, "doOther(); }", "the second, non-matching site must be untouched")
	})

	t.Run("identity_over_a_comment_site_is_a_no_op", func(t *testing.T) {
		// The control: an identity replace over a comment site must stay
		// byte-identical. Without it the suite cannot tell "comments preserved"
		// from "the splice stopped rewriting".
		src := `function f(b) {
  if (b) {
    // lead
    doThing();
  }
}
`
		got, matches := spliceRewrite(t, treesitter.LangJavaScript, "f.js", src, "if ($C) { doThing(); }", "if ($C) { doThing(); }")
		requireOneMatch(t, matches)
		assert.Equal(t, src, got, "an identity replacement over a comment site must be byte-identical")
	})

	t.Run("interior_comment_is_inside_the_caller_rewrite", func(t *testing.T) {
		// CHARACTERIZATION GUARD, and the ONE case here that asserts a comment is
		// NOT preserved. Phase 4.2 recovers comments at the EDGES of the rewritten
		// region, deliberately NOT one strictly interior to it — between two tokens
		// the caller rewrites on both sides. Both statements are rewritten, so the
		// head scan stops before the comment and the tail scan stops after it,
		// leaving it strictly inside the replaced core. This is the DESIGNED
		// boundary named in splice.go's header; if a later ticket decides interior
		// comments must survive too, THIS assertion is the one that flips, and its
		// flip is the record that the boundary moved.
		got, matches := spliceRewrite(t, treesitter.LangJavaScript, "f.js", `function f(b) {
  if (b) {
    doA();
    // interior
    doB();
  }
}
`, "if ($C) { doA(); doB(); }", "if ($C) { doX(); doY(); }")
		requireOneMatch(t, matches)
		assert.Contains(t, got, "doX", "the rewrite must have landed")
		assert.Contains(t, got, "doY", "the rewrite must have landed")
		assert.NotContains(t, got, "// interior",
			"a strictly-interior comment is the boundary Phase 4.2 does not cross (see splice.go header); this deletion is designed, not a bug")
	})
}

// TestCommentSpansAreSourceOrdered pins the claim copyComments and
// recoverEdgeComments both rely on: the walk records skipped comment spans in
// ascending source order, so no sort is needed on the splice's per-match path.
// A body with several skipped comments is matched and its CommentSpans checked
// for strictly ascending, non-overlapping ranges — the ordering is asserted
// rather than defended by a defensive sort on the hot path.
func TestCommentSpansAreSourceOrdered(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{"f.js": `function f(b) {
  if (b) {
    // one
    /* two */
    doThing();
    // three
  }
}
`})
	pat, err := Parse("if ($C) { doThing(); }")
	require.NoError(t, err)
	cp, err := Compile(pat, treesitter.LangJavaScript, "")
	require.NoError(t, err)
	defer cp.Close()

	matches, _, err := Match(context.Background(), dir, treesitter.LangJavaScript, cp, nil, Scope{})
	require.NoError(t, err)
	require.Len(t, matches, 1)

	spans := matches[0].CommentSpans
	require.GreaterOrEqual(t, len(spans), 2, "the body carries several skipped comments; a count below two would make the ordering assertion vacuous")
	for i := 1; i < len(spans); i++ {
		assert.Greaterf(t, spans[i].Start, spans[i-1].Start,
			"CommentSpans must be strictly ascending by source position (entry %d starts at %d, not after %d)", i, spans[i].Start, spans[i-1].Start)
		assert.GreaterOrEqualf(t, spans[i].Start, spans[i-1].End,
			"CommentSpans must be non-overlapping (entry %d starts at %d, inside the prior span ending at %d)", i, spans[i].Start, spans[i-1].End)
	}
}
