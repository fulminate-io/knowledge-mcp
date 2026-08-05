// SPDX-License-Identifier: Apache-2.0

// go_match_test.go — end-to-end Match coverage. Builds a fixture repo
// under t.TempDir, calls ast.Match, and verifies match counts + captures
// + WalkStats.

package ast

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// fixtureRepo writes a single Go file to a fresh temp directory and
// returns its path.
func fixtureRepo(t *testing.T, contents map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range contents {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dir
}

func TestMatch_DeferClose_TwoMatches(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"main.go": `package main
func A() {
	defer x.Close()
}
func B() {
	defer y.Close()
	z.Close()
}
`,
	})

	pat, _ := Parse("defer $X.Close()")
	cp, err := Compile(pat, treesitter.LangGo, "")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer cp.Close()

	raws, walk, err := Match(context.Background(), dir, treesitter.LangGo, cp, nil, Scope{})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(raws) != 2 {
		t.Errorf("matches = %d, want 2", len(raws))
	}
	if walk.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", walk.FilesScanned)
	}
}

func TestMatch_FuncWithSeqArgs(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"sample.go": `package main
func A() error { return nil }
func B(x int) error { return errVal }
func C(x int, y string) error { return wrapErr(x) }
func D() string { return "no" }
`,
	})

	pat, _ := Parse("func $NAME($$$ARGS) error { return $ERR }")
	cp, err := Compile(pat, treesitter.LangGo, "")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer cp.Close()

	raws, _, err := Match(context.Background(), dir, treesitter.LangGo, cp, nil, Scope{})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(raws) != 3 {
		t.Errorf("matches = %d, want 3 (D returns string, must be excluded)", len(raws))
	}
	for _, r := range raws {
		name := r.Captures["NAME"]
		if name.Text == "D" {
			t.Errorf("function D should not match; got NAME=%q", name.Text)
		}
		// match capture must be the function_declaration outer node.
		match, ok := r.Captures["match"]
		if !ok {
			t.Errorf("missing 'match' capture")
		}
		if match.Kind != "function_declaration" {
			t.Errorf("match.Kind = %q, want function_declaration", match.Kind)
		}
	}
}

func TestMatch_WithWhereTreeFilter(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"sample.go": `package main
func Public() error { return nil }
func _private() error { return nil }
`,
	})

	pat, _ := Parse("func $NAME() error { return $ERR }")
	cp, err := Compile(pat, treesitter.LangGo, "")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer cp.Close()

	// Where-tree: not matches X starting with _.
	where, _ := ParseWhere([]byte(`{"not": {"matches": {"of": "NAME", "regex": "^_"}}}`))

	raws, _, err := Match(context.Background(), dir, treesitter.LangGo, cp, where, Scope{})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(raws) != 1 {
		t.Fatalf("matches = %d, want 1 (only Public)", len(raws))
	}
	if raws[0].Captures["NAME"].Text != "Public" {
		t.Errorf("NAME = %q, want Public", raws[0].Captures["NAME"].Text)
	}
}

func TestMatch_RespectsScope_PackagePrefix(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"a/file.go": `package a
func F1() { x.Close() }
`,
		"b/file.go": `package b
func F2() { y.Close() }
`,
	})

	pat, _ := Parse("$X.Close()")
	cp, err := Compile(pat, treesitter.LangGo, "")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer cp.Close()

	raws, _, err := Match(context.Background(), dir, treesitter.LangGo, cp, nil,
		Scope{PackagePrefixes: []string{"a/"}})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(raws) != 1 {
		t.Fatalf("matches = %d, want 1 (only a/file.go)", len(raws))
	}
	if !strings.HasPrefix(raws[0].FilePath, "a/") {
		t.Errorf("FilePath = %q, want prefix a/", raws[0].FilePath)
	}
}

func TestMatch_RespectsScope_IncludeTests(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"main.go": `package main
func F() { x.Close() }
`,
		"main_test.go": `package main
func TestF(_ *T) { y.Close() }
`,
	})

	pat, _ := Parse("$X.Close()")
	cp, err := Compile(pat, treesitter.LangGo, "")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer cp.Close()

	// IncludeTests=false (default): only main.go counts.
	raws, _, err := Match(context.Background(), dir, treesitter.LangGo, cp, nil, Scope{})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(raws) != 1 {
		t.Errorf("matches with IncludeTests=false = %d, want 1", len(raws))
	}

	// IncludeTests=true: both files.
	raws2, _, err := Match(context.Background(), dir, treesitter.LangGo, cp, nil, Scope{IncludeTests: true})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if len(raws2) != 2 {
		t.Errorf("matches with IncludeTests=true = %d, want 2", len(raws2))
	}
}

func TestMatch_ContextCancellation(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"main.go": `package main
func F() { x.Close() }
`,
	})
	pat, _ := Parse("$X.Close()")
	cp, _ := Compile(pat, treesitter.LangGo, "")
	defer cp.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := Match(ctx, dir, treesitter.LangGo, cp, nil, Scope{})
	if err == nil {
		t.Error("expected ctx.Err() propagation")
	}
}

func TestMatch_NilCompiledPatternErrors(t *testing.T) {
	_, _, err := Match(context.Background(), "/", treesitter.LangGo, nil, nil, Scope{})
	if err == nil {
		t.Fatal("expected error for nil cp")
	}
}
