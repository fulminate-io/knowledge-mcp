// SPDX-License-Identifier: Apache-2.0

// indexer_discover_prune_test.go — prefix pruning must be a saving, never a
// change of answer.
//
// Pruning moves the prefix test from AFTER discovery to INSIDE it, and the two
// implementations are not the same code: the git path hands the prefixes to
// `git ls-files` as pathspecs and never sees what they exclude, while the walk
// path prunes directories itself. Either could disagree with the in-process
// predicate in a way no caller would notice — a silently narrower result reads
// exactly like a genuine absence of matches. These tests pin the agreement.

package parser

import (
	"path/filepath"
	"sort"
	"testing"
)

// pruneFixture is a tree with the shapes that separate a correct prefix test
// from a naive one: a target directory, a sibling whose NAME EXTENDS it, a
// nested subdirectory, and an unrelated top-level tree.
func pruneFixture(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "pkg", "in.go"), "package pkg")
	writeFile(t, filepath.Join(dir, "pkg", "deep", "nested.go"), "package deep")
	writeFile(t, filepath.Join(dir, "pkgextra", "sibling.go"), "package pkgextra")
	writeFile(t, filepath.Join(dir, "other", "thing.go"), "package other")
	writeFile(t, filepath.Join(dir, "top.go"), "package top")
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// discoverBothWays returns the pruned result and the unpruned-then-filtered
// result for one repo, which is the equality the pruning must preserve.
func discoverBothWays(t *testing.T, dir string, prefixes []string) (pruned, filtered []string) {
	t.Helper()
	pruned, _, err := DiscoverFilesReporting(t.Context(), dir, DiscoveryOptions{PackagePrefixes: prefixes})
	if err != nil {
		t.Fatalf("pruned discovery: %v", err)
	}
	all, _, err := DiscoverFilesReporting(t.Context(), dir, DiscoveryOptions{})
	if err != nil {
		t.Fatalf("unpruned discovery: %v", err)
	}
	for _, rel := range all {
		if MatchesPathPrefixes(rel, prefixes) {
			filtered = append(filtered, rel)
		}
	}
	return sortedCopy(pruned), sortedCopy(filtered)
}

// TestDiscoverFilesPrefixPruneMatchesUnpruned asserts SET equality, not count
// equality: two different sets of the same size would satisfy a count check
// while returning the wrong files.
func TestDiscoverFilesPrefixPruneMatchesUnpruned(t *testing.T) {
	prefixSets := [][]string{
		{"pkg"},
		{"pkg/"},     // the trailing-slash spelling of the same directory
		{"pkg/deep"}, //
		{"pkg", "other"},
		{"pkgextra/sibling.go"}, // a prefix naming one file
		{"nosuchdir"},           // a prefix matching nothing
	}

	for _, gitRepo := range []bool{false, true} {
		name := "walk path"
		if gitRepo {
			name = "git path"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			pruneFixture(t, dir)
			if gitRepo {
				gitInit(t, dir)
			}

			// Known positive: the unpruned walk must actually find the tree,
			// otherwise every equality below holds between two empty sets.
			all, rep, err := DiscoverFilesReporting(t.Context(), dir, DiscoveryOptions{})
			if err != nil {
				t.Fatalf("unpruned discovery: %v", err)
			}
			if len(all) != 5 {
				t.Fatalf("unpruned discovery found %d files, want the fixture's 5: %v", len(all), all)
			}
			wantPath := DiscoveryPathWalk
			if gitRepo {
				wantPath = DiscoveryPathGit
			}
			if rep.DiscoveryPath != wantPath {
				t.Fatalf("discovery path = %q, want %q — the other path's pruning is under test", rep.DiscoveryPath, wantPath)
			}

			for _, prefixes := range prefixSets {
				pruned, filtered := discoverBothWays(t, dir, prefixes)
				if len(pruned) != len(filtered) {
					t.Errorf("prefixes %v: pruned %v, unpruned-then-filtered %v", prefixes, pruned, filtered)
					continue
				}
				for i := range pruned {
					if pruned[i] != filtered[i] {
						t.Errorf("prefixes %v: pruned %v, unpruned-then-filtered %v", prefixes, pruned, filtered)
						break
					}
				}
				// A non-empty expectation for the prefix sets that have one, so
				// an implementation returning nothing at all cannot pass by
				// agreeing with a filter that also returns nothing.
				if len(prefixes) == 1 && prefixes[0] == "nosuchdir" {
					if len(pruned) != 0 {
						t.Errorf("prefixes %v matched %v, want nothing", prefixes, pruned)
					}
					continue
				}
				if len(pruned) == 0 {
					t.Errorf("prefixes %v matched nothing — the fixture has files under every one", prefixes)
				}
			}
		})
	}
}

// TestDiscoverFilesPrefixPruneRespectsSegmentBoundary is the discriminating
// case: sibling directories pkg and pkgextra, where a bare string-prefix test
// admits the sibling and a segment-boundary test does not. The git pathspec and
// the walk's own prune must both land on the segment-boundary answer, since a
// scoped result that depended on whether the tree was a git repo would be worse
// than either behavior alone.
func TestDiscoverFilesPrefixPruneRespectsSegmentBoundary(t *testing.T) {
	for _, gitRepo := range []bool{false, true} {
		name := "walk path"
		if gitRepo {
			name = "git path"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			pruneFixture(t, dir)
			if gitRepo {
				gitInit(t, dir)
			}

			got, _, err := DiscoverFilesReporting(t.Context(), dir, DiscoveryOptions{
				PackagePrefixes: []string{"pkg"},
			})
			if err != nil {
				t.Fatalf("DiscoverFilesReporting: %v", err)
			}
			want := []string{filepath.Join("pkg", "deep", "nested.go"), filepath.Join("pkg", "in.go")}
			got = sortedCopy(got)
			if len(got) != len(want) {
				t.Fatalf("prefix pkg matched %v, want %v — a sibling that merely extends the name is not under it", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("prefix pkg matched %v, want %v", got, want)
				}
			}

			// Known positive through the same probe: the sibling IS reachable
			// under its own name, so the boundary rule is not satisfied by
			// matching less.
			sib, _, err := DiscoverFilesReporting(t.Context(), dir, DiscoveryOptions{
				PackagePrefixes: []string{"pkgextra"},
			})
			if err != nil {
				t.Fatalf("DiscoverFilesReporting: %v", err)
			}
			if len(sib) != 1 || sib[0] != filepath.Join("pkgextra", "sibling.go") {
				t.Fatalf("prefix pkgextra matched %v, want just its own file", sib)
			}

			// Both spellings of a directory agree. "pkg/" is how callers
			// commonly write it, and a boundary test that appends its own
			// separator turns it into "pkg//" and matches nothing — a silent
			// narrowing that reads exactly like an empty directory.
			slashed, _, err := DiscoverFilesReporting(t.Context(), dir, DiscoveryOptions{
				PackagePrefixes: []string{"pkg/"},
			})
			if err != nil {
				t.Fatalf("DiscoverFilesReporting: %v", err)
			}
			slashed = sortedCopy(slashed)
			if len(slashed) != len(want) {
				t.Fatalf("prefix \"pkg/\" matched %v, want the same %v as \"pkg\"", slashed, want)
			}
			for i := range want {
				if slashed[i] != want[i] {
					t.Fatalf("prefix \"pkg/\" matched %v, want %v", slashed, want)
				}
			}

			// And a prefix naming a single file resolves to that file, which the
			// directory-level prune alone would miss.
			one, _, err := DiscoverFilesReporting(t.Context(), dir, DiscoveryOptions{
				PackagePrefixes: []string{filepath.Join("pkg", "in.go")},
			})
			if err != nil {
				t.Fatalf("DiscoverFilesReporting: %v", err)
			}
			if len(one) != 1 || one[0] != filepath.Join("pkg", "in.go") {
				t.Fatalf("file prefix matched %v, want just pkg/in.go", one)
			}
		})
	}
}
