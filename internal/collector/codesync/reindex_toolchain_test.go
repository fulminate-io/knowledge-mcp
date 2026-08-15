// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestGoToolchainResolvesUnderMinimalPath drives the toolchain resolver through
// the failure the launchd-managed daemon hit: a `go` that is unreachable through
// the PROCESS PATH. Under launchd the daemon runs with
// PATH=/usr/bin:/bin:/usr/sbin:/sbin and an empty GOROOT, because its plist
// declares no EnvironmentVariables. That environment is the reason 94
// consecutive collects logged `exec: "go": executable file not found in $PATH`
// and degraded to pure tree-sitter while every test in this package stayed
// green, because every test run has `go` on PATH.
//
// THE ABSENCE IS CONSTRUCTED, NOT ASSUMED. An earlier form of this test spelled
// the daemon's literal minimal PATH and relied on no `go` being reachable from
// those four directories. That holds on the macOS hosts the daemon runs on and
// does NOT hold on the Linux CI runners, where a `go` IS resolvable from the
// minimal PATH — so the red control could not fail there and the test went red
// for an environmental reason rather than a behavioral one. PATH now points at
// a directory this test creates and never populates, which makes the absence a
// property of the fixture on every platform. The daemon's literal PATH was never
// what the resolver reacts to; an unreachable `go` is, and that is what is
// reproduced here.
//
// Three assertions in one run, and the first is what makes the third mean
// anything:
//
//	(1) RED CONTROL — a packages.Load over a one-package fixture FAILS while
//	    PATH holds no `go`. Without it the test would pass regardless of whether
//	    the resolver works, because `go` might simply have been reachable all
//	    along.
//	(2) The resolver still reports found, from OUTSIDE that PATH.
//	(3) After prepending its directory to the PROCESS PATH, the SAME load
//	    succeeds.
//
// Leg (2)'s toolchain is constructed for the same reason leg (1)'s absence is —
// see resolveOffPathToolchain.
//
// Step (3) prepends through t.Setenv rather than through ensureGoOnPath because
// ensureGoOnPath is sync.Once guarded: in a test binary where another test has
// already called it under a normal PATH, the Once is spent and no prepend would
// happen, making this test's result depend on test order. What is under test
// here is the resolution and the process-PATH lever, which is exactly what
// ensureGoOnPath performs inside its Once. For the same reason the Once's state
// cannot leak INTO this test either: the PATH it may already have mutated is
// overwritten wholesale below, and t.Setenv forbids t.Parallel, so no concurrent
// test can observe the swap.
//
// t.Setenv restores PATH and GOROOT, and resolveOffPathToolchain restores HOME,
// so the test leaves no process-global residue for its neighbors.
func TestGoToolchainResolvesUnderMinimalPath(t *testing.T) {
	// Captured BEFORE PATH is replaced: this is the toolchain the constructed
	// off-PATH home in leg (2) points at.
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("want: a Go toolchain on the test runner's own PATH, which is what the fixture below "+
			"is loaded with and what leg (2) resolves to; got: %v", err)
	}

	root := t.TempDir()
	writePopulateFixtureFile(t, filepath.Join(root, "go.mod"), "module example.com/tc\n\ngo 1.24\n")
	writePopulateFixtureFile(t, filepath.Join(root, "pkg", "p.go"), `package pkg

func F() int {
	return 1
}
`)

	// A directory this test creates and never writes a `go` into. Absence by
	// construction, rather than by an assumption about the host.
	pathWithoutGo := t.TempDir()
	t.Setenv("GOROOT", "")
	t.Setenv("PATH", pathWithoutGo)

	// (1) RED CONTROL.
	err = loadOnePackage(root)
	if err == nil {
		t.Fatalf("want: packages.Load to FAIL while PATH is %s, a directory this test created empty, "+
			"so the success below proves the resolver did the work; got: it succeeded, so something "+
			"resolved a `go` this PATH cannot reach and the run is vacuous", pathWithoutGo)
	}
	t.Logf("RED_UNDER_CONSTRUCTED_ABSENCE err: %v", err)

	// (2) RESOLUTION.
	dir, found := resolveOffPathToolchain(t, realGo)
	if !found {
		t.Fatalf("want: resolveGoToolchainDir to locate a Go toolchain outside the process PATH; got: none")
	}
	t.Logf("RESOLVER found=true dir=%s", dir)

	// (3) THE PROCESS-PATH LEVER.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+pathWithoutGo)
	if err := loadOnePackage(root); err != nil {
		t.Fatalf("want: the same packages.Load to SUCCEED after prepending %s to the process PATH; got: %v", dir, err)
	}
	t.Logf("GREEN_AFTER_PROCESS_PATH_PREPEND dir=%s", dir)
}

// resolveOffPathToolchain calls resolveGoToolchainDir with HOME pointed at a
// constructed home whose $HOME/go/bin holds a symlink to realGo — a genuine
// toolchain reachable only from OUTSIDE the process PATH, which is precisely the
// shape resolveGoToolchainDir exists to find.
//
// WHY THIS LEG IS CONSTRUCTED TOO. resolveGoToolchainDir probes $GOROOT/bin,
// then four fixed install locations, then $HOME/go/bin. On the macOS hosts the
// daemon runs on, one of the fixed locations exists and wins, so the branch that
// actually rescues the daemon is still the branch exercised there — this home is
// a floor, not a replacement. Nothing guarantees any of those absolute paths
// exists on a CI runner, whose `go` can sit entirely outside the list (a
// toolcache directory, say). Leaving this leg to the host would move the same
// environmental failure the constructed PATH just fixed one assertion further
// down rather than removing it.
//
// HOME is restored before returning, so the packages.Load that verifies the
// prepend runs under the real HOME and therefore the real build and module
// caches — a fresh HOME would put a cold cache, and any toolchain the real one
// resolves through, in the path of an assertion about PATH.
func resolveOffPathToolchain(t *testing.T, realGo string) (dir string, found bool) {
	t.Helper()

	home := t.TempDir()
	binDir := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatalf("constructing the off-PATH toolchain home: %v", err)
	}
	if err := os.Symlink(realGo, filepath.Join(binDir, "go")); err != nil {
		t.Fatalf("linking %s into the off-PATH toolchain home: %v", realGo, err)
	}

	prev, had := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("pointing HOME at the off-PATH toolchain home: %v", err)
	}
	defer func() {
		restore := os.Unsetenv
		if had {
			restore = func(string) error { return os.Setenv("HOME", prev) }
		}
		if err := restore("HOME"); err != nil {
			t.Fatalf("restoring HOME: %v", err)
		}
	}()

	return resolveGoToolchainDir()
}

// loadOnePackage runs the cheapest packages.Load that still requires the `go`
// binary, and folds a per-package error into the returned error so a load that
// "succeeds" into an error package is not read as success.
func loadOnePackage(dir string) error {
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles,
		Dir:  dir,
	}, "./...")
	if err != nil {
		return err
	}
	if len(pkgs) == 0 {
		return errors.New("no packages loaded")
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return p.Errors[0]
		}
	}
	return nil
}
