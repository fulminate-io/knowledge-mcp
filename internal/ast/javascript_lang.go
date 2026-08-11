// SPDX-License-Identifier: Apache-2.0

// javascript_lang.go — JavaScript LangConfig + init-time registration. JS
// shares most of its surface with TypeScript but the tree-sitter grammar
// is a distinct registry entry (LangJavaScript), so the two ship as
// separate LangConfigs. The wrapper shapes are identical (JS is a strict
// subset of TS for the constructs the engine wraps) but they are NOT
// dynamically aliased — registering one config under both languages
// would couple unrelated registries and obscure per-language tuning.
//
// Reserved prefix and identifier rule mirror tsLangConfig. See
// typescript_lang.go for the rationale shared across both, including the
// member wrapper's class body.
//
// IDENTICAL WRAPPER TEXT, DIFFERENT REACH. JS registers the same four
// wrappers as TS, but the member one hosts a narrower set of shapes:
// a bare call signature inside a JS class body is a parse ERROR (JS has no
// method signatures — a method needs a body), while the same text in TS and
// TSX is a `method_signature`. So the member wrapper contributes a candidate
// for JS field and method-definition shapes and none for the bare-call shape.

package ast

import (
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// jsLangConfig is the registered LangConfig for JavaScript.
var jsLangConfig = LangConfig{
	Lang:     treesitter.LangJavaScript,
	Reserved: "__META_AST_",
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "function __metaWrapper__() {\n", Suffix: "\n}\n"},
		{Name: "expr", Context: contextExpr, Prefix: "const __metaValue__ = ", Suffix: ";\n"},
		{Name: "member", Context: contextMember, Prefix: "class __MetaWrapper__ {\n", Suffix: "\n}\n"},
	},
	CommentKinds: []string{"comment"},
	IdentRule:    isJSIdent,
	IsTestFile:   isJSTestFile,
	// The grammar accepts JSX (this is what .jsx files parse under) and
	// absorbs newline-bearing inter-child whitespace into the following node's
	// leading anonymous token exactly as tsx does.
	TrimsAnonTokenWhitespace: true,
}

// isJSTestFile reports whether a repo-relative path is test source under the
// JavaScript-family convention, and is shared by the JS, TS and TSX configs the
// way isJSIdent is — one ecosystem, one set of spellings. Jest, Vitest and Mocha
// all key on the same three: a .test. or .spec. infix in the file name, or a
// __tests__ directory.
//
// Both legs earn their place, in opposite corpora. Measured on js-react: 2020
// files sit under __tests__/ and only 9 carry the .test./.spec. infix. On
// ts-vscode the split inverts — 1030 .test.ts files and no __tests__ directory
// at all. Either leg alone would report one of those repos as having no tests.
func isJSTestFile(rel string) bool {
	if hasPathSegment(rel, "__tests__") {
		return true
	}
	base := pathBase(rel)
	return strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
}

func init() {
	registerLangConfig(jsLangConfig)
}
