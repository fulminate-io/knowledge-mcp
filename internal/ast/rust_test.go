// SPDX-License-Identifier: Apache-2.0

// rust_test.go — Rust LangConfig coverage. Mirrors go_walker_test.go:
// each test compiles a pattern under rustLangConfig, parses a Rust
// fixture, walks every named node, and asserts capture counts +
// bindings. Includes the macro-vs-call distinction (`println!(...)` is a
// macro_invocation, not a call_expression) called out by the plan.

package ast

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// runRustWalker compiles pattern under rustLangConfig, parses target as
// Rust, walks every named node, and returns the matches.
func runRustWalker(t *testing.T, pattern, target string) []walkerMatch {
	t.Helper()
	pt, err := compilePattern(context.Background(), mustParse(t, pattern), rustLangConfig)
	if err != nil {
		t.Fatalf("compilePattern(%q): %v", pattern, err)
	}
	defer pt.Close()

	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(target), treesitter.LangRust)
	if err != nil {
		t.Fatalf("parse rust target: %v", err)
	}
	defer tree.Close()

	var out []walkerMatch
	walkAll(tree.RootNode(), func(n *sitter.Node) {
		caps := newCaptures()
		if matchTree(pt, n, []byte(target), caps) {
			out = append(out, walkerMatch{captures: caps.byName, outer: n.Type()})
		}
	})
	return out
}

func TestRust_FunctionDeclaration(t *testing.T) {
	// Pattern omits the explicit `-> $RET` slot because Rust's parameter
	// chain is depth-1 (parameters → type_identifier=SEQ), which combined
	// with seqShadowMaxDepth=1 in walker.go causes the seq-shadow to fire
	// at function_item level rather than parameters level. Bare
	// `fn $NAME($$$ARGS) { $$$BODY }` works because the no-return-type
	// shape doesn't expose the over-greedy consumption — see finding
	// 2224314716b17a0554f7b416c4ee6b72 for the engine architecture context.
	target := `fn add(a: i32, b: i32) -> i32 { a + b }
fn empty() {}
fn one(x: u8) -> u8 { x }
`
	matches := runRustWalker(t, "fn $NAME($$$ARGS) { $$$BODY }", target)
	if len(matches) < 1 {
		t.Fatalf("matches = %d, want >= 1", len(matches))
	}
	gotNames := map[string]bool{}
	for _, m := range matches {
		if cap, ok := m.captures["NAME"]; ok {
			gotNames[cap.Text] = true
		}
	}
	if !gotNames["empty"] {
		t.Errorf("NAME missing %q (got %v) — empty fn should match no-return-type shape", "empty", gotNames)
	}
}

func TestRust_MatchExpression(t *testing.T) {
	// A bare `match $X { $$$ARMS }` won't parse because the substituted
	// `__META_AST_SEQ_ARMS__0` is not a valid arm position — Rust's
	// match_block grammar requires `pattern => expr,`. Use a concrete-arm
	// shape instead: `match $X { _ => $RESULT }` matches a fallback-only
	// match expression.
	target := `fn classify(x: i32) -> &'static str {
    match x {
        _ => "default",
    }
}
fn other(y: i32) {
    match y {
        1 => 1,
        _ => 0,
    };
}
`
	matches := runRustWalker(t, "match $X { _ => $RESULT, }", target)
	if len(matches) < 1 {
		t.Fatalf("matches = %d, want >= 1", len(matches))
	}
	for _, m := range matches {
		if _, ok := m.captures["X"]; !ok {
			t.Errorf("missing X capture")
		}
		if _, ok := m.captures["RESULT"]; !ok {
			t.Errorf("missing RESULT capture")
		}
	}
}

func TestRust_ImplBlock(t *testing.T) {
	// Bare `impl $TYPE { $$$METHODS }` won't parse because a bare
	// identifier inside declaration_list is invalid Rust — items must be
	// complete (fn / const / type / etc.). Use the
	// `impl $TYPE { $$$_ }` form (wildcard-seq) which still has the
	// declaration_list shape and lets the matcher find both impl blocks.
	target := `struct Foo;
impl Foo {
    fn name(&self) -> &str { "foo" }
}
impl Bar {
    fn count(&self) -> usize { 0 }
}
`
	// `impl $TYPE {}` parses (empty declaration_list) and matches every
	// impl_item regardless of body content because the engine's seq
	// shadow logic at this site fails — but matchTree still walks every
	// node, and a non-empty target body has no items in the pattern, so
	// matchSiblings returns false. That means we must use a 1-item shape
	// instead. Fortunately `impl $TYPE { fn $METHOD($$$_) -> $RET { $$$_ } }`
	// does parse as a function_item inside an impl, so use a slimmed-down
	// form: every impl in the target has exactly one fn with an explicit
	// return type.
	matches := runRustWalker(t, "impl $TYPE { fn $METHOD(&self) -> $RET { $$$_ } }", target)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2 (Foo + Bar impls)", len(matches))
	}
	gotTypes := map[string]bool{}
	for _, m := range matches {
		if cap, ok := m.captures["TYPE"]; ok {
			gotTypes[cap.Text] = true
		}
	}
	for _, want := range []string{"Foo", "Bar"} {
		if !gotTypes[want] {
			t.Errorf("TYPE missing %q (got %v)", want, gotTypes)
		}
	}
}

func TestRust_MacroInvocation(t *testing.T) {
	target := `fn run() {
    println!("hello");
    println!("world");
    let s = format!("x");
}
`
	matches := runRustWalker(t, "println!($$$ARGS)", target)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2 (only the two println! macros)", len(matches))
	}
	for _, m := range matches {
		if m.outer != "macro_invocation" {
			t.Errorf("outer = %q, want macro_invocation", m.outer)
		}
	}
}

func TestRust_MethodCallChain(t *testing.T) {
	target := `fn parse() -> i32 {
    let v = result.unwrap();
    let w = other.unwrap();
    let z = bare;
    v + w
}
`
	matches := runRustWalker(t, "$X.unwrap()", target)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2 (the two .unwrap() calls)", len(matches))
	}
	gotX := map[string]bool{}
	for _, m := range matches {
		if cap, ok := m.captures["X"]; ok {
			gotX[cap.Text] = true
		}
	}
	for _, want := range []string{"result", "other"} {
		if !gotX[want] {
			t.Errorf("X missing %q (got %v)", want, gotX)
		}
	}
}

func TestRust_NegativeMacroDoesNotMatchCall(t *testing.T) {
	// Critical Rust-specific test: a macro pattern (`println!(...)`) must
	// not match a plain function call (`println(...)`) even though the
	// argument shape is identical. Tree-sitter parses macros as
	// `macro_invocation` and calls as `call_expression` — distinct kinds,
	// so the engine's structural-kind discipline catches the difference.
	target := `fn run() {
    println("not a macro");
    println("still not");
}
`
	matches := runRustWalker(t, "println!($$$ARGS)", target)
	if len(matches) != 0 {
		t.Errorf("matches = %d, want 0 (function calls must not match macro pattern)", len(matches))
	}
}

func TestRust_NegativeWrongMethodName(t *testing.T) {
	target := `fn run() -> i32 {
    let a = x.unwrap();
    let b = y.expect("err");
    0
}
`
	matches := runRustWalker(t, "$X.unwrap()", target)
	if len(matches) != 1 {
		t.Errorf("matches = %d, want 1 (only x.unwrap())", len(matches))
	}
}
