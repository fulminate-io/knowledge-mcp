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
	results, rep, err := parser.ChunkFiles(context.Background(), dir, files)
	if err != nil {
		t.Fatalf("ChunkFiles: %v", err)
	}
	if rep.Dropped() != 0 {
		t.Fatalf("clean walk dropped files: %s", rep.Describe())
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
	results, rep, err := parser.ChunkFilesParallel(context.Background(), dir, files)
	if err != nil {
		t.Fatalf("ChunkFilesParallel: %v", err)
	}
	if rep.Dropped() != 0 {
		t.Fatalf("clean walk dropped files: %s", rep.Describe())
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

// TestChunkReport_CountsEveryLoss pins the report's counting. Each subtest is a
// KNOWN POSITIVE for a different loss path, which is what makes the clean-walk
// zero above readable: without one, a counter that is never incremented and a
// genuinely clean walk are the same observation.
func TestChunkReport_CountsEveryLoss(t *testing.T) {
	t.Run("unreadable_file_counts_and_is_named", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "present.go"), "package p\n\nfunc P() {}\n")

		// "absent.go" is handed to chunking but never written, so os.ReadFile fails
		// ENOENT at the real read site rather than through a simulated error.
		_, rep, err := parser.ChunkFiles(context.Background(), dir, []string{"present.go", "absent.go"})
		if err != nil {
			t.Fatalf("ChunkFiles: %v", err)
		}
		if got := rep.Dropped(); got != 1 {
			t.Fatalf("expected exactly 1 dropped file, got %d (%s)", got, rep.Describe())
		}
		if got := rep.DroppedByReason[parser.ChunkReasonReadError]; got != 1 {
			t.Errorf("expected 1 %s, got %d", parser.ChunkReasonReadError, got)
		}
		if desc := rep.Describe(); !strings.Contains(desc, "absent.go") {
			t.Errorf("the description must NAME the dropped file; got %q", desc)
		}
	})

	t.Run("cancellation_counts_the_whole_remainder", func(t *testing.T) {
		dir := t.TempDir()
		files := make([]string, 0, 6)
		for _, name := range []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"} {
			writeTestFile(t, filepath.Join(dir, name), "package p\n")
			files = append(files, name)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, rep, err := parser.ChunkFiles(ctx, dir, files)
		if err == nil {
			t.Fatal("a cancelled chunk pass must return the context error")
		}
		// THE COUNT IS A FILE COUNT, NOT AN EVENT COUNT. One cancellation abandoned
		// six files, and a report reading 1 would look small enough to ignore —
		// which is worse than no report at all, because that number is the
		// operator's whole diagnosis under the fail-the-collect contract.
		if got := rep.DroppedByReason[parser.ChunkReasonCanceled]; got != len(files) {
			t.Fatalf("expected all %d abandoned files counted, got %d (%s)",
				len(files), got, rep.Describe())
		}
		if got := rep.Dropped(); got != len(files) {
			t.Fatalf("expected Dropped()==%d, got %d", len(files), got)
		}
	})
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
