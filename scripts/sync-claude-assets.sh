#!/usr/bin/env bash
# sync-claude-assets.sh — mirror .claude/agents and .claude/skills into
# the embed location used by cmd/knowledge/internal/claudeassets, so
# the binary's `//go:embed` directive picks them up at build time.
#
# Why a copy step: Go's //go:embed forbids `..` in patterns, so a
# package in cmd/knowledge/ can't reach .claude/ at the module root
# directly. The canonical files stay where Claude Code expects them
# (.claude/{agents,skills}/), and this script keeps the embed-side
# duplicates in sync. The duplicates are gitignored.
#
# Idempotent: clears the embed directories before mirroring so deleted
# source files don't linger as orphans. Safe to run repeatedly.

set -euo pipefail

# Resolve repo root from this script's location.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"

src_agents="${repo_root}/.claude/agents"
src_skills="${repo_root}/.claude/skills"
dst_root="${repo_root}/internal/claudeassets"
dst_agents="${dst_root}/agents"
dst_skills="${dst_root}/skills"

if [[ ! -d "${src_agents}" ]] || [[ ! -d "${src_skills}" ]]; then
  echo "sync-claude-assets: source directories missing" >&2
  echo "  expected: ${src_agents}" >&2
  echo "  expected: ${src_skills}" >&2
  exit 1
fi

# Wipe + recreate destinations so deleted source files don't survive
# in the embed copy. mkdir -p is idempotent.
rm -rf "${dst_agents}" "${dst_skills}"
mkdir -p "${dst_agents}" "${dst_skills}"

cp -R "${src_agents}/." "${dst_agents}/"
cp -R "${src_skills}/." "${dst_skills}/"

# Strip macOS / hidden cruft that cp -R copies but the embedded
# install-claude-assets subcommand should not write to user homes.
# .DS_Store especially: macOS auto-creates it in any browsed folder
# and gitignore only prevents committing it, not local sync from
# happening. Find -delete is idempotent + safe (no glob misexpansion).
find "${dst_agents}" "${dst_skills}" -name ".*" -type f -delete

agent_count=$(find "${dst_agents}" -type f -name '*.md' | wc -l | tr -d ' ')
skill_count=$(find "${dst_skills}" -type f | wc -l | tr -d ' ')
echo "sync-claude-assets: ${agent_count} agents, ${skill_count} skill files mirrored to internal/claudeassets/"
