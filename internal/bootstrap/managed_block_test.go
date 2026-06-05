// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/assets"
)

// claudeBody is the managed-block body the Claude installer writes: the
// full embedded KNOWLEDGE_TOOLS.md reference.
func claudeBody() string { return string(assets.KnowledgeTools) }

// distinctiveToken is a string unique to the KNOWLEDGE_TOOLS.md
// reference, used to assert the managed region carries the full body.
const distinctiveToken = "primitives-over-shortcuts"

// TestWriteManagedFile_Claude_Fresh: a fresh CLAUDE.md is created with
// markers wrapping the full reference body.
func TestWriteManagedFile_Claude_Fresh(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "CLAUDE.md")
	changed, err := writeManagedFile(dest, claudeBody(), false)
	if err != nil {
		t.Fatalf("writeManagedFile: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true on first create")
	}
	got := readFile(t, dest)
	if !strings.Contains(got, managedBlockBegin) || !strings.Contains(got, managedBlockEnd) {
		t.Error("missing BEGIN/END markers")
	}
	if !strings.Contains(got, distinctiveToken) {
		t.Errorf("managed region missing the reference body token %q", distinctiveToken)
	}
}

// TestWriteManagedFile_Claude_WithMarkers: an existing file with a stale
// managed block between user prose has only the managed region refreshed;
// prose above and below survives verbatim.
func TestWriteManagedFile_Claude_WithMarkers(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "CLAUDE.md")
	above := "# My CLAUDE.md\n\nKeep this above.\n\n"
	below := "\n## Footer\n\nKeep this below.\n"
	stale := managedBlockBegin + "\nSTALE\n" + managedBlockEnd + "\n"
	if err := os.WriteFile(dest, []byte(above+stale+below), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := writeManagedFile(dest, claudeBody(), false); err != nil {
		t.Fatalf("writeManagedFile: %v", err)
	}
	got := readFile(t, dest)
	if !strings.HasPrefix(got, above) {
		t.Errorf("prose above not preserved verbatim:\n%s", got)
	}
	if !strings.HasSuffix(got, below) {
		t.Errorf("prose below not preserved verbatim:\n%s", got)
	}
	if strings.Contains(got, "STALE") {
		t.Error("stale managed content survived")
	}
	if !strings.Contains(got, distinctiveToken) {
		t.Error("refreshed managed region missing reference body")
	}
}

// TestWriteManagedFile_Claude_NoMarkers: a user file without markers gets
// the block appended, prose preserved.
func TestWriteManagedFile_Claude_NoMarkers(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "CLAUDE.md")
	user := "# Existing CLAUDE.md, no managed block\n"
	if err := os.WriteFile(dest, []byte(user), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := writeManagedFile(dest, claudeBody(), false); err != nil {
		t.Fatalf("writeManagedFile: %v", err)
	}
	got := readFile(t, dest)
	if !strings.HasPrefix(got, user) {
		t.Error("existing prose not preserved when appending block")
	}
	if !strings.Contains(got, managedBlockBegin) {
		t.Error("managed block not appended")
	}
}

// TestWriteManagedFile_Claude_Idempotent: a second run on the now-current
// file reports changed=false.
func TestWriteManagedFile_Claude_Idempotent(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "CLAUDE.md")
	if _, err := writeManagedFile(dest, claudeBody(), false); err != nil {
		t.Fatalf("first writeManagedFile: %v", err)
	}
	changed, err := writeManagedFile(dest, claudeBody(), false)
	if err != nil {
		t.Fatalf("second writeManagedFile: %v", err)
	}
	if changed {
		t.Error("second run reported changed=true; want idempotent no-op")
	}
}

// TestWriteManagedFile_Claude_DryRunNoWrite: dry-run reports would-change
// but writes nothing.
func TestWriteManagedFile_Claude_DryRunNoWrite(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "CLAUDE.md")
	changed, err := writeManagedFile(dest, claudeBody(), true)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !changed {
		t.Error("dry-run changed = false, want true (would create)")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("dry-run wrote a file; want none")
	}
}

// TestManagedBlockInSync covers the three states: absent file, in-sync,
// and drifted.
func TestManagedBlockInSync(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent", "CLAUDE.md")
	inSync, exists, err := managedBlockInSync(absent, claudeBody())
	if err != nil {
		t.Fatalf("absent: unexpected err %v", err)
	}
	if exists || inSync {
		t.Errorf("absent file: exists=%v inSync=%v, want false/false", exists, inSync)
	}

	synced := filepath.Join(dir, "CLAUDE.md")
	if _, err := writeManagedFile(synced, claudeBody(), false); err != nil {
		t.Fatalf("seed synced: %v", err)
	}
	inSync, exists, err = managedBlockInSync(synced, claudeBody())
	if err != nil {
		t.Fatalf("synced: unexpected err %v", err)
	}
	if !exists || !inSync {
		t.Errorf("synced file: exists=%v inSync=%v, want true/true", exists, inSync)
	}

	drifted := filepath.Join(dir, "drifted.md")
	stale := managedBlockBegin + "\nOLD\n" + managedBlockEnd + "\n"
	if err := os.WriteFile(drifted, []byte(stale), 0o600); err != nil {
		t.Fatalf("seed drifted: %v", err)
	}
	inSync, exists, err = managedBlockInSync(drifted, claudeBody())
	if err != nil {
		t.Fatalf("drifted: unexpected err %v", err)
	}
	if !exists || inSync {
		t.Errorf("drifted file: exists=%v inSync=%v, want true/false", exists, inSync)
	}
}

// TestCheckClaudeMD drives checkClaudeMD against a temp HOME for each of
// the three outcomes.
func TestCheckClaudeMD(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Missing file → warn.
	if got := checkClaudeMD(); got.status != statusWarn {
		t.Errorf("missing CLAUDE.md: status=%v, want warn", got.status)
	}

	// In-sync → ok.
	claudeMD := filepath.Join(home, ".claude", "CLAUDE.md")
	if _, err := writeManagedFile(claudeMD, string(assets.KnowledgeTools), false); err != nil {
		t.Fatalf("seed CLAUDE.md: %v", err)
	}
	if got := checkClaudeMD(); got.status != statusOK {
		t.Errorf("in-sync CLAUDE.md: status=%v, want ok (msg=%q)", got.status, got.msg)
	}

	// Drifted → warn.
	stale := managedBlockBegin + "\nOLD\n" + managedBlockEnd + "\n"
	if err := os.WriteFile(claudeMD, []byte(stale), 0o600); err != nil {
		t.Fatalf("drift CLAUDE.md: %v", err)
	}
	if got := checkClaudeMD(); got.status != statusWarn {
		t.Errorf("drifted CLAUDE.md: status=%v, want warn", got.status)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
