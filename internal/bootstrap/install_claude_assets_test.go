// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canonicalGovernanceLink is the canonical .claude/skills/GOVERNANCE.md, READ
// THROUGH THIS PACKAGE'S OWN testdata LINK.
//
// THE LINK IS A TEST-CACHE FENCE, and it REPLACES a walk-up resolver that could
// not be one. `go test` keys a package's stored result on the files a run opened
// and DROPS any opened name that does not resolve inside the tested package's
// own module root. The previous helper walked up to the tree carrying both a
// go.mod and .claude/skills, which is the REPO root — above this module — so the
// canonical file was never in this package's key: editing GOVERNANCE.md and
// re-running returned `ok (cached)`, a stored PASS for the guard whose entire
// subject is whether that file is installed byte-identically. A name under this
// package's own testdata is inside cmd/knowledge, so the go tool records it, and
// os.Stat follows the link to the target's size and modification time.
//
// THE LINK'S DEPTH DIFFERS IN THE PUBLISHED MIRROR, so scripts/sync-to-oss.sh
// re-points it: five levels below the repo root here, three below the mirror
// root there, because the script maps cmd/knowledge/internal to internal/.
const canonicalGovernanceLink = "testdata/governance.md"

// canonicalGovernanceTarget is what the link must resolve to. Asserted
// separately, and never used to READ: resolving the link first names the
// repo-root path again, which is outside this module, and undoes the fence.
const canonicalGovernanceTarget = ".claude/skills/GOVERNANCE.md"

// TestCanonicalGovernanceLinkResolves is the PIN on the fence. A checkout
// without symlink support materializes the link as a one-line text stub, and the
// byte comparison below would then fail on a difference that is about the
// checkout rather than about the installer. The mutation that must turn this red
// is replacing the link with a regular file holding the same bytes: the copy
// reads fine, the comparison passes, and this package is cacheable against a
// file it never re-read.
func TestCanonicalGovernanceLinkResolves(t *testing.T) {
	info, err := os.Lstat(canonicalGovernanceLink)
	if err != nil {
		t.Fatalf("%s must exist — it is what puts the canonical governance file in this package's test-cache key: %v", canonicalGovernanceLink, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink (a checkout without symlink support materializes it as a text stub); "+
			"this package would then be cacheable against a file it never read", canonicalGovernanceLink)
	}
	// Abs BEFORE EvalSymlinks: handed a relative name EvalSymlinks returns a
	// relative result, and the suffix check would compare the link's own target
	// text rather than the resolved path.
	abs, err := filepath.Abs(canonicalGovernanceLink)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatalf("%s must resolve; a dangling link fences nothing: %v", canonicalGovernanceLink, err)
	}
	if !strings.HasSuffix(filepath.ToSlash(resolved), canonicalGovernanceTarget) {
		t.Fatalf("%s resolves to %s, which is not the canonical governance file", canonicalGovernanceLink, resolved)
	}
	// KNOWN POSITIVE: it serves the real file, not an empty stub.
	body, err := os.ReadFile(canonicalGovernanceLink)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "GOVERNANCE") {
		t.Fatalf("%s does not serve the governance file's own text", canonicalGovernanceLink)
	}
}

// TestRunInstallClaudeAssets_GovernanceFile drives a REAL claude install
// into a temp dest and asserts the flat governance file lands at
// <dest>/skills/GOVERNANCE.md byte-identical to the canonical
// .claude/skills/GOVERNANCE.md.
//
// Characterization guard: the claude installer writes the embed tree
// verbatim, so this routing already holds. What the test protects is the
// SHIPPING of the file — every agent def mandates reading it as its first
// action, so an install that omits it, or rewrites it on the way through,
// breaks every mandated read at once, silently.
//
// The byte comparison is against the canonical source rather than against
// the embed mirror: the mirror is a gitignored copy that scripts/sync-assets.sh
// regenerates, so comparing embed-to-embed would let the installer agree
// with itself.
func TestRunInstallClaudeAssets_GovernanceFile(t *testing.T) {
	canonical, err := os.ReadFile(canonicalGovernanceLink)
	if err != nil {
		t.Fatalf("read canonical governance file: %v", err)
	}

	dir := t.TempDir()
	dest := filepath.Join(dir, "clauderoot")
	if err := runInstallClaudeAssets([]string{
		"--no-mcp",
		"--dest", dest,
		"--claude-md-dest", filepath.Join(dir, "CLAUDE.md"),
		"--claude-settings-dest", filepath.Join(dir, "settings.json"),
	}); err != nil {
		t.Fatalf("runInstallClaudeAssets: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "skills", "GOVERNANCE.md"))
	if err != nil {
		t.Fatalf("governance file not installed at <dest>/skills/GOVERNANCE.md: %v", err)
	}
	if !bytes.Equal(got, canonical) {
		t.Errorf("installed governance file differs from .claude/skills/GOVERNANCE.md (%d bytes installed, %d canonical)",
			len(got), len(canonical))
	}
}
