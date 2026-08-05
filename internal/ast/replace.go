// SPDX-License-Identifier: Apache-2.0

// replace.go — the apply engine for ast operation:"replace".
//
// The WRITE counterpart to operation:"match". It consumes the existing
// []RawMatch the matcher already produces (the outer matched node under
// Captures["match"] is WHAT to replace; each placeholder's Capture.Text is
// WHAT to interpolate) and:
//
//  1. interpolateTemplate — scans a replacement template with the SAME
//     lexPlaceholder the matcher uses, substituting $NAME -> caps[NAME].Text,
//     $$$NAME -> the verbatim sequence span, $$ -> a literal '$'. Wildcards
//     ($_ / $$$_) are non-referenceable (usage error). It renders TEMPLATE
//     text and knows nothing about the source; spliceFromSource calls it for
//     the part of a replacement the caller actually rewrote.
//  1a. spliceFromSource (splice.go) — builds each match's replacement
//     SOURCE-ANCHORED: everything inside the matched span the template did not
//     explicitly rewrite is copied from the file's own bytes, so inter-token
//     whitespace, line structure, indentation and unnamed anonymous tokens
//     survive and an identity template is a byte-identical no-op.
//  2. buildFileEdits — groups matches by file, sorts each file's edits
//     DESCENDING by start byte (apply right-to-left so offsets stay valid),
//     and REFUSES-AND-REPORTS any file with overlapping/nested matches.
//  3. baselineParseFailures (replace_baseline.go) — parses every candidate
//     file's ORIGINAL bytes first. A file that already carries a grammar error
//     is reported with the error's location and never spliced, so the gate
//     below can only ever fire on breakage the edit caused.
//  4. applyEditsToSource — right-to-left byte splice + re-parse gate
//     (parseErrorSite -> reject the file, mirroring compilePattern).
//  5. ApplyReplace + atomicWriteString — the public entry the handler calls:
//     dry-run by default (diffs only), opt-in atomic write on apply.
//
// 100% CLIENT-SIDE: it edits the working tree. The server has no
// filesystem-write role; atomicWriteString mirrors the server's
// atomicWriteFile (cmd/knowledge-server/internal/store/atomic_write.go),
// which is server-internal and therefore unimportable from the client.

package ast

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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
// replace; spliceFromSource builds the replacement, taking every byte the
// template did not rewrite from that file's own source.
//
// srcByFile maps each matched file's repo-relative path to its bytes. A path
// with no entry still yields an edit — spliceFromSource falls back to a bare
// interpolateTemplate when it has no source to anchor against.
//
// Return shape:
//   - edits: file path -> DESC-sorted edits (overlapping files excluded).
//   - refused: file paths whose matches overlap/nest — dropped entirely and
//     reported, never guessed (U3 refuse-and-report).
//   - error: ONLY a malformed template (interpolateTemplate usage error)
//     fails the whole op; per-file overlap is reported, not errored.
func buildFileEdits(matches []RawMatch, template string, srcByFile map[string][]byte) (map[string][]fileEdit, []string, error) {
	byFile := make(map[string][]fileEdit)
	for _, m := range matches {
		outer, ok := m.Captures[outerCaptureName]
		if !ok {
			return nil, nil, fmt.Errorf("internal: match in %s has no outer 'match' capture", m.FilePath)
		}
		repl, err := spliceFromSource(m, template, srcByFile[m.FilePath])
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

// changedEditCount reports how many of a file's edits actually move bytes, by
// comparing each replacement against the source span it replaces.
//
// Every edit's [Start, End) addresses the ORIGINAL source — that is what the
// right-to-left splice order preserves — so src here is the pre-edit bytes.
// The ranges were already proven in-bounds by applyEditsToSource, which runs
// against the same bytes before this is called; the length guard is a
// defensive floor, not an expected branch.
func changedEditCount(src []byte, edits []fileEdit) int {
	n := 0
	for _, e := range edits {
		if e.Start > e.End || int(e.End) > len(src) {
			continue
		}
		if !bytes.Equal(src[e.Start:e.End], []byte(e.Replacement)) {
			n++
		}
	}
	return n
}

// spliceEdits assembles the rewritten bytes for one file in a SINGLE forward
// pass over an exactly pre-sized output: each inter-edit source run and each
// replacement is copied exactly once. It runs NO re-parse — assembly only, so
// the identical loop backs both the apply path (behind the gate
// applyEditsToSource runs) and the allocation benchmark that measures it.
//
// edits MUST be DESC-sorted by Start (the invariant buildFileEdits
// establishes). Bounds are validated against the immutable src (there is no
// mutating buffer to measure against here) in the same pre-pass that sums the
// exact output size. The forward walk then consumes the edits in ASCENDING
// source order (reverse index), which is why it slices src[prev:e.Start]: that
// PANICS on an out-of-order or overlapping edit, so a monotonicity guard turns
// the violation into an error. Adjacent-touching edits (e.Start == prev) are
// legal — hasOverlap permits them — so the guard is strict `<`, never `<=`.
func spliceEdits(src []byte, edits []fileEdit) ([]byte, error) {
	total := len(src)
	for _, e := range edits {
		// Bound check against the immutable input — buildFileEdits derives
		// offsets from the matcher's own byte ranges, but a corrupt input must
		// not panic.
		if e.Start > e.End || int(e.End) > len(src) {
			return nil, fmt.Errorf("edit range [%d,%d) out of bounds for %d-byte source", e.Start, e.End, len(src))
		}
		total += len(e.Replacement) - int(e.End-e.Start)
	}
	if total < 0 {
		total = 0
	}

	out := make([]byte, 0, total)
	prev := 0
	for _, v := range slices.Backward(edits) {
		e := v
		if int(e.Start) < prev {
			return nil, fmt.Errorf("edit range [%d,%d) is out of order or overlaps an earlier edit ending at %d", e.Start, e.End, prev)
		}
		out = append(out, src[prev:e.Start]...)
		out = append(out, e.Replacement...)
		prev = int(e.End)
	}
	out = append(out, src[prev:]...)
	return out, nil
}

// applyEditsToSource assembles the rewritten bytes via spliceEdits, then
// re-parses the result and REJECTS it if the rewrite introduced a syntax
// error (the same gate compilePattern applies at engine.go:130-145).
//
// The re-parse goes through parseErrorSite (replace_baseline.go), the same
// call the PRE-EDIT baseline makes. A rejection here therefore means the edit
// broke a file the baseline certified clean, and nothing else: files that were
// already ungrammatical never reach this function.
//
// edits MUST be DESC-sorted by Start (the invariant buildFileEdits
// establishes). On a re-parse failure newSrc is dropped and an error is
// returned; the caller maps it to a per-file rejection.
func applyEditsToSource(ctx context.Context, src []byte, edits []fileEdit, lang treesitter.Language) ([]byte, error) {
	out, err := spliceEdits(src, edits)
	if err != nil {
		return nil, err
	}

	line, _, hasError, perr := parseErrorSite(ctx, out, lang)
	if perr != nil {
		return nil, fmt.Errorf("rewritten source failed re-parse (%w); edit rejected", perr)
	}
	if hasError {
		return nil, fmt.Errorf("rewritten source failed re-parse (grammar error at line %d); edit rejected", line)
	}
	return out, nil
}

// ReplaceResult is the LLM-facing outcome of an ApplyReplace run.
type ReplaceResult struct {
	// FilesMatched is the count of files that produced at least one edit and
	// passed the gate. It says nothing about whether the bytes moved.
	FilesMatched int `json:"files_matched"`
	// FilesChanged is the subset of FilesMatched whose bytes actually differ.
	// An identity template splices byte-identically, so it matches files and
	// changes none — the distinction FilesMatched alone cannot carry.
	FilesChanged int `json:"files_changed"`
	// MatchesReplaced is the total number of spliced edits across all matched
	// files.
	MatchesReplaced int `json:"matches_replaced"`
	// MatchesChanged is the subset of MatchesReplaced that were not
	// byte-identical, measured per splice rather than inferred from the
	// whole-file diff (which cannot attribute a change to one splice among
	// several in the same file).
	MatchesChanged int `json:"matches_changed"`
	// RefusedFiles carry overlapping/nested matches and were dropped whole.
	RefusedFiles []string `json:"refused_files,omitempty"`
	// RejectedFiles parsed CLEAN before the edit and failed the re-parse gate
	// after splicing — i.e. the edit broke them. They were never written. A
	// file that was already ungrammatical is reported in
	// PreexistingParseFailures instead and never reaches the gate.
	RejectedFiles []string `json:"rejected_files,omitempty"`
	// PreexistingParseFailures name the files whose ORIGINAL source already
	// carried a grammar error, with the site of that error. They were never
	// spliced and never written: nothing can be concluded about an edit to a
	// file that did not parse before the edit.
	PreexistingParseFailures []ParseFailure `json:"preexisting_parse_failures,omitempty"`
	// Diffs maps each touched file's repo-relative path to its unified diff.
	Diffs map[string]string `json:"diffs,omitempty"`
	// Applied is false for a dry run, true when edits were written to disk.
	Applied bool `json:"applied"`
}

// ApplyReplace is the public entry the handler calls. It reads every matched
// file, builds per-file edits from matches, takes a pre-edit parse baseline
// over the whole candidate set, re-parses each rewritten file behind the same
// gate, and — when !dryRun — writes the survivors atomically. Dry-run is the
// default caller contract: it populates Diffs without touching disk.
//
// cleanHint carries the match-time parse hints (WalkStats.CleanHint) so the
// baseline can skip re-parsing a file the match already parsed clean, guarded by
// a size+fnv64a digest check against the bytes about to be spliced (a file
// mutated between match and replace mismatches and is re-parsed). A nil hint —
// every non-handler caller passes nil — re-parses every file, exactly the
// pre-hint behavior. The digest is over the T2 bytes actually spliced, so a
// match means the certified-clean bytes ARE the spliced bytes: no
// destroy-before-persist gap.
//
// The read happens up front because the replacement itself is source-anchored:
// spliceFromSource needs each match's own file bytes to copy the parts the
// template left alone. Each file is still read exactly once — the same bytes
// feed the splice and the apply.
//
// File I/O is serial: the CPU-heavy match already ran NumCPU-parallel in
// ast.Match; the post-match apply is bounded per-file I/O with no in-tree
// per-file-write parallel analog. Writes are atomic per-file (tmp + rename).
func ApplyReplace(ctx context.Context, repoDir string, lang treesitter.Language, matches []RawMatch, template string, dryRun bool, cleanHint map[string]fileParseHint) (ReplaceResult, error) {
	srcByFile, err := readMatchedSources(repoDir, matches)
	if err != nil {
		return ReplaceResult{}, err
	}

	byFile, refused, err := buildFileEdits(matches, template, srcByFile)
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

	// The PRE-EDIT baseline runs over the whole candidate set before any
	// splice, so a file that was already ungrammatical is excluded before its
	// own write is reached and the report is fully classified even if the loop
	// below stops early.
	res.PreexistingParseFailures, _ = baselineParseFailures(ctx, paths, srcByFile, lang, cleanHint)
	alreadyBroken := make(map[string]struct{}, len(res.PreexistingParseFailures))
	for _, f := range res.PreexistingParseFailures {
		alreadyBroken[f.Path] = struct{}{}
	}

	for _, relPath := range paths {
		if _, broken := alreadyBroken[relPath]; broken {
			continue
		}
		edits := byFile[relPath]
		absPath := filepath.Join(repoDir, relPath)
		oldSrc := srcByFile[relPath]

		newSrc, applyErr := applyEditsToSource(ctx, oldSrc, edits, lang)
		if applyErr != nil {
			res.RejectedFiles = append(res.RejectedFiles, relPath)
			continue
		}

		diff, diffErr := unifiedDiff(relPath, oldSrc, newSrc)
		if diffErr != nil {
			return ReplaceResult{}, fmt.Errorf("diff %s: %w", relPath, diffErr)
		}
		changed := changedEditCount(oldSrc, edits)
		res.Diffs[relPath] = diff
		res.FilesMatched++
		res.MatchesReplaced += len(edits)
		res.MatchesChanged += changed
		if changed > 0 {
			res.FilesChanged++
		}

		if !dryRun {
			if writeErr := atomicWriteString(absPath, newSrc); writeErr != nil {
				return ReplaceResult{}, fmt.Errorf("write %s: %w", relPath, writeErr)
			}
		}
	}
	sort.Strings(res.RejectedFiles)
	return res, nil
}

// readMatchedSources reads each distinct file the match set touches, once,
// keyed by repo-relative path. A read failure fails the whole op: the splice
// cannot anchor against bytes it could not load, and silently degrading to a
// whole-span rewrite would corrupt exactly the file that was hardest to read.
func readMatchedSources(repoDir string, matches []RawMatch) (map[string][]byte, error) {
	out := make(map[string][]byte)
	for _, m := range matches {
		if m.FilePath == "" {
			continue
		}
		if _, done := out[m.FilePath]; done {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repoDir, m.FilePath)) //nolint:gosec // path derived from repoDir + matcher-produced relPath.
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", m.FilePath, err)
		}
		out[m.FilePath] = src
	}
	return out, nil
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
