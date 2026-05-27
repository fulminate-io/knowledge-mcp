// SPDX-License-Identifier: Apache-2.0

package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestCrossCutting_GithubMaterializer_Constraints re-runs the per-step
// grep / dep-guard assertions from Phases 4 and 6 as a regression guard
// at the package test level. Each sub-test mirrors one of the locked
// constraints from the project plan node.
func TestCrossCutting_GithubMaterializer_Constraints(t *testing.T) {
	t.Run("reindex-flow grep clean", func(t *testing.T) {
		path := materializerPath(t)
		matches := grepCount(t, path,
			`codesync\.Sync`,
			`sink\.WriteAndReindex`,
			`store\.RunReindex`,
			`RunSummarize`,
			`RunEmbed`,
			`HandlerKind`,
		)
		if matches != 0 {
			t.Errorf("cmd/knowledge/internal/collector/web/github_materializer.go contains %d reindex-flow references; want 0", matches)
		}
	})

	t.Run("recursion-bound grep clean", func(t *testing.T) {
		path := materializerPath(t)
		matches := grepCount(t, path,
			`enqueueDiscovered`,
			`enqueueSeed`,
		)
		if matches != 0 {
			t.Errorf("cmd/knowledge/internal/collector/web/github_materializer.go contains %d recursion-bound references; want 0", matches)
		}
	})

	t.Run("server-side dep guard", func(t *testing.T) {
		// `go list -deps ./cmd/knowledge-server` must not depend on
		// collector/web (server is a pure receiver — github
		// materialization runs client-side via the codegraph collector
		// already linked into the knowledge binary).
		//
		// smacker/go-tree-sitter is ALSO forbidden: the ast MCP tool that
		// previously pulled it server-side has been moved client-side
		// (corrective rework on plan 0a8c4b30). Source files live on the
		// client's filesystem; the server has no repo (especially in
		// Fulminate Cloud remote-server mode), so AST parsing must happen
		// where the files do. The server keeps only the schema definition
		// (tools_ast.go::AstToolDef) so tools/list still advertises the
		// tool — the schema is metadata-only and pulls no tree-sitter dep.
		repoRoot := repoRoot(t)
		cmd := exec.Command("go", "list", "-deps", "./cmd/knowledge-server/...")
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list -deps failed: %v\n%s", err, out)
		}
		text := string(out)
		forbidden := []string{
			"github.com/fulminate-io/knowledge-mcp/internal/collector/web",
			"github.com/smacker/go-tree-sitter",
		}
		for _, dep := range forbidden {
			if strings.Contains(text, dep) {
				t.Errorf("cmd/knowledge-server unexpectedly depends on %q", dep)
			}
		}
	})

	t.Run("link discovery only in crawl helpers", func(t *testing.T) {
		// No file in cmd/knowledge/internal/collector/web/github_materializer*.go should
		// inspect pageRecord.InternalLinks or call parsePage. Link
		// discovery on materialized chunks would re-enter the BFS and
		// break the recursion bound.
		repoRoot := repoRoot(t)
		dir := filepath.Join(repoRoot, "cmd", "knowledge", "internal", "collector", "web")
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir: %v", err)
		}
		forbiddenSubstrings := []string{".InternalLinks", "parsePage"}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasPrefix(name, "github_materializer") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			body := string(data)
			for _, sub := range forbiddenSubstrings {
				if strings.Contains(body, sub) {
					t.Errorf("%s: contains forbidden link-discovery reference %q", name, sub)
				}
			}
		}
	})
}

// materializerPath returns the absolute path to
// cmd/knowledge/internal/collector/web/github_materializer.go from the repo
// root.
func materializerPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "cmd", "knowledge", "internal", "collector", "web", "github_materializer.go")
}

// repoRoot walks upward from the test cwd until it finds the workspace
// root, identified by go.work. It must anchor on go.work (not go.mod):
// since the 3-module split, the nearest go.mod above this package is the
// nested cmd/knowledge/go.mod, so a go.mod search would return the client
// module dir rather than the repo root — doubling paths and breaking the
// `go list -deps ./cmd/knowledge-server/...` dep guard.
func repoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := cwd
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate workspace root (go.work) from %q", cwd)
	return ""
}

// grepCount returns the total number of regex matches across all
// patterns in the given file. Patterns are OR'd via regex alternation.
func grepCount(t *testing.T, path string, patterns ...string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	combined := strings.Join(patterns, "|")
	re, err := regexp.Compile(combined)
	if err != nil {
		t.Fatalf("compile regex: %v", err)
	}
	return len(re.FindAllIndex(data, -1))
}
