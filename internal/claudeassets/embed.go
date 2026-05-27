// SPDX-License-Identifier: Apache-2.0

// Package claudeassets carries the embedded copy of the project's
// .claude/agents and .claude/skills tree. The directive lives here
// rather than at the module root because cmd/knowledge needs a clean
// internal-package import path; the duplicate files under agents/
// and skills/ are populated by scripts/sync-claude-assets.sh before
// any build (Makefile depends on it; the brew formula calls it
// explicitly). Both directories are gitignored.
//
// Files is consumed by the `knowledge install-claude-assets` CLI
// subcommand (see cmd/knowledge/install_claude_assets.go), which
// walks the embedded FS and writes its contents under the user's
// ~/.claude directory.
package claudeassets

import "embed"

// Files mirrors .claude/agents/*.md and .claude/skills/*/SKILL.md
// at build time. Path layout:
//
//	agents/<agent-name>.md
//	skills/<skill-name>/SKILL.md
//
// Callers walk the FS via fs.WalkDir(Files, ".", ...) and write each
// regular file to the corresponding path under ~/.claude/.
//
//go:embed all:agents all:skills
var Files embed.FS
