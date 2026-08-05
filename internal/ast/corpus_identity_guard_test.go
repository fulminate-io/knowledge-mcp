// SPDX-License-Identifier: Apache-2.0

// corpus_identity_guard_test.go — the declined-file guard for the corpus
// identity census, and its known-positive controls.
//
// WHY THIS EXISTS. evaluateIdentity reached its verdict from firstDiffLine(
// res.Diffs) alone. But a replace declines files three ways that produce NO
// diff — RefusedFiles (overlapping/nested matches, dropped whole), RejectedFiles
// (the edit broke a file that parsed clean before it), and
// PreexistingParseFailures (the file was already ungrammatical) — so a cell that
// spliced NOTHING, or one whose identity splice broke a file, read as a clean OK
// off an empty diff. discloseDeclines closes that: it disposes the zero-spliced
// and rejected cases and stamps a declined-file accounting suffix on every other
// row. It is matchesOfShape's rule — a probe that did not exercise the intended
// surface records a SKIP, not a pass — reached through refusals and pre-existing
// failures instead of through zero shaped matches.
//
// THE VERDICT USUALLY DOES NOT MOVE, AND THAT IS BY DESIGN. This is disclose-
// not-demote: a cell that refuses most of its files but splices at least one
// still records verdictOK, now carrying a non-zero refused= count. A passing OK
// is therefore NOT evidence the guard is inert — the guard's observable effect
// in that case is the DISCLOSURE suffix, not the verdict. The control below
// asserts the suffix, precisely because a verdict-only assertion would pass
// identically against the unmodified harness. Demoting a refusing-but-splicing
// cell to a violation instead would crater the census below its frozen
// evaluated-cell floor; see the sweep step for why disclosure is the remedy.

package ast

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// declineAccounting renders the declined-file suffix for a census row, or "" when
// the cell declined nothing (so the artifact does not grow a column of identical
// zero-suffixes). When something WAS declined it always emits spliced= and
// spliced_matches=, then only the non-zero decline counts. spliced= carries
// files_matched/candidate_files, and spliced_matches carries res.MatchesReplaced
// — the row's own matches= is matches FOUND (len(shaped)), which a file-granular
// count alone can never reconcile against how many were actually spliced.
func declineAccounting(res ReplaceResult) string {
	refused := len(res.RefusedFiles)
	rejected := len(res.RejectedFiles)
	preexisting := len(res.PreexistingParseFailures)
	if refused == 0 && rejected == 0 && preexisting == 0 {
		return ""
	}
	candidates := res.FilesMatched + refused + rejected + preexisting
	parts := []string{
		fmt.Sprintf("spliced=%d/%d", res.FilesMatched, candidates),
		fmt.Sprintf("spliced_matches=%d", res.MatchesReplaced),
	}
	if refused > 0 {
		parts = append(parts, fmt.Sprintf("refused=%d", refused))
	}
	if rejected > 0 {
		parts = append(parts, fmt.Sprintf("rejected=%d", rejected))
	}
	if preexisting > 0 {
		parts = append(parts, fmt.Sprintf("preexisting=%d", preexisting))
	}
	return strings.Join(parts, " ")
}

// discloseDeclines is the guard evaluateIdentity calls between ApplyReplace and
// the diff check. It returns the disposed row and done=true when it OWNS the
// verdict (a zero-spliced SKIP or a rejected-file VIOLATION); done=false means it
// only stamped the disclosure suffix and the caller's diff check still decides
// OK vs VIOLATION.
func discloseDeclines(row censusRow, res ReplaceResult) (censusRow, bool) {
	acct := declineAccounting(res)

	// ZERO FILES SPLICED is a reasoned SKIP. Nothing round-tripped, so the cell
	// measured nothing at all — the same disposition a zero-match cell gets, and
	// the accounting rides in the reason because a skip row carries no matches=
	// anchor to suffix.
	if res.FilesMatched == 0 {
		reason := "no file spliced, so nothing round-tripped and the cell measured nothing"
		if acct != "" {
			reason += "; " + acct
		}
		reason += " (every candidate was refused for overlapping matches, rejected as broken by the edit, or already ungrammatical before it; the underlying grammar gaps are tracked separately)"
		return skipRow(row, reason), true
	}

	// A REJECTED FILE IS A VIOLATION. RejectedFiles parsed clean before the
	// splice and failed the re-parse gate after it — the identity edit broke it,
	// and an identity template must change nothing. There are zero rejections in
	// the corpus today; this branch costs no gate and exists so that if one ever
	// appears it cannot read as OK.
	if len(res.RejectedFiles) > 0 {
		row.verdict = verdictViolation
		row.detail = "identity splice broke a file that parsed clean before the edit: " + strings.Join(res.RejectedFiles, ", ")
		row.decline = acct
		return row, true
	}

	// OTHERWISE the caller's diff check decides OK vs VIOLATION. Carry the
	// disclosure so a refusing-but-splicing cell still reports what it declined
	// even though its verdict stays OK — the disclose-not-demote case.
	row.decline = acct
	return row, false
}

// identityProbe drives the real pipeline for one probe exactly as
// evaluateIdentity does — Parse, Compile, Match, dry-run ApplyReplace — and
// returns the ReplaceResult plus the shaped-match count. shape == "" runs the
// replace over EVERY match (no shape filter), which the elm self-nesting control
// needs to reproduce its whole-repo refusal measurement.
func identityProbe(t *testing.T, reposDir, repo, prefix string, lang treesitter.Language, pattern, shape string) (ReplaceResult, int) {
	t.Helper()
	repoDir := filepath.Join(reposDir, repo)
	if _, err := os.Stat(repoDir); err != nil {
		t.Skipf("fixture repo %s is not present in the corpus", repo)
	}
	ctx := context.Background()

	pat, err := Parse(pattern)
	require.NoError(t, err)
	cp, err := Compile(pat, lang, "")
	require.NoError(t, err)
	defer cp.Close()

	matches, _, err := Match(ctx, repoDir, lang, cp, nil, Scope{
		PackagePrefixes: []string{prefix},
		IncludeTests:    true,
	})
	require.NoError(t, err)

	spliceInput := matches
	if shape != "" {
		spliceInput = matchesOfShape(matches, shape)
	}
	res, err := ApplyReplace(ctx, repoDir, lang, spliceInput, pattern, true, nil)
	require.NoError(t, err)
	return res, len(spliceInput)
}

// TestIdentityVerdict_DeclinedFilesAreDisclosed is the known-positive control set
// for the guard. Per the corpus's measurement caveat it asserts on SHAPE
// (non-empty / zero / non-zero), never the exact counts, because the numbers move
// with the engine while the disposition does not.
func TestIdentityVerdict_DeclinedFilesAreDisclosed(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)
	reposDir := filepath.Join(homeDir, "code", "test-repos")
	if _, statErr := os.Stat(reposDir); os.IsNotExist(statErr) {
		t.Skipf("test-repos directory not found at %s — clone repos first", reposDir)
	}

	// (a) REFUSAL known positive. The self-nesting elm spelling `$N = $E` over
	// elm-compiler reactor/src/ self-nests (a top-level declaration and its
	// let-bound declarations overlap), so most files refuse whole while at least
	// one splices. Run unshaped to reproduce the whole-repo refusal. Without a
	// non-empty RefusedFiles here every later refusal assertion is vacuous.
	refRes, refShaped := identityProbe(t, reposDir, "elm-compiler", "reactor/src/", treesitter.LangElm, "$N = $E", "")
	require.NotEmpty(t, refRes.RefusedFiles, "elm self-nesting spelling must refuse at least one file")
	require.Positive(t, refShaped, "elm self-nesting spelling must find matches")
	require.Positive(t, refRes.FilesMatched, "elm self-nesting spelling must still splice at least one file")

	// (b) THE PRE-FIX PATH WAS SILENT ON BOTH AXES. firstDiffLine over the same
	// result yields no diff line — the UNGUARDED harness called this cell OK —
	// and a row rendered WITHOUT the guard carries no accounting suffix. The
	// second half is what tells the guard apart from a no-op, since the verdict
	// does not move in this case.
	_, hasDiff := firstDiffLine(refRes.Diffs)
	require.False(t, hasDiff, "pre-fix path: identity diff is empty, so the old harness recorded OK")
	unguarded := censusRow{lang: "elm", repo: "elm-compiler", prefix: "reactor/src/", pattern: "$N = $E", shape: "single-line", matches: refShaped, verdict: verdictOK}
	require.NotContains(t, unguarded.line(), "spliced=", "pre-fix row must carry no accounting suffix")
	require.NotContains(t, unguarded.line(), "refused=", "pre-fix row must carry no accounting suffix")

	// (c) THE GUARDED PATH STILL RETURNS verdictOK for that result — the verdict
	// does not move — but the row now discloses a non-zero refused= count. Assert
	// OK explicitly: a refusing-but-splicing cell is SUPPOSED to stay OK.
	guarded, done := discloseDeclines(censusRow{lang: "elm", repo: "elm-compiler", prefix: "reactor/src/", pattern: "$N = $E", shape: "single-line", matches: refShaped}, refRes)
	require.False(t, done, "a refusing-but-splicing cell is not owned by the guard; the diff check still decides")
	require.Contains(t, guarded.decline, "refused=", "the guarded row must disclose the refusal")
	require.Contains(t, guarded.decline, "spliced=", "the guarded row must disclose the splice ratio")
	require.Contains(t, guarded.decline, "spliced_matches=", "the guarded row must disclose spliced match count")
	guarded.verdict = verdictOK // the empty diff (asserted in b) makes evaluateIdentity's tail record OK
	require.Contains(t, guarded.line(), " OK spliced=", "the OK verdict is followed by the disclosure suffix, not preceded by it")

	// (d) PRE-EXISTING known positive. The lua single-line probe `$F($X)` over
	// lua-openresty t/lib/ names an already-ungrammatical file (Memcached.lua)
	// in PreexistingParseFailures. This exercises the list the honesty split
	// created; without it the third field is wired but never read.
	luaRes, _ := identityProbe(t, reposDir, "lua-openresty", "t/lib/", treesitter.LangLua, "$F($X)", "single-line")
	require.NotEmpty(t, luaRes.PreexistingParseFailures, "lua probe must report a pre-existing parse failure")
	var namesMemcached bool
	for _, f := range luaRes.PreexistingParseFailures {
		if strings.HasSuffix(f.Path, "Memcached.lua") {
			namesMemcached = true
		}
	}
	require.True(t, namesMemcached, "the pre-existing failure names Memcached.lua")
	luaRow, luaDone := discloseDeclines(censusRow{lang: "lua", pattern: "$F($X)", matches: 1}, luaRes)
	require.False(t, luaDone, "the lua probe still splices files, so the guard does not own its verdict")
	require.Contains(t, luaRow.decline, "preexisting=", "the lua row must disclose the pre-existing failure count")

	// (e) ZERO-SPLICED known positive driving the SKIP branch. The cpp single-
	// line probe `return $X;` over cpp-json include/ splices no file — every
	// candidate is refused or already ungrammatical — so the guard OWNS the row
	// with a reasoned SKIP naming the counts. Without this case the SKIP branch
	// has no test and a guard that never skips would pass everything else.
	cppRes, _ := identityProbe(t, reposDir, "cpp-json", "include/", treesitter.LangCPP, "return $X;", "single-line")
	require.Zero(t, cppRes.FilesMatched, "cpp return-probe must splice zero files for the SKIP case")
	cppRow, cppDone := discloseDeclines(censusRow{lang: "cpp", pattern: "return $X;", matches: 1}, cppRes)
	require.True(t, cppDone, "a zero-spliced cell is owned by the guard as a SKIP")
	require.Equal(t, verdictSkip, cppRow.verdict, "zero spliced is a reasoned SKIP, not an OK")
	require.Contains(t, cppRow.detail, "spliced=0/", "the SKIP reason carries the full accounting")

	// (f) NEGATIVE control. The SHIPPED elm spelling `$N $P = $E` over the same
	// prefix declines nothing, so its row carries NO accounting suffix and stays
	// OK. Without it the guard cannot be told apart from one that suffixes every
	// row.
	cleanRes, _ := identityProbe(t, reposDir, "elm-compiler", "reactor/src/", treesitter.LangElm, "$N $P = $E", "multi-line")
	require.Empty(t, cleanRes.RefusedFiles, "the shipped elm spelling refuses nothing")
	require.Empty(t, cleanRes.RejectedFiles, "the shipped elm spelling rejects nothing")
	require.Empty(t, cleanRes.PreexistingParseFailures, "the shipped elm spelling hits no pre-existing failure")
	cleanRow, cleanDone := discloseDeclines(censusRow{lang: "elm", pattern: "$N $P = $E", matches: 1, verdict: verdictOK}, cleanRes)
	require.False(t, cleanDone, "a clean cell is not owned by the guard")
	require.Empty(t, cleanRow.decline, "a clean cell carries no disclosure suffix")
	require.NotContains(t, cleanRow.line(), "spliced=", "a clean row renders no accounting suffix")
}
