// SPDX-License-Identifier: Apache-2.0

// import_spec_pattern_test.go — the import-spec pattern form the fourth Go
// wrapper hosts.
//
// `$$$P "os/exec"` binds an import of that path in EVERY spec form Go writes,
// grouped or not, aliased or not, with P bound to the local name and empty when
// there is none; `$P "os/exec"` binds the aliased forms alone. The corpus is
// testdata/importspec, checked in beside this file — the probe trees this
// ticket's research ran against were ephemeral /tmp directories and none of
// them survives, so every number below is re-established on a corpus that
// travels with the test.
//
// testdata/ is one of discovery's excluded path components, so this corpus is
// invisible to a repository-wide ast walk and to the corpus-check scan; the
// tests below reach it by pointing the walk AT the directory, which puts no
// "testdata" component in any repo-relative path.

package ast

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// importFixtureDir is the corpus root every row below walks.
func importFixtureDir(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "importspec"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

// matchImportFixture compiles pattern and walks the fixture corpus, returning
// the per-file capture text for the named capture, keyed by file base name.
// A file with several matches contributes several entries, sorted, so a row can
// assert both WHICH files matched and WHAT each bound.
func matchImportFixture(t *testing.T, pattern, capture string) map[string][]string {
	t.Helper()
	pat, err := Parse(pattern)
	if err != nil {
		t.Fatalf("Parse(%q): %v", pattern, err)
	}
	cp, err := Compile(pat, treesitter.LangGo, "")
	if err != nil {
		t.Fatalf("Compile(%q): %v", pattern, err)
	}
	defer cp.Close()

	matches, _, err := Match(context.Background(), importFixtureDir(t), treesitter.LangGo, cp, nil, Scope{IncludeTests: true})
	if err != nil {
		t.Fatalf("Match(%q): %v", pattern, err)
	}
	out := map[string][]string{}
	for _, m := range matches {
		base := filepath.Base(m.FilePath)
		out[base] = append(out[base], m.Captures[capture].Text)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

func TestImportSpec_SequenceFormMatchesEverySpecForm(t *testing.T) {
	// Row 13. `$$$P "os/exec"` matches all five spec forms — ungrouped plain,
	// ungrouped aliased, grouped plain, grouped aliased and the single-spec
	// group — with P bound to the local name where there is one and empty
	// where there is not. The sequence placeholder is what covers the absent
	// name: it sits in a no-separator optional-child slot, where it matches
	// zero siblings.
	got := matchImportFixture(t, `$$$P "os/exec"`, "P")
	want := map[string][]string{
		"ungrouped_plain.go":   {""},
		"ungrouped_aliased.go": {"ex"},
		"grouped_plain.go":     {""},
		"grouped_aliased.go":   {"ex2"},
		"single_spec_group.go": {""},
	}
	assertFixtureMatches(t, got, want)
}

func TestImportSpec_DecoyStringsAreNotMatchedButABareLiteralFindsThem(t *testing.T) {
	// Row 14. THE CONTROL THAT MAKES ROW 13 MEAN SOMETHING. decoy.go holds
	// "os/exec" as a const value and as a return value and imports nothing;
	// the import form must not match either, while a bare string-literal
	// pattern finds both in the same run. Without this pair, row 13 is
	// consistent with the form having degenerated into a text search.
	got := matchImportFixture(t, `$$$P "os/exec"`, "P")
	if hits, ok := got["decoy.go"]; ok {
		t.Errorf("import form matched decoy.go %v; the decoy holds the path as a value, never as an import", hits)
	}

	literal := matchImportFixture(t, `"os/exec"`, "$match")
	if len(literal["decoy.go"]) != 2 {
		t.Errorf("bare string-literal control matched decoy.go %d times, want 2 — "+
			"the control is what proves the decoy's occurrences are reachable at all", len(literal["decoy.go"]))
	}

	// And the negative control on the path itself: a path no fixture imports
	// returns nothing at all, so the form is constrained by the literal it names.
	if impossible := matchImportFixture(t, `$$$P "os/exec/does/not/exist"`, "P"); len(impossible) != 0 {
		t.Errorf("a path no fixture imports matched %v, want no files", impossible)
	}
}

func TestImportSpec_SingleFormMatchesTheAliasedSpecsOnly(t *testing.T) {
	// Row 15. `$P "os/exec"` requires a local name, so it discriminates the
	// aliased forms from the plain ones — a same-run contrast against row 13's
	// five.
	got := matchImportFixture(t, `$P "os/exec"`, "P")
	want := map[string][]string{
		"ungrouped_aliased.go": {"ex"},
		"grouped_aliased.go":   {"ex2"},
	}
	assertFixtureMatches(t, got, want)
}

func TestImportSpec_CompileDisclosesOneVariantUnderTheImportWrapper(t *testing.T) {
	// Row 16. The compile disclosure a caller reads: one variant, hosted by
	// the importspec wrapper alone, in the decl context, rooted at import_spec.
	// THE MUTATION for the fourth wrapper is removing it, after which both
	// forms fail to compile at all and rows 13-16 go red together.
	for _, pattern := range []string{`$$$P "os/exec"`, `$P "os/exec"`} {
		pat, err := Parse(pattern)
		if err != nil {
			t.Fatalf("Parse(%q): %v", pattern, err)
		}
		cp, err := Compile(pat, treesitter.LangGo, "")
		if err != nil {
			t.Fatalf("Compile(%q): %v", pattern, err)
		}
		if len(cp.Variants) != 1 {
			t.Errorf("%q compiled to %d variants, want 1", pattern, len(cp.Variants))
		}
		for _, v := range cp.Variants {
			if got := v.Wrappers; len(got) != 1 || got[0] != "importspec" {
				t.Errorf("%q wrappers = %v, want [importspec]", pattern, got)
			}
			if got := v.Contexts; len(got) != 1 || got[0] != "decl" {
				t.Errorf("%q contexts = %v, want [decl]", pattern, got)
			}
			if v.RootKind != "import_spec" {
				t.Errorf("%q root_kind = %q, want import_spec", pattern, v.RootKind)
			}
		}
		cp.Close()
	}
}

func TestImportSpec_RootDescendedIsAssertedDirectlyWithTwoControls(t *testing.T) {
	// Row 16a. rootDescended false is the entire mechanism the import form
	// rests on: it is what leaves collectViaQuery's candidate as the raw
	// import_spec instead of descending it to the path literal. The indirect
	// proxy — grouped and ungrouped both matching — is sound but fails in the
	// SAME direction as the feature, so it cannot separate "the wrapper stopped
	// hosting" from "the descent rule changed". patternRootKind is
	// package-private and this suite is in package ast, so the flag is read
	// rather than inferred.
	//
	// TWO SAME-RUN CONTROLS make `false` mean something rather than being a
	// value this assertion would report for anything: a bare `$X` gives an
	// EMPTY root kind (the path that skips the root query and walks every
	// node, which is what lets a bare capture reach a named import_spec at
	// all), and an ordinary in-tree pattern gives a non-empty root kind.
	cfg, ok := langConfigFor(treesitter.LangGo)
	if !ok {
		t.Fatal("go LangConfig not registered")
	}
	rows := []struct {
		pattern      string
		wantRootKind string
	}{
		{`$$$P "os/exec"`, "import_spec"},
		{`$P "os/exec"`, "import_spec"},
		{`$X`, ""},
		{`defer $X.Close()`, "defer_statement"},
	}
	for _, row := range rows {
		pat, err := Parse(row.pattern)
		if err != nil {
			t.Fatalf("Parse(%q): %v", row.pattern, err)
		}
		variants, narrowed, err := compilePatternVariants(context.Background(), pat, cfg, "")
		if err != nil {
			t.Fatalf("compilePatternVariants(%q): %v", row.pattern, err)
		}
		gotKind, gotDescended := patternRootKind(variants[0].Tree)
		if gotKind != row.wantRootKind {
			t.Errorf("%q rootKind = %q, want %q", row.pattern, gotKind, row.wantRootKind)
		}
		if gotDescended {
			t.Errorf("%q rootDescended = true, want false", row.pattern)
		}
		closeVariants(variants)
		closeVariants(narrowed)
	}
}

func TestImportSpec_BareCaptureWithKindImportSpecStillBindsNamedSpecsOnly(t *testing.T) {
	// Row 16b. THE RESIDUAL, PINNED RATHER THAN FIXED. This ticket adds a
	// wrapper; it does not change effectiveTargetNode, so a bare `$X` with
	// kind:import_spec still binds only the specs that carry a LOCAL NAME — an
	// unnamed spec shares its byte span with its path literal and the
	// unconditional same-span descent binds the literal instead.
	//
	// The same-run control is a string-literal kind over the same corpus,
	// which finds every path literal: without it, "2" would be consistent with
	// the walk not reaching these files at all. If a later change fixes the
	// descent, this row goes red DELIBERATELY instead of the answer moving in
	// silence — and the doc sentence beside the import form goes with it.
	named := matchImportFixtureWhere(t, `$X`, `{"kind": {"of": "X", "is": "import_spec"}}`)
	wantNamed := map[string][]string{
		"ungrouped_aliased.go": {`ex "os/exec"`},
		"grouped_aliased.go":   {`ex2 "os/exec"`},
	}
	assertFixtureMatches(t, named, wantNamed)

	literals := matchImportFixtureWhere(t, `$X`, `{"kind": {"of": "X", "is": "interpreted_string_literal"}}`)
	total := 0
	for _, hits := range literals {
		total += len(hits)
	}
	if total != 9 {
		t.Errorf("interpreted_string_literal control found %d literals across the corpus, want 9 "+
			"(5 os/exec import paths + 2 fmt import paths + 2 decoy values); a different number means the corpus moved, not the descent", total)
	}
}

func TestImportSpec_BareLiteralStillDedupesToOneTreeWithTheSameCount(t *testing.T) {
	// Row 17. A bare `"os/exec"` fragment parses under stmt, expr AND the new
	// wrapper to the same string-literal tree, so compilePatternVariants'
	// dedupe merges them: the context SET gains "decl" and the match count does
	// not move. That stamp is true — a string literal in a spec position IS a
	// valid import spec — and it is the union philosophy working rather than a
	// leak.
	pat, err := Parse(`"os/exec"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cp, err := Compile(pat, treesitter.LangGo, "")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer cp.Close()
	if len(cp.Variants) != 1 {
		t.Fatalf("variants = %d, want 1 (the three wrappers dedupe to one tree)", len(cp.Variants))
	}
	gotContexts := append([]string(nil), cp.Variants[0].Contexts...)
	sort.Strings(gotContexts)
	want := []string{"decl", "expr", "stmt"}
	if len(gotContexts) != len(want) {
		t.Fatalf("contexts = %v, want %v", gotContexts, want)
	}
	for i := range want {
		if gotContexts[i] != want[i] {
			t.Fatalf("contexts = %v, want %v", gotContexts, want)
		}
	}

	// The count half of the row: every "os/exec" literal in the corpus, the
	// same eight the pre-wrapper tree found.
	hits := matchImportFixture(t, `"os/exec"`, "$match")
	total := 0
	for _, h := range hits {
		total += len(h)
	}
	if total != 7 {
		t.Errorf("bare literal matched %d times, want 7 (5 import paths + 2 decoy values)", total)
	}
}

func TestImportSpec_AWholeSpecPlaceholderStillFailsToCompileLoudly(t *testing.T) {
	// Row 18. A placeholder standing for a WHOLE SPEC still cannot compile —
	// a bare identifier is a legal spec NAME, so the decl parse reads
	// `$$$A "os/exec"` as one spec and leaves the trailing placeholder as an
	// ERROR node. The refusal must name EVERY wrapper tried, which is now FOUR:
	// a fourth wrapper that silently swallowed the failure would be the
	// regression this row exists to catch.
	pat, err := Parse(`import ($$$A "os/exec" $$$B)`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cp, err := Compile(pat, treesitter.LangGo, "")
	if err == nil {
		cp.Close()
		t.Fatal("expected a compile failure for a whole-spec placeholder, got none")
	}
	for _, name := range []string{"decl", "stmt", "expr", "importspec"} {
		if !containsSubstring(err.Error(), name) {
			t.Errorf("compile error does not name wrapper %q: %v", name, err)
		}
	}
}

// matchImportFixtureWhere is matchImportFixture with a where-tree.
func matchImportFixtureWhere(t *testing.T, pattern, whereJSON string) map[string][]string {
	t.Helper()
	pat, err := Parse(pattern)
	if err != nil {
		t.Fatalf("Parse(%q): %v", pattern, err)
	}
	cp, err := Compile(pat, treesitter.LangGo, "")
	if err != nil {
		t.Fatalf("Compile(%q): %v", pattern, err)
	}
	defer cp.Close()
	where, err := ParseWhere([]byte(whereJSON))
	if err != nil {
		t.Fatalf("ParseWhere(%s): %v", whereJSON, err)
	}
	matches, _, err := Match(context.Background(), importFixtureDir(t), treesitter.LangGo, cp, where, Scope{IncludeTests: true})
	if err != nil {
		t.Fatalf("Match(%q): %v", pattern, err)
	}
	out := map[string][]string{}
	for _, m := range matches {
		base := filepath.Base(m.FilePath)
		out[base] = append(out[base], m.Captures["X"].Text)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// assertFixtureMatches compares a per-file capture map against the expected
// one, reporting missing files, extra files and per-file capture differences
// separately — a row that fails should say which of the three happened.
func assertFixtureMatches(t *testing.T, got, want map[string][]string) {
	t.Helper()
	for file, wantCaps := range want {
		gotCaps, ok := got[file]
		if !ok {
			t.Errorf("no match in %s, want captures %v", file, wantCaps)
			continue
		}
		if len(gotCaps) != len(wantCaps) {
			t.Errorf("%s matched %d times %v, want %d %v", file, len(gotCaps), gotCaps, len(wantCaps), wantCaps)
			continue
		}
		for i := range wantCaps {
			if gotCaps[i] != wantCaps[i] {
				t.Errorf("%s capture[%d] = %q, want %q", file, i, gotCaps[i], wantCaps[i])
			}
		}
	}
	for file := range got {
		if _, ok := want[file]; !ok {
			t.Errorf("unexpected match in %s: %v", file, got[file])
		}
	}
}

func containsSubstring(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
