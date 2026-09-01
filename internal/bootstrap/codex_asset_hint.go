// SPDX-License-Identifier: Apache-2.0

// codex_asset_hint.go — startup-time check that the user's codex asset
// dirs (split roots: ~/.agents/skills for skills, ~/.codex/agents for
// agents) match the codex representation of the shared raw .claude tree
// (assets.Files: skills verbatim, agents translated to .toml). Emits a
// slog warning when files are missing or out-of-date so the user sees a
// hint via their MCP host's stderr/debug log. The codex twin of
// claude_asset_hint.go.
//
// Runs once from the serve daemon bootstrap (buildClient, per-process not
// per-call). Walk + translate + sha256 of a few dozen files is negligible.
//
// AGENTS.md is intentionally NOT part of this check: it is a clobber-safe
// managed-block merge into a user-owned file, so a sha256 mismatch there
// is expected (the user keeps their own prose around the managed block)
// and would produce a false "out of date" warning. Only the skills/agents
// assets — which install-codex-assets owns wholesale — are drift-checked
// here.
//
// Failures (file system errors, walk panics, untranslatable agent) are
// silently swallowed — courtesy hint, not a load-bearing path.

package bootstrap

import (
	"crypto/sha256"
	"errors"
	"io/fs"
	"log/slog"
	"os"

	"github.com/fulminate-io/knowledge-mcp/internal/assets"
)

// hintCodexAssetsIfStale walks the embedded codex asset tree and
// compares each file against its split-root destination (skills →
// ~/.agents/skills, agents → ~/.codex/agents). When one or more files
// are missing or differ, emits a slog.Warn naming the counts and the
// recovery command. No-op when everything matches or when the home dir
// can't be resolved.
func hintCodexAssetsIfStale() {
	dest, err := resolveCodexDest("", "")
	if err != nil {
		return
	}
	missing, drift, ok := codexAssetDrift(dest)
	if !ok {
		// Courtesy hint only — swallow walk failures.
		return
	}
	if missing == 0 && drift == 0 {
		return
	}
	slog.Warn("knowledge: codex assets out of date; run `knowledge install-codex-assets` to update",
		"missing", missing, "out_of_date", drift, "skills_dest", dest.skills, "agents_dest", dest.agents)
}

// codexAssetDrift walks the shared raw asset tree, computes each file's
// codex representation (skills verbatim, agents translated to .toml),
// and counts how many are missing (absent at their split-root
// destination) or drifted (present but sha256-different). ok is false
// when the walk itself errored (including an untranslatable agent).
// Split out from hintCodexAssetsIfStale so tests can drive it against
// temp roots without touching the user's real home dirs.
func codexAssetDrift(dest codexDest) (missing, drift int, ok bool) {
	walkErr := fs.WalkDir(assets.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		out, routed := codexOutPath(dest, p)
		if !routed {
			return nil
		}
		raw, err := assets.Files.ReadFile(p)
		if err != nil {
			return err
		}
		// dest is THIS walk's own resolved destination, never a constant:
		// codexContent renders dest.skills into each translated agent's
		// preamble, so a different root here would compute a want that no
		// install could ever produce and report permanent phantom drift.
		want, err := codexContent(dest, p, raw)
		if err != nil {
			return err
		}
		onDisk, err := os.ReadFile(out) //nolint:gosec // out is a split-root join of the trusted embedded asset path; same read the claude twin does inline
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				missing++
				return nil
			}
			return err
		}
		if sha256.Sum256(want) != sha256.Sum256(onDisk) {
			drift++
		}
		return nil
	})
	if walkErr != nil {
		return 0, 0, false
	}
	return missing, drift, true
}
