// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// chunkFixture loads and chunks a single fixture file. Shared between
// Test_Corpus_Coverage and Test_Corpus_NegativeAssertions for a uniform path.
func chunkFixture(t *testing.T, chunker *Chunker, path string) []Chunk {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	res, err := chunker.ChunkFile(context.Background(), absPath, src)
	if err != nil {
		t.Fatalf("ChunkFile(%s): %v", path, err)
	}
	return res.Chunks
}

// Test_Corpus_NegativeAssertions walks the negative-only kind directories
// (helper/, mock/, fixture/, non-test/) and asserts the inverse contract:
//
//   - helper/: at least one chunk has TestKind=TestKindHelper; no chunk has
//     TestKind=TestKindTest.
//   - mock/: at least one chunk has TestKind=TestKindMock; no chunk has
//     TestKind=TestKindTest.
//   - fixture/: at least one chunk has TestKind=TestKindFixture; no chunk has
//     TestKind=TestKindTest (unless the framework permits both, documented
//     per-framework).
//   - non-test/: NO chunks may have IsTest=true. These are production-shaped
//     files that look test-shaped; the predicate's file-path gate must reject.
//
// Phase 1 ships the infrastructure; per-language phases populate the negative
// directories. With an empty negative tree, this test passes trivially.
func Test_Corpus_NegativeAssertions(t *testing.T) {
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
			// parseFixturePath errors are surfaced by Test_Corpus_Coverage; skip here.
			return nil //nolint:nilerr // intentional: Coverage test reports parse errors; this walker only checks negative fixtures
		}
		if !isNegativeOnly {
			return nil
		}
		if DetectLanguage(name) == LangUnknown {
			t.Errorf("unrecognized extension for negative fixture %q", rel)
			return nil
		}
		chunks := chunkFixture(t, chunker, path)
		assertNegativeKind(t, rel, kind, chunks)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("WalkDir(%s): %v", fixtureRoot, walkErr)
	}
}

// assertNegativeKind enforces the per-kind negative contract.
func assertNegativeKind(t *testing.T, rel, kind string, chunks []Chunk) {
	t.Helper()
	if kind == "non-test" {
		for _, ch := range chunks {
			if ch.IsTest {
				t.Errorf("non-test fixture %s: chunk %q (%s) IsTest=true (TestKind=%q); want IsTest=false",
					rel, ch.Name, ch.ChunkType, ch.TestKind)
			}
		}
		return
	}
	wantKind, ok := negativeOnlyKindDirs[kind]
	if !ok {
		t.Fatalf("assertNegativeKind: unknown negative kind %q for %s", kind, rel)
	}
	hasWant := false
	hasTest := false
	for _, ch := range chunks {
		if ch.IsTest && ch.TestKind == wantKind {
			hasWant = true
		}
		if ch.IsTest && ch.TestKind == TestKindTest {
			hasTest = true
		}
	}
	if !hasWant {
		var seen []string
		for _, ch := range chunks {
			seen = append(seen, fmt.Sprintf("(%s,IsTest=%t,TestKind=%q)",
				ch.ChunkType, ch.IsTest, ch.TestKind))
		}
		t.Errorf("%s fixture %s: no chunk classified as TestKind=%q; got %d chunks: %v",
			kind, rel, wantKind, len(chunks), seen)
	}
	if hasTest {
		t.Errorf("%s fixture %s: at least one chunk wrongly classified TestKind=%q; should be %q",
			kind, rel, TestKindTest, wantKind)
	}
}
