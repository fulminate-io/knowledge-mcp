// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// lookupCodePostPopulate wraps postpopulate.Lookup("code") for the registry
// regression test in TestCodeCollector_Collect.
func lookupCodePostPopulate() (postpopulate.Func, bool) {
	return postpopulate.Lookup("code")
}

func TestCodeCollector_Name(t *testing.T) {
	c := &CodeCollector{}
	if got := c.Name(); got != "code" {
		t.Errorf("Name() = %q, want %q", got, "code")
	}
}

func TestCodeCollector_Collect(t *testing.T) {
	dir := t.TempDir()

	// Create a minimal Go file so tree-sitter has something to parse.
	writeFile(t, filepath.Join(dir, "main.go"), `package main

func main() {
	println("hello")
}
`)

	c := &CodeCollector{}
	result, err := c.Collect(newTestCtx(t), dir, collector.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}

	// Verify result shape.
	if result.GraphType != kgtypes.GraphCode {
		t.Errorf("GraphType = %q, want %q", result.GraphType, kgtypes.GraphCode)
	}
	if result.GraphName != filepath.Base(dir) {
		t.Errorf("GraphName = %q, want %q", result.GraphName, filepath.Base(dir))
	}
	// PostPopulate lives in the postpopulate registry (name-keyed) so it
	// can cross the wire. The "code" PostPopulate is registered from this
	// package's register_postpopulate.go init(); verify it is present.
	if _, ok := lookupCodePostPopulate(); !ok {
		t.Error(`postpopulate.Lookup("code") returned false`)
	}

	// Should have at least the file node and one function node.
	if len(result.Nodes) < 2 {
		t.Errorf("expected at least 2 nodes (file + function), got %d", len(result.Nodes))
	}

	// Check that we got a file node.
	var hasFile bool
	for _, n := range result.Nodes {
		if kgtypes.NodeType(n.Type) == kgtypes.NodeFile {
			hasFile = true
			break
		}
	}
	if !hasFile {
		t.Error("expected at least one file node in results")
	}

	// BatchEdges should have FromIdx/ToIdx=-1 (pre-resolved).
	for i, e := range result.Edges {
		if e.FromIdx != -1 || e.ToIdx != -1 {
			t.Errorf("Edges[%d]: expected FromIdx/ToIdx=-1 for pre-resolved edges, got %d/%d", i, e.FromIdx, e.ToIdx)
		}
	}
}

// TestCodeCollector_Collect_RejectsSystemPaths pins the system-path
// rejection added in 36cdbb01. /etc on macOS carries a Makefile (BSD
// convention) so the marker check alone wouldn't reject it; the prefix
// list runs first and catches the system tree regardless of incidental
// markers.
func TestCodeCollector_Collect_RejectsSystemPaths(t *testing.T) {
	for _, id := range []string{"/etc", "/usr", "/sys", "/proc", "/bin", "/sbin", "/dev", "/boot"} {
		t.Run("id="+id, func(t *testing.T) {
			c := &CodeCollector{}
			_, err := c.Collect(newTestCtx(t), id, collector.CollectOptions{})
			if err == nil {
				t.Fatalf("Collect(%q) returned nil error; expected system-path rejection", id)
			}
			if !strings.Contains(err.Error(), "system path") {
				t.Fatalf("Collect(%q) error=%q; expected system-path rejection mentioning 'system path'", id, err)
			}
		})
	}
}

// TestCodeCollector_Collect_RejectsEmptyDir confirms the marker / source
// fallback rejects directories that look nothing like a code repo. A
// fresh tmpdir with no files and no markers must error with the
// "no repo marker" message rather than silently creating an empty graph.
func TestCodeCollector_Collect_RejectsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	c := &CodeCollector{}
	_, err := c.Collect(newTestCtx(t), dir, collector.CollectOptions{})
	if err == nil {
		t.Fatal("Collect on empty dir returned nil error; expected marker-fallback rejection")
	}
	if !strings.Contains(err.Error(), "no repo marker") {
		t.Fatalf("error=%q; expected 'no repo marker' message", err)
	}
}

// TestCodeCollector_Collect_AcceptsMarkerFile confirms a directory with
// a recognized repo marker file (e.g. go.mod) but no .git dir still
// passes — handles the common case of a sub-tree collected for a
// single-language graph.
func TestCodeCollector_Collect_AcceptsMarkerFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/probe\n")
	c := &CodeCollector{}
	_, err := c.Collect(newTestCtx(t), dir, collector.CollectOptions{})
	if err != nil {
		t.Fatalf("Collect on dir with go.mod marker errored: %v", err)
	}
}

func TestCodeCollector_Collect_RejectsRelativeID(t *testing.T) {
	cases := []string{
		".",
		"..",
		"./foo",
		"../foo",
		"foo",
		"foo/bar",
		"",
	}
	c := &CodeCollector{}
	for _, id := range cases {
		t.Run("id="+id, func(t *testing.T) {
			result, err := c.Collect(newTestCtx(t), id, collector.CollectOptions{})
			if err == nil {
				t.Fatalf("Collect(%q) returned nil error; expected absolute-path rejection. result=%+v", id, result)
			}
		})
	}
}

func TestCodeCollector_Collect_Progress(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")

	var messages []string
	c := &CodeCollector{}
	_, err := c.Collect(newTestCtx(t), dir, collector.CollectOptions{
		OnProgress: func(current, total int, message string) {
			messages = append(messages, message)
		},
	})
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}

	if len(messages) < 2 {
		t.Errorf("expected at least 2 progress messages, got %d: %v", len(messages), messages)
	}
}
