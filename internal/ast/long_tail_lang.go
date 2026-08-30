// SPDX-License-Identifier: Apache-2.0

// long_tail_lang.go — LangConfig registrations for the 16 long-tail languages
// (Phase D). Each language gets a default LangConfig with the reserved prefix
// "__META_AST_" and a small ordered set of context wrappers tuned to the
// language's parse rules.
//
// The wrapper templates are starting points iterated against smoke tests in
// long_tail_*_test.go. Per the validation contract (item 7), only Java fn
// decl, Ruby method def, and Bash function need to match a generic pattern;
// the remaining 13 are best-effort. When a wrapper iteration didn't converge
// for a given language, the smoke test for that language is t.Skip()'d with
// a finding link explaining why.
//
// Languages registered here (15):
//   bash, c, cpp, csharp, elixir, elm, groovy, java, kotlin, lua, ocaml,
//   ruby, scala, swift, zig — note: zig is not in treesitter.Language as of
//   Phase D, so it is omitted.
//
// Languages NOT registered (12) — see deniedLanguages in lang_config.go:
//   yaml, toml, css, html, sql, dockerfile, cue, svelte, markdown, protobuf,
//   hcl, php. Compile() returns errLanguageNotSupported for these. php is
//   denied for a DIFFERENT reason than the markup set — a sigil collision
//   (a PHP variable uses the same `$` the pattern DSL reserves), not grammar
//   shallowness.

package ast

import (
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// reservedPrefix is the shared placeholder-substitution prefix for the
// long-tail languages. The substituted form `__META_AST_<NAME>__<i>` is a
// legal identifier in every language registered below EXCEPT Elm, which
// overrides it — see elmLangConfig. A language whose identifier grammar
// rejects this spelling sets its own Reserved rather than changing this
// constant, which the other fourteen configs share.
const reservedPrefix = "__META_AST_"

// elmReservedPrefix is Elm's placeholder-substitution prefix. Elm identifiers
// may not begin with an underscore, so the shared reservedPrefix makes EVERY
// substituted Elm pattern malformed before any wrapper is tried: the leading
// underscore is read as Elm's anything-pattern and the parse fails with an
// ERROR node. The lowercase spelling substitutes to a plain
// lower_case_identifier, which is what a value position expects.
const elmReservedPrefix = "metaAstReserved"

// underTestSourceSet reports whether rel lives in a JVM TEST SOURCE SET: a
// `src` segment followed immediately by a segment whose name contains "test",
// case-insensitively. It is the shared IsTestFile rule for Java, Kotlin, Scala
// and Groovy, all four of which inherit the Maven/Gradle directory layout where
// the source set — not the file name — decides what is compiled as a test.
//
// The "contains test" spelling rather than a literal src/test is what the
// corpora forced: Gradle's own build (groovy-gradle) puts 5902 of its 6417
// Groovy files under source sets like src/integTest and src/crossVersionTest
// against 2371 under a plain src/test, and kt-okhttp's multiplatform layout uses
// src/jvmTest and src/commonTest. A literal src/test/ rule would call the
// majority of both repos' test code production source.
func underTestSourceSet(rel string) bool {
	rest := rel
	for {
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			return false
		}
		if rest[:i] == "src" {
			next := rest[i+1:]
			if seg, _, found := strings.Cut(next, "/"); found && strings.Contains(strings.ToLower(seg), "test") {
				return true
			}
		}
		rest = rest[i+1:]
	}
}

// bashLangConfig — Bash. Statement wrapper uses a function body; declaration
// wrapper is empty (top-level commands parse as program children).
var bashLangConfig = LangConfig{
	Lang:     treesitter.LangBash,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "function __meta_wrapper__() {\n", Suffix: "\n}\n"},
	},
	CommentKinds: []string{"comment"},
	// A heredoc body is a leaf until it contains an expansion; once it does, the
	// expansion becomes its only child and every surrounding line of the document
	// falls into a span gap. Comparing the body whole is what makes a heredoc
	// written into a pattern mean its own text. A `$` meant literally inside one is
	// written `$$`, the same escape every other literal dollar uses.
	OpaqueTextKinds: []string{"heredoc_body"},
	// IsTestFile is NIL: shell has no test runner that selects files by name.
	// Measured on bash-ohmyzsh, the whole repo carries three test files under
	// two different spellings — lib/tests/cli.test.zsh and
	// plugins/alias-finder/tests/test_run.sh — which is an ad-hoc habit rather
	// than a convention a filter could rely on.
}

// cLangConfig — C. Declaration first (typedef/struct parse top-level), then
// statement (inside a function body), then expression on the right of an
// initializer — without that last one a bare expression, including the `$_`
// wildcard diagnostic, has no context to parse in at all.
var cLangConfig = LangConfig{
	Lang:     treesitter.LangC,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "void __meta_wrapper__() {\n", Suffix: "\n}\n"},
		{Name: "expr", Context: contextExpr, Prefix: "void __meta_wrapper__() {\n  int __metaValue__ = ", Suffix: ";\n}\n"},
	},
	CommentKinds: []string{"comment"},
	// IsTestFile is NIL: C's harnesses (CUnit, Unity, Check, a plain main) are
	// wired by the build, and each names files its own way. Measured on c-redis,
	// no .c file outside vendored deps/ carries "test" in its path at all — the
	// suite is driven from tests/ in another language entirely.
}

// cppLangConfig — C++. Shares C's declaration and statement wrappers;
// tree-sitter-cpp accepts both surface forms, and C++-specific shapes
// (template, namespace) parse top-level.
//
// The member wrapper is what makes a real class member reachable, and the
// gap it closes is a WRONG ROOT rather than a failed parse: tree-sitter-cpp
// reads member text such as `$T $N;` — and even class-only spellings like
// `virtual int f();` — as a plain `declaration` at translation-unit scope
// with no error, so the declaration wrapper already hosts it under a kind no
// class member carries. Only a class body yields `field_declaration`. The
// Suffix closes with `};` because C++ requires the semicolon after a class
// definition; C does not, and C registers no member wrapper.
var cppLangConfig = LangConfig{
	Lang:     treesitter.LangCPP,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "void __meta_wrapper__() {\n", Suffix: "\n}\n"},
		{Name: "member", Context: contextMember, Prefix: "class __MetaWrapper__ {\n", Suffix: "\n};\n"},
		{Name: "expr", Context: contextExpr, Prefix: "void __meta_wrapper__() {\n  int __metaValue__ = ", Suffix: ";\n}\n"},
	},
	CommentKinds: []string{"comment"},
	// IsTestFile is NIL, for C's reason plus one of its own: GoogleTest and
	// Catch2 register cases by macro, so nothing requires the *_test.cc spelling
	// their docs suggest. Measured on cpp-json, zero files use it — the suite
	// lives under tests/ as config.cpp, diag.cpp and friends.
}

// csharpLangConfig — C#. Class scope is required for methods and statements.
// The wrapper NAMED "decl" is a class body, so its Context is member, not
// decl; the empty-prefix wrapper named "top" is the real declaration context.
// The Names are left as they are because compile-failure messages list them.
//
// The expression wrapper puts the fragment on the right of a `var`
// initializer, which is what makes a bare expression — including the `$_`
// wildcard diagnostic — expressible. It cannot widen a statement-shaped
// pattern: `$A = $B;` substituted there leaves its own `;` inside the
// initializer slot, and while that parses ERROR-free (the second `;` is an
// empty statement) the resulting root spans wrapper bytes, so the hosting test
// rejects it and it never becomes a candidate.
var csharpLangConfig = LangConfig{
	Lang:     treesitter.LangCSharp,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextMember, Prefix: "class __MetaWrapper__ {\n", Suffix: "\n}\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "class __MetaWrapper__ {\n  void M() {\n", Suffix: "\n  }\n}\n"},
		{Name: "top", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "expr", Context: contextExpr, Prefix: "class __MetaWrapper__ {\n  void M() {\n    var __metaValue__ = ", Suffix: ";\n  }\n}\n"},
	},
	CommentKinds: []string{"comment"},
	// An interpolation's format clause — the `:yyyyMMdd` half of `{when:yyyyMMdd}`
	// — carries only its `:` as a child and leaves the format string itself in a
	// span gap, so every format clause matched every other. C#'s ordinary and
	// verbatim strings cover their content with children and are not affected.
	OpaqueTextKinds: []string{"interpolation_format_clause"},
	// IsTestFile is NIL: `dotnet test` discovers tests by ATTRIBUTE inside a
	// test-project assembly, so no file name and no directory decides anything.
	// The *Tests.cs habit is widespread — 2007 of cs-roslyn's 13962 C# files use
	// it — but a habit no runner enforces is exactly the ambiguity this field
	// declines to guess at.
}

// elixirLangConfig — Elixir. Function body wraps inside `def`.
var elixirLangConfig = LangConfig{
	Lang:     treesitter.LangElixir,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "def __meta_wrapper__ do\n", Suffix: "\nend\n"},
	},
	CommentKinds: []string{"comment"},
	IsTestFile:   isElixirTestFile,
}

// isElixirTestFile reports whether a repo-relative path is Elixir test source.
// `mix test` compiles the .exs scripts under test/ and runs the *_test.exs
// files, so both legs come straight from the runner.
//
// The .exs requirement on the directory leg is not decoration. Measured on
// ex-phoenix, lib/phoenix/test/ holds .ex modules that are SHIPPED library code
// (Phoenix's own ConnTest helpers), so a bare "test/ segment" rule would hide
// production source behind include_tests:false. Extension and directory
// together separate the two without an exception list.
func isElixirTestFile(rel string) bool {
	if strings.HasSuffix(rel, "_test.exs") {
		return true
	}
	return strings.HasSuffix(rel, ".exs") && hasPathSegment(rel, "test")
}

// elmLangConfig — Elm. Module-level definitions parse top-level; expressions
// land on the RHS of a let binding. Elm is the one registered language that
// does not use the shared reserved prefix; elmReservedPrefix explains why.
var elmLangConfig = LangConfig{
	Lang:     treesitter.LangElm,
	Reserved: elmReservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "expr", Context: contextExpr, Prefix: "x = ", Suffix: "\n"},
	},
	CommentKinds: []string{"block_comment", "line_comment"},
	// Elm's {- -} block comment carries its two delimiter tokens as children and
	// leaves the comment text in a span gap, so one block comment matched any
	// other. Its -- line comment is childless and always constrained its text.
	OpaqueTextKinds: []string{"block_comment"},
	// IsTestFile is NIL: elm-test discovers exposed values of type Test inside
	// modules, so membership is decided by a type in the file rather than by the
	// file's name — the same in-file shape that makes Rust nil. elm-compiler
	// carries no test module at all, so there is not even a local habit to read.
}

// groovyLangConfig — Groovy. Permissive parser; declaration wrapper empty,
// statements wrap inside a script-level closure.
var groovyLangConfig = LangConfig{
	Lang:     treesitter.LangGroovy,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "def __metaWrapper__() {\n", Suffix: "\n}\n"},
	},
	CommentKinds: []string{"comment", "groovy_doc"},
	// A Groovy block comment carries its `/*` and `*/` as children and leaves the
	// comment text in a span gap, so one block comment matched any other. Its
	// groovy_doc sibling is NOT here despite also gapping: the gap there is the
	// ` * ` line decoration, while the doc's text is covered by a first_line child
	// and already constrained — measured by declaring it opaque and watching the
	// regression pair pass with the mechanism disabled. Groovy's `string` gaps only
	// on its quote delimiters and covers its value with a string_content child.
	OpaqueTextKinds: []string{"comment"},
	IsTestFile:      underTestSourceSet,
}

// javaLangConfig — Java. Class scope is required for methods; method scope
// is required for statements. As in C#, the wrapper NAMED "decl" is a class
// body and carries the member Context; the empty-prefix "top" wrapper is the
// declaration context. The Names stay put — they appear in error messages.
// The expression wrapper is the right of an `Object` initializer, for the same
// reason C# has one: without it Java can express no bare expression.
var javaLangConfig = LangConfig{
	Lang:     treesitter.LangJava,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextMember, Prefix: "class __MetaWrapper__ {\n", Suffix: "\n}\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "class __MetaWrapper__ {\n  void m() {\n", Suffix: "\n  }\n}\n"},
		{Name: "top", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "expr", Context: contextExpr, Prefix: "class __MetaWrapper__ {\n  void m() {\n    Object __metaValue__ = ", Suffix: ";\n  }\n}\n"},
	},
	CommentKinds: []string{"block_comment", "line_comment"},
	IsTestFile:   underTestSourceSet,
}

// kotlinLangConfig — Kotlin. Top-level functions parse standalone, but class
// scope is the safe default for statements.
var kotlinLangConfig = LangConfig{
	Lang:     treesitter.LangKotlin,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "class __MetaWrapper__ {\n  fun m() {\n", Suffix: "\n  }\n}\n"},
	},
	CommentKinds: []string{"line_comment", "multiline_comment"},
	// Kotlin's character literal carries only its two quote tokens and leaves the
	// character itself in a span gap, so 'a' matched 'b'. Kotlin's string_literal
	// is NOT here: it covers its value with string_content children and hosts
	// interpolated_expression, so it still compares correctly and still binds.
	OpaqueTextKinds: []string{"character_literal"},
	IsTestFile:      underTestSourceSet,
}

// luaLangConfig — Lua. Top-level chunks parse standalone; statements wrap
// inside a function.
var luaLangConfig = LangConfig{
	Lang:     treesitter.LangLua,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "function __meta_wrapper__()\n", Suffix: "\nend\n"},
	},
	CommentKinds: []string{"comment"},
	// IsTestFile is NIL: Lua ships no test runner, so any spelling belongs to a
	// third-party library rather than to the language — busted defaults to
	// *_spec.lua, luaunit to none at all. Measured on lua-openresty, no .lua file
	// carries "test" or "spec" in its path, so the corpus confirms neither.
}

// ocamlLangConfig — OCaml. Module-level let bindings parse top-level;
// expressions wrap as `let _ = <expr>`.
var ocamlLangConfig = LangConfig{
	Lang:     treesitter.LangOCaml,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "expr", Context: contextExpr, Prefix: "let _ = ", Suffix: "\n"},
	},
	CommentKinds: []string{"comment"},
	// OCaml surfaces string content as its own node, so a plain literal compared
	// correctly — but an escape inside one becomes a child of string_content and
	// splits the surrounding text into span gaps, and the same holds for the
	// {|...|} quoted form's content node. The enclosing `string` and
	// `quoted_string` are not declared: they gap only on their delimiters and
	// cover their values with these children.
	OpaqueTextKinds: []string{"quoted_string_content", "string_content"},
	// IsTestFile is NIL: dune declares tests with a (test) stanza in the build
	// file, which names an executable rather than a file-name pattern, so an .ml
	// file's own name says nothing about whether it is compiled as a test. In
	// ocaml-dune the test sources sit under bin/, bench/ and test/ alike.
}

// PHP is not registered here — it joins the deny set in lang_config.go for a
// sigil collision (a PHP variable uses the same `$` the pattern DSL reserves
// for placeholders). See deniedLanguages.

// rubyLangConfig — Ruby. Methods parse top-level; statements wrap inside a
// method definition. Ruby's grammar tolerates bare expressions at file scope
// so the declaration wrapper is empty.
var rubyLangConfig = LangConfig{
	Lang:     treesitter.LangRuby,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "def __meta_wrapper__\n", Suffix: "\nend\n"},
	},
	CommentKinds: []string{"comment"},
	IsTestFile:   isRubyTestFile,
}

// isRubyTestFile reports whether a repo-relative path is Ruby test source. Both
// of Ruby's runners select by file name: Rake's test task globs
// test/**/*_test.rb, and RSpec globs **/*_spec.rb under spec/.
//
// Measured on rb-rails, 1259 of 3391 .rb files carry the _test.rb suffix and
// none sit in a spec/ directory (Rails is a minitest codebase), so the spec legs
// are the ecosystem's other half rather than this corpus's. The spec/ segment is
// kept alongside the _spec.rb suffix because RSpec's support files —
// spec/spec_helper.rb, spec/support/** — carry no suffix of their own.
func isRubyTestFile(rel string) bool {
	base := pathBase(rel)
	return strings.HasSuffix(base, "_test.rb") || strings.HasSuffix(base, "_spec.rb") || hasPathSegment(rel, "spec")
}

// scalaLangConfig — Scala. Object/class scope wraps definitions; bare
// declarations also accept some top-level shapes.
var scalaLangConfig = LangConfig{
	Lang:     treesitter.LangScala,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "object __MetaWrapper__ {\n  def m() = {\n", Suffix: "\n  }\n}\n"},
	},
	CommentKinds: []string{"block_comment", "comment"},
	// Scala's two comment kinds leave their text in a span gap, as Rust's and
	// Groovy's do. interpolated_string is the sharper case: its LITERAL SEGMENTS
	// are gaps — only the `${...}` interpolations are children — so s"took ${n}ms"
	// matched s"lost ${n}ms". Comparing it whole is what makes those segments
	// constrain; a `$` meant literally inside one is written `$$`, and a
	// placeholder written INSIDE an interpolation is refused at compile time
	// rather than silently never matching. Scala's plain `string` is childless and
	// was always compared correctly.
	OpaqueTextKinds: []string{"block_comment", "comment", "interpolated_string"},
	IsTestFile:      underTestSourceSet,
}

// swiftLangConfig — Swift. Top-level statements parse standalone (Swift is
// script-mode at file scope), so the declaration wrapper is empty.
var swiftLangConfig = LangConfig{
	Lang:     treesitter.LangSwift,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "", Suffix: "\n"},
		{Name: "stmt", Context: contextStmt, Prefix: "func __metaWrapper__() {\n", Suffix: "\n}\n"},
	},
	CommentKinds: []string{"comment", "multiline_comment"},
	// IsTestFile is NIL: SwiftPM decides what is a test in Package.swift, where a
	// testTarget names its own path — Tests/ is only that manifest's default and
	// any package may point elsewhere. swift-vapor also ships
	// Sources/VaporTestUtils and Sources/VaporTesting, which a name-based rule
	// would hide as tests though they are part of the shipped library.
}

func init() {
	registerLangConfig(bashLangConfig)
	registerLangConfig(cLangConfig)
	registerLangConfig(cppLangConfig)
	registerLangConfig(csharpLangConfig)
	registerLangConfig(elixirLangConfig)
	registerLangConfig(elmLangConfig)
	registerLangConfig(groovyLangConfig)
	registerLangConfig(javaLangConfig)
	registerLangConfig(kotlinLangConfig)
	registerLangConfig(luaLangConfig)
	registerLangConfig(ocamlLangConfig)
	registerLangConfig(rubyLangConfig)
	registerLangConfig(scalaLangConfig)
	registerLangConfig(swiftLangConfig)
}
