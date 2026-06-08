// SPDX-License-Identifier: Apache-2.0

// Command docgen is the thin executable entry point invoked by the //go:generate
// directive in package docgen. It resolves the repo-root docs/guides tree and
// calls docgen.Generate, exiting non-zero on any error so a failed regeneration
// (missing scaffold file or marker) surfaces loudly in `go generate` / CI.
//
// Keeping this a thin shim over the importable, testable docgen.Generate is the
// standard go:generate split: the directive runs the shim; unit tests call
// Generate directly against a temp tree.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/docgen"
)

func main() {
	root, err := resolveGuidesRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "docgen: %v\n", err)
		os.Exit(1)
	}
	if err := docgen.Generate(root); err != nil {
		fmt.Fprintf(os.Stderr, "docgen: %v\n", err)
		os.Exit(1)
	}
}

// resolveGuidesRoot returns the absolute path to the repo-root docs/guides tree.
//
// `go generate` evaluates the directive with the working directory set to the
// package dir (cmd/knowledge/internal/docgen), so the repo root is four levels
// up. We walk up from cwd looking for a docs/guides directory rather than
// hardcoding the hop count, so the shim also works when invoked directly from
// the repo root or the module dir.
func resolveGuidesRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	dir := cwd
	for {
		candidate := filepath.Join(dir, "docs", "guides")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate docs/guides walking up from %s", cwd)
		}
		dir = parent
	}
}
