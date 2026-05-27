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
// Languages registered here (16):
//   bash, c, cpp, csharp, elixir, elm, groovy, java, kotlin, lua, ocaml, php,
//   ruby, scala, swift, zig — note: zig is not in treesitter.Language as of
//   Phase D, so it is omitted.
//
// Languages NOT registered (11) — see deniedLanguages in lang_config.go:
//   yaml, toml, css, html, sql, dockerfile, cue, svelte, markdown, protobuf,
//   hcl. Compile() returns errLanguageNotSupported for these.

package ast

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// reservedPrefix is the shared placeholder-substitution prefix for all
// long-tail languages. The substituted form `__META_AST_<NAME>__<i>` is a
// legal identifier in every language registered below.
const reservedPrefix = "__META_AST_"

// bashLangConfig — Bash. Statement wrapper uses a function body; declaration
// wrapper is empty (top-level commands parse as program children).
var bashLangConfig = LangConfig{
	Lang:     treesitter.LangBash,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "function __meta_wrapper__() {\n", Suffix: "\n}\n"},
	},
}

// cLangConfig — C. Declaration first (typedef/struct parse top-level), then
// statement (inside a function body).
var cLangConfig = LangConfig{
	Lang:     treesitter.LangC,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "void __meta_wrapper__() {\n", Suffix: "\n}\n"},
	},
}

// cppLangConfig — C++. Same wrappers as C; tree-sitter-cpp accepts both
// surface forms. C++-specific shapes (template, namespace) parse top-level.
var cppLangConfig = LangConfig{
	Lang:     treesitter.LangCPP,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "void __meta_wrapper__() {\n", Suffix: "\n}\n"},
	},
}

// csharpLangConfig — C#. Class scope is required for methods and statements.
var csharpLangConfig = LangConfig{
	Lang:     treesitter.LangCSharp,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "class __MetaWrapper__ {\n", Suffix: "\n}\n"},
		{Name: "stmt", Prefix: "class __MetaWrapper__ {\n  void M() {\n", Suffix: "\n  }\n}\n"},
		{Name: "top", Prefix: "", Suffix: "\n"},
	},
}

// elixirLangConfig — Elixir. Function body wraps inside `def`.
var elixirLangConfig = LangConfig{
	Lang:     treesitter.LangElixir,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "def __meta_wrapper__ do\n", Suffix: "\nend\n"},
	},
}

// elmLangConfig — Elm. Module-level definitions parse top-level; expressions
// land on the RHS of a let binding.
var elmLangConfig = LangConfig{
	Lang:     treesitter.LangElm,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "expr", Prefix: "x = ", Suffix: "\n"},
	},
}

// groovyLangConfig — Groovy. Permissive parser; declaration wrapper empty,
// statements wrap inside a script-level closure.
var groovyLangConfig = LangConfig{
	Lang:     treesitter.LangGroovy,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "def __metaWrapper__() {\n", Suffix: "\n}\n"},
	},
}

// javaLangConfig — Java. Class scope is required for methods; method scope
// is required for statements.
var javaLangConfig = LangConfig{
	Lang:     treesitter.LangJava,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "class __MetaWrapper__ {\n", Suffix: "\n}\n"},
		{Name: "stmt", Prefix: "class __MetaWrapper__ {\n  void m() {\n", Suffix: "\n  }\n}\n"},
		{Name: "top", Prefix: "", Suffix: "\n"},
	},
}

// kotlinLangConfig — Kotlin. Top-level functions parse standalone, but class
// scope is the safe default for statements.
var kotlinLangConfig = LangConfig{
	Lang:     treesitter.LangKotlin,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "class __MetaWrapper__ {\n  fun m() {\n", Suffix: "\n  }\n}\n"},
	},
}

// luaLangConfig — Lua. Top-level chunks parse standalone; statements wrap
// inside a function.
var luaLangConfig = LangConfig{
	Lang:     treesitter.LangLua,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "function __meta_wrapper__()\n", Suffix: "\nend\n"},
	},
}

// ocamlLangConfig — OCaml. Module-level let bindings parse top-level;
// expressions wrap as `let _ = <expr>`.
var ocamlLangConfig = LangConfig{
	Lang:     treesitter.LangOCaml,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "expr", Prefix: "let _ = ", Suffix: "\n"},
	},
}

// phpLangConfig — PHP. Source files require the `<?php` tag for any code.
var phpLangConfig = LangConfig{
	Lang:     treesitter.LangPHP,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "<?php\n", Suffix: "\n"},
		{Name: "stmt", Prefix: "<?php\nfunction __meta_wrapper__() {\n", Suffix: "\n}\n"},
	},
}

// rubyLangConfig — Ruby. Methods parse top-level; statements wrap inside a
// method definition. Ruby's grammar tolerates bare expressions at file scope
// so the declaration wrapper is empty.
var rubyLangConfig = LangConfig{
	Lang:     treesitter.LangRuby,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "def __meta_wrapper__\n", Suffix: "\nend\n"},
	},
}

// scalaLangConfig — Scala. Object/class scope wraps definitions; bare
// declarations also accept some top-level shapes.
var scalaLangConfig = LangConfig{
	Lang:     treesitter.LangScala,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "object __MetaWrapper__ {\n  def m() = {\n", Suffix: "\n  }\n}\n"},
	},
}

// swiftLangConfig — Swift. Top-level statements parse standalone (Swift is
// script-mode at file scope), so the declaration wrapper is empty.
var swiftLangConfig = LangConfig{
	Lang:     treesitter.LangSwift,
	Reserved: reservedPrefix,
	Wrappers: []ContextWrapper{
		{Name: "decl", Prefix: "", Suffix: "\n"},
		{Name: "stmt", Prefix: "func __metaWrapper__() {\n", Suffix: "\n}\n"},
	},
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
	registerLangConfig(phpLangConfig)
	registerLangConfig(rubyLangConfig)
	registerLangConfig(scalaLangConfig)
	registerLangConfig(swiftLangConfig)
}
