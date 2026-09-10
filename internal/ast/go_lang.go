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
// The four registered wrappers. Three are one per parse context Go can
// express; the fourth is a second HOST for the declaration context, because a
// context is a place in the grammar and Go has two that hold declarations:
//
//   1. Declaration wrapper (`package p\n`) — for top-level forms like
//      `func Foo() {}` or `type X struct {}`.
//   2. Statement wrapper (`package p\nfunc _() {\n` … `\n}`) — for inside-
//      function statements like `defer x.Close()`, `for x := range y {}`.
//   3. Expression wrapper (`package p\nvar _ = ` …) — for bare expressions
//      like `make([]int, 0)` or `f(x).y`.
//   4. Import-spec wrapper (`package p\nimport (\n` … `\n)`) — for a single
//      import SPEC, so `$$$P "os/exec"` binds an import wherever it is
//      written. It carries contextDecl, the context already registered by
//      wrapper 1, so RegisteredContexts is unchanged and no new `context` pin
//      value appears.
//
// WHY A FOURTH WRAPPER AND NOT A CHANGE TO THE DESCENT RULE, stated because
// the alternative is the one a reader reaches for first. An import_spec that
// carries only a path shares its byte span with that path literal, so
// effectiveTargetNode (walker.go) descends through it and a bare `$X` binds the
// literal rather than the spec — which is why `kind:import_spec` on a bare
// capture finds NAMED specs only. Relaxing that descent would change what `$X`
// binds in EVERY registered language to fix one node kind in one, and it would
// still yield a kind filter plus a text regex rather than a structural capture
// of path and local name. The wrapper never meets the descent instead: the
// fragment `__META_AST_P__0 "os/exec"` spans a node with two named children, so
// hostsPattern roots the pattern at a real import_spec, patternRootKind reports
// rootDescended FALSE, and collectViaQuery leaves the candidate as the raw
// import_spec that both declaration shapes contain.
//
// THE COST, stated rather than hidden: this does not fix the descent, so after
// it `kind:import_spec` on a bare capture still binds named specs only and
// still misses unnamed ones with no error. That is a partial answer the
// import form above makes unnecessary rather than a wrong one, and a test pins
// the current behavior so a later descent change turns it red deliberately.
//
// ORDER IS NOT PREFERENCE. Every wrapper that parses without ERROR nodes and
// HOSTS the fragment contributes a candidate, and the walk matches the union
// of the distinct candidates — so listing declaration first excludes nothing
// and prefers nothing. `defer $X.Close()` is grammatical under both the
// declaration and the statement wrapper and compiles identically under each,
// which is why its matches are stamped with the context SET [decl, stmt]
// rather than with whichever entry happens to be registered first. The order
// survives only as candidate order, which decides which equivalent stamp a
// dedupe keeps; callers who need one reading narrow with the `context` pin.
// The import-spec wrapper appends rather than inserts for the same reason it
// changes nothing else: a bare `"os/exec"` fragment parses under stmt, expr AND
// this wrapper to the same string-literal tree, so the dedupe merges them and
// the match simply gains a decl stamp — which is true, since a string literal
// in a spec position IS a valid import spec.

package ast

import (
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// goLangConfig is the registered LangConfig for Go.
var goLangConfig = LangConfig{
	Lang:     treesitter.LangGo,
	Reserved: "__META_AST_",
	Wrappers: []ContextWrapper{
		{Name: "decl", Context: contextDecl, Prefix: "package _\n", Suffix: ""},
		{Name: "stmt", Context: contextStmt, Prefix: "package _\nfunc _() {\n", Suffix: "\n}"},
		{Name: "expr", Context: contextExpr, Prefix: "package _\nvar _ = ", Suffix: ""},
		{Name: "importspec", Context: contextDecl, Prefix: "package _\nimport (\n", Suffix: "\n)"},
	},
	CommentKinds: []string{"comment"},
	// Go's interpreted string parses to exactly its two quote tokens plus any
	// escape sequences; the literal text between them produces NO node, so the
	// child walk skipped every content byte and an inlined literal constrained
	// nothing. Go's raw_string_literal, rune_literal and the numeric literals are
	// childless and were always compared correctly — measured in
	// testdata/opaque_text_census.txt.
	OpaqueTextKinds: []string{"interpreted_string_literal"},
	IdentRule:       isGoIdent,
	// Go's grammar terminates a statement inside a block with an anonymous
	// newline, so a multi-line block carries a child a one-line block does
	// not. Measured across all 21 registered grammars (testdata/
	// layout_token_census.txt): Go is the only one that surfaces such a
	// token, and the one-line spelling parses to the same named nodes in the
	// same order without it — so it distinguishes nothing a caller could have
	// meant.
	LayoutTokens: []string{"\n"},
	IsTestFile:   isGoTestFile,
}

// isGoTestFile reports whether a repo-relative path is a Go test file. Go's
// convention is enforced by the toolchain itself — `go test` compiles exactly
// the _test.go suffix — so it is the least ambiguous of the twelve registered
// conventions. It lived in match.go as the walk's hardcoded gate until the
// filter became per-language; the behavior is unchanged.
func isGoTestFile(rel string) bool { return strings.HasSuffix(rel, "_test.go") }

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
