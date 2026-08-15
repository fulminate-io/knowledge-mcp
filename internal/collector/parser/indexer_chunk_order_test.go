// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// orderFixtureLangs is the per-language source template the fixture repo is
// written from. Three languages so the walk exercises more than one grammar,
// and the file bodies are deliberately tiny — this test measures ORDER, not
// chunk quality.
var orderFixtureLangs = []struct {
	ext  string
	body string
}{
	{".go", "package fixture\n\nfunc Fn%d() int { return %d }\n"},
	{".py", "def fn%d():\n    return %d\n"},
	{".ts", "export function fn%d(): number { return %d; }\n"},
}

// orderFixtureFilesPerLang is chosen so the total file count (3 * 9 = 27)
// clears the 24-file floor the step requires AND clears maxChunkWorkers by a
// wide margin, so several workers genuinely race to append.
const orderFixtureFilesPerLang = 9

// writeOrderFixtureRepo writes the fixture repo and returns its directory plus
// the relative file list, in an order deliberately UNSORTED so a walk that
// merely preserved input order could not pass by accident.
func writeOrderFixtureRepo(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var files []string
	// Descending index, and languages interleaved, so the emitted order is not
	// the sorted order.
	for i := orderFixtureFilesPerLang; i >= 1; i-- {
		for _, lang := range orderFixtureLangs {
			rel := filepath.Join(fmt.Sprintf("pkg%d", i%3), fmt.Sprintf("f%02d%s", i, lang.ext))
			abs := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(abs, fmt.Appendf(nil, lang.body, i, i), 0o600); err != nil {
				t.Fatalf("write %s: %v", rel, err)
			}
			files = append(files, rel)
		}
	}
	return dir, files
}

// TestChunkFilesParallelOrderIsDeterministic pins the ordering contract
// ChunkFilesParallel gained: the returned results are sorted by FilePath, and
// therefore identical across runs regardless of which worker finished first.
//
// FIVE runs rather than one: a single run is trivially equal to itself, and
// worker completion order is the OS scheduler's to choose — one run can easily
// come back sorted by luck.
//
// KNOWN-POSITIVE CONTROL: the per-run length is asserted against the
// FIXTURE-DERIVED constant len(files), never against another run's count. All
// five runs agreeing on an empty slice would satisfy "every run is identical"
// perfectly while proving nothing at all.
func TestChunkFilesParallelOrderIsDeterministic(t *testing.T) {
	dir, files := writeOrderFixtureRepo(t)
	if len(files) < 24 {
		t.Fatalf("fixture writes %d files, want at least 24 so the worker pool is genuinely contended", len(files))
	}

	want := append([]string(nil), files...)
	sort.Strings(want)

	const runs = 5
	var first []string
	for run := range runs {
		results, err := ChunkFilesParallel(context.Background(), dir, files)
		if err != nil {
			t.Fatalf("run %d: ChunkFilesParallel: %v", run, err)
		}
		if len(results) != len(files) {
			t.Fatalf("run %d: chunked %d files, want %d (the fixture-derived count)", run, len(results), len(files))
		}

		got := make([]string, len(results))
		for i, r := range results {
			got[i] = r.FilePath
		}

		// Sorted, against the independently-sorted fixture list.
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("run %d: result[%d] = %q, want %q — results are not FilePath-sorted", run, i, got[i], want[i])
			}
		}

		if run == 0 {
			first = got
			continue
		}
		// Identical to run 0, which is the property resolution depends on.
		for i := range got {
			if got[i] != first[i] {
				t.Fatalf("run %d: result[%d] = %q, run 0 had %q — order varies between runs", run, i, got[i], first[i])
			}
		}
	}
}
