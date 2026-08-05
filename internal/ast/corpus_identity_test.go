// SPDX-License-Identifier: Apache-2.0

// corpus_identity_test.go — the corpus identity invariant and the census it
// writes.
//
// THE INVARIANT, stated so it needs no per-language expected-output table:
// for a pattern P, run replace with the replacement template set to P
// VERBATIM. Every resulting diff must be the empty string. A template that
// rewrites nothing must change nothing.
//
// THE ASSERTION IS ANCHORED, NOT UNCONDITIONAL, AND THIS TEST LANDS GREEN.
// The anchor was built while the engine still violated the invariant, so that
// the harness could land green rather than as a permanently red test — which
// this repo does not allow on main, and which would in any case be enforcement
// that has already fired and can never fire again: nobody could tell a second
// regression from the first. Every evaluable cell now declares OK, so the
// anchor is inert in that direction and the test reads as the plain
// unconditional gate; the machinery stays because it is what catches the
// reverse. Every probe declares a wantVerdict, and the test compares the
// OBSERVED verdict against it, failing on MISMATCH IN EITHER DIRECTION:
//
//   - wantVerdict OK, observed VIOLATION — a REGRESSION. This is the case the
//     red-test design could never catch, because a red test only ever says
//     "not done yet".
//   - wantVerdict VIOLATION, observed OK — an UNRECORDED IMPROVEMENT, equally
//     red until someone flips that probe's wantVerdict. The flip is the record
//     of the fix, reviewed in the diff exactly like deleting an xfail marker
//     from the $$$SEQ contract table. The two harnesses use one discipline.
//
// VERDICTS ONLY ARE COMPARED, never the detail string. The first-differing
// line moves as the engine partially improves, and comparing it would paint
// the test red for cosmetic reasons.
//
// THE DECLARED PROBE SET AND THE PRODUCED ROW SET MUST MATCH EXACTLY. A row
// with no declared probe, a probe that produced no row, or a duplicate key is
// a hard failure — otherwise a cell could be added or dropped outside the
// anchor, and the census could shrink without anything going red.
//
// THE CORPUS is the fixture repo set at ~/code/test-repos. It is walked
// CLIENT-SIDE, by absolute path, through ast.Match's repoDir argument only.
// These repos are never collected and never indexed. Every replace here is a
// DRY RUN, so nothing under the corpus is ever written.
//
// WHY A CELL WITH NO MATCHES IS A SKIP AND NOT A PASS. An identity replace
// over zero matches produces zero diffs, which is textually indistinguishable
// from a clean pass. So is a cell whose pattern never compiled. Every one of
// those records an explicit SKIP carrying its reason, and the census format
// puts the verdict behind a ` matches=<n> ` anchor precisely so a skip reason
// that happens to contain the word OK cannot be read as an evaluated cell.
//
// WHY EACH CELL RUNS BOTH A SINGLE-LINE AND A MULTI-LINE SHAPE. The reflow
// defect only manifests on matches that span lines: a one-token rewrite of a
// multi-line function re-emits the body on one line, while a one-line match in
// the same run comes back untouched. A cell that measured only one-line
// matches would report a clean pass over the exact case that cannot break.
// The shape is enforced against the MATCHES, not the pattern text — a cell
// declaring a multi-line shape that finds only single-line matches is a SKIP,
// not a pass.
//
// THE CENSUS IS WRITTEN ONLY ON REQUEST, into testdata/<name>, when the
// environment variable named by identityCensusEnv is set. Two artifacts are
// produced across the ticket's life and the split is load-bearing: a baseline
// written against the unfixed engine and then frozen, and a later census
// written against the fixed one. If one file served both roles, the guard
// "the regenerated census evaluates at least as many cells as the baseline"
// would compare a file to itself and could never fail. A default-off write
// also keeps ordinary suite runs from dirtying testdata.
//
// PERF SHAPE: cells are independent and each parses a bounded subtree, so they
// run as parallel subtests. No parser is constructed here and no worker pool
// is nested inside a cell — ast.Match already fans out internally and gives
// each of its own workers a private parser and a privately recompiled pattern,
// which is the package's standing discipline for tree-sitter's thread-unsafe
// parsers and trees.

package ast

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// identityCensusEnv names the environment variable that selects the census
// filename. Unset means "run the invariant, write nothing".
const identityCensusEnv = "AST_IDENTITY_CENSUS_FILE"

// Census verdicts. SKIP always carries a reason; OK and VIOLATION always sit
// behind a matches= count.
const (
	verdictOK        = "OK"
	verdictViolation = "VIOLATION"
	verdictSkip      = "SKIP"
)

// Match shapes. The distinction is measured against each match's line span,
// not against the pattern's own text.
const (
	shapeSingleLine = "single-line"
	shapeMultiLine  = "multi-line"
)

// identityPattern is one probe: the pattern (which is also its own identity
// replacement template), the match shape the cell expects it to exercise, and
// the verdict the engine is currently expected to produce.
//
// wantVerdict is the anchor. It records TODAY's measured behavior, so this
// test is green on landing and red the moment that behavior moves in either
// direction. It deliberately lives in code rather than being read from
// testdata/identity_baseline.txt: that file must stay FROZEN to do its two
// jobs (the evaluated-cell floor and the overwrite alarm), and recording a
// fix by editing it would drive its VIOLATION count toward zero — the exact
// condition the alarm exists to detect. A moving expectation belongs where
// review can see it move.
type identityPattern struct {
	pattern     string
	shape       string
	wantVerdict string
}

// identityCell is one language's corner of the corpus: which fixture repo,
// which bounded subtree of it, and which probes to run there.
//
// prefix carries a TRAILING SLASH on purpose. Package-prefix filtering is a
// bare string-prefix test with no separator boundary, so "akka-actor" would
// also admit everything under akka-actor-tests.
//
// skip, when set, makes the whole cell a reasoned SKIP without touching disk.
type identityCell struct {
	lang     treesitter.Language
	repo     string
	prefix   string
	patterns []identityPattern
	skip     string
}

// censusRow is one line of the artifact.
//
// shape is part of the row's identity, not decoration: a row reporting
// "matches=540 OK" without it cannot tell a reader whether the multi-line
// control — the only shape the reflow defect can manifest on — was exercised
// at all. Carrying it also keeps every line uniquely keyed when one pattern
// is the best available probe for both shapes, which is what makes the
// sorted census a deterministic diff.
type censusRow struct {
	lang    string
	repo    string
	prefix  string
	pattern string
	shape   string
	matches int
	verdict string
	detail  string
	// decline is the declined-file accounting suffix, rendered AFTER the verdict
	// token by line(). Empty when the cell declined nothing, so the artifact does
	// not grow a column of identical zero-suffixes. Set by discloseDeclines
	// (corpus_identity_guard_test.go). For a zero-spliced SKIP the accounting
	// rides in the skip reason instead, since a skip row has no matches= anchor.
	decline string
}

// line renders the row in the census format. The verdict sits immediately
// after a ` matches=<n> ` anchor for evaluated cells, and immediately after
// the shape for skipped ones, so the two can never be confused by a reader
// or a grep.
func (r censusRow) line() string {
	head := fmt.Sprintf("lang=%s repo=%s prefix=%s pattern=%q shape=%s", r.lang, r.repo, r.prefix, r.pattern, r.shape)
	// The disclosure suffix goes AFTER the verdict token, never between the
	// matches= anchor and the verdict — the header's "verdict sits immediately
	// after matches=" rule and a landed census gate that greps
	// ` matches=[0-9]+ (OK|VIOLATION)( |$)` both depend on that adjacency.
	sfx := ""
	if r.decline != "" {
		sfx = " " + r.decline
	}
	if r.verdict == verdictSkip {
		return fmt.Sprintf("%s SKIP %s", head, r.detail)
	}
	if r.verdict == verdictViolation {
		return fmt.Sprintf("%s matches=%d VIOLATION %q%s", head, r.matches, r.detail, sfx)
	}
	return fmt.Sprintf("%s matches=%d OK%s", head, r.matches, sfx)
}

// TestCorpusIdentityInvariant runs every declared probe and asserts each
// observed verdict equals the probe's wantVerdict.
func TestCorpusIdentityInvariant(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)
	reposDir := filepath.Join(homeDir, "code", "test-repos")
	if _, statErr := os.Stat(reposDir); os.IsNotExist(statErr) {
		t.Skipf("test-repos directory not found at %s — clone repos first", reposDir)
	}

	var (
		mu   sync.Mutex
		rows []censusRow
	)
	// Registered on the PARENT, so both run after every parallel cell has
	// finished and the row set is complete.
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		reconcileRows(t, rows)
		writeCensus(t, rows)
	})

	for _, cell := range identityCells {
		t.Run(string(cell.lang), func(t *testing.T) {
			t.Parallel()
			got := runIdentityCell(t, reposDir, cell)
			mu.Lock()
			rows = append(rows, got...)
			mu.Unlock()
		})
	}
}

// rowKey is a probe's identity across the declared table and the produced
// rows. shape is part of it because one pattern can be the best probe for
// both shapes, and two such rows must not collapse into one key.
func rowKey(lang, repo, prefix, pattern, shape string) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", lang, repo, prefix, pattern, shape)
}

// reconcileRows fails unless the produced row set is exactly the declared
// probe set. Without this, a cell dropped from the table — or a subtest that
// died before appending — would shrink the census silently, and every
// remaining row would still match its wantVerdict.
func reconcileRows(t *testing.T, rows []censusRow) {
	t.Helper()

	declared := make(map[string]int)
	for _, cell := range identityCells {
		for _, probe := range cell.patterns {
			declared[rowKey(string(cell.lang), cell.repo, cell.prefix, probe.pattern, probe.shape)]++
		}
	}
	produced := make(map[string]int, len(rows))
	for _, r := range rows {
		produced[rowKey(r.lang, r.repo, r.prefix, r.pattern, r.shape)]++
	}

	for key, want := range declared {
		switch got := produced[key]; {
		case got == 0:
			t.Errorf("declared probe produced no census row: %s", renderRowKey(key))
		case got != want:
			t.Errorf("census row count %d != declared count %d for %s", got, want, renderRowKey(key))
		}
	}
	for key := range produced {
		if declared[key] == 0 {
			t.Errorf("census row has no declared probe: %s", renderRowKey(key))
		}
	}
}

// renderRowKey makes a rowKey readable in a failure message.
func renderRowKey(key string) string {
	return strings.ReplaceAll(key, "\x00", " | ")
}

// runIdentityCell evaluates one language's probes and returns its census rows.
func runIdentityCell(t *testing.T, reposDir string, cell identityCell) []censusRow {
	t.Helper()
	repoDir := filepath.Join(reposDir, cell.repo)

	skip := cell.skip
	if skip == "" {
		if _, err := os.Stat(repoDir); err != nil {
			skip = "fixture repo " + cell.repo + " is not present in the corpus"
		}
	}

	out := make([]censusRow, 0, len(cell.patterns))
	for _, probe := range cell.patterns {
		row := censusRow{
			lang:    string(cell.lang),
			repo:    cell.repo,
			prefix:  cell.prefix,
			pattern: probe.pattern,
			shape:   probe.shape,
		}
		if skip != "" {
			row.verdict = verdictSkip
			row.detail = skip
		} else {
			row = evaluateIdentity(t, repoDir, cell, probe, row)
		}
		assertVerdict(t, cell, probe, row)
		out = append(out, row)
	}
	return out
}

// assertVerdict compares one probe's observed verdict against its anchor and
// fails on a mismatch in either direction, naming the direction and the edit
// that resolves it. Saying which way it moved is what stops the next reader
// from "fixing" a genuine improvement by reverting it.
func assertVerdict(t *testing.T, cell identityCell, probe identityPattern, row censusRow) {
	t.Helper()
	if row.verdict == probe.wantVerdict {
		return
	}

	var headline, remedy string
	switch {
	case probe.wantVerdict == verdictOK && row.verdict == verdictViolation:
		headline = "REGRESSION: a cell that satisfied the identity invariant no longer does"
		remedy = "fix the engine — do NOT relax wantVerdict to record the breakage"
	case probe.wantVerdict == verdictViolation && row.verdict == verdictOK:
		headline = "UNRECORDED IMPROVEMENT: a cell that violated the identity invariant now satisfies it"
		remedy = "flip this probe's wantVerdict to verdictOK so the fix is recorded in the diff"
	case row.verdict == verdictSkip:
		headline = "CELL STOPPED BEING EVALUATED: it now skips rather than producing a verdict"
		remedy = "restore an evaluable probe — a skipped cell measures nothing, and demoting cells to SKIP is how a census goes green by looking at less"
	case probe.wantVerdict == verdictSkip:
		headline = "CELL BECAME EVALUABLE: it no longer skips"
		remedy = "set this probe's wantVerdict to the verdict it now produces"
	default:
		headline = "verdict moved"
		remedy = "reconcile wantVerdict with the observed verdict"
	}

	t.Errorf("%s.\n"+
		"  lang:     %s\n"+
		"  repo:     %s\n"+
		"  prefix:   %s\n"+
		"  pattern:  %q (%s, used verbatim as its own replacement)\n"+
		"  want:     %s\n"+
		"  observed: %s (matches=%d)\n"+
		"  detail:   %s\n"+
		"  remedy:   %s",
		headline, cell.lang, cell.repo, cell.prefix, probe.pattern, probe.shape,
		probe.wantVerdict, row.verdict, row.matches, row.detail, remedy)
}

// evaluateIdentity drives the real pipeline for one probe: Parse, Compile,
// Match, then a DRY-RUN ApplyReplace whose template is the pattern verbatim.
func evaluateIdentity(t *testing.T, repoDir string, cell identityCell, probe identityPattern, row censusRow) censusRow {
	t.Helper()
	ctx := context.Background()

	pat, err := Parse(probe.pattern)
	if err != nil {
		return skipRow(row, "pattern does not parse: "+err.Error())
	}
	cp, err := Compile(pat, cell.lang, "")
	if err != nil {
		return skipRow(row, "pattern does not compile under this language's context wrappers: "+err.Error())
	}
	defer cp.Close()

	matches, _, err := Match(ctx, repoDir, cell.lang, cp, nil, Scope{
		PackagePrefixes: []string{cell.prefix},
		IncludeTests:    true,
	})
	if err != nil {
		return skipRow(row, "match failed: "+err.Error())
	}

	shaped := matchesOfShape(matches, probe.shape)
	row.matches = len(shaped)
	if len(shaped) == 0 {
		return skipRow(row, fmt.Sprintf(
			"no %s match under this prefix (%d matches of any shape); an unexercised probe is never recorded as a pass",
			probe.shape, len(matches)))
	}

	res, err := ApplyReplace(ctx, repoDir, cell.lang, shaped, probe.pattern, true, nil)
	if err != nil {
		return skipRow(row, "replace failed: "+err.Error())
	}

	// The declined-file guard runs BEFORE the diff check, because firstDiffLine
	// reads res.Diffs alone and a file that was refused, rejected, or already
	// ungrammatical produces no diff — so without this a cell that spliced
	// nothing, or one whose identity splice BROKE a file, would read as OK off
	// an empty diff. It disposes the zero-spliced and rejected cases and stamps
	// the disclosure suffix on every other row; only when it returns done=false
	// does the diff check below decide OK vs VIOLATION. See discloseDeclines in
	// corpus_identity_guard_test.go.
	row, done := discloseDeclines(row, res)
	if done {
		return row
	}

	// Verdict only — assertVerdict owns the pass/fail decision. The detail is
	// recorded for the census and for the failure message, never compared:
	// the first-differing line moves as the engine partially improves, and
	// comparing it would paint this test red for cosmetic reasons.
	if detail, ok := firstDiffLine(res.Diffs); ok {
		row.verdict = verdictViolation
		row.detail = detail
		return row
	}
	row.verdict = verdictOK
	return row
}

// skipRow stamps a reasoned skip onto a row.
func skipRow(row censusRow, reason string) censusRow {
	row.verdict = verdictSkip
	row.detail = reason
	return row
}

// matchesOfShape keeps only the matches whose line span has the requested
// shape. This is the known-positive control for the multi-line probes: a
// multi-line cell that found only one-liners never exercised the reflow
// surface, and says so rather than reporting a pass.
func matchesOfShape(matches []RawMatch, shape string) []RawMatch {
	out := make([]RawMatch, 0, len(matches))
	for _, m := range matches {
		spansLines := m.EndLine > m.StartLine
		if (shape == shapeMultiLine) == spansLines {
			out = append(out, m)
		}
	}
	return out
}

// firstDiffLine returns the first added-or-removed line across the diffs, in
// deterministic file order, or ok=false when every diff is empty.
func firstDiffLine(diffs map[string]string) (string, bool) {
	paths := make([]string, 0, len(diffs))
	for p := range diffs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		for line := range strings.SplitSeq(diffs[p], "\n") {
			if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
				continue
			}
			if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "+") {
				return p + ": " + line, true
			}
		}
	}
	return "", false
}

// writeCensus sorts the rows and writes them to testdata/<name>, where name
// comes from identityCensusEnv. Unset means write nothing.
func writeCensus(t *testing.T, rows []censusRow) {
	t.Helper()
	name := os.Getenv(identityCensusEnv)
	if name == "" {
		t.Logf("census not written: set %s=<filename> to write the artifact into testdata/", identityCensusEnv)
		return
	}
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, r.line())
	}
	sort.Strings(lines)

	require.NoError(t, os.MkdirAll("testdata", 0o750))
	path := filepath.Join("testdata", filepath.Base(name))
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	t.Logf("census written: %s (%d rows)", path, len(rows))
}
