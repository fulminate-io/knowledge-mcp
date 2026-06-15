// SPDX-License-Identifier: Apache-2.0

// Package assets carries the embedded copy of the project's
// .claude/agents and .claude/skills tree — the SHARED raw source
// consumed by BOTH the `install-claude-assets` and the
// `install-codex-assets` CLI subcommands. The directive lives here
// rather than at the module root because cmd/knowledge needs a clean
// internal-package import path; the duplicate files under agents/
// and skills/ are populated by scripts/sync-assets.sh before any build
// (Makefile depends on it; the brew formula calls it explicitly). Both
// directories are gitignored.
//
// Files is the raw .claude tree. install-claude-assets writes it under
// ~/.claude verbatim. install-codex-assets writes skills verbatim and
// TRANSLATES each agent .md into a codex .toml at install time (see
// cmd/knowledge/internal/codexassets.TranslateAgent) — there is no
// second embed for the codex shapes.
//
// ClaudeHooks is the canonical promote-guard PreToolUse hook entry. Unlike
// Files, it is NEVER walked into ~/.claude as a standalone file — it is the
// single source merged into the user's global ~/.claude/settings.json by
// install-claude-assets (a JSON deep-merge, see bootstrap/settings_merge.go).
package assets

import "embed"

// Files mirrors .claude/agents/*.md and .claude/skills/*/SKILL.md
// at build time. Path layout:
//
//	agents/<agent-name>.md
//	skills/<skill-name>/SKILL.md
//
// Callers walk the FS via fs.WalkDir(Files, ".", ...) and either write
// each regular file verbatim (claude install, codex skills) or translate
// agent .md files to .toml (codex install).
//
//go:embed all:agents all:skills
var Files embed.FS

// KnowledgeTools is the full public tool reference (.claude/KNOWLEDGE_TOOLS.md),
// embedded as a SEPARATE var — deliberately NOT part of Files. Neither
// installer walks it into ~/.claude/KNOWLEDGE_TOOLS.md; it is the source
// content for the knowledge-managed block primed into the global
// instruction files (~/.claude/CLAUDE.md, ~/.codex/AGENTS.md). Populated
// by scripts/sync-assets.sh, which copies .claude/KNOWLEDGE_TOOLS.md into
// this package before any build (the copy is gitignored).
//
//go:embed KNOWLEDGE_TOOLS.md
var KnowledgeTools []byte

// ClaudeHooks is the canonical knowledge-managed PreToolUse hook entry
// (cmd/knowledge/internal/assets/claude_hooks.json), embedded as a SEPARATE
// var — deliberately NOT part of Files. Neither installer walks it into a
// standalone file under ~/.claude; it is the source for the single hook
// entry that install-claude-assets idempotently merges into the user's
// global ~/.claude/settings.json under hooks.PreToolUse. Unlike the synced
// agents/skills/KNOWLEDGE_TOOLS.md trees (gitignored, populated by
// scripts/sync-assets.sh), this asset is checked in and ships verbatim.
//
//go:embed claude_hooks.json
var ClaudeHooks []byte
