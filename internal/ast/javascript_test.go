// SPDX-License-Identifier: Apache-2.0

// javascript_test.go — JavaScript LangConfig coverage. Mirrors
// typescript_test.go but parses targets under the JS grammar; covers
// JS-only forms (class methods without TS type annotations, plain
// function declarations) and verifies that the same DSL surface works
// across the two grammars.

package ast

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// runJSWalker compiles pattern under jsLangConfig, parses target as JS,
// walks every named node, and returns the matches.
func runJSWalker(t *testing.T, pattern, target string) []walkerMatch {
	t.Helper()
	pt, err := compilePattern(context.Background(), mustParse(t, pattern), jsLangConfig)
	if err != nil {
		t.Fatalf("compilePattern(%q): %v", pattern, err)
	}
	defer pt.Close()

	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(target), treesitter.LangJavaScript)
	if err != nil {
		t.Fatalf("parse js target: %v", err)
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

func TestJavaScript_ClassDeclaration(t *testing.T) {
	target := `class Foo {
  bar() { return 1; }
  baz(x) { return x; }
}
class Empty {}
`
	matches := runJSWalker(t, "class $NAME { $$$BODY }", target)
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
	// Verify Foo's BODY captured the two method_definitions.
	for _, m := range matches {
		if cap, ok := m.captures["NAME"]; ok && cap.Text == "Foo" {
			if body, ok := m.captures["BODY"]; ok {
				if len(body.Children) != 2 {
					t.Errorf("Foo BODY.Children = %d, want 2 (bar + baz)", len(body.Children))
				}
			}
		}
	}
}

func TestJavaScript_ArrowFunction(t *testing.T) {
	target := `const inc = (x) => x + 1;
const dbl = (y) => y * 2;
`
	matches := runJSWalker(t, "($PARAM) => $BODY", target)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
	gotParams := map[string]bool{}
	for _, m := range matches {
		if cap, ok := m.captures["PARAM"]; ok {
			gotParams[cap.Text] = true
		}
	}
	for _, want := range []string{"x", "y"} {
		if !gotParams[want] {
			t.Errorf("PARAM missing %q (got %v)", want, gotParams)
		}
	}
}

func TestJavaScript_AwaitExpression(t *testing.T) {
	// `await $X` matches await expressions only — not the bare call on the
	// third line. This is a kind-discrimination case (await_expression vs
	// call_expression); async-keyword discrimination is asserted separately
	// in TestAnonTokenDiscrimination.
	target := `async function fetch() {
  const a = await get(1);
  const b = await get(2);
  const c = get(3);
  return [a, b, c];
}
`
	matches := runJSWalker(t, "await $X", target)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2 (only the two await expressions)", len(matches))
	}
}

func TestJavaScript_FunctionDeclaration(t *testing.T) {
	target := `function alpha(a) { return a; }
function beta(b, c) { return b + c; }
`
	matches := runJSWalker(t, "function $NAME($$$ARGS) { $$$BODY }", target)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
	gotNames := map[string]bool{}
	for _, m := range matches {
		if cap, ok := m.captures["NAME"]; ok {
			gotNames[cap.Text] = true
		}
	}
	for _, want := range []string{"alpha", "beta"} {
		if !gotNames[want] {
			t.Errorf("NAME missing %q (got %v)", want, gotNames)
		}
	}
}

func TestJavaScript_TemplateLiteral(t *testing.T) {
	// Template literal interpolation pattern.
	target := "const a = `hello ${name}`;\nconst b = 'plain';\n"
	matches := runJSWalker(t, "`$$$PARTS`", target)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1 (one template literal)", len(matches))
	}
}

func TestJavaScript_NegativeArrowDoesNotMatchFunction(t *testing.T) {
	target := `function foo(x) { return x; }
function bar(y) { return y; }
`
	matches := runJSWalker(t, "($PARAM) => $BODY", target)
	if len(matches) != 0 {
		t.Errorf("matches = %d, want 0 (function_declaration must not match arrow_function pattern)", len(matches))
	}
}

func TestJavaScript_NegativeWrongMethodName(t *testing.T) {
	target := `const a = obj.write();
const b = obj.read();
`
	matches := runJSWalker(t, "$X.read()", target)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1 (only obj.read())", len(matches))
	}
}
