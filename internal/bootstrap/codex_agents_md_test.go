// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// criterion 399bdd5e: installing into a dir with NO AGENTS.md creates
// AGENTS.md with the managed block bounded by BEGIN/END markers +
// priming content.
func TestWriteManagedAgentsMD_CreatesWithMarkers(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "AGENTS.md")
	changed, err := writeManagedAgentsMD(dest, false)
	if err != nil {
		t.Fatalf("writeManagedAgentsMD: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true on first create")
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read written AGENTS.md: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, codexAgentsMDBegin) {
		t.Error("missing BEGIN marker")
	}
	if !strings.Contains(got, codexAgentsMDEnd) {
		t.Error("missing END marker")
	}
	if !strings.Contains(got, "knowledge") {
		t.Error("missing priming content")
	}
}

// criterion 95e2582d: existing AGENTS.md with user content above/below
// the markers is preserved verbatim; only the managed region changes on
// re-install.
func TestWriteManagedAgentsMD_PreservesUserContent(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "AGENTS.md")
	above := "# My personal codex instructions\n\nKeep this paragraph.\n\n"
	below := "\n## My footer\n\nAnd this one too.\n"
	// Seed a file with a (stale) managed block sandwiched between user
	// prose.
	staleBlock := codexAgentsMDBegin + "\nOLD STALE MANAGED CONTENT\n" + codexAgentsMDEnd + "\n"
	if err := os.WriteFile(dest, []byte(above+staleBlock+below), 0o600); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}

	changed, err := writeManagedAgentsMD(dest, false)
	if err != nil {
		t.Fatalf("writeManagedAgentsMD: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true (stale block refreshed)")
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	got := string(data)
	if !strings.HasPrefix(got, above) {
		t.Errorf("user content above markers not preserved verbatim:\n%s", got)
	}
	if !strings.HasSuffix(got, below) {
		t.Errorf("user content below markers not preserved verbatim:\n%s", got)
	}
	if strings.Contains(got, "OLD STALE MANAGED CONTENT") {
		t.Error("stale managed content survived; should have been replaced")
	}
	if !strings.Contains(got, "recall") {
		t.Error("fresh priming content missing after refresh")
	}

	// Re-running on the now-current file is a no-op (idempotent).
	changed2, err := writeManagedAgentsMD(dest, false)
	if err != nil {
		t.Fatalf("second writeManagedAgentsMD: %v", err)
	}
	if changed2 {
		t.Error("second run reported changed=true; want idempotent no-op")
	}
}

// criterion c993c1eb: AGENTS.md template carries no real API key/secret.
func TestCodexAgentsMD_NoSecrets(t *testing.T) {
	lower := strings.ToLower(codexAgentsMDBody)
	for _, needle := range []string{"sk-ant-", "sk-proj-", "bearer ", "api_key=\"", "secret=\""} {
		if strings.Contains(lower, needle) {
			t.Errorf("AGENTS.md template contains a possible secret literal: %q", needle)
		}
	}
}

// TestWriteManagedAgentsMD_AppendsWhenNoMarkers covers the no-markers
// branch: a user file without the managed markers gets the block
// appended, prose preserved.
func TestWriteManagedAgentsMD_AppendsWhenNoMarkers(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "AGENTS.md")
	user := "# Existing instructions with no managed block\n"
	if err := os.WriteFile(dest, []byte(user), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := writeManagedAgentsMD(dest, false); err != nil {
		t.Fatalf("writeManagedAgentsMD: %v", err)
	}
	data, _ := os.ReadFile(dest)
	got := string(data)
	if !strings.HasPrefix(got, user) {
		t.Error("existing prose not preserved when appending block")
	}
	if !strings.Contains(got, codexAgentsMDBegin) {
		t.Error("managed block not appended")
	}
}

// dry-run never writes.
func TestWriteManagedAgentsMD_DryRunNoWrite(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "AGENTS.md")
	changed, err := writeManagedAgentsMD(dest, true)
	if err != nil {
		t.Fatalf("writeManagedAgentsMD dry-run: %v", err)
	}
	if !changed {
		t.Error("dry-run changed = false, want true (would create)")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("dry-run wrote a file; want none")
	}
}
