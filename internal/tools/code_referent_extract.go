// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"log/slog"
	"regexp"
	"strings"
)

// codeReferentCap bounds how many distinct code referents a single thought
// born-links to. A thought is a hypothesis/observation, not a manifest; ten
// distinct code-graph citations is already a generous hub fan-out, and the cap
// keeps a pathological content-paste from spraying edges. Over-cap referents are
// dropped in first-appearance order (the cap is a measurement-friendly ceiling,
// not an error).
const codeReferentCap = 10

// codeReferentSourceExts is the source-extension allow-list the extractor
// recognizes as the trailing ".ext" of a repo-relative code path. It is a
// DELIBERATE V1 FLOOR transcribed verbatim from the codesync collector's
// hasTopLevelSourceFile repo-detection heuristic
// (cmd/knowledge/internal/collector/codesync/collector.go) — NOT the indexer's
// true language registry. Some genuinely-indexed extensions (.proto, .tf, .sql,
// …) are absent here and are therefore NOT extracted in V1. That under-coverage
// is accepted by design: resolution is resolve-or-drop, so an unlisted extension
// only means a real referent goes unlinked (never a wrong edge), which keeps the
// floor simple and honest versus duplicating the full language set. Keep this
// list in lockstep with the heuristic it mirrors.
var codeReferentSourceExts = []string{
	"go", "py", "js", "ts", "jsx", "tsx", "rs",
	"java", "kt", "rb", "php", "c", "cc", "cpp",
	"h", "hpp", "cs", "swift", "scala", "lua",
	"sh", "pl", "ex", "exs", "clj", "hs", "ml",
}

// codeReferentPattern matches a repo-relative source path ending in an allowed
// extension, optionally followed by a ":Symbol" or ":Type.Method" code-graph
// node-id suffix.
//
//   - The path body is one-or-more "/"-joined segments of [A-Za-z0-9_.-], ending
//     in "name.ext" where ext is one of codeReferentSourceExts. Requiring at
//     least one "/" keeps the match to genuine repo-relative paths (a bare
//     "foo.go" with no directory is not a code-graph node id and would be too
//     greedy against prose).
//   - The optional ":<ident>(.<ident>)?" suffix is an identifier (and at most one
//     dot-joined Method); a digits-only suffix is NOT matched, so a file:line form
//     like "wire.go:457" yields only the bare path (and the bare path is dropped
//     anyway without a leading directory) — never a bogus node id.
//
// The extension list is interpolated into the pattern at package-init from
// codeReferentSourceExts so the two cannot drift.
var codeReferentPattern = regexp.MustCompile(
	`(?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+\.(?:` +
		strings.Join(codeReferentSourceExts, "|") +
		`)(?::[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)?`,
)

// extractCodeReferents lexes summary then content (in that order) into a deduped,
// first-appearance-ordered, capped slice of candidate code-graph node-ID strings.
// It is a pure regex-grade extractor: no graph access, no resolution — every
// returned string is a *candidate* the resolver later resolves-or-drops.
//
// Forms recognized: a repo-relative source path with an indexed extension
// (path/to/file.go), and the path:Symbol / path:Type.Method node-id forms. A
// file:line form (wire.go:457) is rejected (the colon suffix must be an
// identifier, not digits). Bare prose, URLs, and markdown links yield nothing.
//
// Results are deduped on first appearance and capped at codeReferentCap; an
// over-cap body keeps the first codeReferentCap in appearance order and emits a
// debug log (observability only — the cap behavior, not the log line, is the
// contract).
func extractCodeReferents(summary, content string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, body := range []string{summary, content} {
		for _, m := range codeReferentPattern.FindAllStringIndex(body, -1) {
			start, end := m[0], m[1]
			// Reject a match that sits inside a URL: a "://" anywhere to the left
			// of the match with no intervening whitespace means the candidate is a
			// URL path component (e.g. https://host/pkg/file.go), not a
			// repo-relative referent.
			if precededByURLScheme(body, start) {
				continue
			}
			cand := strings.TrimRight(body[start:end], `.,;:)]}"'`)
			if cand == "" {
				continue
			}
			if _, dup := seen[cand]; dup {
				continue
			}
			seen[cand] = struct{}{}
			out = append(out, cand)
		}
	}
	if len(out) > codeReferentCap {
		slog.Debug("code-referent extraction capped",
			"found", len(out), "cap", codeReferentCap)
		out = out[:codeReferentCap]
	}
	return out
}

// precededByURLScheme reports whether the text ending at idx is part of a URL —
// i.e. a "://" scheme separator appears to the left of idx within the same
// whitespace-delimited token. It walks left from idx to the nearest whitespace
// (or start) and checks for "://" in that span.
func precededByURLScheme(body string, idx int) bool {
	left := strings.LastIndexAny(body[:idx], " \t\n\r")
	prefix := body[left+1 : idx]
	return strings.Contains(prefix, "://")
}
