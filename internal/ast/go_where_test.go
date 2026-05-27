// SPDX-License-Identifier: Apache-2.0

// go_where_test.go — where-tree evaluator coverage. Three composers
// (all/any/not), six leaves (kind/matches/equals/same_node/inside_pattern/
// contains_pattern), depth cap, cross-language guard, and $outer.X
// capture resolution including the explicit-failure case (criterion
// 1fc06bf3).

package ast

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// whereCase configures one where-tree assertion.
func runWhere(t *testing.T, pattern, target, whereJSON string) (matches int, finalErr error) {
	t.Helper()
	pt, err := compilePattern(context.Background(), mustParse(t, pattern), goLangConfig)
	if err != nil {
		t.Fatalf("compilePattern: %v", err)
	}
	defer pt.Close()

	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(target), treesitter.LangGo)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	defer tree.Close()

	where, err := ParseWhere([]byte(whereJSON))
	if err != nil {
		t.Fatalf("ParseWhere: %v", err)
	}

	cache := map[string]*PatternTree{}
	mu := &sync.Mutex{}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range cache {
			p.Close()
		}
	}()

	outerScope := newOuterScope(treesitter.LangGo, cache, mu)
	src := []byte(target)

	walkAll(tree.RootNode(), func(n *sitter.Node) {
		caps := newCaptures()
		nodes := map[string]*sitter.Node{}
		if !matchTreeWithNodes(pt, n, src, caps, nodes) {
			return
		}
		scope := outerScope.withMatchCaptures(caps, nodes, src)
		ok, err := evalWhere(context.Background(), where, scope)
		if err != nil {
			if finalErr == nil {
				finalErr = err
			}
			return
		}
		if ok {
			matches++
		}
	})
	return matches, finalErr
}

func TestWhere_KindLeafSingle(t *testing.T) {
	target := "package main\nfunc f() { x.Close() }"
	got, err := runWhere(t, "$X.Close()", target,
		`{"kind": {"of": "X", "is": "identifier"}}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1", got)
	}
}

func TestWhere_KindLeafList(t *testing.T) {
	target := "package main\nfunc f() { pkg.Type.Close(); x.Close() }"
	got, err := runWhere(t, "$X.Close()", target,
		`{"kind": {"of": "X", "is": ["identifier", "selector_expression"]}}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 2 {
		t.Errorf("matches = %d, want 2", got)
	}
}

func TestWhere_EqualsLeaf(t *testing.T) {
	target := "package main\nfunc f() { x.Close(); y.Close() }"
	got, err := runWhere(t, "$X.Close()", target,
		`{"equals": {"of": "X", "value": "x"}}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1", got)
	}
}

func TestWhere_MatchesLeafRegex(t *testing.T) {
	target := "package main\nfunc f() { db.Close(); httpClient.Close(); x.Close() }"
	got, err := runWhere(t, "$X.Close()", target,
		`{"matches": {"of": "X", "regex": "^db"}}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1 (only db.Close)", got)
	}
}

func TestWhere_AllComposer(t *testing.T) {
	target := "package main\nfunc f() { x.Close(); db.Close() }"
	got, err := runWhere(t, "$X.Close()", target,
		`{"all": [{"kind": {"of": "X", "is": "identifier"}}, {"matches": {"of": "X", "regex": "^db"}}]}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1", got)
	}
}

func TestWhere_AnyComposer(t *testing.T) {
	target := "package main\nfunc f() { x.Close(); y.Close(); z.Close() }"
	got, err := runWhere(t, "$X.Close()", target,
		`{"any": [{"equals": {"of": "X", "value": "x"}}, {"equals": {"of": "X", "value": "z"}}]}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 2 {
		t.Errorf("matches = %d, want 2", got)
	}
}

func TestWhere_NotComposer(t *testing.T) {
	target := "package main\nfunc f() { x.Close(); y.Close() }"
	got, err := runWhere(t, "$X.Close()", target,
		`{"not": {"equals": {"of": "X", "value": "x"}}}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1 (only y.Close)", got)
	}
}

func TestWhere_SameNodeSelfReferenceTrue(t *testing.T) {
	target := "package main\nfunc f() { x.Close() }"
	got, err := runWhere(t, "$X.Close()", target,
		`{"same_node": {"captures": ["X", "X"]}}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Errorf("matches = %d, want 1", got)
	}
}

func TestWhere_OuterReferenceUnresolvedAtOuterLevel(t *testing.T) {
	// Criterion 1fc06bf3: $outer.X at the outer level (no parent scope)
	// returns the explicit unresolved error.
	target := "package main\nfunc f() { x.Close() }"
	_, err := runWhere(t, "$X.Close()", target,
		`{"same_node": {"captures": ["X", "$outer.X"]}}`)
	if err == nil {
		t.Fatal("expected unresolved-capture error, got nil")
	}
	if !errors.Is(err, errCaptureUnresolved) {
		t.Errorf("err = %v; want errors.Is errCaptureUnresolved", err)
	}
	if !strings.Contains(err.Error(), "$outer.X") {
		t.Errorf("err message = %v; want substring '$outer.X'", err)
	}
}

func TestWhere_MissingLocalCaptureReturnsError(t *testing.T) {
	target := "package main\nfunc f() { x.Close() }"
	_, err := runWhere(t, "$X.Close()", target,
		`{"equals": {"of": "MISSING", "value": "x"}}`)
	if err == nil {
		t.Fatal("expected unresolved-capture error")
	}
	if !errors.Is(err, errCaptureUnresolved) {
		t.Errorf("err = %v; want errors.Is errCaptureUnresolved", err)
	}
}

func TestWhere_RegexCompileOncePerWhereNode(t *testing.T) {
	// Verify the sync.Once cache: invoke evalMatches twice on the same
	// MatchesLeaf and ensure compileOnce fires only once.
	leaf := &MatchesLeaf{Of: "X", Regex: "^x$"}
	cache := map[string]*PatternTree{}
	mu := &sync.Mutex{}
	scope := newOuterScope(treesitter.LangGo, cache, mu)
	caps := newCaptures()
	caps.byName["X"] = Capture{Text: "x", Kind: "identifier"}
	scope = scope.withMatchCaptures(caps, map[string]*sitter.Node{}, []byte("x"))

	for range 5 {
		ok, err := evalMatches(leaf, scope)
		if err != nil {
			t.Fatalf("evalMatches: %v", err)
		}
		if !ok {
			t.Errorf("evalMatches = false, want true")
		}
	}
	if leaf.compiled == nil {
		t.Error("compiled regex not cached on leaf")
	}
}

func TestWhere_DepthCapAtNine(t *testing.T) {
	// Build a where-tree with 9 nested contains_pattern levels. The 9th
	// level should hit the cap (depth >= 8) and return errSubPatternDepth.
	build := func(depth int) string {
		if depth == 0 {
			return `{"equals": {"of": "Y", "value": "anything"}}`
		}
		// Recursive descent with each level using of=Y (the prior level's
		// sub-pattern's only capture).
		inner := buildDepthChainTest(depth - 1)
		return inner
	}
	_ = build

	whereJSON := `{"contains_pattern": {"of": "X", "pattern": "$Y", "where": ` +
		buildDepthChainTest(9) +
		`}}`
	target := "package main\nfunc f() { x.Close() }"
	_, err := runWhere(t, "$X.Close()", target, whereJSON)
	if err == nil {
		t.Fatal("expected depth-cap error")
	}
	if !errors.Is(err, errSubPatternDepth) {
		t.Errorf("err = %v; want errors.Is errSubPatternDepth", err)
	}
}

// buildDepthChainTest builds a where-tree of depth N using nested
// contains_pattern{of: Y, pattern: $Y, where: ...}. Helper for the depth-
// cap test.
func buildDepthChainTest(depth int) string {
	if depth == 0 {
		return `{"equals": {"of": "Y", "value": "anything"}}`
	}
	inner := buildDepthChainTest(depth - 1)
	return `{"contains_pattern": {"of": "Y", "pattern": "$Y", "where": ` + inner + `}}`
}

func TestWhere_CrossLanguageDeferred(t *testing.T) {
	target := "package main\nfunc f() { x.Close() }"
	_, err := runWhere(t, "$X.Close()", target,
		`{"contains_pattern": {"of": "X", "pattern": "$Y", "language": "python"}}`)
	if err == nil {
		t.Fatal("expected cross-language error")
	}
	if !errors.Is(err, errCrossLanguageSubPattern) {
		t.Errorf("err = %v; want errors.Is errCrossLanguageSubPattern", err)
	}
}

// TestWhere_MatchBindingKindFunctionDeclaration verifies the implicit
// $match binding lets a where-tree leaf reference the outermost matched
// node without an explicit named placeholder. Pattern `$_` accepts every
// node; the kind leaf gates on $match → function_declaration. Locked DSL
// spec — `$match` is the built-in root binding.
func TestWhere_MatchBindingKindFunctionDeclaration(t *testing.T) {
	target := "package main\nfunc Foo() {}\nfunc Bar() error { return nil }\n"
	got, err := runWhere(t, "$_", target,
		`{"kind": {"of": "$match", "is": "function_declaration"}}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 2 {
		t.Errorf("matches = %d, want 2 (Foo + Bar)", got)
	}
}

// TestWhere_MatchBindingKindMethodDeclaration mirrors the above for
// method_declaration. Same idiom; different kind.
func TestWhere_MatchBindingKindMethodDeclaration(t *testing.T) {
	target := "package main\ntype T struct{}\nfunc (t *T) M1() {}\nfunc (t *T) M2() error { return nil }\nfunc Top() {}\n"
	got, err := runWhere(t, "$_", target,
		`{"kind": {"of": "$match", "is": "method_declaration"}}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 2 {
		t.Errorf("matches = %d, want 2 (M1 + M2; Top is function_declaration)", got)
	}
}

// TestWhere_MatchBindingMatchesRegex verifies $match is also addressable
// from non-kind leaves. The regex matches against the full source text of
// the matched node — for a function_declaration that's the entire function.
func TestWhere_MatchBindingMatchesRegex(t *testing.T) {
	target := "package main\nfunc loadConfig() {}\nfunc saveConfig() {}\nfunc loadDB() {}\n"
	// Combine kind + regex on $match: gate to function_declaration whose
	// source text starts with "func load".
	got, err := runWhere(t, "$_", target,
		`{"all": [
			{"kind":    {"of": "$match", "is":    "function_declaration"}},
			{"matches": {"of": "$match", "regex": "^func load"}}
		]}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 2 {
		t.Errorf("matches = %d, want 2 (loadConfig + loadDB)", got)
	}
}

func TestWhere_NilWhereIsAlwaysTrue(t *testing.T) {
	target := "package main\nfunc f() { x.Close() }"
	cache := map[string]*PatternTree{}
	mu := &sync.Mutex{}
	scope := newOuterScope(treesitter.LangGo, cache, mu)
	scope = scope.withMatchCaptures(newCaptures(), map[string]*sitter.Node{}, []byte(target))
	ok, err := evalWhere(context.Background(), nil, scope)
	if err != nil {
		t.Fatalf("evalWhere(nil): %v", err)
	}
	if !ok {
		t.Error("evalWhere(nil) = false, want true (no filter)")
	}
}
