// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

// criterion aea08598: install-codex-assets --skills-dest/--agents-dest
// writes skills/<n>/SKILL.md + agents/<n>.toml under the split roots.
func TestRunInstallCodexAssets_SplitRoots(t *testing.T) {
	skillsRoot := t.TempDir()
	agentsRoot := t.TempDir()
	agentsMD := filepath.Join(t.TempDir(), "AGENTS.md")

	if err := runInstallCodexAssets([]string{
		"--skills-dest", skillsRoot,
		"--agents-dest", agentsRoot,
		"--agents-md-dest", agentsMD,
	}); err != nil {
		t.Fatalf("runInstallCodexAssets: %v", err)
	}

	// At least one agent .toml directly under the agents root.
	agentEntries, err := os.ReadDir(agentsRoot)
	if err != nil {
		t.Fatalf("read agents root: %v", err)
	}
	gotTOML := false
	for _, e := range agentEntries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".toml" {
			gotTOML = true
		}
	}
	if !gotTOML {
		t.Error("no <name>.toml written under agents root")
	}

	// At least one skill dir with a SKILL.md under the skills root.
	skillEntries, err := os.ReadDir(skillsRoot)
	if err != nil {
		t.Fatalf("read skills root: %v", err)
	}
	gotSkill := false
	for _, e := range skillEntries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(skillsRoot, e.Name(), "SKILL.md")); err == nil {
				gotSkill = true
			}
		}
	}
	if !gotSkill {
		t.Error("no <name>/SKILL.md written under skills root")
	}
}

// criterion ae3380f5: --dry-run writes nothing.
func TestRunInstallCodexAssets_DryRunWritesNothing(t *testing.T) {
	skillsRoot := t.TempDir()
	agentsRoot := t.TempDir()
	agentsMDDir := t.TempDir()

	if err := runInstallCodexAssets([]string{
		"--skills-dest", skillsRoot,
		"--agents-dest", agentsRoot,
		"--agents-md-dest", filepath.Join(agentsMDDir, "AGENTS.md"),
		"--dry-run",
	}); err != nil {
		t.Fatalf("runInstallCodexAssets --dry-run: %v", err)
	}

	if n := countFiles(t, skillsRoot) + countFiles(t, agentsRoot) + countFiles(t, agentsMDDir); n != 0 {
		t.Errorf("--dry-run wrote %d files, want 0", n)
	}
}

// criterion ae3380f5: --diff prints NEW/diffs and writes nothing.
func TestRunInstallCodexAssets_DiffWritesNothing(t *testing.T) {
	skillsRoot := t.TempDir()
	agentsRoot := t.TempDir()
	agentsMDDir := t.TempDir()

	if err := runInstallCodexAssets([]string{
		"--skills-dest", skillsRoot,
		"--agents-dest", agentsRoot,
		"--agents-md-dest", filepath.Join(agentsMDDir, "AGENTS.md"),
		"--diff",
	}); err != nil {
		t.Fatalf("runInstallCodexAssets --diff: %v", err)
	}

	if n := countFiles(t, skillsRoot) + countFiles(t, agentsRoot) + countFiles(t, agentsMDDir); n != 0 {
		t.Errorf("--diff wrote %d files, want 0", n)
	}
}

// criterion 01c0f446: codexOutPath routes raw .claude paths to the split
// roots — skills verbatim, agents .md→.toml (the routing the subcommand
// depends on). printUnifiedDiff reuse is a source-level fact verified by
// install_codex_assets.go importing no second diff implementation.
func TestCodexOutPath_SplitRouting(t *testing.T) {
	dest := codexDest{skills: "/S", agents: "/A"}
	cases := []struct {
		embed string
		want  string
		ok    bool
	}{
		{"skills/research/SKILL.md", filepath.Join("/S", "research", "SKILL.md"), true},
		{"agents/planner.md", filepath.Join("/A", "planner.toml"), true},
		{"agents/planner.toml", "", false}, // non-.md under agents/ is skipped
		{"unknown/x", "", false},
	}
	for _, c := range cases {
		got, ok := codexOutPath(dest, c.embed)
		if ok != c.ok || got != c.want {
			t.Errorf("codexOutPath(%q) = (%q,%v), want (%q,%v)", c.embed, got, ok, c.want, c.ok)
		}
	}
}

func countFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			n += countFiles(t, filepath.Join(root, e.Name()))
		} else {
			n++
		}
	}
	return n
}
