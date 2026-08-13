// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

// testBlocksPredicateLanguages returns every registered language whose
// TestBlocks query text carries a #match? predicate, sorted by name. Derived
// from the registry rather than a hand list, so a query file that misses the
// grouped respelling fails these gates instead of passing unnoticed.
func testBlocksPredicateLanguages(t *testing.T) []Language {
	t.Helper()
	var out []Language
	for lang, entry := range registry {
		qs := entry.Queries()
		if qs == nil || !strings.Contains(qs.TestBlocks, "#match?") {
			continue
		}
		out = append(out, lang)
	}
	slices.Sort(out)
	return out
}

// TestTestBlocksPredicatesAreLive is the census gate. A #match? written as a
// SIBLING of a completed pattern compiles into a pattern of its own, so it
// filters nothing and the capture-bearing pattern carries no predicates at all.
// Requiring a non-empty PredicatesForPattern(0) is what tells the grouped
// spelling from the inert one — both compile, and both pass a plain
// "the query compiles" check.
func TestTestBlocksPredicatesAreLive(t *testing.T) {
	langs := testBlocksPredicateLanguages(t)
	covered := 0
	for _, lang := range langs {
		entry := registry[lang]
		q, err := sitter.NewQuery([]byte(entry.Queries().TestBlocks), entry.lang)
		if err != nil {
			t.Errorf("%s: TestBlocks failed to compile: %v", lang, err)
			continue
		}
		if n := len(q.PredicatesForPattern(0)); n == 0 {
			t.Errorf("%s: pattern 0 carries no predicates — the #match? is inert "+
				"(wrap the whole query in one paren pair so the predicate groups "+
				"with the capture-bearing pattern)", lang)
		}
		q.Close()
		covered++
	}
	t.Logf("languages with a #match?-carrying TestBlocks: %d", covered)
	if covered != 14 {
		t.Errorf("covered %d languages; want 14 — a language gained or lost a "+
			"TestBlocks predicate, so re-derive this number and the pinned "+
			"counts in TestTestBlocksPredicatesFilter", covered)
	}
}

// pinnedWithCaptures records, per language, how many predicate-surviving
// matches the checked-in corpus produces. Only the five languages whose corpus
// contains a call the predicate must REJECT are listed: for them
// withCaptures < raw, so the number discriminates a live predicate from an
// inert one.
//
// The other nine covered languages have no negative case in their corpus, so
// withCaptures == raw there and no count could tell a live predicate from a
// dead one — their coverage comes from TestTestBlocksPredicatesAreLive's
// predicate-count assertion instead. Pinning ten equal numbers would look like
// evidence while proving nothing.
//
// THESE NUMBERS ARE TREE-DERIVED, not chosen. If fixtures are added or removed,
// RE-DERIVE them by running this test and reading the logged raw/with counts —
// do not adjust them to whatever makes the run green.
var pinnedWithCaptures = map[Language]int{
	LangElixir: 6,
	LangLua:    3,
	LangOCaml:  2,
	LangRuby:   17,
	LangSwift:  2,
}

// TestTestBlocksPredicatesFilter is the behavioral gate: it proves the
// predicates actually reject matches on the real corpus.
//
// It compares WITH-CAPTURES against RAW for the SAME query, never raw counts
// across spellings. An inert predicate makes the cursor surface every rejected
// alternative as a zero-capture match, so raw counts read an UNDER-filtering
// query as wildly over-matching — the Elixir corpus returns 169 raw matches
// under the inert spelling against 11 under the grouped one. Raw is meaningful
// here only as the within-run partner of with-captures.
func TestTestBlocksPredicatesFilter(t *testing.T) {
	for _, lang := range testBlocksPredicateLanguages(t) {
		t.Run(string(lang), func(t *testing.T) {
			entry := registry[lang]
			q, err := sitter.NewQuery([]byte(entry.Queries().TestBlocks), entry.lang)
			if err != nil {
				t.Fatalf("TestBlocks failed to compile: %v", err)
			}
			defer q.Close()

			raw, with := 0, 0
			dir := filepath.Join(fixtureRoot, string(lang))
			walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || shouldSkipFixtureFile(d.Name()) {
					return err
				}
				src, rerr := os.ReadFile(path) //nolint:gosec // checked-in corpus fixture
				if rerr != nil {
					return rerr
				}
				p := NewParser()
				defer p.Close()
				tree, perr := p.Parse(context.Background(), src, lang)
				if perr != nil {
					return perr
				}
				defer tree.Close()

				qc := sitter.NewQueryCursor()
				defer qc.Close()
				qc.Exec(q, tree.RootNode())
				for {
					m, ok := qc.NextMatch()
					if !ok {
						break
					}
					raw++
					if len(filterPredicates(q, m, src).Captures) > 0 {
						with++
					}
				}
				return nil
			})
			if walkErr != nil {
				t.Fatalf("WalkDir(%s): %v", dir, walkErr)
			}

			t.Logf("raw=%d withCaptures=%d", raw, with)
			if with > raw {
				t.Errorf("withCaptures=%d exceeds raw=%d, which is impossible — "+
					"the filter cannot admit more than the cursor produced", with, raw)
			}
			want, pinned := pinnedWithCaptures[lang]
			if !pinned {
				return
			}
			if with >= raw {
				t.Errorf("withCaptures=%d is not below raw=%d; this corpus contains a "+
					"call the predicate must reject, so an equal count means the "+
					"predicate filtered nothing", with, raw)
			}
			if with != want {
				t.Errorf("withCaptures=%d; want %d", with, want)
			}
		})
	}
}

// TestOCamlTestBlocks_RunnerIsNotATestBlock catches the single emitted-output
// change of the predicate respelling. `Alcotest.run` is the test RUNNER entry
// point, not a test case, and the OCaml predicate's alternant list never
// admitted it — it was emitted only because the predicate was inert.
//
// The fixture names both the runner and the genuine case "login", so the two
// legs discriminate by LINE, not by name. The positive leg is load-bearing:
// without it the negative assertion passes on an empty chunk set.
func TestOCamlTestBlocks_RunnerIsNotATestBlock(t *testing.T) {
	const (
		fixture      = "testdata/test_kind/ocaml/ocaml-alcotest/test/login_test.ml"
		runnerLine   = 7 // Alcotest.run "login" [ ... ]
		testCaseLine = 8 // Alcotest.test_case "login" `Quick test_login
	)

	src, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	abs, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}

	chunker := NewChunker()
	defer chunker.Close()
	res, err := chunker.ChunkFile(context.Background(), abs, src)
	if err != nil {
		t.Fatalf("ChunkFile: %v", err)
	}

	var blocks []Chunk
	for _, c := range res.Chunks {
		if c.ChunkType == "test_block" {
			blocks = append(blocks, c)
		}
	}

	for _, c := range blocks {
		if c.StartLine == runnerLine {
			t.Errorf("the Alcotest.run wrapper at line %d is emitted as a test_block "+
				"(name=%q) — the predicate did not reject it", runnerLine, c.Name)
		}
	}

	var sawTestCase bool
	for _, c := range blocks {
		if c.StartLine == testCaseLine {
			sawTestCase = true
		}
	}
	if !sawTestCase {
		t.Errorf("no test_block starts at line %d; the genuine Alcotest.test_case "+
			"must survive the predicate. Got %d test_blocks: %v",
			testCaseLine, len(blocks), blocks)
	}
	if len(blocks) != 1 {
		t.Errorf("fixture emits %d test_blocks; want exactly 1 (the test_case at "+
			"line %d)", len(blocks), testCaseLine)
	}
}
