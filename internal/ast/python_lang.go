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
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// pythonLangConfig is the registered LangConfig for Python.
var pythonLangConfig = LangConfig{
	Lang:     treesitter.LangPython,
	Reserved: "__META_AST_",
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "def __meta_ast_wrapper__():\n    ", Suffix: "\n"},
		{Name: "expr", Context: contextExpr, Prefix: "__meta_ast_value__ = ", Suffix: "\n"},
	},
	CommentKinds: []string{"comment"},
	// Python surfaces a string's content as its own string_content node, so a
	// plain literal always compared correctly — but an ESCAPE inside one becomes a
	// child of string_content, and the text either side of that child falls into a
	// span gap. `"a\nb"` and `"c\nd"` were indistinguishable. Declaring
	// string_content rather than `string` is what keeps f-string interpolation
	// working: an `interpolation` node is a SIBLING of string_content under
	// `string`, so it is still descended into and still binds placeholders.
	OpaqueTextKinds: []string{"string_content"},
	IdentRule:       isPythonIdent,
	IsTestFile:      isPythonTestFile,
}

// isPythonTestFile reports whether a repo-relative path is Python test source.
// The rule is FILENAME-ONLY — unittest discovery and pytest's default
// `python_files` both key on the test_*.py / *_test.py spellings — and
// deliberately does NOT treat a tests/ or test/ directory as test source.
// Measured on py-django: 617 files carry the filename convention, while a
// directory rule would additionally swallow django/test/ (client.py, runner.py,
// selenium.py …), which is Django's SHIPPED testing library rather than its test
// suite. Hiding shipped source behind include_tests:false is the more expensive
// error, so the directory leg is left out.
func isPythonTestFile(rel string) bool {
	base := pathBase(rel)
	return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")
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
