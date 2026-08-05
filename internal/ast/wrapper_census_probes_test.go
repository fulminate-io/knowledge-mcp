// SPDX-License-Identifier: Apache-2.0

// wrapper_census_probes_test.go — the probe table the wrapper-context census
// walks. Split from the harness in wrapper_census_test.go only to keep both
// files inside the repo's per-file line budget; the two are one subject.

package ast

import (
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// wrapperCensusCells declares one probe per (language, shape). The probes are
// chosen to be what a caller would actually WRITE for that shape in that
// language — a permissive stand-in that happens to parse in some other
// context would make the census measure the probe rather than the grammar.
//
// The member probes for the class-carrying grammars are deliberately spellings
// that only a class body accepts (TS/TSX `private readonly`, JS `static`), so
// an unreachable class body shows up as hosted=none rather than as a
// coincidental hit on the declaration wrapper.
//
// THE stmt_keyword PROBES ARE SPELLED AGAINST A RULE, not chosen freely: each
// is a statement form that cannot ALSO parse as a bare expression in its
// grammar — keyword-led wherever the grammar has such a form — and each carries
// NO block, so the sequence-shadow machinery does not confound the row with a
// second variable. That rule is what separates this class from shapeStmt, whose
// every probe is a bare call and therefore an expression in most grammars. The
// two expression-only grammars declare the shape unprobeable with the same
// reason their stmt probes already carry.
//
// THE stmt_block PROBES are the if-with-braced-body form, one keyword that is
// also a legal method name followed by a parameter-shaped parenthesis and a
// braced sequence — the ONE shape that surfaces the member-keyword ambiguity a
// keyword-only probe cannot. javascript, typescript and tsx host it under decl,
// stmt AND member (the `if` reads as a method_definition inside a class body),
// which is the pre-narrowing truth Phase 5 removes and Phase 6 re-records. cpp
// carries a real probe that no wrapper hosts — a MEASURED hosted=none, not an
// unprobeable-by-assumption. The two expression-only grammars (elm, ocaml) whose
// `if` is an expression with no braced statement sequence declare it unprobeable.
var wrapperCensusCells = []wrapperCell{
	{lang: treesitter.LangBash, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,stmt"},
		{shape: shapeStmt, pattern: "$F $X", wantHosted: "decl,stmt"},
		{shape: shapeStmtKeyword, pattern: "return $X", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if $C; then\n$$$B\nfi", wantHosted: "decl,stmt"},
		{shape: shapeMember, unprobeable: "the shell grammar has no class or struct body, so there is no member shape to write", wantHosted: hostedNone},
		{shape: shapeDecl, pattern: "function $N() {\n  $$$B\n}", wantHosted: "decl,stmt"},
		{shape: shapeExpr, unprobeable: "every bash fragment is a command; the grammar has no bare-expression form", wantHosted: hostedNone},
	}},
	{lang: treesitter.LangC, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "expr"},
		{shape: shapeStmt, pattern: "$F($X);", wantHosted: "decl,stmt"},
		{shape: shapeStmtKeyword, pattern: "goto $L;", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if ($C) { $$$B; }", wantHosted: "decl,stmt"},
		{shape: shapeMember, pattern: "$T $N;", wantHosted: "decl,stmt"},
		{shape: shapeDecl, pattern: "void $N() {}", wantHosted: "decl,stmt"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "expr"},
	}},
	{lang: treesitter.LangCPP, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "expr"},
		{shape: shapeStmt, pattern: "$F($X);", wantHosted: "decl,stmt,member"},
		{shape: shapeStmtKeyword, pattern: "goto $L;", wantHosted: "decl,stmt,member"},
		{shape: shapeStmtBlock, pattern: "if ($C) { $$$B }", wantHosted: hostedNone},
		{shape: shapeMember, pattern: "$T $N;", wantHosted: "decl,stmt,member"},
		{shape: shapeDecl, pattern: "void $N() {}", wantHosted: "decl,stmt,member"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "expr"},
	}},
	{lang: treesitter.LangCSharp, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "expr"},
		{shape: shapeStmt, pattern: "$F($X);", wantHosted: "decl,stmt,top"},
		{shape: shapeStmtKeyword, pattern: "return $X;", wantHosted: "decl,stmt,top"},
		{shape: shapeStmtBlock, pattern: "if ($C) { $$$B; }", wantHosted: hostedNone},
		{shape: shapeMember, pattern: "$T $N;", wantHosted: "decl,stmt,top"},
		{shape: shapeDecl, pattern: "class $N {}", wantHosted: "decl,top"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "expr"},
	}},
	{lang: treesitter.LangElixir, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,stmt"},
		{shape: shapeStmt, pattern: "$F($X)", wantHosted: "decl,stmt"},
		{shape: shapeStmtKeyword, pattern: "alias $M", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if $C do\n$$$B\nend", wantHosted: "decl,stmt"},
		{shape: shapeMember, unprobeable: "an Elixir module body holds ordinary expressions, so a member has no spelling distinct from the declaration shape", wantHosted: hostedNone},
		{shape: shapeDecl, pattern: "def $N do\n  $$$B\nend", wantHosted: "decl,stmt"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "decl,stmt"},
	}},
	{lang: treesitter.LangElm, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "expr"},
		{shape: shapeStmt, unprobeable: "Elm is expression-only; the grammar has no statement form", wantHosted: hostedNone},
		{shape: shapeStmtKeyword, unprobeable: "Elm is expression-only; the grammar has no statement form", wantHosted: hostedNone},
		{shape: shapeStmtBlock, unprobeable: "Elm is expression-only; its if is an expression requiring then/else and has no braced statement sequence", wantHosted: hostedNone},
		{shape: shapeMember, unprobeable: "Elm has no class or struct body", wantHosted: hostedNone},
		{shape: shapeDecl, pattern: "$N = $V", wantHosted: "decl"},
		{shape: shapeExpr, pattern: "$F $X", wantHosted: "expr"},
	}},
	{lang: treesitter.LangGo, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "stmt,expr"},
		{shape: shapeStmt, pattern: "$F($X)", wantHosted: "stmt,expr"},
		{shape: shapeStmtKeyword, pattern: "defer $X.Close()", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if $C { $$$B }", wantHosted: "stmt"},
		{shape: shapeMember, pattern: "$N $T", wantHosted: "none"},
		{shape: shapeDecl, pattern: "func $N() {}", wantHosted: "decl"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "stmt,expr"},
	}},
	{lang: treesitter.LangGroovy, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,stmt"},
		{shape: shapeStmt, pattern: "$F($X)", wantHosted: "decl,stmt"},
		{shape: shapeStmtKeyword, pattern: "return $X", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if ($C) { $$$B }", wantHosted: "decl,stmt"},
		{shape: shapeMember, pattern: "private $T $N", wantHosted: "decl,stmt"},
		{shape: shapeDecl, pattern: "def $N() {\n  $$$B\n}", wantHosted: "decl,stmt"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "decl,stmt"},
	}},
	{lang: treesitter.LangJava, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "expr"},
		{shape: shapeStmt, pattern: "$F($X);", wantHosted: "stmt,top"},
		{shape: shapeStmtKeyword, pattern: "return $X;", wantHosted: "decl,stmt,top"},
		{shape: shapeStmtBlock, pattern: "if ($C) { $$$B; }", wantHosted: "stmt,top"},
		{shape: shapeMember, pattern: "$T $N = $V;", wantHosted: "decl,stmt,top"},
		{shape: shapeDecl, pattern: "class $N {}", wantHosted: "decl,stmt,top"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "expr"},
	}},
	{lang: treesitter.LangJavaScript, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,stmt,expr,member"},
		{shape: shapeStmt, pattern: "$F($X);", wantHosted: "decl,stmt"},
		{shape: shapeStmtKeyword, pattern: "return $X;", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if ($C) { $$$B }", wantHosted: "decl,stmt"},
		{shape: shapeMember, pattern: "static $N = $V;", wantHosted: "member"},
		{shape: shapeDecl, pattern: "function $N() {}", wantHosted: "decl,stmt,expr"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "decl,stmt,expr"},
	}},
	{lang: treesitter.LangKotlin, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,stmt"},
		{shape: shapeStmt, pattern: "$F($X)", wantHosted: "decl,stmt"},
		{shape: shapeStmtKeyword, pattern: "return $X", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if ($C) { $$$B }", wantHosted: "decl,stmt"},
		{shape: shapeMember, pattern: "val $N: $T = $V", wantHosted: "decl,stmt"},
		{shape: shapeDecl, pattern: "fun $N() {}", wantHosted: "decl,stmt"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "decl,stmt"},
	}},
	{lang: treesitter.LangLua, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,stmt"},
		{shape: shapeStmt, pattern: "$F($X)", wantHosted: "decl,stmt"},
		{shape: shapeStmtKeyword, pattern: "return $X", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if $C then\n$$$B\nend", wantHosted: "decl,stmt"},
		{shape: shapeMember, unprobeable: "Lua has no class body; a table field is an ordinary assignment", wantHosted: hostedNone},
		{shape: shapeDecl, pattern: "function $N()\n  $$$B\nend", wantHosted: "decl,stmt"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "decl,stmt"},
	}},
	{lang: treesitter.LangOCaml, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,expr"},
		{shape: shapeStmt, unprobeable: "OCaml is expression-only; the grammar has no statement form", wantHosted: hostedNone},
		{shape: shapeStmtKeyword, unprobeable: "OCaml is expression-only; the grammar has no statement form", wantHosted: hostedNone},
		{shape: shapeStmtBlock, unprobeable: "OCaml is expression-only; its if is an expression with no braced statement sequence", wantHosted: hostedNone},
		{shape: shapeMember, unprobeable: "OCaml record fields are declared in a type definition, not in a body a pattern fragment can sit in", wantHosted: hostedNone},
		{shape: shapeDecl, pattern: "let $N = $V", wantHosted: "decl"},
		{shape: shapeExpr, pattern: "$F $X", wantHosted: "decl,expr"},
	}},
	{lang: treesitter.LangPython, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,stmt,expr"},
		{shape: shapeStmt, pattern: "$F($X)", wantHosted: "decl,stmt,expr"},
		{shape: shapeStmtKeyword, pattern: "return $X", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if $C:\n    $$$B", wantHosted: "decl,stmt"},
		{shape: shapeMember, pattern: "$N: $T = $V", wantHosted: "decl,stmt,expr"},
		{shape: shapeDecl, pattern: "def $N(): pass", wantHosted: "decl,stmt"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "decl,stmt,expr"},
	}},
	{lang: treesitter.LangRuby, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,stmt"},
		{shape: shapeStmt, pattern: "$F($X)", wantHosted: "decl,stmt"},
		{shape: shapeStmtKeyword, pattern: "return $X", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if $C\n$$$B\nend", wantHosted: "decl,stmt"},
		{shape: shapeMember, unprobeable: "a Ruby class body holds ordinary statements, so a member has no spelling distinct from the statement shape", wantHosted: hostedNone},
		{shape: shapeDecl, pattern: "def $N\n  $$$B\nend", wantHosted: "decl,stmt"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "decl,stmt"},
	}},
	{lang: treesitter.LangRust, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "stmt,expr"},
		{shape: shapeStmt, pattern: "$F($X);", wantHosted: "decl,stmt,expr"},
		{shape: shapeStmtKeyword, pattern: "let $N = $V;", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if $C { $$$B }", wantHosted: "decl,stmt,expr"},
		{shape: shapeMember, pattern: "$N: $T", wantHosted: "none"},
		{shape: shapeDecl, pattern: "fn $N() {}", wantHosted: "decl,stmt"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "stmt,expr"},
	}},
	{lang: treesitter.LangScala, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,stmt"},
		{shape: shapeStmt, pattern: "$F($X)", wantHosted: "decl,stmt"},
		{shape: shapeStmtKeyword, pattern: "val $N = $V", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if ($C) { $$$B }", wantHosted: "decl,stmt"},
		{shape: shapeMember, pattern: "def $N = $E", wantHosted: "decl,stmt"},
		{shape: shapeDecl, pattern: "class $N {}", wantHosted: "decl,stmt"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "decl,stmt"},
	}},
	{lang: treesitter.LangSwift, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,stmt"},
		{shape: shapeStmt, pattern: "$F($X)", wantHosted: "decl,stmt"},
		{shape: shapeStmtKeyword, pattern: "return $X", wantHosted: "stmt"},
		{shape: shapeStmtBlock, pattern: "if $C { $$$B }", wantHosted: "decl,stmt"},
		{shape: shapeMember, pattern: "let $N: $T = $V", wantHosted: "decl,stmt"},
		{shape: shapeDecl, pattern: "func $N() {}", wantHosted: "decl,stmt"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "decl,stmt"},
	}},
	{lang: treesitter.LangTSX, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,stmt,expr,member"},
		{shape: shapeStmt, pattern: "$F($X);", wantHosted: "decl,stmt,member"},
		{shape: shapeStmtKeyword, pattern: "return $X;", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if ($C) { $$$B }", wantHosted: "decl,stmt"},
		{shape: shapeMember, pattern: "private readonly $N: $T;", wantHosted: "member"},
		{shape: shapeDecl, pattern: "function $N() {}", wantHosted: "decl,stmt,expr"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "decl,stmt,expr,member"},
	}},
	{lang: treesitter.LangTypeScript, probes: []wrapperProbe{
		{shape: shapeWildcard, pattern: "$_", wantHosted: "decl,stmt,expr,member"},
		{shape: shapeStmt, pattern: "$F($X);", wantHosted: "decl,stmt,member"},
		{shape: shapeStmtKeyword, pattern: "return $X;", wantHosted: "decl,stmt"},
		{shape: shapeStmtBlock, pattern: "if ($C) { $$$B }", wantHosted: "decl,stmt"},
		{shape: shapeMember, pattern: "private readonly $N: $T;", wantHosted: "member"},
		{shape: shapeDecl, pattern: "function $N() {}", wantHosted: "decl,stmt,expr"},
		{shape: shapeExpr, pattern: "$F($X)", wantHosted: "decl,stmt,expr,member"},
	}},
}
