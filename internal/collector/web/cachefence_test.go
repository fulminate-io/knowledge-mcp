// SPDX-License-Identifier: Apache-2.0

package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cachefence_test.go — THE TEST-CACHE FENCE for the server-side dep guard in
// cross_cutting_test.go, whose subject is a tree in ANOTHER module read by a
// CHILD PROCESS.
//
// THE DEFECT IT CLOSES, and it is two defects stacked. `go test` keys a
// package's stored result on the files a run opened, and (1) it DROPS any opened
// name that does not resolve inside the tested package's own module root, so
// cmd/knowledge-server could never enter this package's key by its real path;
// (2) the guard does not open that tree itself at all — it shells out to
// `go list -deps`, and the go tool records what the TEST PROCESS opened, never
// what a child did. Either alone is enough to make the guard blind. Adding a
// forbidden import to the server module and re-running this package returned
// `ok (cached)`: a stored PASS for the one test that would have caught it.
//
// THE REMEDY. cmd/knowledge/testdata/server-src is a link to the server module,
// and this fence opens every Go file under it. The names are inside
// cmd/knowledge, so the go tool records them; os.Stat follows the link, so each
// file is tracked at its TARGET's size and modification time.
//
// WHY THE LINK IS NOT UNDER THIS PACKAGE. This file's sibling cross_cutting_test.go
// SHIPS to the public mirror, which is the client module alone. A link under
// this package's own testdata would be copied there and dangle, naming a module
// the mirror does not carry. cmd/knowledge/testdata is outside the ship set, so
// the link stays here — and the fence's one call site sits after the mirror's
// own skip, so the mirror never reaches it.
const serverSrcLink = "../../../testdata/server-src"

// serverSrcFenceFloor is the KNOWN POSITIVE. A fence that opened nothing is
// indistinguishable from no fence and fails exactly as silently; the floor is
// far below the real count (the server module is in the thousands).
const serverSrcFenceFloor = 100

// fenceTestCacheOnServerTree opens every server-module Go file through this
// module's own testdata link.
//
// IT IS NEVER CALLED FROM TestMain. The go tool installs the hook that records a
// test's opened files when it parses -test.testlogfile, and that happens inside
// m.Run — so every file a TestMain opens before m.Run is outside the recording
// window and reaches no cache key at all.
func fenceTestCacheOnServerTree(t *testing.T) {
	t.Helper()
	opened := 0
	if err := openServerTree(serverSrcLink, &opened); err != nil {
		t.Fatalf("the server-source fence must be able to read %s; a dangling link fences nothing: %v", serverSrcLink, err)
	}
	if opened <= serverSrcFenceFloor {
		t.Fatalf("the server-source fence opened %d files; %s must resolve to cmd/knowledge-server", opened, serverSrcLink)
	}
}

// openServerTree recurses with os.ReadDir, NOT filepath.WalkDir. WalkDir lstats
// its root: handed a symlink it yields one non-directory entry and the walk opens
// nothing. os.Open — which ReadDir uses — follows the link, and every name built
// here stays under cmd/knowledge/testdata, which is what keeps the opens inside
// this module where the cache key can see them.
func openServerTree(dir string, opened *int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)
		if entry.IsDir() {
			if name == "testdata" || name == ".git" {
				continue
			}
			if err := openServerTree(path, opened); err != nil {
				return err
			}
			continue
		}
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		f, openErr := os.Open(path) //nolint:gosec // a path built from this module's own testdata link
		if openErr != nil {
			return openErr
		}
		_ = f.Close()
		*opened++
	}
	return nil
}

// TestServerSrcLinkResolves is the PIN on the fence, and it carries the same
// mirror caveat the fence's call site does: it SKIPS where the server module is
// absent, because this package ships to a mirror that does not carry it.
//
// The mutation that must turn it red is replacing the link with a regular file
// holding the same bytes, or pointing it anywhere that is not the server module.
func TestServerSrcLinkResolves(t *testing.T) {
	if _, err := os.Lstat(serverSrcLink); err != nil {
		t.Skip("cmd/knowledge/testdata/server-src not present (OSS repo layout)")
	}
	info, err := os.Lstat(serverSrcLink)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink (a checkout without symlink support materializes it as a text stub); "+
			"the server dep guard would then be cacheable against a tree it never read", serverSrcLink)
	}
	// Abs BEFORE EvalSymlinks: a relative name yields a relative result, and the
	// suffix check would compare the link's own target text.
	abs, err := filepath.Abs(serverSrcLink)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatalf("%s must resolve; a dangling link fences nothing: %v", serverSrcLink, err)
	}
	if !strings.HasSuffix(filepath.ToSlash(resolved), "cmd/knowledge-server") {
		t.Fatalf("%s resolves to %s, which is not the server module", serverSrcLink, resolved)
	}
	// AND THE FENCE ITSELF RUNS, so this pin covers the mechanism and not only the
	// link: a fence that opened nothing would pass every assertion above.
	fenceTestCacheOnServerTree(t)
}
