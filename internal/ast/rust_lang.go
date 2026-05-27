// SPDX-License-Identifier: Apache-2.0

// rust_lang.go — Rust LangConfig + init-time registration.
//
// Reserved prefix `__META_AST_` produces valid Rust identifiers (Rust's
// identifier grammar accepts ASCII letters / digits / underscores; the
// substituted form `__META_AST_<NAME>__<i>` is a legal identifier).
//
// Wrapper order (declaration → statement → expression). Declaration is
// tried first because Rust top-level forms (`fn`, `impl`, `struct`,
// `trait`, `enum`, `use`) parse cleanly as bare source-file fragments.
// Statement wraps inside a function so patterns like `match $X { $$$ARMS }`
// or `println!($$$ARGS)` parse as the corresponding `*_statement` /
// `*_expression`. Expression wraps inside a `let _ = ...;` binding so
// bare expressions like `$X.unwrap()` parse as a `call_expression`. The
// wrapper deliberately uses a let-binding inside a fn body rather than a
// top-level `const _: () = ...;` because the const form is restrictive
// (requires a const-evaluable RHS) and most user expressions are not
// const-evaluable.

package ast

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// rustLangConfig is the registered LangConfig for Rust.
var rustLangConfig = LangConfig{
	Lang:     treesitter.LangRust,
	Reserved: "__META_AST_",
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "fn __meta_wrapper__() {\n", Suffix: "\n}\n"},
		{Name: "expr", Prefix: "fn __meta_wrapper__() {\n    let _ = ", Suffix: ";\n}\n"},
	},
	IdentRule: isRustIdent,
}

// isRustIdent reports whether s is a valid ASCII Rust identifier (the
// subset the engine generates for substituted placeholders). Rust's
// formal identifier grammar accepts broader Unicode plus a `r#`-prefixed
// raw-identifier form; the engine emits only ASCII identifiers built
// from the reserved prefix + capture name + occurrence index, so the
// ASCII subset suffices for IdentRule validation.
func isRustIdent(s string) bool { return asciiGoIdent(s) }

func init() {
	registerLangConfig(rustLangConfig)
}
