// SPDX-License-Identifier: Apache-2.0

// replace.go — the apply engine for ast operation:"replace" (FUL-308).
//
// The WRITE counterpart to operation:"match". It consumes the existing
// []RawMatch the matcher already produces (the outer matched node under
// Captures["match"] is WHAT to replace; each placeholder's Capture.Text is
// WHAT to interpolate) and:
//
//  1. interpolateTemplate — scans a replacement template with the SAME
//     lexPlaceholder the matcher uses, substituting $NAME -> caps[NAME].Text,
//     $$$NAME -> the verbatim sequence span, $$ -> a literal '$'. Wildcards
//     ($_ / $$$_) are non-referenceable (usage error).
//  2. buildFileEdits — groups matches by file, sorts each file's edits
//     DESCENDING by start byte (apply right-to-left so offsets stay valid),
//     and REFUSES-AND-REPORTS any file with overlapping/nested matches.
//  3. applyEditsToSource — right-to-left byte splice + re-parse gate
//     (RootNode().HasError() -> reject the file, mirroring compilePattern).
//  4. ApplyReplace + atomicWriteString — the public entry the handler calls:
//     dry-run by default (diffs only), opt-in atomic write on apply.
//
// 100% CLIENT-SIDE: it edits the working tree. The server has no
// filesystem-write role; atomicWriteString mirrors the server's
// atomicWriteFile (cmd/knowledge-server/internal/store/atomic_write.go),
// which is server-internal and therefore unimportable from the client.

package ast

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// interpolateTemplate scans a replacement template and substitutes captures
// from a single match. It reuses lexPlaceholder (dsl.go:157) verbatim so the
// replacement grammar tracks the match grammar exactly.
//
// Substitution rules (the ticket's U1/U2/U4):
//   - $NAME    -> caps[NAME].Text (verbatim; absent name -> usage error)
//   - $$$NAME  -> caps[NAME].Text (the verbatim sequence span the matcher
//     already built via bindSeq; "" for an empty seq; absent -> usage error)
//   - $$       -> a single literal '$' (escape), ONLY when the byte after the
//     two dollars is NOT a third '$'. $$$NAME / $$$_ is a sequence reference
//     and delegates to lexPlaceholder — the leading $$ is NOT consumed as an
//     escape.
//   - $_ / $$$_ -> wildcards, NOT referenceable -> usage error.
func interpolateTemplate(template string, caps map[string]Capture) (string, error) {
	var b strings.Builder
	b.Grow(len(template))

	n := len(template)
	i := 0
	for i < n {
		if template[i] != '$' {
			b.WriteByte(template[i])
			i++
			continue
		}

		// Count the consecutive '$' run the way lexPlaceholder does
		// (dsl.go:163-166), capped at 4. A run of exactly two — i.e. two
		// dollars followed by a non-'$' — is the literal-'$' escape. Three
		// dollars ($$$NAME / $$$_) is a sequence reference and must fall
		// through to lexPlaceholder; the leading $$ is NOT an escape there.
		dollars := 1
		for j := i + 1; j < n && template[j] == '$' && dollars < 4; j++ {
			dollars++
		}
		if dollars == 2 {
			// $$ followed by a non-'$' -> emit a single literal '$'.
			b.WriteByte('$')
			i += 2
			continue
		}

		ph, end, err := lexPlaceholder(template, i)
		if err != nil {
			return "", fmt.Errorf("replacement template: %w", err)
		}
		switch ph.Kind {
		case KindNode, KindSeq:
			capture, ok := caps[ph.Name]
			if !ok {
				return "", fmt.Errorf("replacement references unbound capture $%s", ph.Name)
			}
			b.WriteString(capture.Text)
		case KindNodeWild, KindSeqWild:
			return "", fmt.Errorf("replacement cannot reference a wildcard placeholder ($_ / $$$_)")
		default:
			return "", fmt.Errorf("replacement template: unknown placeholder kind %q", ph.Kind)
		}
		i = end
	}
	return b.String(), nil
}

// fileEdit is a single byte-range splice within one file: replace
// [Start, End) with Replacement.
type fileEdit struct {
	Start       uint32
	End         uint32
	Replacement string
}

// buildFileEdits turns []RawMatch into per-file edit lists, sorted DESCENDING
// by Start so the right-to-left splice keeps earlier byte offsets valid. The
// outer matched span (m.Captures["match"], set by toRawMatch) is the range to
// replace; the interpolated template is the replacement.
//
// Return shape:
//   - edits: file path -> DESC-sorted edits (overlapping files excluded).
//   - refused: file paths whose matches overlap/nest — dropped entirely and
//     reported, never guessed (U3 refuse-and-report).
//   - error: ONLY a malformed template (interpolateTemplate usage error)
//     fails the whole op; per-file overlap is reported, not errored.
func buildFileEdits(matches []RawMatch, template string) (map[string][]fileEdit, []string, error) {
	byFile := make(map[string][]fileEdit)
	for _, m := range matches {
		outer, ok := m.Captures["match"]
		if !ok {
			return nil, nil, fmt.Errorf("internal: match in %s has no outer 'match' capture", m.FilePath)
		}
		repl, err := interpolateTemplate(template, m.Captures)
		if err != nil {
			return nil, nil, err
		}
		byFile[m.FilePath] = append(byFile[m.FilePath], fileEdit{
			Start:       outer.StartByte,
			End:         outer.EndByte,
			Replacement: repl,
		})
	}

	var refused []string
	for path, edits := range byFile {
		sort.Slice(edits, func(i, j int) bool { return edits[i].Start > edits[j].Start })
		if hasOverlap(edits) {
			delete(byFile, path)
			refused = append(refused, path)
			continue
		}
		byFile[path] = edits
	}
	sort.Strings(refused)
	return byFile, refused, nil
}

// hasOverlap reports whether any two edits intersect (including full
// nesting). edits MUST be sorted DESCENDING by Start. After that sort,
// edits[i] is the LATER-in-file edit and edits[i+1] is the EARLIER one; they
// intersect when the earlier edit's End extends into the later edit's Start —
// i.e. edits[i+1].End > edits[i].Start. (walkAll yields only strictly
// nested-or-disjoint tree spans, never partial overlap.)
func hasOverlap(edits []fileEdit) bool {
	for i := 0; i+1 < len(edits); i++ {
		if edits[i+1].End > edits[i].Start {
			return true
		}
	}
	return false
}

// applyEditsToSource splices DESC-sorted edits into src right-to-left, then
// re-parses the result and REJECTS it if the rewrite introduced a syntax
// error (RootNode().HasError() — the same gate compilePattern applies at
// engine.go:130-145). The right-to-left order means every later (lower-offset)
// edit's byte range stays valid against the not-yet-mutated prefix.
//
// edits MUST be DESC-sorted by Start (the invariant buildFileEdits
// establishes). On a re-parse failure newSrc is dropped and an error is
// returned; the caller maps it to a per-file rejection.
func applyEditsToSource(ctx context.Context, src []byte, edits []fileEdit, lang treesitter.Language) ([]byte, error) {
	out := append([]byte{}, src...)
	for _, e := range edits {
		// Defensive bound check — buildFileEdits derives offsets from the
		// matcher's own byte ranges, but a corrupt input must not panic.
		if e.Start > e.End || int(e.End) > len(out) {
			return nil, fmt.Errorf("edit range [%d,%d) out of bounds for %d-byte source", e.Start, e.End, len(out))
		}
		out = append(append(append([]byte{}, out[:e.Start]...), []byte(e.Replacement)...), out[e.End:]...)
	}

	parser := treesitter.NewParser()
	defer parser.Close()
	tree, perr := parser.Parse(ctx, out, lang)
	if perr != nil {
		return nil, fmt.Errorf("rewritten source failed re-parse (%w); edit rejected", perr)
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil || root.HasError() {
		return nil, fmt.Errorf("rewritten source failed re-parse (HasError); edit rejected")
	}
	return out, nil
}

// ReplaceResult is the LLM-facing outcome of an ApplyReplace run.
type ReplaceResult struct {
	// FilesTouched is the count of files that were (dry-run) or would be
	// (apply) rewritten — i.e. files with applied edits.
	FilesTouched int `json:"files_touched"`
	// MatchesReplaced is the total number of spliced edits across all touched
	// files.
	MatchesReplaced int `json:"matches_replaced"`
	// RefusedFiles carry overlapping/nested matches and were dropped whole.
	RefusedFiles []string `json:"refused_files,omitempty"`
	// RejectedFiles failed the re-parse gate after splicing and were never
	// written.
	RejectedFiles []string `json:"rejected_files,omitempty"`
	// Diffs maps each touched file's repo-relative path to its unified diff.
	Diffs map[string]string `json:"diffs,omitempty"`
	// Applied is false for a dry run, true when edits were written to disk.
	Applied bool `json:"applied"`
}

// ApplyReplace is the public entry the handler calls. It builds per-file edits
// from matches, re-parses each rewritten file behind the HasError gate, and —
// when !dryRun — writes the survivors atomically. Dry-run is the default
// caller contract: it populates Diffs without touching disk.
//
// File I/O is serial: the CPU-heavy match already ran NumCPU-parallel in
// ast.Match; the post-match apply is bounded per-file I/O with no in-tree
// per-file-write parallel analog. Writes are atomic per-file (tmp + rename).
func ApplyReplace(ctx context.Context, repoDir string, lang treesitter.Language, matches []RawMatch, template string, dryRun bool) (ReplaceResult, error) {
	byFile, refused, err := buildFileEdits(matches, template)
	if err != nil {
		return ReplaceResult{}, err
	}

	res := ReplaceResult{
		RefusedFiles: refused,
		Diffs:        map[string]string{},
		Applied:      !dryRun,
	}

	// Stable file order for deterministic output.
	paths := make([]string, 0, len(byFile))
	for path := range byFile {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, relPath := range paths {
		edits := byFile[relPath]
		absPath := filepath.Join(repoDir, relPath)
		oldSrc, readErr := os.ReadFile(absPath) //nolint:gosec // path derived from repoDir + matcher-produced relPath.
		if readErr != nil {
			return ReplaceResult{}, fmt.Errorf("read %s: %w", relPath, readErr)
		}

		newSrc, applyErr := applyEditsToSource(ctx, oldSrc, edits, lang)
		if applyErr != nil {
			res.RejectedFiles = append(res.RejectedFiles, relPath)
			continue
		}

		diff, diffErr := unifiedDiff(relPath, oldSrc, newSrc)
		if diffErr != nil {
			return ReplaceResult{}, fmt.Errorf("diff %s: %w", relPath, diffErr)
		}
		res.Diffs[relPath] = diff
		res.FilesTouched++
		res.MatchesReplaced += len(edits)

		if !dryRun {
			if writeErr := atomicWriteString(absPath, newSrc); writeErr != nil {
				return ReplaceResult{}, fmt.Errorf("write %s: %w", relPath, writeErr)
			}
		}
	}
	sort.Strings(res.RejectedFiles)
	return res, nil
}

// unifiedDiff renders a unified diff between oldSrc and newSrc for relPath
// using go-difflib. Empty for an unchanged file; ---/+++/@@ hunk headers and
// -/+ lines otherwise.
func unifiedDiff(relPath string, oldSrc, newSrc []byte) (string, error) {
	return difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(oldSrc)),
		B:        difflib.SplitLines(string(newSrc)),
		FromFile: "a/" + relPath,
		ToFile:   "b/" + relPath,
		Context:  3,
	})
}

// atomicWriteString writes data to path atomically: temp -> write -> fsync ->
// close -> rename -> parent-dir fsync, cleaning up the temp on any failure.
//
// This MIRRORS the server's atomicWriteFile
// (cmd/knowledge-server/internal/store/atomic_write.go:16) rather than reusing
// it: that helper is unexported and lives under cmd/knowledge-server/internal,
// which the client (cmd/knowledge/internal) cannot import — the same
// client/server-split duplication rationale flex_types.go:18-25 documents.
func atomicWriteString(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec // path derived from repoDir + matcher-produced relPath.
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	if dir, openErr := os.Open(filepath.Dir(path)); openErr == nil {
		if syncErr := dir.Sync(); syncErr != nil {
			slog.Warn("atomicWriteString: parent directory fsync failed (rename is durable)",
				"path", path, "error", syncErr)
		}
		_ = dir.Close()
	} else {
		slog.Warn("atomicWriteString: could not open parent directory for fsync",
			"path", path, "error", openErr)
	}
	return nil
}
