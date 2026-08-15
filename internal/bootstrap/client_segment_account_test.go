// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// TestSegmentCacheDirIsAccountPartitioned proves two accounts can never share a
// segment cache root, and that no selection leaves today's path untouched.
func TestSegmentCacheDirIsAccountPartitioned(t *testing.T) {
	root := "/data/knowledge"

	unset := accountSegmentRoot(root, "")
	if want := filepath.Join(root, "segments"); unset != want {
		t.Errorf("no selection: root = %q, want today's unchanged %q", unset, want)
	}

	a := accountSegmentRoot(root, "acct_01AAA")
	b := accountSegmentRoot(root, "acct_01BBB")
	if a == b {
		t.Errorf("two accounts share a cache root: %q", a)
	}
	if a == unset || b == unset {
		t.Errorf("a selected account shares the unset root: a=%q b=%q unset=%q", a, b, unset)
	}
	// Neither may be a prefix-directory of the other, or a walk over one would
	// see the other's blobs.
	if isUnder(a, b) || isUnder(b, a) {
		t.Errorf("one account's root is nested under the other's: a=%q b=%q", a, b)
	}
	// Both stay under the shared segments base, so the existing store layout is
	// preserved rather than relocated.
	base := filepath.Join(root, "segments")
	if !isUnder(a, base) || !isUnder(b, base) {
		t.Errorf("account roots escaped the segments base: a=%q b=%q base=%q", a, b, base)
	}

	// A path-separator-bearing id cannot escape the base.
	evil := accountSegmentRoot(root, "../../etc")
	if !isUnder(evil, base) {
		t.Errorf("a path-traversing account id escaped the base: %q", evil)
	}

	// And the production reader wires the selection through: segmentCacheDirFor
	// reflects whatever account is stored.
	home := t.TempDir()
	cfgPath := filepath.Join(home, "config")
	if err := os.WriteFile(cfgPath, []byte("[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-5\"\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := config.WriteSelectedAccountID(cfgPath, "acct_01WIRED"); err != nil {
		t.Fatalf("seed selection: %v", err)
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(cfgPath, time.Second)))

	got := segmentCacheDirFor(root)
	if want := accountSegmentRoot(root, "acct_01WIRED"); got != want {
		t.Errorf("segmentCacheDirFor = %q, want %q — the stored selection is not reaching the cache root", got, want)
	}

	// Negative control: with no selection installed, the production reader
	// returns the unchanged path, so the positive above is a real distinction.
	empty := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(empty, []byte("[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-5\"\n"), 0o600); err != nil {
		t.Fatalf("seed empty config: %v", err)
	}
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(empty, time.Second)))
	if got := segmentCacheDirFor(root); got != unset {
		t.Errorf("no selection: segmentCacheDirFor = %q, want %q", got, unset)
	}
}

// isUnder reports whether path is at or under base.
func isUnder(path, base string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel != ".." && !filepath.IsAbs(rel) && (rel == "." || rel[0] != '.')
}
