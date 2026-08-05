// SPDX-License-Identifier: Apache-2.0

// typescript_test.go — TypeScript LangConfig coverage. Mirrors
// go_walker_test.go: each test compiles a pattern under tsLangConfig,
// parses a TS fixture, walks every named node, and asserts capture
// counts + bindings. Includes a TS-specific case for `as_expression`
// (type assertion) plus a negative case where an arrow-function pattern
// must NOT match a regular function declaration.

package ast

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// runTSWalker compiles pattern under tsLangConfig, parses target as TS,
// walks every named node, and returns the matches.
func runTSWalker(t *testing.T, pattern, target string) []walkerMatch {
	t.Helper()
	pt, err := compilePattern(context.Background(), mustParse(t, pattern), tsLangConfig)
	if err != nil {
		t.Fatalf("compilePattern(%q): %v", pattern, err)
	}
	defer pt.Close()

	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(target), treesitter.LangTypeScript)
	if err != nil {
		t.Fatalf("parse ts target: %v", err)
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

// runTSXWalker compiles pattern under tsxLangConfig, parses target as TSX
// (the JSX-capable grammar), walks every named node, and returns the
// matches. Mirrors runTSWalker but exercises the tsx language so JSX
// elements parse without ERROR-soup.
func runTSXWalker(t *testing.T, pattern, target string) []walkerMatch {
	t.Helper()
	pt, err := compilePattern(context.Background(), mustParse(t, pattern), tsxLangConfig)
	if err != nil {
		t.Fatalf("compilePattern(%q): %v", pattern, err)
	}
	defer pt.Close()

	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(target), treesitter.LangTSX)
	if err != nil {
		t.Fatalf("parse tsx target: %v", err)
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

// TestTSX_JSXElementMatch proves the ast DSL works on tsx: a JSX-returning
// component is matched and a capture over the JSX element binds non-empty.
// Fails-when-absent: without tsxLangConfig, langConfigFor(LangTSX) misses
// and compilePattern/parse can't drive the tsx grammar — this is what makes
// `ast(language:"tsx")` first-class rather than explain-only.
func TestTSX_JSXElementMatch(t *testing.T) {
	target := `function App() {
  return <div className="root">{label}</div>;
}
`
	// The JSX expression `{label}` parses as a jsx_expression child of the
	// jsx_element; capturing it proves the JSX subtree parsed structurally
	// (under the plain typescript grammar this derails into ERROR nodes).
	matches := runTSXWalker(t, "<div className=\"root\">{$CHILD}</div>", target)
	if len(matches) == 0 {
		t.Fatalf("matches = 0, want >= 1 (the JSX element must match under tsx)")
	}
	bound := false
	for _, m := range matches {
		if cap, ok := m.captures["CHILD"]; ok && cap.Text != "" {
			bound = true
		}
	}
	if !bound {
		t.Errorf("CHILD capture did not bind non-empty over the JSX element")
	}
}

func TestTypeScript_ArrowFunction(t *testing.T) {
	target := `const inc = (x) => x + 1;
const dbl = (y) => y * 2;
function foo(z) { return z; }
`
	matches := runTSWalker(t, "($PARAM) => $BODY", target)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2 (only the two arrow functions)", len(matches))
	}
	gotParams := map[string]bool{}
	for _, m := range matches {
		if cap, ok := m.captures["PARAM"]; ok {
			gotParams[cap.Text] = true
		}
		if _, ok := m.captures["BODY"]; !ok {
			t.Errorf("missing BODY capture in match %+v", m)
		}
	}
	for _, want := range []string{"x", "y"} {
		if !gotParams[want] {
			t.Errorf("PARAM missing %q (got %v)", want, gotParams)
		}
	}
}

func TestTypeScript_InterfaceDeclaration(t *testing.T) {
	target := `interface Foo {
  bar: number;
  baz(): void;
}
interface Empty {}
`
	matches := runTSWalker(t, "interface $NAME { $$$MEMBERS }", target)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2 (Foo + Empty)", len(matches))
	}
	gotNames := map[string]bool{}
	for _, m := range matches {
		if cap, ok := m.captures["NAME"]; ok {
			gotNames[cap.Text] = true
		}
	}
	for _, want := range []string{"Foo", "Empty"} {
		if !gotNames[want] {
			t.Errorf("NAME missing %q (got %v)", want, gotNames)
		}
	}
}

func TestTypeScript_AsExpression(t *testing.T) {
	target := `const a = x as string;
const b = obj as Record<string, unknown>;
const c = bare;
`
	matches := runTSWalker(t, "$X as $T", target)
	if len(matches) < 2 {
		t.Fatalf("matches = %d, want at least 2 (the two as-expressions)", len(matches))
	}
	// Verify all as-expressions captured both X and T.
	for _, m := range matches {
		if _, ok := m.captures["X"]; !ok {
			t.Errorf("missing X capture")
		}
		if _, ok := m.captures["T"]; !ok {
			t.Errorf("missing T capture")
		}
	}
}

func TestTypeScript_FunctionDeclaration(t *testing.T) {
	// `async` is an anonymous token of function_declaration, and the matcher
	// compares anonymous tokens in both directions: a pattern that does not
	// carry `async` must not match a declaration that does. The two async
	// declarations below are the discriminated-against set; syncFn is the
	// only structural peer of the pattern.
	target := `async function fetchOne(id) { return id; }
async function fetchTwo(a, b) { return a + b; }
function syncFn(x) { return x; }
`
	matches := runTSWalker(t, "function $NAME($$$ARGS) { $$$BODY }", target)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1 (an async-free pattern must not match the two async declarations)", len(matches))
	}
	gotNames := map[string]bool{}
	for _, m := range matches {
		if cap, ok := m.captures["NAME"]; ok {
			gotNames[cap.Text] = true
		}
	}
	if !gotNames["syncFn"] {
		t.Errorf("NAME = %v, want exactly syncFn", gotNames)
	}
}

func TestTypeScript_TypeAlias(t *testing.T) {
	// The trailing semicolon is load-bearing: it is an anonymous child of
	// type_alias_declaration, so a pattern that omits it does not match a
	// declaration that carries one. Both targets below are terminated, so the
	// pattern is too.
	target := `type Id = string;
type User = { name: string; id: number };
`
	matches := runTSWalker(t, "type $NAME = $TYPE;", target)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
	gotNames := map[string]bool{}
	for _, m := range matches {
		if cap, ok := m.captures["NAME"]; ok {
			gotNames[cap.Text] = true
		}
	}
	for _, want := range []string{"Id", "User"} {
		if !gotNames[want] {
			t.Errorf("NAME missing %q (got %v)", want, gotNames)
		}
	}
}

func TestTypeScript_NegativeArrowDoesNotMatchFunction(t *testing.T) {
	// An arrow function pattern must not match a regular function
	// declaration — they have different node kinds (arrow_function vs
	// function_declaration). Validates the structural-kind discipline.
	target := `function foo(x) { return x; }
function bar(y) { return y; }
`
	matches := runTSWalker(t, "($PARAM) => $BODY", target)
	if len(matches) != 0 {
		t.Errorf("matches = %d, want 0 (function_declaration must not match arrow_function pattern)", len(matches))
	}
}
