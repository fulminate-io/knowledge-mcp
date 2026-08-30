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
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "fn __meta_wrapper__() {\n", Suffix: "\n}\n"},
		{Name: "expr", Context: contextExpr, Prefix: "fn __meta_wrapper__() {\n    let _ = ", Suffix: ";\n}\n"},
	},
	CommentKinds: []string{"block_comment", "line_comment"},
	// Rust's comment kinds carry their `//` and `/* */` markers as children and
	// leave the comment TEXT in a span gap, so a comment written into a pattern
	// matched any comment at all. Go's `comment` is childless and has always
	// constrained its text; declaring these two makes Rust agree. Rust's string
	// literals are NOT here: raw_string_literal gaps only on its `r"` / `"#`
	// delimiters and covers its value with a string_content child, which both
	// compares correctly today and can host a placeholder.
	OpaqueTextKinds: []string{"block_comment", "line_comment"},
	IdentRule:       isRustIdent,
	// IsTestFile is NIL for Rust, and that is a decision rather than an
	// omission. Rust marks unit tests with an in-FILE `#[cfg(test)] mod tests`
	// block, so the test code sits inside ordinary source files and no filename
	// can separate it — measured on rust-tokio, 34 files carry `mod tests` and
	// they are the same .rs files that carry the implementation. Cargo's tests/
	// directory does hold integration tests, but a predicate covering only that
	// would report "this walk excluded Rust's tests" while leaving every unit
	// test in place, which is a worse claim than declining to make one. A caller
	// who asks for include_tests on Rust gets a hard error naming the language.
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
