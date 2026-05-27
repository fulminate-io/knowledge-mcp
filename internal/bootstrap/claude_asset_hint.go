// SPDX-License-Identifier: Apache-2.0

// claude_asset_hint.go — startup-time check that the user's ~/.claude
// directory matches the embedded agents/skills tree. Emits a slog
// warning when files are missing or out-of-date so the user sees a
// hint via their MCP host's stderr/debug log without having to know
// `knowledge doctor` exists.
//
// Runs from runMCPMode (per-session, not per-call) so the cost is
// amortized across the entire MCP-host session. Walk + sha256 of
// 19 files is ~10ms — negligible relative to the existing
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

	"github.com/fulminate-io/knowledge-mcp/internal/claudeassets"
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
	walkErr := fs.WalkDir(claudeassets.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		embedded, err := claudeassets.Files.ReadFile(p)
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
