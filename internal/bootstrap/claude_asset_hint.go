// SPDX-License-Identifier: Apache-2.0

// claude_asset_hint.go — startup-time check that the user's ~/.claude
// directory matches the embedded agents/skills tree. Emits a slog
// warning when files are missing or out-of-date so the user sees a
// hint via their MCP host's stderr/debug log without having to know
// `knowledge doctor` exists.
//
// Runs once from the serve daemon bootstrap (buildClient, per-process not
// per-call) so the cost is amortized across the entire daemon lifetime.
// Walk + sha256 of 19 files is ~10ms — negligible relative to the existing
// ensureServerReachable call.
//
// Failures (file system errors, walk panics) are silently swallowed
// — this is a courtesy hint, not a load-bearing path. If we can't
// read embedded files, the install-claude-assets subcommand wouldn't
// work either and the user would discover that through a more
// targeted error path.

package bootstrap

import (
	"crypto/sha256"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/assets"
)

// hintClaudeAssetsIfStale walks the embedded asset tree and compares
// each file against the corresponding path under ~/.claude/. When one
// or more files are missing or differ, emits a slog.Warn naming the
// counts and the recovery command. No-op when everything matches or
// when the home dir can't be resolved.
func hintClaudeAssetsIfStale() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dest := filepath.Join(home, ".claude")

	missing, drift := 0, 0
	walkErr := fs.WalkDir(assets.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		embedded, err := assets.Files.ReadFile(p)
		if err != nil {
			return err
		}
		onDisk, err := os.ReadFile(filepath.Join(dest, p))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				missing++
				return nil
			}
			return err
		}
		if sha256.Sum256(embedded) != sha256.Sum256(onDisk) {
			drift++
		}
		return nil
	})
	if walkErr != nil {
		// Courtesy hint only — swallow walk failures so a busted
		// home dir doesn't fail the MCP session.
		return
	}
	if missing == 0 && drift == 0 {
		return
	}
	slog.Warn("knowledge: ~/.claude assets out of date; run `knowledge install-claude-assets` to update",
		"missing", missing, "out_of_date", drift, "dest", dest)
}

// hintClaudeMDIfStale checks whether the knowledge-managed block in
// ~/.claude/CLAUDE.md matches the embedded KNOWLEDGE_TOOLS.md reference
// and emits a slog.Warn when it drifts (or the file/markers are absent).
// Only the managed region is compared (via managedBlockInSync), so a
// user's own prose around the block never trips a false "out of date"
// warning — the same managed-block discipline the AGENTS.md hint relies
// on (codex_asset_hint.go documents why AGENTS.md is otherwise excluded).
// No-op when the home dir can't be resolved or the read errors (courtesy
// hint, not load-bearing).
func hintClaudeMDIfStale() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(home, ".claude", "CLAUDE.md")
	inSync, exists, err := managedBlockInSync(path, string(assets.KnowledgeTools))
	if err != nil || !exists || inSync {
		// Swallow read errors (courtesy hint); a missing file is not a
		// drift signal (install-claude-assets seeds it on first run);
		// in-sync is the happy path.
		return
	}
	slog.Warn("knowledge: ~/.claude/CLAUDE.md managed block out of date; run `knowledge install-claude-assets` to update",
		"path", path)
}
