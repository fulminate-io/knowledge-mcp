// SPDX-License-Identifier: Apache-2.0

// go_walker_test.go — walker.go::matchTree coverage. Tests every
// placeholder form against in-memory Go target ASTs, plus position-bearing
// capture verification, sequence text-vs-children, and leaf-text
// comparison.

package ast

import (
	"context"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// walkerMatch holds the per-match capture map + outer node kind for tests.
type walkerMatch struct {
	captures map[string]Capture
	outer    string
}

// runWalker compiles pattern, parses target, walks every named node, and
// returns the matches.
func runWalker(t *testing.T, pattern, target string) []walkerMatch {
	t.Helper()
	pt, err := compilePattern(context.Background(), mustParse(t, pattern), goLangConfig)
	if err != nil {
		t.Fatalf("compilePattern(%q): %v", pattern, err)
	}
	defer pt.Close()

	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(target), treesitter.LangGo)
	if err != nil {
		t.Fatalf("parse target: %v", err)
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

func TestWalker_SingleNamedPlaceholder(t *testing.T) {
	target := "package main\nfunc f() { x.Close(); y.Close() }"
	matches := runWalker(t, "$X.Close()", target)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
	for _, m := range matches {
		if cap, ok := m.captures["X"]; !ok {
			t.Errorf("missing X capture")
		} else if cap.Kind != "identifier" {
			t.Errorf("X.Kind = %q, want identifier", cap.Kind)
		}
	}
}

func TestWalker_SingleWildcard(t *testing.T) {
	target := "package main\nfunc f() { defer x.Close(); defer pkg.Type.Close() }"
	matches := runWalker(t, "defer $_.Close()", target)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
}

func TestWalker_SequenceCapture(t *testing.T) {
	target := `package main
func ZeroArgs() {}
func OneArg(a int) {}
func ThreeArgs(a int, b string, c bool) {}
`
	matches := runWalker(t, "func $_($$$ARGS) { $$$_ }", target)
	if len(matches) != 3 {
		t.Fatalf("matches = %d, want 3", len(matches))
	}
	// Verify the 3-arg case has Children of length 3.
	var threeArg *walkerMatch
	for i := range matches {
		if cap, ok := matches[i].captures["ARGS"]; ok && len(cap.Children) == 3 {
			threeArg = &matches[i]
			break
		}
	}
	if threeArg == nil {
		t.Fatal("missing ThreeArgs match with 3 children")
	}
	cap := threeArg.captures["ARGS"]
	if !strings.Contains(cap.Text, "a int") || !strings.Contains(cap.Text, "c bool") {
		t.Errorf("ARGS.Text = %q; want substrings 'a int' and 'c bool'", cap.Text)
	}
}

func TestWalker_SequenceWildcard(t *testing.T) {
	target := `package main
func A() error { return nil }
func B(x int, y string) error { return errVal }
`
	matches := runWalker(t, "func $NAME($$$_) error { return $ERR }", target)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
	for _, m := range matches {
		if _, ok := m.captures["NAME"]; !ok {
			t.Error("missing NAME")
		}
		if _, ok := m.captures["ERR"]; !ok {
			t.Error("missing ERR")
		}
		// $$$_ is a wildcard sequence — no capture name, should not appear.
		for k := range m.captures {
			if k == "" {
				t.Errorf("unexpected empty-named capture: %v", m.captures)
			}
		}
	}
}

func TestWalker_PositionBearingCapture(t *testing.T) {
	// Verify $NAME binds at the function-name field position, NOT at any
	// other identifier position (validation contract item 4).
	target := `package main
func TopFunc(x int) {
	TopFunc(x)
}
`
	matches := runWalker(t, "func $NAME($$$ARGS) { $$$_ }", target)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1 (only function_declaration, not call site)", len(matches))
	}
	m := matches[0]
	if cap := m.captures["NAME"]; cap.Text != "TopFunc" {
		t.Errorf("NAME = %q, want TopFunc", cap.Text)
	}
}

func TestWalker_LeafTextComparison(t *testing.T) {
	// Pattern requires return type 'error'. Target with 'string' return must
	// not match.
	target := `package main
func A() error { return nil }
func B() string { return "no" }
`
	matches := runWalker(t, "func $_() error { return $ERR }", target)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1 (only the error-returning function)", len(matches))
	}
}

func TestWalker_NegativeNoDeferDoesNotMatchBareCall(t *testing.T) {
	target := "package main\nfunc f() { x.Close() }" // no defer
	matches := runWalker(t, "defer $X.Close()", target)
	if len(matches) != 0 {
		t.Errorf("matches = %d, want 0 (bare x.Close() must not match defer pattern)", len(matches))
	}
}

func TestWalker_NegativeWrongMethodDoesNotMatch(t *testing.T) {
	target := "package main\nfunc f() { defer x.Open() }"
	matches := runWalker(t, "defer $X.Close()", target)
	if len(matches) != 0 {
		t.Errorf("matches = %d, want 0 (defer x.Open() must not match defer-Close pattern)", len(matches))
	}
}

func TestWalker_ZeroArgSequenceMatches(t *testing.T) {
	target := `package main
func F() error { return nil }
`
	matches := runWalker(t, "func $NAME($$$ARGS) error { $$$_ }", target)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	cap := matches[0].captures["ARGS"]
	if cap.Text != "" {
		t.Errorf("zero-arg ARGS.Text = %q, want empty", cap.Text)
	}
	if len(cap.Children) != 0 {
		t.Errorf("zero-arg ARGS.Children = %d, want 0", len(cap.Children))
	}
}
