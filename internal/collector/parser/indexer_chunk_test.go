// SPDX-License-Identifier: Apache-2.0

package parser_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestChunkAndBuild(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "pkg", "hello.go"), `package pkg

func Hello() string {
	return "hello"
}

func Goodbye() string {
	return "goodbye"
}
`)

	files := []string{filepath.Join("pkg", "hello.go")}
	results, err := parser.ChunkFiles(context.Background(), dir, files)
	if err != nil {
		t.Fatalf("ChunkFiles: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if len(results[0].Chunks) < 2 {
		t.Errorf("expected at least 2 chunks (Hello, Goodbye), got %d", len(results[0].Chunks))
	}

	// Verify chunks can build a valid graph via Populate.
	pop, err := parser.Populate(context.Background(), filepath.Base(dir), dir)
	if err != nil {
		t.Fatalf("Populate: %v", err)
	}
	if len(pop.Nodes) < 3 { // file node + 2 function nodes
		t.Errorf("expected at least 3 nodes, got %d", len(pop.Nodes))
	}

	// Verify file node.
	var hasFileNode bool
	for _, n := range pop.Nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeFile && strings.HasSuffix(n.Id, "hello.go") {
			hasFileNode = true
			break
		}
	}
	if !hasFileNode {
		t.Error("expected file node for hello.go")
	}

	// Verify function node ID format.
	for _, e := range pop.Entries {
		if !strings.Contains(e.NodeID, "hello.go:") {
			t.Errorf("expected node ID to contain file path, got %s", e.NodeID)
		}
	}
}

// TestChunkAndPopulate_FoldsDocComments confirms the end-to-end pipeline
// — tree-sitter chunker emitting orphan comment chunks AND populate
// folding them into the next declaration's Description — produces a code
// node whose Description carries the doc comment text.
//
// Regression for the c086ac9e populate-folder logic, which was emitting
// no Description because the chunker emits declarations from the
// TopLevel query loop BEFORE orphan comments via collectOrphans (so the
// raw chunk slice was decl-first-then-comment, never document order).
// populate.go now sorts by StartByte before folding.
func TestChunkAndPopulate_FoldsDocComments(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "pkg", "doc.go"), `package pkg

// Authenticate validates the supplied token and returns true on success.
// Returns false for malformed or expired tokens.
func Authenticate(token string) bool {
	return token != ""
}
`)

	pop, err := parser.Populate(context.Background(), filepath.Base(dir), dir)
	if err != nil {
		t.Fatalf("Populate: %v", err)
	}

	var found *knowledgev1.Node
	for _, n := range pop.Nodes {
		if n.SymbolName == "Authenticate" {
			found = n
			break
		}
	}
	if found == nil {
		t.Fatalf("Authenticate node not produced")
	}
	if found.Description == "" {
		t.Fatalf("Authenticate.Description is empty — chunker did not emit comment chunk OR populate did not fold it")
	}
	if !strings.Contains(found.Description, "validates the supplied token") {
		t.Errorf("Description missing comment text, got: %q", found.Description)
	}
}

func TestChunkFilesParallel(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.go"), `package a
func A() string { return "a" }
`)
	writeTestFile(t, filepath.Join(dir, "b.go"), `package b
func B() string { return "b" }
`)
	writeTestFile(t, filepath.Join(dir, "c.go"), `package c
func C() string { return "c" }
`)

	files := []string{"a.go", "b.go", "c.go"}
	results, err := parser.ChunkFilesParallel(context.Background(), dir, files)
	if err != nil {
		t.Fatalf("ChunkFilesParallel: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Collect all chunk names.
	names := make(map[string]bool)
	for _, r := range results {
		for _, c := range r.Chunks {
			names[c.Name] = true
		}
	}
	for _, expected := range []string{"A", "B", "C"} {
		if !names[expected] {
			t.Errorf("expected chunk %q not found", expected)
		}
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
