// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// testCorpusMatrix is the SINGLE source of truth for the (language, framework,
// kind) cells expected to have at least one positive fixture under
// testdata/test_kind/<lang>/<framework>/<kind>/<file>. Per locked Q1 (framework-
// supports subset), only cells the framework actually defines appear here.
//
// Negative-only kinds (helper, mock, fixture, non-test) are NEVER listed in
// this matrix; they are exercised by Test_Corpus_NegativeAssertions instead.
//
// INITIAL VALUE is empty. Per-language phases populate this map in lockstep
// with the testdata/ tree. Adding a matrix entry without a fixture FAILS
// Test_Corpus_Coverage (unattested cell). Adding a positive fixture without
// a matrix entry also FAILS (unexpected cell). The bidirectional gate is the
// value of the corpus.
//
// Use Framework + TestKind constants (not string literals) so a typo fails at
// compile time.
var testCorpusMatrix = map[Language]map[Framework][]TestKind{
	LangGo: {
		FrameworkGoTesting: {
			TestKindTest, TestKindBenchmark, TestKindExample, TestKindFuzz, TestKindSetup,
		},
	},
	LangRust: {
		// Rust doc-tests (TestKindExample) are deferred per locked Q3 — the
		// predicate doesn't recognize them; doc-comment blocks are not
		// classified. Matrix entry omits Example accordingly.
		FrameworkRustTest: {
			TestKindTest, TestKindBenchmark, TestKindFuzz,
		},
		FrameworkRustTokio:      {TestKindTest},
		FrameworkRustRSTest:     {TestKindTest},
		FrameworkRustProptest:   {TestKindTest},
		FrameworkRustQuickcheck: {TestKindTest},
	},
	LangJava: {
		FrameworkJavaJUnit4: {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkJavaJUnit5: {
			TestKindTest, TestKindSetup, TestKindTeardown, TestKindBenchmark,
		},
		FrameworkJavaTestNG: {TestKindTest, TestKindSetup, TestKindTeardown},
	},
	LangKotlin: {
		FrameworkKotlinJUnit: {
			TestKindTest, TestKindSetup, TestKindTeardown, TestKindBenchmark,
		},
		FrameworkKotlinKotest: {TestKindTest},
		FrameworkKotlinSpek:   {TestKindTest},
	},
	LangScala: {
		FrameworkScalaJUnit:     {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkScalaScalaTest: {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkScalaMUnit:     {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkScalaSpecs2:    {TestKindTest},
	},
	LangGroovy: {
		// Spock test methods declared via string-literal names (`def "..."()`)
		// parse as ERROR in tree-sitter-groovy and are out of scope per locked
		// Q10. Setup/teardown via identifier names are recognized.
		FrameworkGroovySpock: {TestKindSetup, TestKindTeardown},
	},
	LangPython: {
		FrameworkPyPyTest:   {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkPyUnittest: {TestKindTest, TestKindSetup, TestKindTeardown},
	},
	LangRuby: {
		// fixture/, mock/, helper/ are negative-only kinds (Test_Corpus_NegativeAssertions
		// covers them) so they are NOT listed here despite RSpec defining them.
		FrameworkRubyRSpec:    {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkRubyMinitest: {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkRubyTestUnit: {TestKindTest, TestKindSetup, TestKindTeardown},
	},
	LangJavaScript: jsFrameworkMatrix(),
	LangTypeScript: tsFrameworkMatrix(),
	LangC: {
		FrameworkCppUnity:  {TestKindTest},
		FrameworkCppCMocka: {TestKindTest},
	},
	LangCPP: {
		FrameworkCppGTest:     {TestKindTest, TestKindBenchmark},
		FrameworkCppCatch2:    {TestKindTest},
		FrameworkCppDoctest:   {TestKindTest},
		FrameworkCppBoostTest: {TestKindTest},
	},
	LangPHP: {
		// fixture/ is a negative-only kind covered by Test_Corpus_NegativeAssertions;
		// the @dataProvider TestKindFixture is verified there, not in the
		// positive matrix.
		FrameworkPHPPHPUnit:     {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkPHPPest:        {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkPHPCodeception: {TestKindTest, TestKindSetup, TestKindTeardown},
	},
	LangElixir: {
		// ExUnit has no teardown_all equivalent (cleanup uses on_exit). Setup
		// covers both setup and setup_all; on_exit handles teardown.
		FrameworkElixirExUnit: {TestKindTest, TestKindSetup, TestKindTeardown},
	},
	LangLua: {
		FrameworkLuaBusted:  {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkLuaLuaUnit: {TestKindTest, TestKindSetup, TestKindTeardown},
	},
	LangBash: {
		FrameworkBashBats: {TestKindTest, TestKindSetup, TestKindTeardown},
	},
	LangSwift: {
		FrameworkSwiftXCTest:  {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkSwiftTesting: {TestKindTest},
	},
	LangCSharp: {
		FrameworkCSNUnit:  {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkCSXUnit:  {TestKindTest, TestKindBenchmark},
		FrameworkCSMSTest: {TestKindTest, TestKindSetup, TestKindTeardown},
	},
	LangElm: {
		FrameworkElmTest: {TestKindTest, TestKindFuzz},
	},
	LangOCaml: {
		FrameworkOCamlAlcotest:      {TestKindTest},
		FrameworkOCamlPpxInlineTest: {TestKindTest},
	},
	LangHCL: {
		FrameworkHCLTfTest: {TestKindTest},
	},
}

// jsFrameworkMatrix returns the shared per-framework cells for JS/TS. Both
// languages exercise the same predicate (classifyTestBlockJS) so the matrix
// rows are identical.
func jsFrameworkMatrix() map[Framework][]TestKind {
	return map[Framework][]TestKind{
		FrameworkJSJest: {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkJSVitest: {
			TestKindTest, TestKindBenchmark, TestKindSetup, TestKindTeardown,
		},
		FrameworkJSMocha:    {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkJSJasmine:  {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkJSAva:      {TestKindTest},
		FrameworkJSTape:     {TestKindTest},
		FrameworkJSNodeTest: {TestKindTest, TestKindSetup, TestKindTeardown},
		FrameworkJSBunTest:  {TestKindTest, TestKindSetup, TestKindTeardown},
	}
}

// tsFrameworkMatrix extends the JS-shared frameworks with Playwright + Cypress
// (E2E frameworks usually paired with TS per the corpus convention).
func tsFrameworkMatrix() map[Framework][]TestKind {
	m := jsFrameworkMatrix()
	m[FrameworkJSPlaywright] = []TestKind{TestKindTest, TestKindSetup, TestKindTeardown}
	m[FrameworkJSCypress] = []TestKind{TestKindTest, TestKindSetup, TestKindTeardown}
	return m
}

// positiveKindDirs maps kind directory name → TestKind for the positive-kind
// allowlist. STRICTLY SINGULAR per the walker contract.
var positiveKindDirs = map[string]TestKind{
	"test":      TestKindTest,
	"benchmark": TestKindBenchmark,
	"example":   TestKindExample,
	"fuzz":      TestKindFuzz,
	"setup":     TestKindSetup,
	"teardown":  TestKindTeardown,
}

// negativeOnlyKindDirs is the set of negative-only kind directory names.
// STRICTLY SINGULAR. Fixtures under these directories are EXCLUDED from
// Test_Corpus_Coverage and instead asserted by Test_Corpus_NegativeAssertions.
var negativeOnlyKindDirs = map[string]TestKind{
	"helper":   TestKindHelper,
	"mock":     TestKindMock,
	"fixture":  TestKindFixture,
	"non-test": TestKindNone, // non-test/ asserts IsTest=false for all chunks; no enum value.
}

// pluralRejections maps plural variants → singular form for typo-friendly
// error messages.
var pluralRejections = map[string]string{
	"tests":      "test",
	"benchmarks": "benchmark",
	"examples":   "example",
	"fuzzes":     "fuzz",
	"setups":     "setup",
	"teardowns":  "teardown",
	"helpers":    "helper",
	"mocks":      "mock",
	"fixtures":   "fixture",
}

// fixtureRoot is the on-disk path (relative to the package working directory)
// of the corpus tree.
const fixtureRoot = "testdata/test_kind"

// parseFixturePath parses a path relative to testdata/test_kind/ into its
// (lang, framework, kind) components. Returns isNegativeOnly=true when
// the kind directory is in the negative-only allowlist; in that case the
// caller skips the positive-coverage check.
//
// Path layout: <lang>/<framework>/<kind>/<filename> (minimum 4 components).
// Components beyond the first three form an OPAQUE tail passed verbatim to
// language-specific test-file predicates — the walker itself doesn't need
// the tail, so it's discarded here.
func parseFixturePath(rel string) (lang, framework, kind string, isNegativeOnly bool, err error) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 4 {
		return "", "", "", false, fmt.Errorf(
			"path needs at least 4 components <lang>/<framework>/<kind>/<filename>: %q", rel)
	}
	lang = parts[0]
	framework = parts[1]
	kind = parts[2]

	if singular, ok := pluralRejections[kind]; ok {
		return "", "", "", false, fmt.Errorf(
			"use singular kind directory: %q is not allowed; use %q", kind, singular)
	}
	if _, ok := positiveKindDirs[kind]; ok {
		return lang, framework, kind, false, nil
	}
	if _, ok := negativeOnlyKindDirs[kind]; ok {
		return lang, framework, kind, true, nil
	}
	positives := sortedKeys(positiveKindDirs)
	negatives := sortedKeys(negativeOnlyKindDirs)
	return "", "", "", false, fmt.Errorf(
		"unrecognized kind directory: %q; allowed positive kinds: %v; allowed negative kinds: %v",
		kind, positives, negatives)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// shouldSkipFixtureFile returns true for entries that aren't real fixture
// source files (.gitkeep, READMEs, dotfiles other than .gitkeep).
func shouldSkipFixtureFile(name string) bool {
	if name == ".gitkeep" {
		return true
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	if strings.HasPrefix(strings.ToUpper(name), "README") {
		return true
	}
	return false
}

// cellKey is a (lang, framework, kind) triple used to compare matrix entries
// against on-disk fixtures.
type cellKey struct {
	Lang      Language
	Framework Framework
	Kind      TestKind
}

// matrixCells expands testCorpusMatrix into a flat set of cellKey values for
// set-difference computation against the on-disk attested set.
func matrixCells(matrix map[Language]map[Framework][]TestKind) map[cellKey]struct{} {
	out := make(map[cellKey]struct{})
	for lang, frameworks := range matrix {
		for fw, kinds := range frameworks {
			for _, kind := range kinds {
				out[cellKey{Lang: lang, Framework: fw, Kind: kind}] = struct{}{}
			}
		}
	}
	return out
}

func formatCells(cells map[cellKey]struct{}) []string {
	out := make([]string, 0, len(cells))
	for c := range cells {
		out = append(out, fmt.Sprintf("%s/%s/%s", c.Lang, c.Framework, c.Kind))
	}
	sort.Strings(out)
	return out
}

// Test_Corpus_Coverage walks testdata/test_kind/ and asserts that:
//  1. Every (lang, framework, kind) triple in testCorpusMatrix has at least
//     one fixture file at the expected path that the chunker classifies with
//     the expected TestKind (unattested → fail).
//  2. Every positive-kind fixture on disk corresponds to an entry in
//     testCorpusMatrix (unexpected → fail).
//
// Negative-only kinds (helper, mock, fixture, non-test) are EXCLUDED from
// both checks; Test_Corpus_NegativeAssertions covers them.
//
// Locked Q4: failure path enumerates missing/unexpected cells; full table only
// under -v.
func Test_Corpus_Coverage(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	expected := matrixCells(testCorpusMatrix)
	attested := make(map[cellKey]struct{})
	positiveFixturesOnDisk := make(map[cellKey][]string)

	if _, err := os.Stat(fixtureRoot); errors.Is(err, fs.ErrNotExist) {
		// Empty corpus: pass-through if matrix is also empty, else surface
		// missing cells via the unattested gate below.
		t.Logf("fixture root %q does not exist; treating as empty corpus", fixtureRoot)
	} else {
		walkErr := filepath.WalkDir(fixtureRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			name := d.Name()
			if shouldSkipFixtureFile(name) {
				return nil
			}
			rel, err := filepath.Rel(fixtureRoot, path)
			if err != nil {
				t.Errorf("filepath.Rel(%q, %q): %v", fixtureRoot, path, err)
				return nil
			}
			lang, framework, kind, isNegativeOnly, parseErr := parseFixturePath(rel)
			if parseErr != nil {
				t.Errorf("parseFixturePath: %v (path=%s)", parseErr, rel)
				return nil
			}
			if isNegativeOnly {
				return nil // covered by Test_Corpus_NegativeAssertions
			}
			detected := DetectLanguage(name)
			if detected == LangUnknown {
				t.Errorf("unrecognized extension for fixture %q (lang dir=%s)", rel, lang)
				return nil
			}
			src, err := os.ReadFile(path) //nolint:gosec // test reads checked-in corpus fixtures; no symlink TOCTOU risk
			if err != nil {
				t.Errorf("ReadFile(%s): %v", path, err)
				return nil
			}
			absPath, err := filepath.Abs(path)
			if err != nil {
				absPath = path
			}
			res, err := chunker.ChunkFile(context.Background(), absPath, src)
			if err != nil {
				t.Errorf("ChunkFile(%s): %v", rel, err)
				return nil
			}
			expectedKind := positiveKindDirs[kind]
			matched := false
			for _, ch := range res.Chunks {
				if ch.IsTest && ch.TestKind == expectedKind {
					matched = true
					break
				}
			}
			cell := cellKey{
				Lang:      Language(lang),
				Framework: Framework(framework),
				Kind:      expectedKind,
			}
			if !matched {
				// Build a small diagnostic of what we got instead.
				var seen []string
				for _, ch := range res.Chunks {
					seen = append(seen, fmt.Sprintf("(%s,IsTest=%t,TestKind=%q)",
						ch.ChunkType, ch.IsTest, ch.TestKind))
				}
				t.Errorf("fixture %s: no chunk classified as TestKind=%q; got %d chunks: %v",
					rel, expectedKind, len(res.Chunks), seen)
				// Still treat as attested-on-disk so we don't get spurious
				// "unexpected fixture" reports for a fixture that exists but
				// fails predicate classification — the per-fixture failure
				// above is the actionable signal.
			}
			attested[cell] = struct{}{}
			positiveFixturesOnDisk[cell] = append(positiveFixturesOnDisk[cell], rel)
			return nil
		})
		if walkErr != nil {
			t.Fatalf("WalkDir(%s): %v", fixtureRoot, walkErr)
		}
	}

	// Bidirectional gate.
	unattested := make(map[cellKey]struct{})
	for c := range expected {
		if _, ok := attested[c]; !ok {
			unattested[c] = struct{}{}
		}
	}
	unexpected := make(map[cellKey]struct{})
	for c := range attested {
		if _, ok := expected[c]; !ok {
			unexpected[c] = struct{}{}
		}
	}

	if len(unattested) > 0 {
		t.Errorf("unattested cells (matrix entry without fixture, %d):\n  %s",
			len(unattested), strings.Join(formatCells(unattested), "\n  "))
	}
	if len(unexpected) > 0 {
		t.Errorf("unexpected fixtures (positive fixture without matrix entry, %d):\n  %s",
			len(unexpected), strings.Join(formatCells(unexpected), "\n  "))
	}

	if testing.Verbose() {
		t.Logf("matrix size = %d cells; attested = %d; unattested = %d; unexpected = %d",
			len(expected), len(attested), len(unattested), len(unexpected))
	}
}
