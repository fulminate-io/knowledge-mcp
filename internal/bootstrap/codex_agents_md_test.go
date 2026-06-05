// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/assets"
)

// codexTestBody is the managed-block body the Codex installer now writes:
// the full embedded KNOWLEDGE_TOOLS.md reference. The old concise
// codexAgentsMDBody const was removed; these tests assert
// against the same body production uses.
func codexTestBody() string { return string(assets.KnowledgeTools) }

// Installing into a dir with NO AGENTS.md creates
// AGENTS.md with the managed block bounded by BEGIN/END markers +
// priming content.
func TestWriteManagedAgentsMD_CreatesWithMarkers(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "AGENTS.md")
	changed, err := writeManagedFile(dest, codexTestBody(), false)
	if err != nil {
		t.Fatalf("writeManagedFile: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true on first create")
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read written AGENTS.md: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, managedBlockBegin) {
		t.Error("missing BEGIN marker")
	}
	if !strings.Contains(got, managedBlockEnd) {
		t.Error("missing END marker")
	}
	if !strings.Contains(got, "knowledge") {
		t.Error("missing priming content")
	}
}

// Existing AGENTS.md with user content above/below
// the markers is preserved verbatim; only the managed region changes on
// re-install.
func TestWriteManagedAgentsMD_PreservesUserContent(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "AGENTS.md")
	above := "# My personal codex instructions\n\nKeep this paragraph.\n\n"
	below := "\n## My footer\n\nAnd this one too.\n"
	// Seed a file with a (stale) managed block sandwiched between user
	// prose.
	staleBlock := managedBlockBegin + "\nOLD STALE MANAGED CONTENT\n" + managedBlockEnd + "\n"
	if err := os.WriteFile(dest, []byte(above+staleBlock+below), 0o600); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}

	changed, err := writeManagedFile(dest, codexTestBody(), false)
	if err != nil {
		t.Fatalf("writeManagedFile: %v", err)
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
	changed2, err := writeManagedFile(dest, codexTestBody(), false)
	if err != nil {
		t.Fatalf("second writeManagedFile: %v", err)
	}
	if changed2 {
		t.Error("second run reported changed=true; want idempotent no-op")
	}
}

// The managed-block body (now the full
// KNOWLEDGE_TOOLS.md reference) carries no real API key/secret literal.
func TestCodexAgentsMD_NoSecrets(t *testing.T) {
	lower := strings.ToLower(codexTestBody())
	for _, needle := range []string{"sk-ant-", "sk-proj-", "bearer ", "api_key=\"", "secret=\""} {
		if strings.Contains(lower, needle) {
			t.Errorf("managed-block body contains a possible secret literal: %q", needle)
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
	if _, err := writeManagedFile(dest, codexTestBody(), false); err != nil {
		t.Fatalf("writeManagedFile: %v", err)
	}
	data, _ := os.ReadFile(dest)
	got := string(data)
	if !strings.HasPrefix(got, user) {
		t.Error("existing prose not preserved when appending block")
	}
	if !strings.Contains(got, managedBlockBegin) {
		t.Error("managed block not appended")
	}
}

// dry-run never writes.
func TestWriteManagedAgentsMD_DryRunNoWrite(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "AGENTS.md")
	changed, err := writeManagedFile(dest, codexTestBody(), true)
	if err != nil {
		t.Fatalf("writeManagedFile dry-run: %v", err)
	}
	if !changed {
		t.Error("dry-run changed = false, want true (would create)")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("dry-run wrote a file; want none")
	}
}
