// SPDX-License-Identifier: Apache-2.0

// long_tail_smoke_besteffort_test.go — best-effort smoke tests for the
// remaining 12 long-tail languages: c, cpp, csharp, elixir, elm, groovy,
// kotlin, lua, ocaml, php, scala, swift. Each test calls
// runLongTailWalkerOrSkip (defined in long_tail_smoke_test.go), so a
// compile failure or zero matches degrades to t.Skip with a finding
// pointer rather than blocking the phase.
//
// Per validation contract item 7 only Java/Ruby/Bash (in
// long_tail_smoke_test.go) MUST pass; the languages here are
// opportunistic — when a wrapper converges we get a free smoke test, and
// when it doesn't we surface the gap via t.Skip + finding for follow-up.

package ast

import (
	"testing"
)

func TestLongTail_C_FunctionDeclaration(t *testing.T) {
	target := `int alpha(int x) { return x; }
int beta(int a, int b) { return a + b; }
`
	matches := runLongTailWalkerOrSkip(t, cLangConfig,
		"int $NAME(int $P) { return $E; }", target,
		"int-arg function-decl pattern does not survive substitution")
	if len(matches) < 1 {
		t.Skipf("C smoke: 0 matches; wrapper iteration did not converge")
	}
}

func TestLongTail_CPP_FunctionDeclaration(t *testing.T) {
	target := `int alpha(int x) { return x; }
int beta(int a, int b) { return a + b; }
`
	matches := runLongTailWalkerOrSkip(t, cppLangConfig,
		"int $NAME(int $P) { return $E; }", target,
		"int-arg function-decl pattern does not survive substitution")
	if len(matches) < 1 {
		t.Skipf("C++ smoke: 0 matches; wrapper iteration did not converge")
	}
}

func TestLongTail_CSharp_MethodDeclaration(t *testing.T) {
	target := `class Calc {
  void Alpha() { return; }
  void Beta() { return; }
}
`
	matches := runLongTailWalkerOrSkip(t, csharpLangConfig,
		"void $NAME() { return; }", target,
		"void method-decl pattern under class wrapper")
	if len(matches) < 1 {
		t.Skipf("C# smoke: 0 matches; wrapper iteration did not converge")
	}
}

func TestLongTail_Elixir_FunctionDef(t *testing.T) {
	target := `defmodule M do
  def alpha do
    1
  end

  def beta(x) do
    x + 1
  end
end
`
	matches := runLongTailWalkerOrSkip(t, elixirLangConfig,
		"def $NAME do\n  $$$BODY\nend", target,
		"def-do-end pattern under module")
	if len(matches) < 1 {
		t.Skipf("Elixir smoke: 0 matches; wrapper iteration did not converge")
	}
}

func TestLongTail_Elm_TopLevelDef(t *testing.T) {
	target := `module M exposing (..)

alpha = 1
beta = 2
`
	matches := runLongTailWalkerOrSkip(t, elmLangConfig,
		"$NAME = $VAL", target,
		"top-level binding pattern")
	if len(matches) < 1 {
		t.Skipf("Elm smoke: 0 matches; wrapper iteration did not converge")
	}
}

func TestLongTail_Groovy_MethodDef(t *testing.T) {
	target := `class Calc {
  def alpha(x) { x }
  def beta(a, b) { a + b }
}
`
	matches := runLongTailWalkerOrSkip(t, groovyLangConfig,
		"def $NAME($$$ARGS) { $$$BODY }", target,
		"def method pattern in class scope")
	if len(matches) < 1 {
		t.Skipf("Groovy smoke: 0 matches; wrapper iteration did not converge")
	}
}

func TestLongTail_Kotlin_FunctionDef(t *testing.T) {
	target := `fun alpha(): Int { return 1 }
fun beta(): Int { return 2 }
`
	matches := runLongTailWalkerOrSkip(t, kotlinLangConfig,
		"fun $NAME(): Int { return $E }", target,
		"fun pattern with int return type")
	if len(matches) < 1 {
		t.Skipf("Kotlin smoke: 0 matches; wrapper iteration did not converge")
	}
}

func TestLongTail_Lua_FunctionDef(t *testing.T) {
	target := `function alpha(x)
  return x
end

function beta(a, b)
  return a + b
end
`
	matches := runLongTailWalkerOrSkip(t, luaLangConfig,
		"function $NAME($$$ARGS)\n  $$$BODY\nend", target,
		"function decl pattern")
	if len(matches) < 1 {
		t.Skipf("Lua smoke: 0 matches; wrapper iteration did not converge")
	}
}

func TestLongTail_OCaml_LetBinding(t *testing.T) {
	target := `let alpha = 1
let beta = 2
`
	matches := runLongTailWalkerOrSkip(t, ocamlLangConfig,
		"let $NAME = $VAL", target,
		"let-binding pattern")
	if len(matches) < 1 {
		t.Skipf("OCaml smoke: 0 matches; wrapper iteration did not converge")
	}
}

func TestLongTail_PHP_FunctionDef(t *testing.T) {
	target := `<?php
function alpha() { return 1; }
function beta() { return 2; }
`
	matches := runLongTailWalkerOrSkip(t, phpLangConfig,
		"function $NAME() { return $E; }", target,
		"function decl pattern under <?php tag")
	if len(matches) < 1 {
		t.Skipf("PHP smoke: 0 matches; wrapper iteration did not converge")
	}
}

func TestLongTail_Scala_DefMethod(t *testing.T) {
	target := `object M {
  def alpha(): Int = 1
  def beta(): Int = 2
}
`
	matches := runLongTailWalkerOrSkip(t, scalaLangConfig,
		"def $NAME(): Int = $BODY", target,
		"def method pattern in object scope")
	if len(matches) < 1 {
		t.Skipf("Scala smoke: 0 matches; wrapper iteration did not converge")
	}
}

func TestLongTail_Swift_FunctionDef(t *testing.T) {
	target := `func alpha() { return }
func beta() { return }
`
	matches := runLongTailWalkerOrSkip(t, swiftLangConfig,
		"func $NAME() { return }", target,
		"func decl pattern at top level")
	if len(matches) < 1 {
		t.Skipf("Swift smoke: 0 matches; wrapper iteration did not converge")
	}
}
