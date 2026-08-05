// SPDX-License-Identifier: Apache-2.0

// identity_splice_test.go — the identity invariant for the source-anchored
// splice, measured on temp fixtures rather than the corpus.
//
// THE INVARIANT: run replace with the replacement template set to the pattern
// VERBATIM. Every diff must be the empty string. A template that rewrites
// nothing must change nothing — every byte inside the matched span that the
// template did not explicitly rewrite comes from SOURCE, so inter-token
// whitespace, line structure, indentation and the anonymous tokens the pattern
// never named all survive untouched.
//
// EVERY ROW CARRIES ITS MATCH COUNT, and the count is asserted before the
// diffs. An identity replace over zero matches produces zero diffs, which is
// textually indistinguishable from a clean pass; so is a run whose pattern
// quietly stopped matching the construct it was written for. The count is the
// known-positive control that tells "preserved it" from "stopped looking at
// it" — the exact confusion the corpus census guards against with its SKIP
// rule.
//
// THE MULTI-LINE ROWS ARE THE LOAD-BEARING ONES. Whole-span splicing re-emits
// the interpolated template over the match's byte range, so a match that spans
// lines comes back reflowed onto one line while a single-line match in the
// same run comes back untouched. A row whose fixture is one line cannot
// observe the reflow at all, so each language's row is written multi-line
// wherever that language's probe can be.
//
// THE THREE TYPESCRIPT INTERFACE-PROPERTY FORMS are mandatory rows: the
// optional, plain and readonly spellings of the same property position. They
// are the audit's headline defect — an identity template there silently turned
// an optional property into a required one and dropped `readonly` — and each
// one matching EXACTLY ONE of the three fixture interfaces is what proves the
// three forms stay mutually exclusive.

package ast

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// spliceCase is one identity payload: a fixture tree, the pattern that is also
// its own replacement template, and the number of matches the pattern must
// find. wantMatches is asserted, never merely reported.
//
// xfail inverts the row: the invariant is still VIOLATED here for a reason that
// lives outside the splice, and the row asserts the violation so the row goes
// red the day it is fixed. Same discipline, and the same inverted semantics, as
// the $$$SEQ contract table's own xfail marker — a fix is recorded by deleting
// the marker, in a diff a reviewer can see. The Rust and Groovy rows carried
// one while `$$$B` still bound the block whole, braces included, so an identity
// template emitted a second pair around a capture that already had one; true
// $$$SEQ sibling semantics retired both, and no row carries a marker now.
type spliceCase struct {
	name        string
	lang        treesitter.Language
	files       map[string]string
	pattern     string
	wantMatches int
	xfail       string
}

// identitySpliceTSFixture holds one interface per property form. Every
// property is written on its own line so the reflow surface is exercised, and
// the three forms sit side by side so a pattern naming one form must leave the
// other two unmatched. It is the MULTI-LINE counterpart to the single-line
// tsInterfaceFixture the anonymous-token discrimination test uses: that one
// measures which targets match, this one measures what the write path does to
// them.
var identitySpliceTSFixture = map[string]string{
	"types.ts": "interface Opt {\n\to?: number;\n}\n\n" +
		"interface Plain {\n\tp: number;\n}\n\n" +
		"interface Ro {\n\treadonly r: number;\n}\n",
}

// identitySpliceCases is the payload table. Each entry was run RED against the
// whole-span splice before the source-anchored one landed.
func identitySpliceCases() []spliceCase {
	return []spliceCase{
		{
			name: "go_multiline_function_body",
			lang: treesitter.LangGo,
			files: map[string]string{
				"main.go": "package main\n\nfunc alpha() {\n\tfirst()\n\tsecond()\n}\n",
			},
			pattern:     "func $N() { $$$B }",
			wantMatches: 1,
		},
		{
			name: "go_multiline_binary_operator",
			lang: treesitter.LangGo,
			files: map[string]string{
				"main.go": "package main\n\nfunc alpha() int {\n\treturn firstOperand +\n\t\tsecondOperand\n}\n",
			},
			pattern:     "$A + $B",
			wantMatches: 1,
		},
		{
			name: "java_annotated_modifier_method",
			lang: treesitter.LangJava,
			files: map[string]string{
				"Alpha.java": "class Alpha {\n\t@Ann\n\tprivate void beta() {\n\t\tx();\n\t}\n\n" +
					"\tpublic void gamma() {\n\t\tx();\n\t}\n}\n",
			},
			pattern:     "@Ann private void $N() { x(); }",
			wantMatches: 1,
		},
		{
			name:        "ts_interface_optional_property",
			lang:        treesitter.LangTypeScript,
			files:       identitySpliceTSFixture,
			pattern:     "interface $I { $N?: $T; }",
			wantMatches: 1,
		},
		{
			name:        "ts_interface_plain_property",
			lang:        treesitter.LangTypeScript,
			files:       identitySpliceTSFixture,
			pattern:     "interface $I { $N: $T; }",
			wantMatches: 1,
		},
		{
			name:        "ts_interface_readonly_property",
			lang:        treesitter.LangTypeScript,
			files:       identitySpliceTSFixture,
			pattern:     "interface $I { readonly $N: $T; }",
			wantMatches: 1,
		},
		{
			// The case that originated the audit: a multi-line type-only
			// import. The `type` keyword is anonymous in the grammar, so the
			// identity template used to strip it off a type import and inject
			// it into a value one; the multi-line spelling adds the reflow on
			// top. The value import beside it keeps the row honest — the
			// pattern must find one of the two, not both.
			name: "ts_multiline_type_only_import",
			lang: treesitter.LangTypeScript,
			files: map[string]string{
				"imports.ts": "import type {\n\tFoo\n} from \"./foo\";\n\n" +
					"import {\n\tBar\n} from \"./foo\";\n",
			},
			pattern:     "import type { $N } from \"./foo\";",
			wantMatches: 1,
		},
		{
			name: "rust_multiline_function_body",
			lang: treesitter.LangRust,
			files: map[string]string{
				"lib.rs": "fn alpha() {\n    first();\n    second();\n}\n",
			},
			pattern:     "fn $N() { $$$B }",
			wantMatches: 1,
		},
		{
			name: "groovy_multiline_if_body",
			lang: treesitter.LangGroovy,
			files: map[string]string{
				"Alpha.groovy": "def alpha() {\n    if (cond) {\n        x()\n        y()\n    }\n}\n",
			},
			pattern:     "if ($C) { $$$B }",
			wantMatches: 1,
		},
	}
}

// runSplice drives the real pipeline — Parse, Compile, Match, ApplyReplace —
// and returns the result alongside the match count so callers can assert the
// count as a known-positive control.
func runSplice(t *testing.T, dir string, lang treesitter.Language, pattern, template string, dryRun bool) (ReplaceResult, int) {
	t.Helper()
	ctx := context.Background()

	pat, err := Parse(pattern)
	require.NoError(t, err, "pattern must parse")
	cp, err := Compile(pat, lang, "")
	require.NoError(t, err, "pattern must compile under a context wrapper")
	defer cp.Close()

	matches, _, err := Match(ctx, dir, lang, cp, nil, Scope{IncludeTests: true})
	require.NoError(t, err)

	res, err := ApplyReplace(ctx, dir, lang, matches, template, dryRun, nil)
	require.NoError(t, err)
	return res, len(matches)
}

// TestIdentityTemplateIsNoOp asserts the identity invariant on every payload,
// and closes with a discriminating control proving the engine still emits the
// edits a genuinely-rewriting template asks for — an engine that refused to
// emit any edit at all would satisfy every identity row above.
func TestIdentityTemplateIsNoOp(t *testing.T) {
	for _, tc := range identitySpliceCases() {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixtureRepo(t, tc.files)
			res, matches := runSplice(t, dir, tc.lang, tc.pattern, tc.pattern, true)

			require.Equal(t, tc.wantMatches, matches,
				"match count is the known-positive control: an identity replace over zero — or over a quietly narrowed — match set proves nothing")
			require.Empty(t, res.RefusedFiles, "no fixture has overlapping matches")
			require.Empty(t, res.RejectedFiles, "an identity rewrite must survive the re-parse gate")

			if tc.xfail != "" {
				_, changed := firstDiffLine(res.Diffs)
				assert.True(t, changed,
					"this row satisfies the identity invariant now — that is a FIX, and the record of it "+
						"is deleting this row's xfail marker rather than keeping a stale one. Reason it carried: %s",
					tc.xfail)
				return
			}

			for path, diff := range res.Diffs {
				assert.Empty(t, diff, "identity template rewrote %s:\n%s", path, diff)
			}
		})
	}

	t.Run("control_rewriting_template_still_diffs", func(t *testing.T) {
		dir := fixtureRepo(t, map[string]string{
			"main.go": "package main\n\nfunc alpha() {\n\tfirst()\n\tsecond()\n}\n",
		})
		res, matches := runSplice(t, dir, treesitter.LangGo,
			"func $N() { $$$B }", "func renamed_$N() { $$$B }", true)

		require.Equal(t, 1, matches)
		require.Contains(t, res.Diffs, "main.go")
		assert.NotEmpty(t, res.Diffs["main.go"], "a template that rewrites a token must still produce a diff")
		assert.Contains(t, res.Diffs["main.go"], "renamed_alpha",
			"the rewritten identifier must reach the diff")
	})
}
