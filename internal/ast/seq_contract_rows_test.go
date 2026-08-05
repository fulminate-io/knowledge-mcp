// SPDX-License-Identifier: Apache-2.0

// seq_contract_rows_test.go — the rows of the $$$SEQ contract table. The row
// shape, the contract it encodes and the inverted xfail semantics are all
// documented in seq_contract_test.go, which holds the runner.
//
// Split from the runner only to keep both files inside the package's
// context-optimization line cap. The row set is never shrunk to fit.

package ast

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

var seqContractRows = []seqContractRow{
	// ---- C statement body -------------------------------------------------
	// The trailing semicolon in the pattern is LOAD-BEARING: it is what
	// interposes the expression_statement wrapper between the compound
	// statement and the substituted identifier, which is the whole reason
	// this position lands in the correct-sibling regime. Dropping it does not
	// change the regime — it fails to COMPILE, with a MISSING ";" error under
	// both the declaration and statement wrappers. Do not simplify these rows.
	{
		name:     "c_body_semicolon_arity0",
		lang:     treesitter.LangC,
		cfg:      cLangConfig,
		pattern:  "void $N() { $$$B; }",
		source:   "void f() { }\n",
		capture:  "B",
		wantText: "",
	},
	{
		name:           "c_body_semicolon_arity1",
		lang:           treesitter.LangC,
		cfg:            cLangConfig,
		pattern:        "void $N() { $$$B; }",
		source:         "void f() { int a; }\n",
		capture:        "B",
		wantText:       "int a;",
		wantChildKinds: []string{"declaration"},
	},
	{
		name:           "c_body_semicolon_arity2",
		lang:           treesitter.LangC,
		cfg:            cLangConfig,
		pattern:        "void $N() { $$$B; }",
		source:         "void f() { int a; int b; }\n",
		capture:        "B",
		wantText:       "int a; int b;",
		wantChildKinds: []string{"declaration", "declaration"},
	},
	{
		name:           "c_body_semicolon_arity3",
		lang:           treesitter.LangC,
		cfg:            cLangConfig,
		pattern:        "void $N() { $$$B; }",
		source:         "void f() { int a; int b; int c; }\n",
		capture:        "B",
		wantText:       "int a; int b; int c;",
		wantChildKinds: []string{"declaration", "declaration", "declaration"},
	},

	// ---- Go parameters ----------------------------------------------------
	// Go is NOT privileged as a language. Its declaration positions interpose
	// exactly one wrapper, which is what puts them in the correct regime; its
	// call-argument position (below) is broken exactly as Python's is. The
	// guard is scoped per POSITION, never stated as "Go is safe".
	{
		name:     "go_params_arity0",
		lang:     treesitter.LangGo,
		cfg:      goLangConfig,
		pattern:  "func $N($$$P) { $$$B }",
		source:   "package p\nfunc zero() {}\n",
		capture:  "P",
		wantText: "",
	},
	{
		name:           "go_params_arity1",
		lang:           treesitter.LangGo,
		cfg:            goLangConfig,
		pattern:        "func $N($$$P) { $$$B }",
		source:         "package p\nfunc one(a int) {}\n",
		capture:        "P",
		wantText:       "a int",
		wantChildKinds: []string{"parameter_declaration"},
	},
	{
		name:           "go_params_arity2",
		lang:           treesitter.LangGo,
		cfg:            goLangConfig,
		pattern:        "func $N($$$P) { $$$B }",
		source:         "package p\nfunc two(a int, b string) {}\n",
		capture:        "P",
		wantText:       "a int, b string",
		wantChildKinds: []string{"parameter_declaration", "parameter_declaration"},
	},

	// ---- Go statement body ------------------------------------------------
	{
		name:     "go_body_arity0",
		lang:     treesitter.LangGo,
		cfg:      goLangConfig,
		pattern:  "func $N($$$P) { $$$B }",
		source:   "package p\nfunc zero() {}\n",
		capture:  "B",
		wantText: "",
	},
	{
		name:           "go_body_arity1",
		lang:           treesitter.LangGo,
		cfg:            goLangConfig,
		pattern:        "func $N($$$P) { $$$B }",
		source:         "package p\nfunc one() { x() }\n",
		capture:        "B",
		wantText:       "x()",
		wantChildKinds: []string{"expression_statement"},
	},
	{
		name:           "go_body_arity2",
		lang:           treesitter.LangGo,
		cfg:            goLangConfig,
		pattern:        "func $N($$$P) { $$$B }",
		source:         "package p\nfunc two() { x(); y() }\n",
		capture:        "B",
		wantText:       "x(); y()",
		wantChildKinds: []string{"expression_statement", "expression_statement"},
	},

	// ---- C# parameters ----------------------------------------------------
	{
		name:     "csharp_params_arity0",
		lang:     treesitter.LangCSharp,
		cfg:      csharpLangConfig,
		pattern:  "void $N($$$P) { }",
		source:   "class C { void zero() { } }\n",
		capture:  "P",
		wantText: "",
	},
	{
		name:           "csharp_params_arity1",
		lang:           treesitter.LangCSharp,
		cfg:            csharpLangConfig,
		pattern:        "void $N($$$P) { }",
		source:         "class C { void one(int a) { } }\n",
		capture:        "P",
		wantText:       "int a",
		wantChildKinds: []string{"parameter"},
	},
	{
		name:           "csharp_params_arity2",
		lang:           treesitter.LangCSharp,
		cfg:            csharpLangConfig,
		pattern:        "void $N($$$P) { }",
		source:         "class C { void two(int a, int b) { } }\n",
		capture:        "P",
		wantText:       "int a, int b",
		wantChildKinds: []string{"parameter", "parameter"},
	},

	// ---- Groovy parameters ------------------------------------------------
	// Groovy shows three regimes in ONE grammar, because the regime is a
	// property of the POSITION, not of the language: its parameter position
	// interposes a wrapper and binds correctly, while its class-body position
	// (below) does not. These rows also pin the absence of the Ruby-style
	// greedy spill at the parameter position.
	{
		name:     "groovy_params_arity0",
		lang:     treesitter.LangGroovy,
		cfg:      groovyLangConfig,
		pattern:  "def $N($$$P) { }",
		source:   "def zero() { }\n",
		capture:  "P",
		wantText: "",
	},
	{
		name:           "groovy_params_arity1",
		lang:           treesitter.LangGroovy,
		cfg:            groovyLangConfig,
		pattern:        "def $N($$$P) { }",
		source:         "def one(a) { }\n",
		capture:        "P",
		wantText:       "a",
		wantChildKinds: []string{"parameter"},
	},
	{
		name:           "groovy_params_arity2",
		lang:           treesitter.LangGroovy,
		cfg:            groovyLangConfig,
		pattern:        "def $N($$$P) { }",
		source:         "def two(a, b) { }\n",
		capture:        "P",
		wantText:       "a, b",
		wantChildKinds: []string{"parameter", "parameter"},
	},

	// ---- Ruby: parameters and body must not collide -----------------------
	// Two rows over ONE pattern and ONE source, because what they pin is a
	// RELATIONSHIP between the two captures: a parameter capture greedy enough
	// to swallow the body would leave the body capture empty, and asserting
	// only one of them cannot see that. Both of Ruby's containers here are
	// field-named slots — `parameters` and `body` — so each sequence resolves
	// inside its own container and neither can reach the other's siblings.
	{
		name:           "ruby_method_no_spill_params",
		lang:           treesitter.LangRuby,
		cfg:            rubyLangConfig,
		pattern:        "def $N($$$P)\n  $$$B\nend",
		source:         "def m(a)\n  x\nend\n",
		capture:        "P",
		wantText:       "a",
		wantChildKinds: []string{"identifier"},
	},
	{
		name:           "ruby_method_no_spill_body",
		lang:           treesitter.LangRuby,
		cfg:            rubyLangConfig,
		pattern:        "def $N($$$P)\n  $$$B\nend",
		source:         "def m(a)\n  x\nend\n",
		capture:        "B",
		wantText:       "x",
		wantChildKinds: []string{"identifier"},
	},

	// ---- Python def body --------------------------------------------------
	// Arity 1 wears a disguise: a single-bind of the body descends the target
	// block to its only statement and reports the capture a correct engine
	// would. Arity 2 is what separates them, and it is the row that fails the
	// moment the body sequence stops ranging over the block's children. There
	// is no arity-0 row here because Python's grammar admits no zero-statement
	// suite — `def f():` with an empty body is not parseable source, so the
	// cell does not exist.
	{
		name:           "py_def_body_arity1",
		lang:           treesitter.LangPython,
		cfg:            pythonLangConfig,
		pattern:        "def $N():\n    $$$B",
		source:         "def f():\n    pass\n",
		capture:        "B",
		wantText:       "pass",
		wantChildKinds: []string{"pass_statement"},
	},
	{
		name:           "py_def_body_arity2",
		lang:           treesitter.LangPython,
		cfg:            pythonLangConfig,
		pattern:        "def $N():\n    $$$B",
		source:         "def f():\n    a()\n    b()\n",
		capture:        "B",
		wantText:       "a()\n    b()",
		wantChildKinds: []string{"expression_statement", "expression_statement"},
	},

	// ---- bash function body -----------------------------------------------
	// The arity-2 row is the one that fails hardest under a single-bind: a
	// sequence that consumes exactly one sibling cannot align a two-statement
	// body at all, so the row goes from a wrong capture to no match. bash also
	// carries the deepest measured chain — compound_statement → command →
	// command_name → word — so it is what proves the chain walk is not
	// depth-capped.
	{
		name:           "bash_body_arity1",
		lang:           treesitter.LangBash,
		cfg:            bashLangConfig,
		pattern:        "$N() { $$$B; }",
		source:         "f() { echo hi; }\n",
		capture:        "B",
		wantText:       "echo hi",
		wantChildKinds: []string{"command"},
	},
	{
		name:           "bash_body_arity2",
		lang:           treesitter.LangBash,
		cfg:            bashLangConfig,
		pattern:        "$N() { $$$B; }",
		source:         "f() { echo hi; echo bye; }\n",
		capture:        "B",
		wantText:       "echo hi; echo bye",
		wantChildKinds: []string{"command", "command"},
	},

	// ---- container positions ----------------------------------------------
	// Every row below sits one named child below a container the target
	// carries in its own right — an argument list, a Rust block, a Groovy
	// class body. Promoting that container to the sequence shadow is what
	// captured it whole, delimiters included; each row asserts the capture
	// holds the container's CONTENTS and no bracket of its own.
	{
		name:           "go_call_args",
		lang:           treesitter.LangGo,
		cfg:            goLangConfig,
		pattern:        "handler($$$A)",
		source:         "package p\nfunc f() { handler(alpha, beta) }\n",
		capture:        "A",
		wantText:       "alpha, beta",
		wantChildKinds: []string{"identifier", "identifier"},
	},
	{
		name:           "py_call_args_arity2",
		lang:           treesitter.LangPython,
		cfg:            pythonLangConfig,
		pattern:        "handler($$$A)",
		source:         "handler(alpha, beta)\n",
		capture:        "A",
		wantText:       "alpha, beta",
		wantChildKinds: []string{"identifier", "identifier"},
	},
	{
		name:     "py_call_args_arity0",
		lang:     treesitter.LangPython,
		cfg:      pythonLangConfig,
		pattern:  "handler($$$A)",
		source:   "handler()\n",
		capture:  "A",
		wantText: "",
	},
	{
		name:           "rust_block",
		lang:           treesitter.LangRust,
		cfg:            rustLangConfig,
		pattern:        "fn $N() { $$$B }",
		source:         "fn f() { let a = 1; }\n",
		capture:        "B",
		wantText:       "let a = 1;",
		wantChildKinds: []string{"let_declaration"},
	},
	{
		name:           "groovy_class_body",
		lang:           treesitter.LangGroovy,
		cfg:            groovyLangConfig,
		pattern:        "class $N { $$$B }",
		source:         "class C { def m() { } }\n",
		capture:        "B",
		wantText:       "def m() { }",
		wantChildKinds: []string{"function_definition"},
	},
}
