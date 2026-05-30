// SPDX-License-Identifier: Apache-2.0

// managed_block.go — the shared, clobber-safe managed-block merger used
// by BOTH global-instruction-file writers: the Claude ~/.claude/CLAUDE.md
// writer and the Codex ~/.codex/AGENTS.md writer. The merge logic is
// generic over the block body and the target path — there is exactly ONE
// managed-block merger in the repo.
//
// The managed region is bounded by HTML-comment markers shared across
// both clients. On install we replace only the bytes between the markers
// (or append the block when the markers are absent), leaving everything
// else byte-for-byte intact, so a user's own prose around the block
// survives every re-install.

package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	managedBlockBegin = "<!-- BEGIN knowledge-managed -->"
	managedBlockEnd   = "<!-- END knowledge-managed -->"
)

// managedBlock wraps body in the shared markers, returning the full
// managed region (markers + body) with a trailing newline, ready to
// splice into a CLAUDE.md / AGENTS.md.
func managedBlock(body string) string {
	return managedBlockBegin + "\n" + body + "\n" + managedBlockEnd + "\n"
}

// mergeManagedBlock returns the file content with the managed block
// inserted or refreshed, generic over the block body. Pure function (no
// I/O) so it is trivially testable. User content outside the markers is
// preserved verbatim.
//
// Semantics (clobber-safe):
//   - empty existing → block only.
//   - markers present → replace the managed region (markers included) in
//     place, keeping everything before and after byte-for-byte; consume a
//     single trailing newline after END so re-runs stay idempotent.
//   - markers absent → append the block after existing content, separated
//     by a blank line, preserving the user's prose.
func mergeManagedBlock(existing, blockBody string) string {
	block := managedBlock(blockBody)
	if existing == "" {
		return block
	}
	beginIdx := strings.Index(existing, managedBlockBegin)
	endIdx := strings.Index(existing, managedBlockEnd)
	if beginIdx >= 0 && endIdx > beginIdx {
		// Replace the existing managed region (markers included) in
		// place, keeping everything before and after byte-for-byte.
		before := existing[:beginIdx]
		afterStart := endIdx + len(managedBlockEnd)
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

// writeManagedFile writes the managed block (wrapping body) into the file
// at path, clobber-safe:
//   - No existing file → create it containing only the managed block.
//   - Existing file with both markers → replace only the bytes between
//     them, preserving user prose above and below verbatim.
//   - Existing file without markers → append the managed block after the
//     existing content (separated by a blank line), preserving it.
//
// Returns whether the file content changed (false when already in sync),
// so the caller can report accurately in dry-run mode.
func writeManagedFile(path, body string, dryRun bool) (changed bool, err error) {
	existing, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, fmt.Errorf("read %s: %w", path, readErr)
	}

	next := mergeManagedBlock(string(existing), body)
	if next == string(existing) {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil { //nolint:gosec // user-readable docs, 0644 is correct
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// managedBlockInSync reports whether the managed region at path already
// matches body, reusing the merge-equality check (an in-sync file is one
// where re-merging body changes nothing). exists is false when the file
// is absent (a not-found read is not an error here — callers treat a
// missing file as "needs install"). It is the cheap drift signal shared
// by the doctor check and the startup hint; it never false-positives on
// user prose because mergeManagedBlock only touches the managed region.
func managedBlockInSync(path, body string) (inSync, exists bool, err error) {
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("read %s: %w", path, readErr)
	}
	existing := string(data)
	return mergeManagedBlock(existing, body) == existing, true, nil
}

// diffManagedFile reports, in read-only --diff mode, whether the managed
// block at path would change if body were installed. It never writes.
// A full unified diff is overkill for a single managed region, so it
// reports NEW (file absent) / WOULD UPDATE (block differs) / in-sync.
// Shared by both global-instruction-file twins (Claude CLAUDE.md and
// Codex AGENTS.md) — one diff reporter repo-wide. label names the block
// in the printed lines (e.g. "CLAUDE.md" / "AGENTS.md").
func diffManagedFile(path, body, label string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stdout, "NEW: %s (knowledge-managed %s block)\n", path, label)
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if mergeManagedBlock(string(existing), body) == string(existing) {
		fmt.Fprintf(os.Stdout, "%s managed block in sync: %s\n", label, path)
		return nil
	}
	fmt.Fprintf(os.Stdout, "WOULD UPDATE: %s (knowledge-managed %s block)\n", path, label)
	return nil
}
