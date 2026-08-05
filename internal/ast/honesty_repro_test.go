// SPDX-License-Identifier: Apache-2.0

// honesty_repro_test.go — one reproduction per reporting-honesty defect the
// honesty plan fixes.
//
// EVERY TEST HERE LANDED ASSERTING ITS DEFECT'S MEASURED BROKEN BEHAVIOR, so all
// five were GREEN from the first commit. That is deliberate and it is stronger
// than a standing red test. A red test says only "not done yet", and says it
// identically from the moment it lands until the moment it flips. An assertion
// anchored to the measured defect is enforced continuously: it fails on a NEW
// defect and equally on an UNRECORDED IMPROVEMENT, so no partial or accidental
// fix can land silently.
//
// Each reproduction is INVERTED to assert correct behavior by the phase that
// fixes its defect, so the file is a mix while the plan is in flight and every
// test in it is green either way. Read each test's own anchor marker for where
// it stands; the plan's close-out gate requires all five to read correct.
//
// The red-first evidence was still taken. Each assertion was first authored in
// its CORRECT-BEHAVIOR form, run against this tree, and observed failing with
// the failure the defect predicts; that raw run is committed at
// testdata/honesty_repro_red.txt. Only then was each assertion inverted to
// state the observed brokenness.
//
// THE ANCHOR MARKER. Each test carries exactly one marker line immediately
// above its func:
//
//	// HONESTY-ANCHOR <TestName>: broken    <- as first written, asserting the defect
//	// HONESTY-ANCHOR <TestName>: correct   <- after the owning phase inverts it
//
// The marker names its own test so per-test greps are exact and
// position-independent. The PRIMARY catcher for each inversion is behavioral —
// a test still asserting brokenness fails against a fixed engine. The marker
// exists so the close-out can make one structural statement about all five at
// once; without it, five tests that were never inverted would satisfy an
// all-green check by asserting brokenness, which is the one hole the close-out
// must not have.
//
// Inverting a reproduction means flipping BOTH the assertion and its marker, in
// the commit of the phase that owns the fix.

package ast

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// honestyDeferPattern matches a deferred Close in Go. Its replacement template
// is the pattern verbatim, which makes the splice byte-identical — the property
// the pre-edit-baseline reproduction turns on.
const honestyDeferPattern = "defer $X.Close()"

// honestyRubyPattern matches a Ruby method definition. Both containers are
// field-named slots, so neither sequence spills into the other's siblings.
const honestyRubyPattern = "def $N($$$P)\n  $$$B\nend"

// honestyMatch runs the real Parse -> Compile -> Match pipeline and returns the
// walk error rather than asserting it away: one reproduction's whole subject is
// whether an error is returned at all.
func honestyMatch(
	t *testing.T,
	dir string,
	lang treesitter.Language,
	pattern string,
	where *WhereNode,
	scope Scope,
) ([]RawMatch, WalkStats, error) {
	t.Helper()
	pat, err := Parse(pattern)
	require.NoError(t, err)
	cp, err := Compile(pat, lang, "")
	require.NoError(t, err)
	defer cp.Close()
	return Match(context.Background(), dir, lang, cp, where, scope)
}

// honestyMatchOK is honestyMatch for the reproductions where a walk error would
// be a setup failure rather than the subject under test.
func honestyMatchOK(
	t *testing.T,
	dir string,
	lang treesitter.Language,
	pattern string,
	scope Scope,
) ([]RawMatch, WalkStats) {
	t.Helper()
	raws, stats, err := honestyMatch(t, dir, lang, pattern, nil, scope)
	require.NoError(t, err)
	return raws, stats
}

// matchedFiles is the distinct file set a match slice touched, so a
// reproduction can assert WHICH files contributed rather than only how many.
func matchedFiles(raws []RawMatch) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, r := range raws {
		if _, dup := seen[r.FilePath]; dup {
			continue
		}
		seen[r.FilePath] = struct{}{}
		out = append(out, r.FilePath)
	}
	return out
}

// ---------------------------------------------------------------------------
// 1. FIXED. A pre-existing parse failure is no longer charged to the caller's
// edit.
//
// The defect: rejected_files is documented as the files the caller's edit
// broke, but the re-parse gate (replace.go applyEditsToSource) tested the
// REWRITTEN source without ever asking whether the ORIGINAL already carried an
// error, so a file that was ungrammatical before the call came back as
// edit-caused. An identity replace made it unambiguous: the splice is
// byte-identical, so nothing the edit did could have broken the parse.
//
// Measured on js-react packages/react-dom/src with pattern and replacement both
// `$F($$$A);`: of 185 files scanned, 18 came back in rejected_files and every
// one is @flow-annotated; the single cleanly touched file, ReactTestUtils.js,
// is @noflow.
//
// Now a pre-edit baseline parses every candidate's original bytes first, and a
// file that already fails is reported in preexisting_parse_failures with the
// error's location instead of being spliced. Note what that means for the
// counts: a declined file is NOT written, so it is not counted as matched
// either. An earlier note here predicted 2; the step that defines the counters
// scopes files_matched to files that produced an edit AND passed the gate, and
// counting an unwritten file there would re-create the overstatement this plan
// exists to remove.
// ---------------------------------------------------------------------------

// HONESTY-ANCHOR TestHonesty_PreexistingParseFailureIsNotRejected: correct
func TestHonesty_PreexistingParseFailureIsNotRejected(t *testing.T) {
	// The pair is identical but for a syntax error on a line the edit does not
	// touch: `func B( {` never parses, while A's deferred Close does.
	const clean = `package main

func A() {
	defer x.Close()
}
`
	const dirty = `package main

func A() {
	defer x.Close()
}

func B( {
	return
}
`
	dir := fixtureRepo(t, map[string]string{"clean.go": clean, "dirty.go": dirty})
	raws, _ := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, Scope{})
	require.ElementsMatch(t, []string{"clean.go", "dirty.go"}, matchedFiles(raws),
		"setup: both files must match, otherwise the reproduction proves nothing")

	res, err := ApplyReplace(context.Background(), dir, treesitter.LangGo, raws, honestyDeferPattern, true, nil)
	require.NoError(t, err)

	// FIXED. The pre-edit baseline sees dirty.go was already ungrammatical, so
	// it is reported as a pre-existing failure and never spliced — and
	// rejected_files is left meaning only what the edit broke.
	//
	// These assertions READ the new field on purpose. The fix is additive, so
	// an inversion that only re-stated the old fields could still pass against
	// the pre-fix engine; reading PreexistingParseFailures cannot even compile
	// against it.
	assert.Equal(t, []string{"dirty.go"}, preexistingPaths(res),
		"the file that was already broken is named as such, not blamed on the edit")
	assert.NotContains(t, res.RejectedFiles, "dirty.go",
		"a pre-existing error must not be charged to an edit that changed nothing")
	assert.NotContains(t, res.RejectedFiles, "clean.go",
		"negative control: the clean sibling is not rejected either")
	assert.Equal(t, 1, res.FilesMatched,
		"the clean sibling is still replaced; the dirty one is declined rather than rejected")
}

// ---------------------------------------------------------------------------
// 2. FIXED. A file excluded by the 500KB size cap is disclosed under the rule
// that excluded it.
//
// parser.isIndexable drops any file over maxFileSize before ast ever sees it.
// It is still neither scanned nor skipped — those two count files the walk was
// HANDED — but it is no longer disclosed nowhere: the exclusion report charges
// it to skip_too_large and names it, so scanned + skipped + excluded now
// accounts for every candidate on disk.
//
// Note on the anchor's catcher: this reproduction's original assertions were
// insensitive to their own fix, because the fix is purely ADDITIVE — it changed
// no existing field, so FilesScanned, FilesSkipped and their sum all read
// exactly as before and the un-inverted test stayed green against a fixed
// engine. The assertions below close that hole by reading the new field
// directly, which cannot even compile against the pre-fix engine.
//
// The oversize fixture is generated rather than checked in, so the repo carries
// no 500KB blob.
// ---------------------------------------------------------------------------

// HONESTY-ANCHOR TestHonesty_SizeCappedFileIsDisclosed: correct
func TestHonesty_SizeCappedFileIsDisclosed(t *testing.T) {
	const small = `package main

func A() {
	defer x.Close()
}
`
	// Comment padding keeps the oversize file grammatical, so the only reason
	// it can be excluded is its size.
	huge := small + "\n// " + strings.Repeat("pad", 220_000) + "\n"
	require.Greater(t, len(huge), 512*1024, "setup: fixture must exceed maxFileSize")

	dir := fixtureRepo(t, map[string]string{"small.go": small, "huge.go": huge})
	onDisk, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	require.Len(t, onDisk, 2, "setup: two candidate files on disk")

	raws, stats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern, Scope{})
	assert.Equal(t, []string{"small.go"}, matchedFiles(raws),
		"the oversize file is never parsed")

	// The oversize file is still not scanned and still not skipped — it was
	// never handed to the walk — but it is now attributed.
	assert.Equal(t, 1, stats.FilesScanned)
	assert.Equal(t, 0, stats.FilesSkipped)
	assert.Equal(t, 1, stats.ExcludedByRule["skip_too_large"],
		"the size cap declined exactly one candidate and says so")
	assert.Equal(t, []string{"huge.go"}, stats.ExcludedSamples["skip_too_large"],
		"and names it, so the caller can act on the exclusion rather than only count it")
	assert.False(t, stats.ExcludedTruncated["skip_too_large"],
		"one name is under the sample cap, so nothing was withheld")

	// THE ACCOUNTING. Every candidate on disk now lands in exactly one of the
	// three buckets, which is the property that was missing: previously the two
	// available numbers summed to less than the candidate set with no third
	// bucket to hold the difference.
	excluded := 0
	for _, n := range stats.ExcludedByRule {
		excluded += n
	}
	assert.Equal(t, len(onDisk), stats.FilesScanned+stats.FilesSkipped+excluded,
		"scanned + skipped + excluded accounts for every candidate file")

	// Known positive against a report that simply mirrors the walk: the rules
	// that ran and declined nothing are present at zero, and the path that ran
	// is named, so this zero-heavy report is a measurement rather than an
	// empty map.
	assert.Equal(t, "nongit", stats.DiscoveryPath,
		"a bare temp dir is not a git repo, so the fallback walk is what ran")
	assert.Contains(t, stats.ExcludedByRule, "skip_dir",
		"skip_dir is reachable on the walk path, so it must be reported even at zero")
	assert.Equal(t, 0, stats.ExcludedByRule["skip_dir"])
}

// ---------------------------------------------------------------------------
// 3. FIXED, and fixed EARLIER THAN PLANNED — read this before touching it.
//
// The defect: matchesPackagePrefixes (match.go) is a bare strings.HasPrefix, so
// scoping to a directory also admitted every sibling directory whose name merely
// EXTENDS it. That widened a blast-radius control on the write path — a replace
// scoped to one package silently rewrote its neighbors. It reproduced on
// py-django: package_prefixes ["django/contrib/admin"] returned files_scanned 36,
// 30 admin plus 6 admindocs.
//
// It is now closed, but NOT by the step that was scheduled to close it. Prefix
// pruning pushed package_prefixes down into discovery (git pathspecs on the git
// path, a directory prune on the walk), and both match at path-segment
// boundaries — so an out-of-scope sibling is no longer discovered at all and the
// bare prefix test upstream never sees it. The fix therefore arrived with the
// pruning work rather than with the matcher rewrite.
//
// WHAT REMAINS for the matcher step: the bare strings.HasPrefix is still present
// in matchesPackagePrefixes, now a no-op superset filter over an already-pruned
// set. It is dead weight that disagrees with the semantics around it, and
// removing it is still worth doing — this test just no longer depends on it.
// ---------------------------------------------------------------------------

// HONESTY-ANCHOR TestHonesty_PackagePrefixRespectsSegmentBoundary: correct
func TestHonesty_PackagePrefixRespectsSegmentBoundary(t *testing.T) {
	const body = `package p

func A() {
	defer x.Close()
}
`
	dir := fixtureRepo(t, map[string]string{
		"pkg/in.go":           body,
		"pkgextra/sibling.go": body,
	})

	raws, stats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern,
		Scope{PackagePrefixes: []string{"pkg"}})
	// A prefix meaning the pkg directory means that directory, not every
	// directory whose name starts with those three letters.
	assert.Equal(t, []string{"pkg/in.go"}, matchedFiles(raws),
		"a directory prefix admits only what is under that directory")
	assert.Equal(t, 1, stats.FilesScanned,
		"and the sibling is never even scanned — it is pruned during discovery, not filtered after")

	// Known positive through the same probe: a prefix naming a single file must
	// still resolve, so the boundary rule cannot be satisfied by matching less.
	fileScoped, fileStats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern,
		Scope{PackagePrefixes: []string{"pkgextra/sibling.go"}})
	assert.Equal(t, []string{"pkgextra/sibling.go"}, matchedFiles(fileScoped),
		"control: a file-path prefix still resolves")
	assert.Equal(t, 1, fileStats.FilesScanned)
}

// ---------------------------------------------------------------------------
// 4. FIXED. include_tests acts on every language that has a convention, and is
// refused for every language that does not.
//
// discoverScopedFiles used to filter through isGoTestFile — a hardcoded _test.go
// suffix test — so for every other registered language the flag was accepted,
// documented, and inert: the worst shape for a filter, because the caller
// believes a control is in force. It now consults LangConfig.IsTestFile, so the
// flag means the same thing in Ruby as in Go. The languages with no unambiguous
// filename convention (Rust and eight others) carry a nil predicate and the tool
// layer REFUSES an explicit include_tests for them rather than accepting a flag
// it would ignore — that refusal is pinned in the tools package by
// TestAstIncludeTests_UnsupportedLanguageErrorsOnlyWhenSupplied.
//
// The Go control in the same run is what keeps the Ruby legs honest: it moved
// before this fix and still moves, so a harness that ignored include_tests
// entirely fails on the control before the Ruby assertions are read. Measured on
// rb-rails while the defect was live: activesupport/test/core_ext/object returned
// 12 files / 5 matches under BOTH flag values, while the Go control moved
// 21->52 files and 15->147 matches.
// ---------------------------------------------------------------------------

// HONESTY-ANCHOR TestHonesty_IncludeTestsIsNotSilentlyInertForNonGo: correct
func TestHonesty_IncludeTestsIsNotSilentlyInertForNonGo(t *testing.T) {
	dir := fixtureRepo(t, map[string]string{
		"app.rb":       "def alpha(a)\n  a\nend\n",
		"app_test.rb":  "def beta(b)\n  b\nend\n",
		"main.go":      "package main\n\nfunc A() {\n\tdefer x.Close()\n}\n",
		"main_test.go": "package main\n\nfunc TestA() {\n\tdefer y.Close()\n}\n",
	})

	rubyOff, rubyOffStats := honestyMatchOK(t, dir, treesitter.LangRuby, honestyRubyPattern,
		Scope{IncludeTests: false})
	rubyOn, rubyOnStats := honestyMatchOK(t, dir, treesitter.LangRuby, honestyRubyPattern,
		Scope{IncludeTests: true})

	// KNOWN POSITIVE, same run, same flag: Go moves. A harness that ignored
	// include_tests altogether would fail here before the Ruby legs are read.
	goOff, goOffStats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern,
		Scope{IncludeTests: false})
	goOn, goOnStats := honestyMatchOK(t, dir, treesitter.LangGo, honestyDeferPattern,
		Scope{IncludeTests: true})
	require.Equal(t, []string{"main.go"}, matchedFiles(goOff),
		"control: Go excludes _test.go when the flag is off")
	require.ElementsMatch(t, []string{"main.go", "main_test.go"}, matchedFiles(goOn),
		"control: Go admits _test.go when the flag is on")
	require.Equal(t, 1, goOffStats.FilesScanned)
	require.Equal(t, 2, goOnStats.FilesScanned)

	// FIXED. Ruby's own convention decides now, so the flag moves the Ruby walk
	// exactly as it moves the Go one.
	assert.Equal(t, []string{"app.rb"}, matchedFiles(rubyOff),
		"include_tests:false excludes app_test.rb under Ruby's _test.rb convention")
	assert.ElementsMatch(t, []string{"app.rb", "app_test.rb"}, matchedFiles(rubyOn),
		"include_tests:true admits it again")
	assert.Equal(t, 1, rubyOffStats.FilesScanned,
		"the excluded file is not scanned")
	assert.Equal(t, 2, rubyOnStats.FilesScanned,
		"and the two walks differ, which is what the flag being inert used to hide")
}

// ---------------------------------------------------------------------------
// 5. FIXED. An unknown where-tree kind is refused instead of returning a
// vacuous zero.
//
// The defect: a kind leaf naming a node kind the grammar lacks can never match
// anything, so the call is answerable before any walk. Instead it walked the
// whole scope and returned total 0 with no error and no hint —
// indistinguishable from a correct search that found nothing. On the replace
// path the same silence certified a migration complete after changing nothing.
// Confirmed live while the defect was open: where {kind:{of:"F",
// is:"identifierr"}} over match.go returned total 0, files_scanned 1, no error,
// no hint.
//
// A validation pass now checks every kind leaf against the grammar's own named
// vocabulary — the same enumeration operation=list_node_kinds prints — and
// refuses the call with the offending kind, the language and the nearest valid
// spellings. The tool handlers run it after language resolution on match, count
// and replace, which is pinned in the tools package by
// TestAstWhereKind_ValidatedOnMatchCountAndReplace.
//
// Note what the fix deliberately does NOT do: it never consults the corpus. A
// valid kind that simply does not occur stays a clean zero, because erroring
// there would break legitimate searches — the four-way ladder in
// TestWhereKind_ControlLadderDistinguishesAbsentFromBogus is what holds that
// line.
// ---------------------------------------------------------------------------

// HONESTY-ANCHOR TestHonesty_UnknownWhereKindErrors: correct
func TestHonesty_UnknownWhereKindErrors(t *testing.T) {
	const body = `package main

func A() {
	alpha(beta)
}
`
	dir := fixtureRepo(t, map[string]string{"main.go": body})

	// Control ladder rung 1: unfiltered, the pattern matches.
	unfiltered, _ := honestyMatchOK(t, dir, treesitter.LangGo, "$F($$$A)", Scope{})
	require.NotEmpty(t, unfiltered, "setup: the pattern must match before a filter is applied")

	// Rung 2: a REAL kind that is present still matches, so rung 3's zero
	// cannot be blamed on the where-tree refusing everything.
	valid, _, err := honestyMatch(t, dir, treesitter.LangGo, "$F($$$A)",
		&WhereNode{Kind: &KindLeaf{Of: "F", Is: []string{"identifier"}}}, Scope{})
	require.NoError(t, err)
	require.NotEmpty(t, valid, "control: a valid, present kind still matches")

	// Rung 3: the misspelled kind. FIXED — a kind the grammar lacks is
	// decidable before the walk, and is now refused rather than walked.
	//
	// This assertion READS THE NEW ENTRY POINT on purpose. The fix is additive,
	// so an inversion that only re-stated the old fields could still pass
	// against the pre-fix engine; calling ValidateWhereKinds cannot even
	// compile against it.
	bogusWhere := &WhereNode{Kind: &KindLeaf{Of: "F", Is: []string{"identifierr"}}}
	verr := ValidateWhereKinds(bogusWhere, treesitter.LangGo)
	require.Error(t, verr, "an unknown where-tree kind is refused instead of walked")
	assert.Contains(t, verr.Error(), "identifierr", "the error names the offending kind")
	// The QUOTED near miss: bare `identifier` is a substring of the offending
	// name itself, so only the quoted form proves a suggestion was made.
	assert.Contains(t, verr.Error(), `"identifier"`, "and offers the near miss")
	// That the refused call also stops costing a walk is observable only where
	// the walk is dispatched, so it is pinned in the tools package: the replace
	// leg of TestAstWhereKind_ValidatedOnMatchCountAndReplace runs with dry_run
	// false and finds the file on disk untouched.
}
