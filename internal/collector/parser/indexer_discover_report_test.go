// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInit makes dir a git repo so discovery takes the git path. No commit is
// needed: `git ls-files --others --exclude-standard` lists untracked files, so
// an initialized repo with files in it is enough, and the test needs no
// committer identity.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "--quiet")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
}

// declineFixture writes exactly one file per rule the chain can decline, plus
// one file that survives every rule, so a report over it has a known
// decomposition of one each.
//
// vendor/ deliberately does NOT appear: every skipPathComponents name is also a
// skipDirs name, so one vendored file would be charged to skip_path_component on
// the git path and skip_dir on the walk — two rules, one file, and a per-rule
// count of one no longer distinguishes a working attribution from a double
// count. node_modules/ alone carries both roles, one per path.
func declineFixture(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "keep.go"), "package main")
	writeFile(t, filepath.Join(dir, "README.md"), "# doc")                             // skip_extension
	writeFile(t, filepath.Join(dir, "go.sum"), "h1:abc")                               // skip_lockfile
	writeFile(t, filepath.Join(dir, "types.d.ts"), "export type A = 1")                // skip_dts
	writeFile(t, filepath.Join(dir, "api.pb.go"), "package api")                       // skip_generated_go
	writeFile(t, filepath.Join(dir, "data.csv"), "a,b,c")                              // skip_unknown_lang
	writeFile(t, filepath.Join(dir, "huge.go"), strings.Repeat("// pad\n", 100_000))   // skip_too_large
	writeFile(t, filepath.Join(dir, "node_modules", "p", "i.js"), "module.exports={}") // skip_dir on the walk, skip_path_component on git
}

// TestDiscoverFilesReporting_NamesPathAndRules pins the property the report
// exists for: a zero on a rule is only readable once the caller knows which
// discovery path ran. The two paths do not run the same chain, so the same zero
// means "declined nothing" under one and "never executed" under the other — and
// the report must render those differently.
func TestDiscoverFilesReporting_NamesPathAndRules(t *testing.T) {
	t.Run("walk path names itself and seeds only its own rules", func(t *testing.T) {
		dir := t.TempDir()
		declineFixture(t, dir)

		files, rep, err := DiscoverFilesReporting(t.Context(), dir, DiscoveryOptions{})
		if err != nil {
			t.Fatalf("DiscoverFilesReporting: %v", err)
		}
		if rep.DiscoveryPath != DiscoveryPathWalk {
			t.Fatalf("discovery path = %q, want %q (a non-git dir must take the walk)", rep.DiscoveryPath, DiscoveryPathWalk)
		}
		if got := len(files); got != 1 || files[0] != "keep.go" {
			t.Fatalf("included = %v, want exactly [keep.go]", files)
		}
		// skip_dir is reachable ONLY here, and it fires: one entry naming the
		// pruned directory rather than one per file beneath it.
		if got := rep.ExcludedByRule[RuleDir]; got != 1 {
			t.Errorf("%s = %d, want 1 (the pruned directory, counted once)", RuleDir, got)
		}
		// skip_path_component is seeded, runs, and declines nothing: skipDirs
		// pruned node_modules/ before isIndexable could charge it. That is a
		// truthful zero, and it is why "seeded at zero" and "absent" must differ.
		if _, seeded := rep.ExcludedByRule[RulePathComponent]; !seeded {
			t.Errorf("%s must be seeded on the walk path — it is evaluated there", RulePathComponent)
		}
		if got := rep.ExcludedByRule[RulePathComponent]; got != 0 {
			t.Errorf("%s = %d, want 0 (skip_dir pre-empts it on the walk)", RulePathComponent, got)
		}
		for _, rule := range walkPathRules {
			if _, ok := rep.ExcludedByRule[rule]; !ok {
				t.Errorf("walk-path report is missing rule %s", rule)
			}
		}
	})

	t.Run("git path names itself and omits the rule it never runs", func(t *testing.T) {
		dir := t.TempDir()
		declineFixture(t, dir)
		gitInit(t, dir)

		files, rep, err := DiscoverFilesReporting(t.Context(), dir, DiscoveryOptions{})
		if err != nil {
			t.Fatalf("DiscoverFilesReporting: %v", err)
		}
		if rep.DiscoveryPath != DiscoveryPathGit {
			t.Fatalf("discovery path = %q, want %q", rep.DiscoveryPath, DiscoveryPathGit)
		}
		if got := len(files); got != 1 || files[0] != "keep.go" {
			t.Fatalf("included = %v, want exactly [keep.go]", files)
		}
		// THE DISTINCTION. skip_dir never executes on this path, so it is absent
		// rather than zero — a caller reading "0 directories excluded" on a git
		// repo would otherwise conclude the rule looked and found nothing.
		if _, present := rep.ExcludedByRule[RuleDir]; present {
			t.Errorf("%s must be ABSENT on the git path, not zero — git ls-files never consults skipDirs", RuleDir)
		}
		// And its counterpart fires here, where the walk had it pre-empted.
		if got := rep.ExcludedByRule[RulePathComponent]; got != 1 {
			t.Errorf("%s = %d, want 1 (node_modules/p/i.js)", RulePathComponent, got)
		}
		for _, rule := range gitPathRules {
			if _, ok := rep.ExcludedByRule[rule]; !ok {
				t.Errorf("git-path report is missing rule %s", rule)
			}
			if got := rep.ExcludedByRule[rule]; got != 1 {
				t.Errorf("%s = %d, want 1 — the fixture declines exactly one file per rule", rule, got)
			}
			if s := rep.ExcludedSamples[rule]; len(s) != 1 {
				t.Errorf("%s samples = %v, want one name", rule, s)
			}
			if rep.ExcludedTruncated[rule] {
				t.Errorf("%s truncated on a single-file decline", rule)
			}
		}
	})

	t.Run("lifted run is not a run that had nothing to decline", func(t *testing.T) {
		dir := t.TempDir()
		declineFixture(t, dir)

		files, rep, err := DiscoverFilesReporting(t.Context(), dir, DiscoveryOptions{LiftExclusions: true})
		if err != nil {
			t.Fatalf("DiscoverFilesReporting: %v", err)
		}
		if rep.DiscoveryPath != DiscoveryPathWalk+discoveryLifted {
			t.Fatalf("discovery path = %q, want %q", rep.DiscoveryPath, DiscoveryPathWalk+discoveryLifted)
		}
		// The counts read zero on BOTH a lifted run and a clean tree; the path
		// name is what separates them, so assert the widening too rather than
		// trusting a zero to mean anything by itself.
		for rule, n := range rep.ExcludedByRule {
			if n != 0 {
				t.Errorf("%s = %d on a lifted run, want 0 — nothing may be declined", rule, n)
			}
		}
		if len(files) <= 1 {
			t.Fatalf("lifted walk returned %d files, want more than the 1 the unlifted walk kept", len(files))
		}
		set := map[string]bool{}
		for _, f := range files {
			set[f] = true
		}
		for _, want := range []string{"keep.go", "README.md", "huge.go", filepath.Join("node_modules", "p", "i.js")} {
			if !set[want] {
				t.Errorf("lifted walk dropped %s", want)
			}
		}
	})

	t.Run("sample names are capped while counts stay exact", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "keep.go"), "package main")
		const declined = maxExclusionSamples + 3
		for i := range declined {
			writeFile(t, filepath.Join(dir, string(rune('a'+i))+".md"), "# doc")
		}

		_, rep, err := DiscoverFilesReporting(t.Context(), dir, DiscoveryOptions{})
		if err != nil {
			t.Fatalf("DiscoverFilesReporting: %v", err)
		}
		if got := rep.ExcludedByRule[RuleExtension]; got != declined {
			t.Errorf("%s = %d, want %d — the COUNT is exact and uncapped", RuleExtension, got, declined)
		}
		if got := len(rep.ExcludedSamples[RuleExtension]); got != maxExclusionSamples {
			t.Errorf("%s samples = %d, want the cap of %d", RuleExtension, got, maxExclusionSamples)
		}
		if !rep.ExcludedTruncated[RuleExtension] {
			t.Errorf("%s must report truncated once its declines outrun the sample cap", RuleExtension)
		}
		// Known negative through the same probe: a rule inside the budget is not
		// flagged, so the flag tracks the cap rather than being always-on.
		if rep.ExcludedTruncated[RuleUnknownLang] {
			t.Errorf("%s truncated though it declined nothing", RuleUnknownLang)
		}
	})
}
