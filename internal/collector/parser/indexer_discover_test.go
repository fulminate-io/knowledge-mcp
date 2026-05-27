// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverFiles(t *testing.T) {
	// Create a temp repo structure.
	dir := t.TempDir()
	// Supported Go file.
	writeFile(t, filepath.Join(dir, "main.go"), "package main")
	// Test file (should be included — discoverFiles doesn't skip tests, chunker can).
	writeFile(t, filepath.Join(dir, "main_test.go"), "package main")
	// Supported TypeScript file.
	writeFile(t, filepath.Join(dir, "src", "app.ts"), "const x = 1")
	// Unsupported file (no tree-sitter grammar).
	writeFile(t, filepath.Join(dir, "data.csv"), "a,b,c")
	// Generated Go file (should be skipped).
	writeFile(t, filepath.Join(dir, "api.pb.go"), "package api")
	writeFile(t, filepath.Join(dir, "model_gen.go"), "package gen")
	writeFile(t, filepath.Join(dir, "model_generated.go"), "package gen")
	// File in skipped directory.
	writeFile(t, filepath.Join(dir, "node_modules", "pkg", "index.js"), "module.exports = {}")
	writeFile(t, filepath.Join(dir, "vendor", "lib.go"), "package vendor")
	writeFile(t, filepath.Join(dir, ".git", "config"), "[core]")

	files, err := DiscoverFiles(t.Context(), dir)
	if err != nil {
		t.Fatalf("DiscoverFiles: %v", err)
	}

	// Check expected files are found.
	fileSet := make(map[string]bool)
	for _, f := range files {
		fileSet[f] = true
	}

	if !fileSet["main.go"] {
		t.Error("expected main.go to be discovered")
	}
	if !fileSet["main_test.go"] {
		t.Error("expected main_test.go to be discovered")
	}
	if !fileSet[filepath.Join("src", "app.ts")] {
		t.Error("expected src/app.ts to be discovered")
	}

	// Check excluded files are NOT found.
	if fileSet["data.csv"] {
		t.Error("expected data.csv to be excluded (unsupported language)")
	}
	if fileSet["api.pb.go"] {
		t.Error("expected api.pb.go to be excluded (generated)")
	}
	if fileSet["model_gen.go"] {
		t.Error("expected model_gen.go to be excluded (generated)")
	}
	if fileSet["model_generated.go"] {
		t.Error("expected model_generated.go to be excluded (generated)")
	}
	for _, f := range files {
		if strings.Contains(f, "node_modules") {
			t.Error("expected node_modules to be excluded")
		}
		if strings.Contains(f, "vendor") {
			t.Error("expected vendor to be excluded")
		}
		if strings.Contains(f, ".git") {
			t.Error("expected .git to be excluded")
		}
	}
}

func TestDiscoverFilesLargeFileSkipped(t *testing.T) {
	dir := t.TempDir()
	// Normal file.
	writeFile(t, filepath.Join(dir, "small.go"), "package main")
	// Large file (>500KB).
	largeContent := strings.Repeat("// line\n", 100000) // ~800KB
	writeFile(t, filepath.Join(dir, "large.go"), largeContent)

	files, err := DiscoverFiles(t.Context(), dir)
	if err != nil {
		t.Fatalf("DiscoverFiles: %v", err)
	}

	fileSet := make(map[string]bool)
	for _, f := range files {
		fileSet[f] = true
	}

	if !fileSet["small.go"] {
		t.Error("expected small.go")
	}
	if fileSet["large.go"] {
		t.Error("expected large.go to be skipped (>500KB)")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
