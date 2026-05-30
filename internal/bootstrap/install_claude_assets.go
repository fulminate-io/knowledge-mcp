// SPDX-License-Identifier: Apache-2.0

// install_claude_assets.go — `knowledge install-claude-assets` CLI
// subcommand. Walks the embedded .claude/ tree and writes its
// contents under ~/.claude/, so users who installed via brew (or
// tarball) get the same agent + skill catalog the source-built devs
// have.
//
// CLI mode, not MCP mode: legitimate to write to stdout. The MCP-
// stdio-discipline rule from lifecycle.go does not apply here.

package bootstrap

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/assets"
)

// installAssetsFlags holds the parsed flags for the subcommand.
type installAssetsFlags struct {
	dest         string
	claudeMDDest string
	dryRun       bool
	verbose      bool
	diff         bool
	noMCP        bool
}

// runInstallClaudeAssets walks the embedded FS and copies every
// regular file to <dest>/<embed-relative-path>. Default dest is
// ~/.claude. Existing files are overwritten (these are project-
// shipped agents/skills that get updated as the project evolves).
//
// Output: prints one line per file written ("wrote PATH (NN bytes)").
// Closes with a summary count.
//
// Flags:
//   - --dry-run: walk + report what would be written without touching disk
//   - --diff: read-only mode that prints a unified diff of every file
//     that differs from the embedded version (uses /usr/bin/diff
//     for changed files; lists new files inline). Implies
//     --dry-run — never writes when --diff is set.
//   - --verbose: per-file write line (otherwise just summary)
//
// Exit:
//   - 0 on full success
//   - 1 on any I/O error mid-walk (caller surfaces via stderr)
func runInstallClaudeAssets(args []string) error {
	fs := flag.NewFlagSet("knowledge install-claude-assets", flag.ContinueOnError)
	var f installAssetsFlags
	fs.StringVar(&f.dest, "dest", "", "Destination directory (default ~/.claude)")
	fs.StringVar(&f.claudeMDDest, "claude-md-dest", "", "CLAUDE.md destination path (default ~/.claude/CLAUDE.md)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "Print what would be written without touching disk")
	fs.BoolVar(&f.diff, "diff", false, "Print a unified diff of every file that differs (read-only; implies --dry-run)")
	fs.BoolVar(&f.verbose, "verbose", false, "Print each file path written (default: summary only)")
	fs.BoolVar(&f.noMCP, "no-mcp", false, "Skip registering the knowledge MCP server with the client (default: register at user scope)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dest, err := resolveClaudeDest(f.dest)
	if err != nil {
		return err
	}
	claudeMD, err := resolveClaudeMD(f.claudeMDDest)
	if err != nil {
		return err
	}

	if f.diff {
		// --diff is strictly preview; run the dedicated path that
		// prints diffs without writing anything, then report the
		// CLAUDE.md managed-block state via the shared reporter.
		if err := runDiff(dest); err != nil {
			return err
		}
		return diffManagedFile(claudeMD, string(assets.KnowledgeTools), "CLAUDE.md")
	}

	written, err := installAssets(dest, f.dryRun, f.verbose)
	if err != nil {
		return err
	}

	mdChanged, err := writeManagedFile(claudeMD, string(assets.KnowledgeTools), f.dryRun)
	if err != nil {
		return err
	}

	verb := "wrote"
	if f.dryRun {
		verb = "would write"
	}
	mdNote := "CLAUDE.md in sync"
	if mdChanged {
		mdNote = fmt.Sprintf("%s CLAUDE.md managed block in %s", verb, claudeMD)
	}
	fmt.Fprintf(os.Stdout, "knowledge install-claude-assets: %s %d files under %s; %s\n", verb, written, dest, mdNote)

	if f.noMCP {
		fmt.Fprintln(os.Stdout, "  --no-mcp: skipped MCP registration")
		return nil
	}
	// Default-on, non-fatal MCP registration at user scope.
	return registerKnowledgeMCP("claude", []string{"-s", "user"}, f.dryRun)
}

// runDiff walks the embedded FS, compares each file against the on-
// disk version, and prints either a "NEW:" line (file doesn't exist
// yet) or a unified diff (file exists but differs). Identical files
// are skipped silently. Closes with a summary count.
//
// Uses /usr/bin/diff for the diff computation — POSIX-standard,
// available on macOS and Linux. On Windows the diff binary may be
// missing; we surface that as a clear error message rather than
// silently degrading.
func runDiff(dest string) error {
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
		return fmt.Errorf("walk embedded FS: %w", err)
	}
	sort.Strings(paths)

	newCount, changedCount, sameCount := 0, 0, 0
	for _, p := range paths {
		embedded, err := assets.Files.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", p, err)
		}
		out := filepath.Join(dest, p)
		onDisk, err := os.ReadFile(out)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(os.Stdout, "NEW: %s (%d bytes)\n", out, len(embedded))
				newCount++
				continue
			}
			return fmt.Errorf("read on-disk %s: %w", out, err)
		}
		if bytes.Equal(embedded, onDisk) {
			sameCount++
			continue
		}
		// Files differ — pipe through `diff -u` for a real unified
		// diff. Write embedded content to a tempfile so diff has
		// two paths to compare.
		if err := printUnifiedDiff(out, embedded); err != nil {
			return fmt.Errorf("diff %s: %w", out, err)
		}
		changedCount++
	}

	fmt.Fprintf(os.Stdout, "\nknowledge install-claude-assets --diff: %d new, %d changed, %d unchanged under %s\n",
		newCount, changedCount, sameCount, dest)
	return nil
}

// printUnifiedDiff invokes `diff -u <existingPath> <embeddedTempfile>`
// and streams the output to stdout. Returns an error when /usr/bin/diff
// is unavailable; a non-zero exit from diff (which means files differ
// — diff's intended signaling) is treated as success.
func printUnifiedDiff(existingPath string, embedded []byte) error {
	tmp, err := os.CreateTemp("", "knowledge-claude-diff-*.md")
	if err != nil {
		return fmt.Errorf("create tempfile: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(embedded); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write tempfile: %w", err)
	}
	_ = tmp.Close()

	cmd := exec.Command("diff", "-u", existingPath, tmp.Name())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// diff exits non-zero when files differ — that's the SIGNAL,
		// not a failure. Only a real exec error (binary missing)
		// matters here.
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil
		}
		return fmt.Errorf("exec diff: %w", err)
	}
	return nil
}

// resolveClaudeDest returns the destination directory for the install.
// When the flag is empty (the common case), defaults to ~/.claude.
// Tilde expansion uses the existing expandTilde helper so behavior
// matches the rest of the CLI.
func resolveClaudeDest(flagDest string) (string, error) {
	if flagDest != "" {
		return expandTilde(flagDest), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
}

// resolveClaudeMD returns the destination CLAUDE.md path. Empty flag
// defaults to ~/.claude/CLAUDE.md; a non-empty flag is tilde-expanded.
// Mirrors resolveCodexAgentsMD (codex_agents_md.go) — the codex one
// hardcodes ~/.codex/AGENTS.md, so a Claude-specific mirror is needed.
func resolveClaudeMD(flagDest string) (string, error) {
	if flagDest != "" {
		return expandTilde(flagDest), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "CLAUDE.md"), nil
}

// installAssets walks the embedded FS and writes regular files to
// dest with the same relative layout. Returns the count of files
// written (or that would have been written, in dry-run mode).
//
// Why sort: deterministic output makes diffs across runs (and brew
// reinstalls) easier to reason about. fs.WalkDir already walks in
// lexical order but we sort the gathered paths once more so any
// future caller-side filtering preserves the property.
func installAssets(dest string, dryRun, verbose bool) (int, error) {
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
		return 0, fmt.Errorf("walk embedded FS: %w", err)
	}
	sort.Strings(paths)

	written := 0
	for _, p := range paths {
		data, err := assets.Files.ReadFile(p)
		if err != nil {
			return written, fmt.Errorf("read embedded %s: %w", p, err)
		}
		out := filepath.Join(dest, p)
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
		if err := os.WriteFile(out, data, 0o644); err != nil { //nolint:gosec // user-readable docs, 0644 is correct
			return written, fmt.Errorf("write %s: %w", out, err)
		}
		if verbose {
			fmt.Fprintf(os.Stdout, "  wrote %s (%d bytes)\n", out, len(data))
		}
		written++
	}
	return written, nil
}
