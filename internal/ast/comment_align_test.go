// SPDX-License-Identifier: Apache-2.0

// comment_align_test.go — the comment-transparent alignment reproduction.
//
// THE BUG. Ordinary LITERAL child alignment compares the pattern block's
// children against the target block's children position by position. A comment
// tree-sitter attaches inside a body is a child like any other, so a
// comment-free pattern (`if ($C) { doThing(); }`) fails to align against a
// body that carries a comment beside the statement it names — the matcher reads
// the comment as an extra constraint the pattern did not ask for. The bug is
// UNIVERSAL across grammars and wider than block bodies: it reaches argument
// lists too.
//
// WHY THIS TEST IS RED-FIRST. Every alignment case below asserts the POST-FIX
// match count, so it fails against the unfixed tree with a match-count shortfall
// — the specific red that distinguishes a real reproduction from a fixture that
// failed to compile. Twelve of the thirteen subtests are red today; the lone
// exception is seq_capture_still_spans_comments, a CHARACTERIZATION guard that
// is green before AND after by design, because the sequence path is not the
// path being changed.
//
// THE GUARD THAT SEPARATES THE RIGHT FIX FROM THE WRONG ONE is
// csharp_preproc_region_still_constrains. Every other case uses a COMMENT, and a
// comment is IsExtra — so all twelve pass equally under the declared-CommentKinds
// design this ticket specifies AND under a naive `c.IsExtra()` predicate, which
// is the design measured to be WRONG (it would skip meaningful extras). A C#
// `#region` is IsExtra AND named AND MEANINGFUL; it must STILL constrain a
// literal alignment. Without this subtest nothing fails when an implementer
// wires IsExtra into the walker and the corpus silently starts dropping
// preprocessor regions and heredoc bodies from alignment.

package ast

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// alignMatches builds a one-file fixture repo, compiles pattern under lang with
// no context pin, and returns the matches. It is the same Match entry point the
// existing end-to-end tests drive, so every assertion here is expressible
// against the unfixed tree and the file fails on assertions, not on the build.
func alignMatches(t *testing.T, lang treesitter.Language, rel, body, pattern string) []RawMatch {
	t.Helper()
	dir := fixtureRepo(t, map[string]string{rel: body})
	pat, err := Parse(pattern)
	require.NoErrorf(t, err, "pattern %q must parse", pattern)
	cp, err := Compile(pat, lang, "")
	require.NoErrorf(t, err, "pattern %q must compile under %s", pattern, lang)
	defer cp.Close()
	raws, _, err := Match(context.Background(), dir, lang, cp, nil, Scope{})
	require.NoError(t, err)
	return raws
}

// conditionTexts collects the $C capture text of every match, sorted, so a
// subtest can assert WHICH sites matched rather than only how many.
func conditionTexts(raws []RawMatch) []string {
	out := make([]string, 0, len(raws))
	for _, r := range raws {
		out = append(out, r.Captures["C"].Text)
	}
	sort.Strings(out)
	return out
}

// TestCommentTransparentAlignment reproduces the comment-blindness across
// grammars and placements. THIRTEEN subtests exactly — the count is pinned by a
// criterion, so a fourteenth means updating that criterion in the same commit.
func TestCommentTransparentAlignment(t *testing.T) {
	// A single literal-body pattern shared by the block-body cases whose only
	// difference is the grammar or the comment placement.
	const jsIfPattern = "if ($C) { doThing(); }"

	t.Run("javascript_leading", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangJavaScript, "f.js", `function f(a, b) {
  if (a) { doThing(); }
  if (b) {
    // lead
    doThing();
  }
}
`, jsIfPattern)
		assert.Len(t, raws, 2, "control (a) plus the leading-comment site (b) must both match")
	})

	t.Run("javascript_trailing", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangJavaScript, "f.js", `function f(a, b) {
  if (a) { doThing(); }
  if (b) {
    doThing();
    // trail
  }
}
`, jsIfPattern)
		assert.Len(t, raws, 2, "control (a) plus the trailing-comment site (b) must both match")
	})

	t.Run("javascript_block_comment", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangJavaScript, "f.js", `function f(a, b) {
  if (a) { doThing(); }
  if (b) { /* blk */ doThing(); }
}
`, jsIfPattern)
		assert.Len(t, raws, 2, "control (a) plus the block-comment site (b) must both match")
	})

	t.Run("javascript_midblock", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangJavaScript, "f.js", `function f(a, b) {
  if (a) { doA(); doB(); }
  if (b) {
    doA();
    // mid
    doB();
  }
}
`, "if ($C) { doA(); doB(); }")
		assert.Len(t, raws, 2, "a comment BETWEEN two literal statements must not block alignment")
	})

	t.Run("go_leading", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangGo, "f.go", `package p
func f(a, b bool) {
	if a { doThing() }
	if b {
		// lead
		doThing()
	}
}
`, "if $C { doThing() }")
		assert.Len(t, raws, 2, "control (a) plus the leading-comment site (b) must both match")
	})

	t.Run("python_leading", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangPython, "f.py", `def f(a, b):
    if a:
        do_thing()
    if b:
        # lead
        do_thing()
`, "if $C:\n    do_thing()")
		assert.Len(t, raws, 2, "control (a) plus the leading-comment site (b) must both match")
	})

	t.Run("typescript_leading", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangTypeScript, "f.ts", `function f(a: boolean, b: boolean) {
  if (a) { doThing(); }
  if (b) {
    // lead
    doThing();
  }
}
`, jsIfPattern)
		assert.Len(t, raws, 2, "control (a) plus the leading-comment site (b) must both match")
	})

	t.Run("java_leading", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangJava, "T.java", `class T {
  void m(boolean a, boolean b) {
    if (a) { doThing(); }
    if (b) {
      // lead
      doThing();
    }
  }
}
`, jsIfPattern)
		assert.Len(t, raws, 2, "control (a) plus the leading-comment site (b) must both match")
	})

	t.Run("rust_leading", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangRust, "f.rs", `fn f(a: bool, b: bool) {
    if a { do_thing(); }
    if b {
        // lead
        do_thing();
    }
}
`, "if $C { do_thing(); }")
		assert.Len(t, raws, 2, "control (a) plus the leading-comment site (b) must both match")
	})

	t.Run("csharp_leading", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangCSharp, "T.cs", `class T {
    void M(bool a, bool b) {
        if (a) { DoThing(); }
        if (b) {
            // lead
            DoThing();
        }
    }
}
`, "if ($C) { DoThing(); }")
		assert.Len(t, raws, 2, "control (a) plus the leading-comment site (b) must both match")
	})

	t.Run("go_arglist_inline", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangGo, "f.go", `package p
func f(x int) {
	g(x)
	g(/* c */ x)
}
`, "g($X)")
		assert.Len(t, raws, 2, "an inline comment inside an argument list must not block alignment")
	})

	// CHARACTERIZATION GUARD — green before AND after. Its whole job is to go RED
	// if 3.2 is implemented in allChildren (the sequence path) instead of the
	// literal-sibling path: a $$$ capture must STILL bind and span the comment
	// verbatim, so a $$$ body re-interpolates as valid source.
	t.Run("seq_capture_still_spans_comments", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangJavaScript, "f.js", `function f(a) {
  if (a) {
    // lead
    doThing();
  }
}
`, "if ($C) { $$$B }")
		require.Len(t, raws, 1, "exactly one if-statement to match")
		b := raws[0].Captures["B"]
		require.Len(t, b.Children, 2, "B must bind BOTH the comment and the statement")
		assert.Equal(t, "comment", b.Children[0].Kind, "first sequence child is the comment")
		assert.Equal(t, "expression_statement", b.Children[1].Kind, "second sequence child is the statement")
		assert.Contains(t, b.Text, "// lead", "B text must span the comment verbatim")
		assert.Contains(t, b.Text, "doThing();", "B text must span the statement verbatim")
	})

	// THE DISCRIMINATOR. A meaningful extra (#region) must STILL constrain: the
	// comment site (cond3) starts matching post-fix, but the #region site (cond2)
	// must stay out. Under a wrong IsExtra implementation cond2 is skipped too and
	// the total is 3 — the failure this subtest exists to produce.
	t.Run("csharp_preproc_region_still_constrains", func(t *testing.T) {
		raws := alignMatches(t, treesitter.LangCSharp, "P.cs", `class T {
    void M(bool cond1, bool cond2, bool cond3) {
        if (cond1) { DoA(); DoB(); }
        if (cond2) {
            DoA();
            #region stuff
            DoB();
        }
        if (cond3) {
            DoA();
            // just a comment
            DoB();
        }
    }
}
`, "if ($C) { DoA(); DoB(); }")
		assert.Len(t, raws, 2, "the plain site and the comment site match; the #region site must not")
		assert.Equal(t, []string{"cond1", "cond3"}, conditionTexts(raws),
			"a meaningful extra (#region, cond2) must still constrain alignment; only cond1 and cond3 may match")
	})
}
