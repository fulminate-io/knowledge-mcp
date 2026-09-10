// SPDX-License-Identifier: Apache-2.0

package codexassets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claudeAssetsLink is the canonical .claude tree, READ THROUGH THIS PACKAGE'S
// OWN testdata LINK. It is a DIRECTORY link because the four fixture tests in
// this package read many files under it: every agent definition and every
// skill's SKILL.md.
//
// THE LINK IS A TEST-CACHE FENCE, and it REPLACES a walk-up resolver that could
// not be one. `go test` keys a package's stored result on the files a run opened
// and DROPS any opened name that does not resolve inside the tested package's
// own module root. The previous helper walked up to the tree carrying both a
// go.mod and .claude/agents, which here is the REPO root — above this module —
// so not one agent definition or skill page was in this package's key: editing
// an agent's frontmatter and re-running returned `ok (cached)`, a stored PASS
// for the tests whose entire subject is that frontmatter. Names under this
// package's own testdata are inside cmd/knowledge, so the go tool records them,
// and os.Open follows the link so each file is tracked at its TARGET's size and
// modification time.
//
// THE WALK-UP IT REPLACES WAS SOLVING A REAL PROBLEM and the link solves it the
// same way: the previous form before that joined a fixed four parents and
// skipped when .claude/agents was not there, which silently skipped every
// fixture test in the published mirror, where the sync script maps
// cmd/knowledge/internal to internal/ and the same tree sits two segments
// higher. The link is re-pointed by scripts/sync-to-oss.sh for exactly that
// offset, so the tests RUN in both layouts and are cache-visible in both.
const claudeAssetsLink = "testdata/dotclaude"

// claudeAssetsTarget is what the link must resolve to. Asserted separately, and
// never used to READ: resolving the link first names the repo-root path again,
// which is outside this module, and undoes the fence.
const claudeAssetsTarget = ".claude"

// claudeAssetsFileFloor is the KNOWN POSITIVE on the directory link, in the
// shape cmd/server-bench's source fence uses. A directory link that resolved to
// an empty or wrong tree lets every fixture test below skip its loop body and
// pass having read nothing, which fails exactly as silently as no link at all.
// The floor is far below the real count and catches that case.
const claudeAssetsFileFloor = 4

// repoRoot is the tree the fixture reads are rooted at: this package's own
// testdata link rather than an ancestor found by walking.
func repoRoot(t *testing.T) string {
	t.Helper()
	return claudeAssetsLink
}

// TestClaudeAssetsLinkResolves is the PIN on the fence, with the floor a
// directory link needs. Two mutations must turn it red: replacing the link with
// a regular file or an empty directory, and pointing it anywhere that does not
// carry the agent definitions.
func TestClaudeAssetsLinkResolves(t *testing.T) {
	info, err := os.Lstat(claudeAssetsLink)
	if err != nil {
		t.Fatalf("%s must exist — it is what puts the canonical assets in this package's test-cache key: %v", claudeAssetsLink, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink (a checkout without symlink support materializes it as a text stub); "+
			"every fixture test here would then read a path this module's cache key cannot see", claudeAssetsLink)
	}
	// Abs BEFORE EvalSymlinks: a relative name yields a relative result, and the
	// suffix check would compare the link's own target text.
	abs, err := filepath.Abs(claudeAssetsLink)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatalf("%s must resolve; a dangling link fences nothing: %v", claudeAssetsLink, err)
	}
	if !strings.HasSuffix(filepath.ToSlash(resolved), claudeAssetsTarget) {
		t.Fatalf("%s resolves to %s, which is not the canonical .claude tree", claudeAssetsLink, resolved)
	}

	// THE FLOOR, and it is the assertion that distinguishes a working link from
	// one that opens nothing: count the agent definitions actually OPENED through
	// the link. os.ReadDir follows the link; filepath.WalkDir would not, because
	// it lstats its root and a symlinked root yields one non-directory entry.
	entries, err := os.ReadDir(filepath.Join(claudeAssetsLink, "agents"))
	if err != nil {
		t.Fatalf("read the agents directory through %s: %v", claudeAssetsLink, err)
	}
	opened := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		f, openErr := os.Open(filepath.Join(claudeAssetsLink, "agents", e.Name()))
		if openErr != nil {
			t.Fatalf("open %s through the link: %v", e.Name(), openErr)
		}
		_ = f.Close()
		opened++
	}
	if opened < claudeAssetsFileFloor {
		t.Fatalf("the link served %d agent definitions, below the floor of %d; a link that opens nothing fences nothing",
			opened, claudeAssetsFileFloor)
	}
}

// parseFrontmatter on .claude/agents/planner.md
// yields name='planner', non-empty description, model='opus', non-empty
// tools, skills present, body beginning with the role/precedence block.
func TestParseFrontmatter_PlannerAgent(t *testing.T) {
	root := repoRoot(t) // the in-module link at testdata/dotclaude, not an ancestor found by walking
	data, err := os.ReadFile(filepath.Join(root, "agents", "planner.md"))
	if err != nil {
		t.Fatalf("read planner.md: %v", err)
	}
	fm, body, ok := parseFrontmatter(string(data))
	if !ok {
		t.Fatalf("parseFrontmatter ok=false, want true")
	}
	if fm.Name != "planner" {
		t.Errorf("Name = %q, want planner", fm.Name)
	}
	if fm.Description == "" {
		t.Error("Description is empty, want non-empty")
	}
	if fm.Model != "opus" {
		t.Errorf("Model = %q, want opus", fm.Model)
	}
	if fm.Tools == "" {
		t.Error("Tools is empty, want non-empty")
	}
	if len(fm.Skills) == 0 {
		t.Error("Skills is empty, want present")
	}
	if strings.TrimSpace(body) == "" {
		t.Fatal("body is empty")
	}
	// Body begins with the role/precedence block — first non-empty line
	// is the <precedence> opener.
	trimmed := strings.TrimLeft(body, "\n")
	if !strings.HasPrefix(trimmed, "<precedence>") {
		t.Errorf("body does not begin with <precedence>; got prefix %q", firstLine(trimmed))
	}
}

// a file with no leading --- returns ok=false and
// body==full content (the parser's documented tolerant fallthrough).
func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	content := "# Just a heading\n\nSome body text with no frontmatter.\n"
	fm, body, ok := parseFrontmatter(content)
	if ok {
		t.Errorf("ok = true, want false for content with no leading ---")
	}
	if body != content {
		t.Errorf("body = %q, want full content %q", body, content)
	}
	if fm.Name != "" {
		t.Errorf("Name = %q, want empty on no-frontmatter fallthrough", fm.Name)
	}
}

// TestParseFrontmatter_UnterminatedFrontmatter covers the closeIdx==-1
// branch: a leading --- with no closing --- falls through to ok=false.
func TestParseFrontmatter_UnterminatedFrontmatter(t *testing.T) {
	content := "---\nname: x\nno closing marker\n"
	_, body, ok := parseFrontmatter(content)
	if ok {
		t.Error("ok = true, want false for unterminated frontmatter")
	}
	if body != content {
		t.Errorf("body = %q, want full content", body)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
