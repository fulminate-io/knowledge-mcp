// SPDX-License-Identifier: Apache-2.0

package codexassets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the tree carrying the canonical .claude fixtures by walking
// up from this test's working directory to the first ancestor holding BOTH a
// go.mod and .claude/agents.
//
// THIS REPLACES A DELIBERATE SKIP, AND OVERTURNS IT RATHER THAN RELAXING IT.
// The previous form joined a fixed four parents and skipped when .claude/agents
// was not there. Four parents is the repo root here and a directory ABOVE the
// mirror root in the published tree, where the sync script copies
// cmd/knowledge/internal to internal/ — so the four fixture tests hanging off
// this helper skipped silently in the mirror and asserted nothing about the
// assets that tree actually ships, while the package still exited 0. The mirror
// carries .claude/agents beside its own go.mod, so a walk for the fixture tree
// itself resolves in both layouts and the tests RUN in both. It fails loudly at
// the filesystem root rather than skipping, because a fixture the walk cannot
// find is now a defect in either layout.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			if _, statErr := os.Stat(filepath.Join(dir, ".claude", "agents")); statErr == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("walked to the filesystem root from the test working directory without finding .claude/agents beside a go.mod")
		}
		dir = parent
	}
}

// parseFrontmatter on .claude/agents/planner.md
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
