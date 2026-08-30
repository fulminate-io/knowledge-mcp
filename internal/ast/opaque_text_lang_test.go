// SPDX-License-Identifier: Apache-2.0

// opaque_text_lang_test.go — one regression case per language and per declared
// opaque kind, so no grammar's entry in LangConfig.OpaqueTextKinds rests on the
// census alone.
//
// THE CASE SHAPE. Every row carries a MATCHING target and a DIFFERING target
// that are byte-identical except inside the opaque node, and asserts the pattern
// reaches exactly one site in the first and none in the second. The differing
// target is the property under test; the matching target is its known positive,
// because "matches nothing" passes just as well against a pattern that compiles
// to something unreachable — the exact failure mode the defect's own control legs
// exist to rule out.
//
// WHY A PAIR OF TARGETS RATHER THAN TWO SITES IN ONE. The two-sites-in-one-parse
// spelling looks tighter and is a trap: tree-sitter-groovy emits no groovy_doc
// node AT ALL when one file carries two doc comments, so such a row asserts
// "exactly one match" over a node the target never produced. Separate targets
// keep each parse in the shape the grammar actually emits.
//
// EVERY ROW HERE HAS BEEN OBSERVED RED. The rows were run against a build with
// isOpaqueTextKind forced to false, and each one failed. Three did not on the
// first attempt, and each was a different lesson: two discriminated on an
// IDENTIFIER rather than on the literal, so they would have passed against the
// unfixed matcher; the third was a groovy_doc row that passed because groovy_doc
// covers its text with a first_line child and was never content-blind at all —
// that one is why groovy_doc is classified delimiter-only and carries no
// registration, rather than why it has a better row.

package ast

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// opaqueLangCase is one (language, kind) regression row.
type opaqueLangCase struct {
	// name identifies the row; it is the subtest name and carries the kind.
	name string
	cfg  LangConfig
	// kind is the LangConfig.OpaqueTextKinds entry this row exercises. It is
	// asserted to BE declared, so a row can never silently test a kind the
	// registration dropped.
	kind string
	// pattern carries an inlined literal or comment. matching holds one site
	// spelling exactly that literal; differing is the same source with the
	// literal's CONTENT changed and nothing else.
	pattern   string
	matching  string
	differing string
}

var opaqueLangCases = []opaqueLangCase{
	{
		name:      "go/interpreted_string_literal",
		cfg:       goLangConfig,
		kind:      "interpreted_string_literal",
		pattern:   `zz("alpha")`,
		matching:  "package p\n\nfunc g() {\n\tzz(\"alpha\")\n}\n",
		differing: "package p\n\nfunc g() {\n\tzz(\"beta\")\n}\n",
	},
	{
		// Python's string_content is a childless leaf until an ESCAPE splits it,
		// so the discriminating pair has to carry one — a plain-literal pair would
		// pass against the unfixed matcher and prove nothing.
		name:      "python/string_content",
		cfg:       pythonLangConfig,
		kind:      "string_content",
		pattern:   `zz("al\npha")`,
		matching:  "zz(\"al\\npha\")\n",
		differing: "zz(\"be\\nta\")\n",
	},
	{
		name:      "ocaml/string_content",
		cfg:       ocamlLangConfig,
		kind:      "string_content",
		pattern:   `let zz = "al\npha"`,
		matching:  "let zz = \"al\\npha\"\n",
		differing: "let zz = \"be\\nta\"\n",
	},
	{
		// A %s conversion specifier is what gives quoted_string_content a child and
		// so splits its text into gaps; without one it is a leaf.
		name:      "ocaml/quoted_string_content",
		cfg:       ocamlLangConfig,
		kind:      "quoted_string_content",
		pattern:   `let zz = {|al %s pha|}`,
		matching:  "let zz = {|al %s pha|}\n",
		differing: "let zz = {|be %s ta|}\n",
	},
	{
		name:      "kotlin/character_literal",
		cfg:       kotlinLangConfig,
		kind:      "character_literal",
		pattern:   `zz('a')`,
		matching:  "fun f() { zz('a') }\n",
		differing: "fun f() { zz('b') }\n",
	},
	{
		name: "csharp/interpolation_format_clause",
		cfg:  csharpLangConfig,
		kind: "interpolation_format_clause",
		// C#'s interpolated-string sigil is the DSL's placeholder sigil, so the
		// pattern spells it with the `$$` literal-dollar escape. The pattern is the
		// bare EXPRESSION rather than a `var zz = …;` statement because C#'s
		// statement wrapper hosts no local-declaration pattern at all — measured:
		// the literal-free `var zz = 1;` reaches zero sites too, so that is a
		// pre-existing wrapper limitation and not something this row can assert
		// around.
		pattern:   `$$"{tm:yyyyMM}"`,
		matching:  "class C { void M() { var zz = $\"{tm:yyyyMM}\"; } }\n",
		differing: "class C { void M() { var zz = $\"{tm:ddMMyy}\"; } }\n",
	},
	{
		name:      "scala/interpolated_string",
		cfg:       scalaLangConfig,
		kind:      "interpolated_string",
		pattern:   `zz(s"took $${n}ms")`,
		matching:  "object O { def m() = { zz(s\"took ${n}ms\") } }\n",
		differing: "object O { def m() = { zz(s\"lost ${n}ms\") } }\n",
	},
	{
		name:      "rust/line_comment",
		cfg:       rustLangConfig,
		kind:      "line_comment",
		pattern:   "// alpha",
		matching:  "// alpha\n",
		differing: "// beta\n",
	},
	{
		name:      "rust/block_comment",
		cfg:       rustLangConfig,
		kind:      "block_comment",
		pattern:   "/*\n * alpha\n */",
		matching:  "/*\n * alpha\n */\n",
		differing: "/*\n * beta\n */\n",
	},
	{
		name:      "scala/comment",
		cfg:       scalaLangConfig,
		kind:      "comment",
		pattern:   "// alpha",
		matching:  "// alpha\n",
		differing: "// beta\n",
	},
	{
		name:      "scala/block_comment",
		cfg:       scalaLangConfig,
		kind:      "block_comment",
		pattern:   "/*\n * alpha\n */",
		matching:  "/*\n * alpha\n */\n",
		differing: "/*\n * beta\n */\n",
	},
	{
		name:      "groovy/comment",
		cfg:       groovyLangConfig,
		kind:      "comment",
		pattern:   "/*\n * alpha\n */",
		matching:  "def a = 1\n/*\n * alpha\n */\ndef b = 2\n",
		differing: "def a = 1\n/*\n * beta\n */\ndef b = 2\n",
	},
	{
		name:      "elm/block_comment",
		cfg:       elmLangConfig,
		kind:      "block_comment",
		pattern:   "{- alpha -}",
		matching:  "module M exposing (..)\n\n{- alpha -}\na = 1\n",
		differing: "module M exposing (..)\n\n{- beta -}\na = 1\n",
	},
	{
		name:      "bash/heredoc_body",
		cfg:       bashLangConfig,
		kind:      "heredoc_body",
		pattern:   "cat <<EOF\nalpha $${x}\nEOF",
		matching:  "cat <<EOF\nalpha ${x}\nEOF\n",
		differing: "cat <<EOF\nbeta ${x}\nEOF\n",
	},
}

// TestOpaqueTextKinds_PerLanguage runs every row. It also asserts the row's kind
// is actually declared for its language, so dropping a registration fails here
// with a message naming the language rather than as an inscrutable match count.
func TestOpaqueTextKinds_PerLanguage(t *testing.T) {
	// Coverage floor: every declared opaque kind across the registry must have a
	// row. Without it a row could be deleted alongside its registration and the
	// suite would stay green over a shrinking guarantee.
	t.Run("every_declared_opaque_kind_has_a_row", func(t *testing.T) {
		covered := map[string]bool{}
		for _, c := range opaqueLangCases {
			covered[string(c.cfg.Lang)+"/"+c.kind] = true
		}
		for lang, cfg := range registrySnapshot() {
			for _, kind := range cfg.OpaqueTextKinds {
				key := string(lang) + "/" + kind
				assert.Truef(t, covered[key],
					"%s declares opaque kind %q with no regression row in opaqueLangCases — the declaration would rest on the census alone", lang, kind)
			}
		}
	})

	for _, c := range opaqueLangCases {
		t.Run(c.name, func(t *testing.T) {
			require.Containsf(t, c.cfg.OpaqueTextKinds, c.kind,
				"%s does not declare %q as an opaque kind — this row would be testing the ordinary child walk", c.cfg.Lang, c.kind)

			// The known positive runs FIRST and with require, so a row whose pattern
			// reaches nothing fails here saying so, instead of sailing through the
			// differing-target assertion as a vacuous zero.
			hit := runLongTailWalker(t, c.cfg, c.pattern, c.matching)
			require.Lenf(t, hit, 1,
				"known positive: pattern %q must reach exactly one site in the matching target, or the zero below proves nothing", c.pattern)

			miss := runLongTailWalker(t, c.cfg, c.pattern, c.differing)
			assert.Emptyf(t, miss,
				"pattern %q must NOT match a target differing only inside the %s — a match means those content bytes were never compared",
				c.pattern, c.kind)
		})
	}
}
