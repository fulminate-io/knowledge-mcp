// SPDX-License-Identifier: Apache-2.0

// go_lang.go — Go LangConfig + init-time registration. The Go config is
// the reference implementation; future per-language configs follow the
// same shape (Lang + Reserved + ordered Wrappers + IdentRule).
//
// Reserved prefix `__META_AST_` produces valid Go identifiers and is
// unlikely to collide with user code (the leading underscore + uppercase
// META marks it visually as engine-internal). Pattern strings substitute
// `$X` to `__META_AST_X__0` (where the trailing index disambiguates
// repeated occurrences — see engine.go::substitutePlaceholders).
//
// Wrapper order (declaration → statement → expression) per ticket
// validation contract item 3:
//
//   1. Declaration wrapper (`package p\n`) — for top-level forms like
//      `func Foo() {}` or `type X struct {}`.
//   2. Statement wrapper (`package p\nfunc _() {\n` … `\n}`) — for inside-
//      function statements like `defer x.Close()`, `for x := range y {}`.
//   3. Expression wrapper (`package p\nvar _ = ` …) — for bare expressions
//      like `make([]int, 0)` or `f(x).y`.
//
// Declaration is tried first because it accepts the broadest scope of
// pattern shapes (top-level funcs / types / consts / vars). Statement is
// next because the most common patterns are inside-body fragments.
// Expression is last because expressions are the most-permissive grammar
// — almost anything ambiguous parses as an expression somewhere, and
// preferring the more-specific contexts reduces false-positive parses.

package ast

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// goLangConfig is the registered LangConfig for Go.
var goLangConfig = LangConfig{
	Lang:     treesitter.LangGo,
	Reserved: "__META_AST_",
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "package _\n", Suffix: ""},
		{Name: "stmt", Prefix: "package _\nfunc _() {\n", Suffix: "\n}"},
		{Name: "expr", Prefix: "package _\nvar _ = ", Suffix: ""},
	},
	IdentRule: isGoIdent,
}

// isGoIdent reports whether s is a valid ASCII Go identifier (the subset
// the engine generates for substituted placeholders). Mirrors the deleted
// selector_call.go::isAsciiIdentifier shape — Go accepts unicode letters
// in user-written identifiers, but the engine only generates ASCII names
// out of the reserved prefix + capture name + occurrence index, so the
// ASCII subset suffices for IdentRule validation.
func isGoIdent(s string) bool { return asciiGoIdent(s) }

func init() {
	registerLangConfig(goLangConfig)
}
