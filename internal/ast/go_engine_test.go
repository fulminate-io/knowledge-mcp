// SPDX-License-Identifier: Apache-2.0

// go_engine_test.go — engine.go::compilePattern + LangConfig coverage.
// Verifies wrapper iteration order, ERROR-rejection, placeholder map
// correctness, and tree-sitter Close discipline.

package ast

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

func TestCompile_StatementPattern(t *testing.T) {
	pt, err := compilePattern(context.Background(), mustParse(t, "defer $X.Close()"), goLangConfig)
	if err != nil {
		t.Fatalf("compilePattern: %v", err)
	}
	defer pt.Close()
	if pt.Tree == nil || pt.Root == nil {
		t.Fatal("PatternTree.Tree or .Root is nil")
	}
	if pt.Root.Type() != "defer_statement" {
		t.Errorf("Root.Type = %q, want defer_statement", pt.Root.Type())
	}
	if pt.WrapperSkip == 0 {
		t.Error("WrapperSkip = 0; expected positive (statement wrapper)")
	}
	if got := len(pt.Placeholders); got != 1 {
		t.Errorf("Placeholders = %d, want 1", got)
	}
}

func TestCompile_DeclarationPattern(t *testing.T) {
	pt, err := compilePattern(context.Background(), mustParse(t, "func $NAME($$$ARGS) { $$$_ }"), goLangConfig)
	if err != nil {
		t.Fatalf("compilePattern: %v", err)
	}
	defer pt.Close()
	if pt.Root.Type() != "function_declaration" {
		t.Errorf("Root.Type = %q, want function_declaration", pt.Root.Type())
	}
	if got := len(pt.Placeholders); got != 3 {
		t.Errorf("Placeholders = %d, want 3", got)
	}
}

func TestCompile_ExpressionPattern(t *testing.T) {
	pt, err := compilePattern(context.Background(), mustParse(t, "make([]int, $N)"), goLangConfig)
	if err != nil {
		t.Fatalf("compilePattern: %v", err)
	}
	defer pt.Close()
	if pt.Root == nil {
		t.Fatal("Root nil")
	}
	if pt.Root.Type() != "call_expression" {
		t.Errorf("Root.Type = %q, want call_expression", pt.Root.Type())
	}
}

func TestCompile_RejectsGarbage(t *testing.T) {
	_, err := compilePattern(context.Background(), mustParse(t, "garbage that won't parse {{{"), goLangConfig)
	if err == nil {
		t.Fatal("expected error on garbage pattern")
	}
	if !errors.Is(err, errCompileNoWrapper) {
		t.Errorf("err = %v; want errors.Is errCompileNoWrapper", err)
	}
	if !strings.Contains(err.Error(), "tried") {
		t.Errorf("err message = %v; want substring 'tried' listing wrappers", err)
	}
}

func TestCompile_PlaceholderByteRanges(t *testing.T) {
	pt, err := compilePattern(context.Background(), mustParse(t, "$X.Close()"), goLangConfig)
	if err != nil {
		t.Fatalf("compilePattern: %v", err)
	}
	defer pt.Close()
	if got := len(pt.Placeholders); got != 1 {
		t.Fatalf("Placeholders = %d, want 1", got)
	}
	for r, ph := range pt.Placeholders {
		if ph.Name != "X" || ph.Kind != KindNode {
			t.Errorf("placeholder = {Name:%q Kind:%s}, want {X node}", ph.Name, ph.Kind)
		}
		if r.Start >= r.End {
			t.Errorf("byteRange = [%d, %d); expected start < end", r.Start, r.End)
		}
	}
}

func TestCompile_CloseReleasesTree(t *testing.T) {
	pt, err := compilePattern(context.Background(), mustParse(t, "$X.Close()"), goLangConfig)
	if err != nil {
		t.Fatalf("compilePattern: %v", err)
	}
	pt.Close()
	if pt.Tree != nil {
		t.Error("PatternTree.Tree != nil after Close")
	}
	if pt.Root != nil {
		t.Error("PatternTree.Root != nil after Close")
	}
	pt.Close() // must be nil-safe
}

func TestCompile_NilSafePatternTreeClose(t *testing.T) {
	var pt *PatternTree
	pt.Close() // must not panic
}

func TestCompile_LangNotRegistered(t *testing.T) {
	_, err := Compile(mustParse(t, "$X"), treesitter.Language("notalang"))
	if err == nil {
		t.Fatal("expected error for unregistered language")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("err = %v; want substring 'not supported'", err)
	}
}

// mustParse is a test helper that panics on Parse failure.
func mustParse(t *testing.T, source string) Pattern {
	t.Helper()
	pat, err := Parse(source)
	if err != nil {
		t.Fatalf("Parse(%q): %v", source, err)
	}
	return pat
}
