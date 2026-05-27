// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Test_Corpus_DirectoryNameMatchesClassification iterates every fixture under
// testdata/test_kind/ and asserts the chunker's classification matches the
// kind directory name. Catches regressions where a fixture exists and the
// matrix entry is correct but the predicate produces the wrong TestKind.
//
// Per locked Q5 / step 536b0b8e:
//   - Positive kinds (test/benchmark/example/fuzz/setup/teardown): at least
//     one chunk classified IsTest=true && TestKind==<kind>.
//   - Negative-only kinds (helper/mock/fixture): at least one chunk classified
//     IsTest=true && TestKind==<kind>.
//   - Special case `non-test/`: NO TestKind enum value; assert IsTest=false
//     for ALL chunks (zero IsTest=true chunks). Verifies the predicate's
//     path-segment gate rejects production-shaped files.
func Test_Corpus_DirectoryNameMatchesClassification(t *testing.T) {
	chunker := NewChunker()
	defer chunker.Close()

	if _, err := os.Stat(fixtureRoot); errors.Is(err, fs.ErrNotExist) {
		t.Logf("fixture root %q does not exist; treating as empty corpus", fixtureRoot)
		return
	}

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
		_, _, kind, isNegativeOnly, parseErr := parseFixturePath(rel)
		if parseErr != nil {
			t.Errorf("parseFixturePath(%q): %v", rel, parseErr)
			return nil //nolint:nilerr // recorded via t.Errorf; continue walking siblings
		}
		if DetectLanguage(name) == LangUnknown {
			return nil
		}
		chunks := chunkFixture(t, chunker, path)

		if kind == "non-test" {
			for _, ch := range chunks {
				if ch.IsTest {
					t.Errorf("non-test fixture %s: chunk %q (%s) IsTest=true (TestKind=%q); want IsTest=false",
						rel, ch.Name, ch.ChunkType, ch.TestKind)
				}
			}
			return nil
		}

		var wantKind TestKind
		var ok bool
		if isNegativeOnly {
			wantKind, ok = negativeOnlyKindDirs[kind]
		} else {
			wantKind, ok = positiveKindDirs[kind]
		}
		if !ok {
			t.Errorf("unrecognized kind %q for fixture %s", kind, rel)
			return nil
		}

		matched := false
		for _, ch := range chunks {
			if ch.IsTest && ch.TestKind == wantKind {
				matched = true
				break
			}
		}
		if !matched {
			var seen []string
			for _, ch := range chunks {
				seen = append(seen, fmt.Sprintf("(%s,IsTest=%t,TestKind=%q)",
					ch.ChunkType, ch.IsTest, ch.TestKind))
			}
			t.Errorf("fixture %s: directory name %q does not match any chunk classification; got: %v",
				rel, kind, seen)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("WalkDir(%s): %v", fixtureRoot, walkErr)
	}
}

// Test_Corpus_MatrixCompletenessAudit asserts every Language with a registered
// classifier in testKindClassifiers OR testBlockClassifiers has at least one
// entry in testCorpusMatrix. Catches the regression where a new language
// predicate is registered but no fixture is added.
func Test_Corpus_MatrixCompletenessAudit(t *testing.T) {
	registered := make(map[Language]bool)
	for lang := range testKindClassifiers {
		registered[lang] = true
	}
	for lang := range testBlockClassifiers {
		registered[lang] = true
	}

	missing := make([]string, 0)
	for lang := range registered {
		fwMap, ok := testCorpusMatrix[lang]
		if !ok || len(fwMap) == 0 {
			missing = append(missing, string(lang))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("registered classifier languages without testCorpusMatrix entry (%d):\n  %v",
			len(missing), missing)
	}
}
