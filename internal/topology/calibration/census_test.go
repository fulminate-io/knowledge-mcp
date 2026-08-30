// SPDX-License-Identifier: Apache-2.0

package calibration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// envMirrorRoot names a local clone of the public mirror. The census walk is
// gated on it; the partition logic is proved without it.
const envMirrorRoot = "KNOWLEDGE_MCP_ROOT"

// existsFunc answers whether a path in THIS repo's coordinates is on disk.
//
// IT IS INJECTED RATHER THAN CALLED DIRECTLY, and that is what makes the
// hermetic companion correct in the public mirror's CI. A fixture that stat'd
// cmd/knowledge/internal/tools/tools_logs_search.go would find it here and NOT
// in the mirror — where this package arrives at internal/topology/calibration
// and the whole cmd/knowledge/ prefix is gone — so the fixture's "exists"
// member would land in DRIFT and the test would fail deterministically in the
// one environment we least want a red.
type existsFunc func(internalPath string) bool

// partitionMirrorPaths splits mirror paths three ways: MAPPED (a member rule
// applies and the counterpart is on disk here), MIRROR-ONLY (no member rule
// applies), and DRIFT (a member rule applies but the counterpart is absent).
//
// DRIFT IS NOT A MAPPING FAILURE. It means the mirror is behind this repo,
// which is the normal state between syncs. Folding it into the mirror-only
// bucket would turn a property of sync lag into an accusation against the rule
// table, and the census's one hard assertion is about that table.
func partitionMirrorPaths(mirrorPaths []string, exists existsFunc) (mapped, mirrorOnly, drift []string, err error) {
	for _, p := range mirrorPaths {
		internal, class, mapErr := MapMirrorPath(p)
		if mapErr != nil {
			// A malformed path is not a fourth bucket. The census enumerates a
			// git working tree, so an input the mapper refuses means the
			// enumeration is wrong, and continuing would score a partition
			// derived from a tree we cannot read.
			return nil, nil, nil, mapErr
		}
		if class != PathMapped {
			mirrorOnly = append(mirrorOnly, p)
			continue
		}
		if exists(internal) {
			mapped = append(mapped, p)
			continue
		}
		drift = append(drift, p)
	}
	return mapped, mirrorOnly, drift, nil
}

// repoRoot resolves the git repo root so the census oracle anchors to absolute
// paths regardless of the test's working directory. Copied in shape from the
// engine package's helper of the same name, which exists for the same reason.
func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// TestMirrorTreeCensus_HermeticFixture proves the partition LOGIC against a
// map-backed oracle. No env, no network, no daemon, NO FILESYSTEM — so its
// result is identical in this repo, in the public mirror's CI, and on an
// offline machine.
func TestMirrorTreeCensus_HermeticFixture(t *testing.T) {
	line := "root=fixture (partition logic only)"
	defer func() { t.Logf("mirror-census: %s", line) }()

	const (
		wantMapped     = "internal/tools/tools_logs_search.go"
		wantDrift      = "internal/does_not_exist_xyz.go"
		wantMirrorOnly = ".github/workflows/ci.yml"
	)
	// Seeded so exactly one fixture path resolves. Every other lookup is false,
	// which is what separates the drift member from the mapped one.
	stub := map[string]bool{"cmd/knowledge/" + wantMapped: true}
	exists := func(p string) bool { return stub[p] }

	fixture := []string{wantMapped, wantDrift, wantMirrorOnly}
	mapped, mirrorOnly, drift, err := partitionMirrorPaths(fixture, exists)
	if err != nil {
		t.Fatalf("partition the fixture: %v", err)
	}
	line = fmt.Sprintf("root=fixture paths=%d mapped=%d unmapped=%d drift=%d",
		len(fixture), len(mapped), len(mirrorOnly), len(drift))

	for _, c := range []struct {
		name string
		got  []string
		want string
	}{
		{"mapped", mapped, wantMapped},
		{"mirror-only", mirrorOnly, wantMirrorOnly},
		{"drift", drift, wantDrift},
	} {
		if len(c.got) != 1 {
			t.Fatalf("%s bucket holds %d members, want exactly 1: %v", c.name, len(c.got), c.got)
		}
		if c.got[0] != c.want {
			t.Fatalf("%s bucket holds %q, want %q", c.name, c.got[0], c.want)
		}
	}
}

// TestMirrorTreeCensus walks a real mirror clone and asserts the member rules
// are TOTAL over its Go surface. This is the only place in the package that
// stats anything.
//
// The numbers are RE-DERIVED, never pinned. The tracked-file count and the
// drift count both move with every sync, and the drift count in particular is a
// property of how far behind the mirror is rather than of the code under test.
// The one hard assertion is that no .go file falls outside the rule table.
//
// UNVERIFIED IN CI: that the member rules are total over the mirror's REAL Go
// surface. This test needs a mirror clone and runs on no standing gate, so a
// sync that adds a Go file outside every member rule is caught by an operator
// running it deliberately and by nothing else. What IS proven unconditionally is
// the PARTITION LOGIC — TestMirrorTreeCensus_HermeticFixture drives the
// mapped / mirror-only / drift split against a stubbed oracle with no
// filesystem at all.
func TestMirrorTreeCensus(t *testing.T) {
	line := "root=unset (set " + envMirrorRoot + " to run)"
	defer func() { t.Logf("mirror-census: %s", line) }()

	mirrorRoot := os.Getenv(envMirrorRoot)
	if mirrorRoot == "" {
		t.Skipf("set %s to a local mirror clone to run the census", envMirrorRoot)
	}
	// Validate before handing an environment value to a subprocess: clean it,
	// then require it to name a real directory. The value is named by its
	// variable rather than printed, so a failure here publishes no local path.
	mirrorRoot = filepath.Clean(mirrorRoot)
	if info, statErr := os.Stat(mirrorRoot); statErr != nil || !info.IsDir() {
		t.Fatalf("%s does not name an existing directory", envMirrorRoot)
	}

	// THE ORACLE'S ROOT MUST BE BOUND EXPLICITLY. MapMirrorPath returns paths
	// relative to THIS repo's root, while the test's working directory is the
	// package directory five levels below it. Joining a mirror-derived path
	// onto the test's cwd resolves nothing, every file lands in drift, and the
	// drift report silently becomes the whole census.
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	exists := func(p string) bool {
		// MapMirrorPath already refuses an absolute or escaping input, so p is
		// repo-relative by construction; Clean makes that explicit at the one
		// place in this package that touches the filesystem.
		_, statErr := os.Stat(filepath.Join(root, filepath.Clean(p)))
		return statErr == nil
	}

	// git ls-files rather than a filesystem walk, so the mirror's .gitignore
	// and untracked build output do not enter the census.
	// The validated root is carried as the child's working DIRECTORY rather
	// than spliced into argv, so no environment value becomes a command
	// argument.
	lsFiles := exec.Command("git", "ls-files", "*.go")
	lsFiles.Dir = mirrorRoot
	out, err := lsFiles.Output()
	if err != nil {
		t.Fatalf("list tracked Go files under %s: %v", filepath.Base(mirrorRoot), err)
	}
	var goFiles []string
	for p := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if p != "" {
			goFiles = append(goFiles, p)
		}
	}
	if len(goFiles) == 0 {
		t.Fatalf("%s holds no tracked Go files, so this census measured nothing", filepath.Base(mirrorRoot))
	}

	mapped, mirrorOnly, drift, err := partitionMirrorPaths(goFiles, exists)
	if err != nil {
		t.Fatalf("partition the mirror's Go surface: %v", err)
	}
	// ONLY THE BASENAME IS STAMPED, never an operator's absolute path: this
	// file ships to the public mirror and an absolute root would publish a home
	// directory layout to every reader of the OSS repo.
	line = fmt.Sprintf("root=%s go_files=%d mapped=%d unmapped=%d drift=%d",
		filepath.Base(mirrorRoot), len(goFiles), len(mapped), len(mirrorOnly), len(drift))

	if len(mirrorOnly) > 0 {
		shown := mirrorOnly
		if len(shown) > 20 {
			shown = shown[:20]
		}
		t.Fatalf("%d of %d tracked Go files fall outside every member rule; the mirror's Go surface is exactly the subtree the sync script copies, so each is a hole in the rule table: %v",
			len(mirrorOnly), len(goFiles), shown)
	}

	// Drift is REPORTED, never failed. The mirror being behind this repo is a
	// normal state between syncs.
	if len(drift) > 0 {
		shown := drift
		if len(shown) > 20 {
			shown = shown[:20]
		}
		t.Logf("sync drift: %d of %d mapped files have no counterpart here (the mirror is behind); first %d: %v",
			len(drift), len(goFiles), len(shown), shown)
	}
}
