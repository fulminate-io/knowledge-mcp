// SPDX-License-Identifier: Apache-2.0

// python_lang.go — Python LangConfig + init-time registration. Python is
// a significant-whitespace language, so the statement wrapper must produce
// a syntactically valid suite (one indented line) when the user-supplied
// pattern is a single statement at column 0. Multi-line statement patterns
// rely on the user supplying their own internal indentation; the wrapper's
// 4-space prefix indents only the first physical line.
//
// Reserved prefix `__META_AST_` produces valid Python identifiers (Python
// allows ASCII letters / digits / underscores in identifiers, and the
// substituted form `__META_AST_<NAME>__<i>` is a legal identifier).
//
// Wrapper order (declaration → statement → expression) mirrors the Go
// reference. Declaration is tried first because Python module-level forms
// (function_definition / class_definition / import_statement) are fully
// valid as bare top-level fragments. Statement wraps inside a function so
// patterns like `with $X as $Y: $$$BODY` parse as a `with_statement`.
// Expression wraps as an assignment RHS so bare expressions like
// `$LIST[$INDEX]` parse as a `subscript`.

package ast

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// pythonLangConfig is the registered LangConfig for Python.
var pythonLangConfig = LangConfig{
	Lang:     treesitter.LangPython,
	Reserved: "__META_AST_",
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "def __meta_ast_wrapper__():\n    ", Suffix: "\n"},
		{Name: "expr", Prefix: "__meta_ast_value__ = ", Suffix: "\n"},
	},
	IdentRule: isPythonIdent,
}

// isPythonIdent reports whether s is a valid ASCII Python identifier (the
// subset the engine generates for substituted placeholders). Python's
// formal identifier grammar allows broader Unicode, but the engine only
// emits ASCII identifiers built from the reserved prefix + capture name +
// occurrence index, so the ASCII subset suffices for IdentRule validation.
func isPythonIdent(s string) bool { return asciiGoIdent(s) }

func init() {
	registerLangConfig(pythonLangConfig)
}
