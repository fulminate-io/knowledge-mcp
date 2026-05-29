// SPDX-License-Identifier: Apache-2.0

// codex_agents_md.go — the knowledge-priming ~/.codex/AGENTS.md content
// and a clobber-safe managed-block writer. install-codex-assets writes a
// concise priming block (what knowledge is, the recall/think cycle, the
// recovery command) into the user's global codex instructions WITHOUT
// destroying any prose the user already keeps there.
//
// The managed region is bounded by HTML-comment markers. On install we
// replace only the bytes between the markers (or append the block when
// the markers are absent), leaving everything else byte-for-byte intact.

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	codexAgentsMDBegin = "<!-- BEGIN knowledge-managed -->"
	codexAgentsMDEnd   = "<!-- END knowledge-managed -->"
)

// codexAgentsMDBody is the priming content inside the managed block. No
// secrets or API keys — registration uses an env-var placeholder, never
// a literal token (the actual config lives in ~/.codex/config.toml).
const codexAgentsMDBody = `## knowledge (Fulminate) MCP server

This project uses the **knowledge** MCP server — a persistent memory and
reasoning graph. It indexes the codebase, cloud, and a knowledge graph of
decisions, findings, and reasoning.

**Start every task by recalling prior context:**

- ` + "`thoughts(operation:\"recall\", query:\"...\")`" + ` — past debugging notes,
  design rationale, gotchas. Do this before reasoning from scratch.
- ` + "`search(graph:\"knowledge\", ...)`" + ` — decisions, findings, rules.
- ` + "`search`" + ` / ` + "`file_symbols`" + ` / ` + "`traverse`" + ` — code exploration FIRST,
  before grep/read.

**Externalize reasoning:** record hypotheses with ` + "`thoughts(operation:\"think\")`" + `
and back them with evidence via ` + "`thoughts(operation:\"charge\")`" + `.

**Skills + agents:** ` + "`knowledge install-codex-assets`" + ` installs the skill
catalog under ` + "`~/.agents/skills/`" + ` and subagents under ` + "`~/.codex/agents/`" + `.
Re-run it whenever this binary is upgraded to refresh them.

If the knowledge tools are unavailable, confirm the server is registered
in ` + "`~/.codex/config.toml`" + ` under ` + "`[mcp_servers.knowledge]`" + ` and running.`

// managedBlock returns the full managed region (markers + body) with a
// trailing newline, ready to splice into an AGENTS.md.
func managedBlock() string {
	return codexAgentsMDBegin + "\n" + codexAgentsMDBody + "\n" + codexAgentsMDEnd + "\n"
}

// writeManagedAgentsMD writes the knowledge priming block into the
// AGENTS.md at destAgentsMD, clobber-safe:
//   - No existing file → create it containing only the managed block.
//   - Existing file with both markers → replace only the bytes between
//     them, preserving user prose above and below verbatim.
//   - Existing file without markers → append the managed block after the
//     existing content (separated by a blank line), preserving it.
//
// Returns whether the file content changed (false when already in sync),
// so the caller can report accurately in dry-run mode.
func writeManagedAgentsMD(destAgentsMD string, dryRun bool) (changed bool, err error) {
	existing, readErr := os.ReadFile(destAgentsMD)
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, fmt.Errorf("read %s: %w", destAgentsMD, readErr)
	}

	next := mergeManagedBlock(string(existing))
	if next == string(existing) {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(destAgentsMD), 0o750); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(destAgentsMD), err)
	}
	if err := os.WriteFile(destAgentsMD, []byte(next), 0o644); err != nil { //nolint:gosec // user-readable docs, 0644 is correct
		return false, fmt.Errorf("write %s: %w", destAgentsMD, err)
	}
	return true, nil
}

// mergeManagedBlock returns the AGENTS.md content with the managed block
// inserted or refreshed. Pure function (no I/O) so it is trivially
// testable. User content outside the markers is preserved verbatim.
func mergeManagedBlock(existing string) string {
	block := managedBlock()
	if existing == "" {
		return block
	}
	beginIdx := strings.Index(existing, codexAgentsMDBegin)
	endIdx := strings.Index(existing, codexAgentsMDEnd)
	if beginIdx >= 0 && endIdx > beginIdx {
		// Replace the existing managed region (markers included) in
		// place, keeping everything before and after byte-for-byte.
		before := existing[:beginIdx]
		afterStart := endIdx + len(codexAgentsMDEnd)
		// Consume a single trailing newline after END so re-runs stay
		// idempotent (managedBlock already ends with one).
		after := existing[afterStart:]
		after = strings.TrimPrefix(after, "\n")
		return before + block + after
	}
	// No markers — append after existing content, separated by a blank
	// line, preserving the user's prose.
	sep := "\n"
	if !strings.HasSuffix(existing, "\n") {
		sep = "\n\n"
	} else if !strings.HasSuffix(existing, "\n\n") {
		sep = "\n"
	}
	return existing + sep + block
}

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
