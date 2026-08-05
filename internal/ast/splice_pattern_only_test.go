// SPDX-License-Identifier: Apache-2.0

// splice_pattern_only_test.go — the write-side rule for pattern tokens that
// MATCHING dropped.
//
// THE DEFECT THIS PINS. A sequence position promoted through a wrapper drops
// the wrapper's own tokens: C's `{ $$$B; }` parses its body slot as an
// expression_statement spanning `<placeholder>;`, the sequence is promoted so
// the placeholder consumes the target's statements, and the `;` goes with the
// wrapper. It earns no alignment entry, because nothing in the target was
// compared against it. The identity template still carries that `;`, the splice
// finds a template token with no anchor, and emits it beside statements that
// already carry their own terminators — `g(a);` written back as `g(a);;`.
//
// THE RULE: a template token that repeats a pattern token the match dropped is
// CONSUMED rather than emitted — but only when it falls inside a span the
// matcher recorded as dropped, and only for as many bytes as that span holds.
//
// WHY THE DOUBLED-TERMINATOR ROW IS THE LOAD-BEARING ONE. Every other row here
// is green under the forbidden blanket reading of the rule ("unaligned means
// consume"). The doubled-terminator row is not: its first `;` repeats the
// pattern and must vanish, its second is the caller's own and must survive. A
// blanket rule deletes a token the caller explicitly wrote, which is the silent
// deletion the scoping exists to prevent — the exact failure mode that is worse
// than the duplication being fixed, because a duplication is visible in the
// diff and a deletion is not.
//
// ROWS ASSERT FULL EXPECTED BYTES, not a diff summary: the preservation half of
// this rule lives in the bytes the template never named — indentation, line
// structure, the single terminators already in the source.

package ast

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// cSemicolonBodyFixture is the measured repro payload: a C if-body holding two
// statements that each carry their own terminator, so a re-emitted pattern
// semicolon lands visibly beside one of them.
const cSemicolonBodyFixture = "void f(int c) {\n    if (c) {\n        int a = 1;\n        g(a);\n    }\n}\n"

// patternOnlyCase is one write-side payload. want is the FULL expected file
// content after the rewrite; wantMatches is the known-positive control that
// separates "preserved every byte" from "quietly stopped matching".
type patternOnlyCase struct {
	name        string
	lang        treesitter.Language
	path        string
	src         string
	pattern     string
	template    string
	wantMatches int
	want        string
}

func patternOnlyCases() []patternOnlyCase {
	return []patternOnlyCase{
		{
			// Identity: the pattern is its own template, so nothing may change.
			// Before the rule landed this produced `g(a);;`.
			name:        "identity_c_semicolon_body",
			lang:        treesitter.LangC,
			path:        "main.c",
			src:         cSemicolonBodyFixture,
			pattern:     "if ($C) { $$$B; }",
			template:    "if ($C) { $$$B; }",
			wantMatches: 1,
			want:        cSemicolonBodyFixture,
		},
		{
			// Non-identity: the condition is genuinely rewritten and every body
			// byte — indentation, line breaks, single terminators — survives.
			// Catches a rule that consumes one token too many.
			name:        "non_identity_condition_rewrite_preserves_body",
			lang:        treesitter.LangC,
			path:        "main.c",
			src:         cSemicolonBodyFixture,
			pattern:     "if ($C) { $$$B; }",
			template:    "if (!($C)) { $$$B; }",
			wantMatches: 1,
			want:        "void f(int c) {\n    if (!(c)) {\n        int a = 1;\n        g(a);\n    }\n}\n",
		},
		{
			// Aligned semicolon, return form: this `;` is part of the
			// return_statement, is compared against the target's own and earns
			// an alignment entry. It must still be emitted.
			name:        "aligned_semicolon_return",
			lang:        treesitter.LangC,
			path:        "ret.c",
			src:         "int f(int x) {\n    return x;\n}\n",
			pattern:     "return $X;",
			template:    "return $X;",
			wantMatches: 1,
			want:        "int f(int x) {\n    return x;\n}\n",
		},
		{
			// Aligned semicolon, call form: same mechanism as the return row,
			// spelled the way the corpus cells spell it. Together these two
			// catch a rule that generalized from "the pattern has a semicolon".
			name:        "aligned_semicolon_call",
			lang:        treesitter.LangC,
			path:        "call.c",
			src:         "void f(int a) {\n    g(a);\n}\n",
			pattern:     "$F($X);",
			template:    "$F($X);",
			wantMatches: 1,
			want:        "void f(int a) {\n    g(a);\n}\n",
		},
		{
			// THE DISCRIMINATING ROW. The caller deliberately asked for a
			// doubled terminator: the first `;` repeats the pattern's dropped
			// one and is consumed, the second is template-authored and must
			// reach the file. A blanket "unaligned means consume" rule drops
			// both and silently produces `g(a);`.
			name:        "template_authored_second_semicolon_survives",
			lang:        treesitter.LangC,
			path:        "main.c",
			src:         cSemicolonBodyFixture,
			pattern:     "if ($C) { $$$B; }",
			template:    "if ($C) { $$$B;; }",
			wantMatches: 1,
			want:        "void f(int c) {\n    if (c) {\n        int a = 1;\n        g(a);;\n    }\n}\n",
		},
	}
}

// TestSplicePatternOnlyToken drives every row through the real match-and-splice
// path and asserts the full rewritten bytes.
func TestSplicePatternOnlyToken(t *testing.T) {
	for _, tc := range patternOnlyCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, matches := spliceRewrite(t, tc.lang, tc.path, tc.src, tc.pattern, tc.template)
			require.Equal(t, tc.wantMatches, matches,
				"match count is the known-positive control: a rewrite over zero matches preserves every byte for the wrong reason")
			assert.Equal(t, tc.want, got, "rewritten bytes differ")
		})
	}
}

// spliceRewrite parses, compiles, matches and splices one single-file fixture,
// returning the rewritten source and the match count. It stops short of
// ApplyReplace's write: buildFileEdits already runs the splice and
// applyEditsToSource already runs the re-parse gate, so the bytes are the real
// ones without any fixture ever being written back.
func spliceRewrite(t *testing.T, lang treesitter.Language, path, src, pattern, template string) (string, int) {
	t.Helper()
	ctx := context.Background()
	dir := fixtureRepo(t, map[string]string{path: src})

	pat, err := Parse(pattern)
	require.NoError(t, err, "pattern must parse")
	cp, err := Compile(pat, lang, "")
	require.NoError(t, err, "pattern must compile under a context wrapper")
	defer cp.Close()

	matches, _, err := Match(ctx, dir, lang, cp, nil, Scope{IncludeTests: true})
	require.NoError(t, err)

	srcBytes := []byte(src)
	edits, refused, err := buildFileEdits(matches, template, map[string][]byte{path: srcBytes})
	require.NoError(t, err)
	require.Empty(t, refused, "fixture must not carry overlapping matches")
	if len(edits[path]) == 0 {
		return src, len(matches)
	}
	out, err := applyEditsToSource(ctx, srcBytes, edits[path], lang)
	require.NoError(t, err, "rewritten fixture must survive the re-parse gate")
	return string(out), len(matches)
}
