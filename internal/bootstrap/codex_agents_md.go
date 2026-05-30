// SPDX-License-Identifier: Apache-2.0

// codex_agents_md.go — Codex-specific glue around the shared managed-block
// merger (managed_block.go). It resolves the ~/.codex/AGENTS.md path; the
// clobber-safe merge/write logic itself is generic and lives in
// managed_block.go (one merger repo-wide). The managed-block body for
// Codex is the full KNOWLEDGE_TOOLS.md reference (assets.KnowledgeTools).

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
)

// resolveCodexAgentsMD returns the destination AGENTS.md path. Empty
// flag defaults to ~/.codex/AGENTS.md; a non-empty flag is tilde-
// expanded.
func resolveCodexAgentsMD(flagDest string) (string, error) {
	if flagDest != "" {
		return expandTilde(flagDest), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".codex", "AGENTS.md"), nil
}
