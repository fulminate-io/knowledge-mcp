// SPDX-License-Identifier: Apache-2.0

// python_test.go — Python LangConfig coverage. Mirrors go_walker_test.go:
// each test compiles a pattern under pythonLangConfig, parses a fixture
// source, walks every named node, and asserts capture counts + bindings.
// Includes the Phase C 'async-with-await' validation case and a negative
// case where a `with` pattern must NOT match a `for` block.

package ast

import (
	"context"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// runPythonWalker compiles pattern under pythonLangConfig, parses target
// as Python, walks every named node, and returns the matches.
func runPythonWalker(t *testing.T, pattern, target string) []walkerMatch {
	t.Helper()
	pt, err := compilePattern(context.Background(), mustParse(t, pattern), pythonLangConfig)
	if err != nil {
		t.Fatalf("compilePattern(%q): %v", pattern, err)
	}
	defer pt.Close()

	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(target), treesitter.LangPython)
	if err != nil {
		t.Fatalf("parse python target: %v", err)
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

func TestPython_ImportStatement(t *testing.T) {
	target := `import os
import sys
import json
`
	matches := runPythonWalker(t, "import $MODULE", target)
	if len(matches) != 3 {
		t.Fatalf("matches = %d, want 3", len(matches))
	}
	want := map[string]bool{"os": true, "sys": true, "json": true}
	for _, m := range matches {
		cap, ok := m.captures["MODULE"]
		if !ok {
			t.Fatalf("missing MODULE capture in match %+v", m)
		}
		if !want[cap.Text] {
			t.Errorf("MODULE = %q, want one of os/sys/json", cap.Text)
		}
		delete(want, cap.Text)
	}
	if len(want) != 0 {
		t.Errorf("uncovered MODULE values: %v", want)
	}
}

func TestPython_WithStatement(t *testing.T) {
	target := `def reader():
    with open("foo.txt") as f:
        data = f.read()
        return data
`
	matches := runPythonWalker(t, "with $CTX as $X: $$$BODY", target)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	m := matches[0]
	if cap, ok := m.captures["X"]; !ok || cap.Text != "f" {
		t.Errorf("X = %q, want f", cap.Text)
	}
	if cap, ok := m.captures["CTX"]; !ok || !strings.Contains(cap.Text, `open("foo.txt")`) {
		t.Errorf("CTX = %q, want substring open(\"foo.txt\")", cap.Text)
	}
	if cap, ok := m.captures["BODY"]; !ok {
		t.Errorf("missing BODY capture")
	} else if len(cap.Children) == 0 {
		t.Errorf("BODY.Children empty; want at least one statement")
	}
}

func TestPython_AsyncWithAwait(t *testing.T) {
	// Phase C validation case from the ticket. The async with statement
	// is a distinct grammar form (`async_with_statement` doesn't exist in
	// tree-sitter-python; it's a `with_statement` with an async modifier).
	target := `async def fetch():
    async with session.get(url) as resp:
        body = await resp.read()
`
	// The pattern intentionally does NOT include `async` so we match the
	// inner with form. Structurally, tree-sitter-python represents
	// `async with` as `with_statement` whose first child is `async`.
	matches := runPythonWalker(t, "with $CTX as $X: $$$BODY", target)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1 (the async with should match the with-statement form)", len(matches))
	}
	if cap := matches[0].captures["X"]; cap.Text != "resp" {
		t.Errorf("X = %q, want resp", cap.Text)
	}
}

func TestPython_SubscriptExpression(t *testing.T) {
	target := `def f(items):
    a = items[0]
    b = data["key"]
    c = matrix[1]
`
	matches := runPythonWalker(t, "$LIST[$INDEX]", target)
	if len(matches) != 3 {
		t.Fatalf("matches = %d, want 3", len(matches))
	}
	gotIndex := map[string]bool{}
	for _, m := range matches {
		if cap, ok := m.captures["INDEX"]; ok {
			gotIndex[cap.Text] = true
		}
	}
	for _, want := range []string{"0", `"key"`, "1"} {
		if !gotIndex[want] {
			t.Errorf("missing INDEX value %q (got %v)", want, gotIndex)
		}
	}
}

func TestPython_FunctionDefinition(t *testing.T) {
	target := `def alpha(x):
    return x

def beta(a, b):
    return a + b

def gamma():
    pass
`
	matches := runPythonWalker(t, "def $NAME($$$ARGS): $$$BODY", target)
	if len(matches) != 3 {
		t.Fatalf("matches = %d, want 3", len(matches))
	}
	want := map[string]bool{"alpha": true, "beta": true, "gamma": true}
	for _, m := range matches {
		if cap, ok := m.captures["NAME"]; ok {
			if !want[cap.Text] {
				t.Errorf("NAME = %q, not in want set", cap.Text)
			}
			delete(want, cap.Text)
		}
	}
	if len(want) != 0 {
		t.Errorf("uncovered NAME values: %v", want)
	}
}

func TestPython_NegativeWithDoesNotMatchFor(t *testing.T) {
	// `with $X as $Y: $$$BODY` must NOT match a for-loop, even though both
	// are compound statements with bodies (criterion C.1 negative case).
	target := `def loop(items):
    for item in items:
        print(item)
`
	matches := runPythonWalker(t, "with $X as $Y: $$$BODY", target)
	if len(matches) != 0 {
		t.Errorf("matches = %d, want 0 (for-loop must not match with-statement)", len(matches))
	}
}

func TestPython_NegativeWrongFunctionName(t *testing.T) {
	// Pattern requires call name `open`. A call to `read` must not match.
	target := `def io_ops():
    a = open("x")
    b = read("y")
`
	matches := runPythonWalker(t, "open($ARG)", target)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1 (only the open(...) call)", len(matches))
	}
	if cap := matches[0].captures["ARG"]; cap.Text != `"x"` {
		t.Errorf("ARG = %q, want \"x\"", cap.Text)
	}
}
