// SPDX-License-Identifier: Apache-2.0

package codexassets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the knowledge repo root from this test file's
// location. The codexassets package lives at
// cmd/knowledge/internal/codexassets, so the root is four parents up.
// Used to read canonical .claude/ fixtures without hardcoding an
// absolute path.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// .../cmd/knowledge/internal/codexassets → up 4 to repo root.
	root := filepath.Clean(filepath.Join(wd, "..", "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, ".claude", "agents")); err != nil {
		t.Skipf("canonical .claude/agents not found at %s (skip in synced OSS tree): %v", root, err)
	}
	return root
}

// criterion e6fffdb7: parseFrontmatter on .claude/agents/planner.md
// yields name='planner', non-empty description, model='opus', non-empty
// tools, skills present, body beginning with the role/precedence block.
func TestParseFrontmatter_PlannerAgent(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".claude", "agents", "planner.md"))
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

// criterion 4e0b17e8: a file with no leading --- returns ok=false and
// body==full content (mirrors parseInstructionFrontmatter fallthrough).
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
