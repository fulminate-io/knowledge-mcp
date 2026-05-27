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
// Wrapper order (declaration → statement → expression). Declaration is
// tried first because TS module-level forms (function declarations,
// classes, interfaces, type aliases, imports) parse cleanly as bare top-
// level fragments. Statement wraps inside a function so patterns like
// `for ($X of $Y) { $$$BODY }` parse as a `for_in_statement`. Expression
// wraps as a const assignment so bare expressions like
// `($$$ARGS) => $BODY` (arrow function expression) parse as an
// `arrow_function`.

package ast

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// tsLangConfig is the registered LangConfig for TypeScript.
var tsLangConfig = LangConfig{
	Lang:     treesitter.LangTypeScript,
	Reserved: "__META_AST_",
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "function __metaWrapper__() {\n", Suffix: "\n}\n"},
		{Name: "expr", Prefix: "const __metaValue__ = ", Suffix: ";\n"},
	},
	IdentRule: isJSIdent,
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
}
