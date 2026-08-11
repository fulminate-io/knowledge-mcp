// SPDX-License-Identifier: Apache-2.0

// typescript_lang.go — TypeScript LangConfig + init-time registration. TS
// is essentially JS plus types — the tree-sitter grammar is distinct from
// the JavaScript grammar (typescript vs javascript registry entries) so
// the engine ships them as separate LangConfigs.
//
// Reserved prefix `__META_AST_` produces valid TS identifiers (TS accepts
// ASCII letters / digits / underscores; the substituted form
// `__META_AST_<NAME>__<i>` is a legal identifier and `$`-prefixed names
// are intentionally avoided because `$` is also the placeholder marker in
// the user-facing DSL).
//
// Wrapper order (declaration → statement → expression → member).
// Declaration is listed first because TS module-level forms (function
// declarations, classes, interfaces, type aliases, imports) parse cleanly
// as bare top-level fragments. Statement wraps inside a function so
// patterns like `for ($X of $Y) { $$$BODY }` parse as a
// `for_in_statement`. Expression wraps as a const assignment so bare
// expressions like `($$$ARGS) => $BODY` (arrow function expression) parse
// as an `arrow_function`. Member wraps in a class body, which is the only
// context that accepts class-only spellings such as
// `private readonly $N: $T;` — without it no TS class member is
// expressible at all.
//
// The order no longer decides which wrapper a pattern compiles under —
// compilation unions every wrapper that HOSTS the pattern — but it does
// decide candidate order, and member is listed last so the primary stamp
// on every pre-existing pattern stays where it was.
//
// Class members reach the compiler's trailing-separator absorption rule:
// tree-sitter-typescript keeps a member's `;` in the class_body list
// rather than inside the member node, so the hosting root ends one token
// short of what the caller wrote.

package ast

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// tsLangConfig is the registered LangConfig for TypeScript.
var tsLangConfig = LangConfig{
	Lang:     treesitter.LangTypeScript,
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
}

// tsxLangConfig is the registered LangConfig for the JSX-capable TypeScript
// dialect (.tsx). It mirrors tsLangConfig in every field except Lang — the
// tsx grammar is a strict superset of typescript, so the same wrapper order
// and identifier rule apply. A distinct config is required because the DSL
// engine keys LangConfigs by treesitter.Language: without it, langConfigFor
// (lang_config.go:91) misses for LangTSX and ast match/replace/where on tsx
// error out even though Phase 1 registration already resolves the grammar
// (which alone is enough for explain/list_node_kinds, not pattern support).
var tsxLangConfig = LangConfig{
	Lang:     treesitter.LangTSX,
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
	// The JSX half of the grammar absorbs newline-bearing inter-child
	// whitespace into the following node's leading anonymous token, which is
	// the one field where tsx does NOT mirror tsLangConfig: plain typescript
	// has no JSX and nothing to absorb.
	TrimsAnonTokenWhitespace: true,
}

// isJSIdent reports whether s is a valid ASCII JavaScript / TypeScript
// identifier (the subset the engine generates for substituted
// placeholders). The full JS identifier grammar accepts broader Unicode
// plus `$`, but the engine only emits ASCII identifiers built from the
// reserved prefix + capture name + occurrence index, so the ASCII subset
// suffices for IdentRule validation.
func isJSIdent(s string) bool { return asciiGoIdent(s) }

func init() {
	registerLangConfig(tsLangConfig)
	registerLangConfig(tsxLangConfig)
}
