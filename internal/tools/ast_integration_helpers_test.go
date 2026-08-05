// SPDX-License-Identifier: Apache-2.0

// ast_integration_helpers_test.go — fixture writer + JSON wire-shape
// decoder shared by ast_integration_test.go. Split from the integration
// file to keep both under the 300-line warn threshold.

package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// astIntegrationFixture writes a 3-file Go module under t.TempDir() and
// returns the directory. No code-graph population is needed (the client
// intercept hydrates against NoOpBackend).
//
// Fixture layout:
//
//	$repoDir/
//	  go.mod
//	  main.go              package main, func Main()
//	  lib/lib.go           package lib, func Open() / type T struct{}, func (t *T) Close() error
//	  lib/lib_test.go      package lib, func TestOpen(t *testing.T)
//
// The lib/ subdir lets us verify package_prefixes filtering. The
// lib_test.go file lets us verify include_tests defaulting (false drops it).
func astIntegrationFixture(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()

	mainGo := `package main

import "fix/lib"

func Main() {
	f, _ := lib.Open()
	defer f.Close()
}
`
	libGo := `package lib

type T struct{}

func Open() (*T, error) { return &T{}, nil }

func (t *T) Close() error { return nil }
`
	libTestGo := `package lib

import "testing"

func TestOpen(t *testing.T) {
	f, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
}
`
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module fix\n\ngo 1.21\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "main.go"), []byte(mainGo), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "lib"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "lib", "lib.go"), []byte(libGo), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "lib", "lib_test.go"), []byte(libTestGo), 0o600))
	return repoDir
}

// matchResultsShape decodes the LLM-facing MatchResults wire shape.
type matchResultsShape struct {
	Matches []struct {
		FilePath           string `json:"file_path"`
		StartLine          int    `json:"start_line"`
		EndLine            int    `json:"end_line"`
		EnclosingNodeID    string `json:"enclosing_node_id"`
		EnclosingSignature string `json:"enclosing_signature"`
		Captures           map[string]struct {
			Text string `json:"text"`
			Line int    `json:"line"`
		} `json:"captures"`
	} `json:"matches"`
	Total int `json:"total"`
	Stats struct {
		FilesScanned int `json:"files_scanned"`
		FilesSkipped int `json:"files_skipped"`
	} `json:"stats"`
	Hint string `json:"hint"`
}
