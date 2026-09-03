// SPDX-License-Identifier: Apache-2.0

// install_codex_assets.go — `knowledge install-codex-assets` CLI
// subcommand. The codex twin of install-claude-assets. It walks the
// SHARED raw .claude asset tree (assets.Files — the same embed
// install-claude-assets uses) and writes its contents to the user's
// home, so brew/tarball installs get the same agent + skill catalog
// source-built devs have.
//
// The codex shapes are produced at INSTALL time, not embedded: skill
// files are written verbatim; agent .md files are TRANSLATED to codex
// .toml via codexassets.TranslateAgent before writing. There is no
// second embed for the codex twin.
//
// Codex uses SPLIT roots (unlike claude's single ~/.claude): skills
// install under ~/.agents/skills/<name>/SKILL.md (codex user-skill
// scope), agents under ~/.codex/agents/<name>.toml. There is therefore
// no single --dest; the installer resolves a per-asset-type root and
// routes each embedded file by its top-level dir. --skills-dest /
// --agents-dest override the roots (used by tests; never point them at
// the user's live dirs in development).
//
// CLI mode, not MCP mode: writing to stdout is legitimate here.
//
// SCOPE NOTE — no permission hooks here (deliberate): the Claude installer
// merges a promote-guard PreToolUse hook into ~/.claude/settings.json that
// gates `collect promote:true` behind a confirmation prompt (see
// settings_merge.go). The codex installer intentionally installs NO
// equivalent: codex per-tool permission-hook support (a hook that can
// inspect a tool's input params, the way Claude Code's PreToolUse can read
// .tool_input.promote) is unverified, so the promote-guard is Claude-only.
// The codex config touch points are MCP-server registration and the
// tool_timeout_sec patch (mcp_register.go) — neither is a permission gate.
// This is a deferred follow-up, not an omission; if codex gains a verified
// per-tool permission-hook mechanism, wiring the promote-guard for codex is
// a separate ticket.

package bootstrap

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/assets"
	"github.com/fulminate-io/knowledge-mcp/internal/codexassets"
)

// installCodexFlags holds the parsed flags for the subcommand.
type installCodexFlags struct {
	skillsDest   string
	agentsDest   string
	agentsMDDest string
	dryRun       bool
	verbose      bool
	diff         bool
	noMCP        bool
}

// registerInstallCodexFlags registers the `knowledge install-codex-assets`
// flags on fs, binding each into f. Pure register-only seam (no fs.Parse) —
// shared by runInstallCodexAssets (the live CLI path) and the docs generator,
// which VisitAll's the FlagSet to render the flag table. Mirrors
// registerInstallClaudeFlags / registerInstallFlags.
func registerInstallCodexFlags(fs *flag.FlagSet, f *installCodexFlags) {
	fs.StringVar(&f.skillsDest, "skills-dest", "", "Skills destination root (default ~/.agents/skills)")
	fs.StringVar(&f.agentsDest, "agents-dest", "", "Agents destination root (default ~/.codex/agents)")
	fs.StringVar(&f.agentsMDDest, "agents-md-dest", "", "AGENTS.md destination path (default ~/.codex/AGENTS.md)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "Print what would be written without touching disk")
	fs.BoolVar(&f.diff, "diff", false, "Print a unified diff of every file that differs (read-only; implies --dry-run)")
	fs.BoolVar(&f.verbose, "verbose", false, "Print each file path written (default: summary only)")
	fs.BoolVar(&f.noMCP, "no-mcp", false, "Skip registering the knowledge MCP server with the client (default: register)")
}

// codexDest pairs the resolved split roots.
type codexDest struct {
	skills string
	agents string
}

// runInstallCodexAssets walks the shared raw .claude FS and routes each
// file to its split home root: skills/<rest> → <skills-root>/<rest>
// (verbatim), agents/<name>.md → <agents-root>/<name>.toml (translated
// to codex TOML). Default skills root is ~/.agents/skills, default
// agents root is ~/.codex/agents.
//
// Flags:
//   - --dry-run: walk + report what would be written without touching disk
//   - --diff: read-only mode that prints a unified diff of every file
//     that differs from the (verbatim skill / translated agent) version.
//     Implies --dry-run.
//   - --verbose: per-file write line (otherwise just a summary)
//   - --skills-dest / --agents-dest: override the split roots (tests)
func runInstallCodexAssets(args []string) error {
	fs := flag.NewFlagSet("knowledge install-codex-assets", flag.ContinueOnError)
	var f installCodexFlags
	registerInstallCodexFlags(fs, &f)
	if err := fs.Parse(args); err != nil {
		return err
	}

	dest, err := resolveCodexDest(f.skillsDest, f.agentsDest)
	if err != nil {
		return err
	}
	agentsMD, err := resolveCodexAgentsMD(f.agentsMDDest)
	if err != nil {
		return err
	}

	if f.diff {
		if err := runCodexDiff(dest); err != nil {
			return err
		}
		return diffManagedFile(agentsMD, string(assets.KnowledgeTools), "AGENTS.md")
	}

	written, err := installCodexAssets(dest, f.dryRun, f.verbose)
	if err != nil {
		return err
	}

	mdChanged, err := writeManagedFile(agentsMD, string(assets.KnowledgeTools), f.dryRun)
	if err != nil {
		return err
	}

	verb := "wrote"
	if f.dryRun {
		verb = "would write"
	}
	mdNote := "AGENTS.md in sync"
	if mdChanged {
		mdNote = fmt.Sprintf("%s AGENTS.md managed block in %s", verb, agentsMD)
	}
	fmt.Fprintf(os.Stdout, "knowledge install-codex-assets: %s %d files (skills→%s, agents→%s); %s\n",
		verb, written, dest.skills, dest.agents, mdNote)

	if f.noMCP {
		fmt.Fprintln(os.Stdout, "  --no-mcp: skipped MCP registration")
		return nil
	}
	// Default-on, non-fatal MCP registration against the daemon's
	// loopback streamable-HTTP MCP endpoint. Codex has no -s user scope
	// flag, so scopeArgs is nil.
	return registerKnowledgeMCP("codex", nil, f.dryRun)
}

// resolveCodexDest returns the split destination roots. Empty flags
// default to ~/.agents/skills (skills) and ~/.codex/agents (agents).
// Non-empty flags are tilde-expanded via the shared expandTilde helper.
func resolveCodexDest(skillsFlag, agentsFlag string) (codexDest, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return codexDest{}, fmt.Errorf("resolve home directory: %w", err)
	}
	skills := filepath.Join(home, ".agents", "skills")
	if skillsFlag != "" {
		skills = expandTilde(skillsFlag)
	}
	agents := filepath.Join(home, ".codex", "agents")
	if agentsFlag != "" {
		agents = expandTilde(agentsFlag)
	}
	return codexDest{skills: skills, agents: agents}, nil
}

// codexOutPath maps a raw .claude embed path to its on-disk codex
// destination under the split roots:
//
//	skills/<rest>      → <skills-root>/<rest>    (written verbatim)
//	agents/<name>.md   → <agents-root>/<name>.toml (translated to TOML)
//
// Returns ("", false) for any path that is neither a skill nor an agent
// .md asset (defensive — the embed only carries those two trees; a
// stray non-.md under agents/ is skipped rather than mis-written).
func codexOutPath(dest codexDest, embedPath string) (string, bool) {
	switch {
	case strings.HasPrefix(embedPath, "skills/"):
		return filepath.Join(dest.skills, strings.TrimPrefix(embedPath, "skills/")), true
	case strings.HasPrefix(embedPath, "agents/") && strings.HasSuffix(embedPath, ".md"):
		stem := strings.TrimSuffix(strings.TrimPrefix(embedPath, "agents/"), ".md")
		return filepath.Join(dest.agents, stem+".toml"), true
	default:
		return "", false
	}
}

// codexContent returns the bytes to write at embedPath's destination:
// skill files pass through verbatim; agent .md files are translated to
// codex TOML via codexassets.TranslateAgent. The raw bytes come from
// the shared assets.Files embed.
func codexContent(embedPath string, raw []byte) ([]byte, error) {
	if strings.HasPrefix(embedPath, "agents/") {
		out, err := codexassets.TranslateAgent(raw)
		if err != nil {
			return nil, fmt.Errorf("translate agent %s: %w", embedPath, err)
		}
		return out, nil
	}
	return raw, nil
}

// embeddedCodexPaths returns the sorted list of regular-file paths in
// the shared raw asset FS that map to a codex destination. Sorted for
// deterministic output across runs.
func embeddedCodexPaths() ([]string, error) {
	var paths []string
	err := fs.WalkDir(assets.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk embedded FS: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

// installCodexAssets walks the shared raw FS and writes each file to its
// split-root destination — skills verbatim, agents translated to codex
// TOML. Returns the count of files written (or that would be written, in
// dry-run mode).
func installCodexAssets(dest codexDest, dryRun, verbose bool) (int, error) {
	paths, err := embeddedCodexPaths()
	if err != nil {
		return 0, err
	}

	written := 0
	for _, p := range paths {
		out, ok := codexOutPath(dest, p)
		if !ok {
			continue
		}
		raw, err := assets.Files.ReadFile(p)
		if err != nil {
			return written, fmt.Errorf("read embedded %s: %w", p, err)
		}
		data, err := codexContent(p, raw)
		if err != nil {
			return written, err
		}
		if dryRun {
			if verbose {
				fmt.Fprintf(os.Stdout, "  would write %s (%d bytes)\n", out, len(data))
			}
			written++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
			return written, fmt.Errorf("mkdir %s: %w", filepath.Dir(out), err)
		}
		if err := os.WriteFile(out, data, 0o644); err != nil { //nolint:gosec // user-readable docs/config, 0644 is correct
			return written, fmt.Errorf("write %s: %w", out, err)
		}
		if verbose {
			fmt.Fprintf(os.Stdout, "  wrote %s (%d bytes)\n", out, len(data))
		}
		written++
	}
	return written, nil
}

// runCodexDiff walks the shared raw FS, compares each file's codex
// representation (skills verbatim, agents translated to TOML) against
// the on-disk version at its split-root destination, and prints either
// a "NEW:" line (file absent) or a unified diff (file differs).
// Identical files are skipped. Reuses printUnifiedDiff from
// install_claude_assets.go (root-agnostic — takes an existing path +
// the bytes to compare). Closes with a summary count. Never writes.
func runCodexDiff(dest codexDest) error {
	paths, err := embeddedCodexPaths()
	if err != nil {
		return err
	}

	newCount, changedCount, sameCount := 0, 0, 0
	for _, p := range paths {
		out, ok := codexOutPath(dest, p)
		if !ok {
			continue
		}
		raw, err := assets.Files.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", p, err)
		}
		want, err := codexContent(p, raw)
		if err != nil {
			return err
		}
		onDisk, err := os.ReadFile(out)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(os.Stdout, "NEW: %s (%d bytes)\n", out, len(want))
				newCount++
				continue
			}
			return fmt.Errorf("read on-disk %s: %w", out, err)
		}
		if bytes.Equal(want, onDisk) {
			sameCount++
			continue
		}
		if err := printUnifiedDiff(out, want); err != nil {
			return fmt.Errorf("diff %s: %w", out, err)
		}
		changedCount++
	}

	fmt.Fprintf(os.Stdout, "\nknowledge install-codex-assets --diff: %d new, %d changed, %d unchanged (skills→%s, agents→%s)\n",
		newCount, changedCount, sameCount, dest.skills, dest.agents)
	return nil
}
