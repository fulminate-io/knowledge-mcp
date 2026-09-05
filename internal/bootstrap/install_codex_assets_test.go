// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// install-codex-assets --skills-dest/--agents-dest
// writes skills/<n>/SKILL.md + agents/<n>.toml under the split roots.
//
// It also carries the INSTALL-SEAM assertion for the resolved-paths
// preamble: a written agent .toml must name THIS run's actual skills
// root. That is the catcher for a translation wired to a constant or to
// an empty root — the seam a package-level translation test cannot see,
// because only the installer knows the resolved dest. The assertion is on
// the anchor text plus the root value rather than on the const, which is
// unexported in codexassets and unreachable from this package.
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
	gotTOML := ""
	for _, e := range agentEntries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".toml" {
			gotTOML = filepath.Join(agentsRoot, e.Name())
		}
	}
	if gotTOML == "" {
		t.Error("no <name>.toml written under agents root")
	} else {
		written, err := os.ReadFile(gotTOML) //nolint:gosec // path is a temp root join built by this test
		if err != nil {
			t.Fatalf("read written agent toml: %v", err)
		}
		if !strings.Contains(string(written), "Resolved install paths:") {
			t.Errorf("%s carries no resolved-paths preamble anchor:\n%s", gotTOML, written)
		}
		if !strings.Contains(string(written), skillsRoot) {
			t.Errorf("%s does not name this install's skills root %q — the translation was handed a constant or an empty root", gotTOML, skillsRoot)
		}
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

// --dry-run writes nothing.
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

// --diff prints NEW/diffs and writes nothing.
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

// codexOutPath routes raw .claude paths to the split
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

// TestCodexOutPath_GovernanceRoutesToSkillsRoot pins the routing of the
// FLAT governance file. It is its own named function rather than a row in
// TestCodexOutPath_SplitRouting's table on purpose: that table is flat
// (t.Errorf, no subtests), so an added row emits no PASS line of its own
// and its absence would be invisible to the criterion's anchored grep.
//
// Characterization guard: the routing already holds — codexOutPath
// prefix-matches "skills/" and does not care about nesting depth. What
// this test protects against is a later narrowing to <name>/SKILL.md,
// which would silently stop shipping the one file every agent def is
// mandated to read first.
func TestCodexOutPath_GovernanceRoutesToSkillsRoot(t *testing.T) {
	dest := codexDest{skills: "/S", agents: "/A"}
	want := filepath.Join("/S", "GOVERNANCE.md")
	got, ok := codexOutPath(dest, "skills/GOVERNANCE.md")
	if !ok || got != want {
		t.Errorf("codexOutPath(skills/GOVERNANCE.md) = (%q,%v), want (%q,true)", got, ok, want)
	}
}

// TestRunInstallCodexAssets_GovernanceUnderSkillsRoot drives a REAL
// split-root install and asserts the governance file lands under the
// SKILLS root and nowhere under the agents root. The negative half is
// load-bearing: codex's split roots are the shape in which a
// mis-routed flat file would still "be installed" while being
// unreachable from the skills root a translated agent's preamble names.
func TestRunInstallCodexAssets_GovernanceUnderSkillsRoot(t *testing.T) {
	skillsRoot := t.TempDir()
	agentsRoot := t.TempDir()
	agentsMD := filepath.Join(t.TempDir(), "AGENTS.md")

	if err := runInstallCodexAssets([]string{
		"--no-mcp",
		"--skills-dest", skillsRoot,
		"--agents-dest", agentsRoot,
		"--agents-md-dest", agentsMD,
	}); err != nil {
		t.Fatalf("runInstallCodexAssets: %v", err)
	}

	if _, err := os.Stat(filepath.Join(skillsRoot, "GOVERNANCE.md")); err != nil {
		t.Errorf("GOVERNANCE.md not written under skills root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(agentsRoot, "GOVERNANCE.md")); err == nil {
		t.Error("GOVERNANCE.md written under the agents root; want skills root only")
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
