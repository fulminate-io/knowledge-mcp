// SPDX-License-Identifier: Apache-2.0

package ast

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// writeReuseCorpus writes a single Go file carrying two calls to f with
// DIFFERENT, non-empty argument lists, and returns the corpus root. The two
// call sites produce two matches of `f($$$ARGS)` in one file — the shape that
// exercises mc.caps/mc.nodes reuse across successful matches within a file.
func writeReuseCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := "package corpus\n\n" +
		"func f(a ...int) int { return len(a) }\n\n" +
		"func use() {\n" +
		"\t_ = f(11, 22)\n" +
		"\t_ = f(33, 44, 55)\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(dir, "reuse.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write corpus file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module corpus\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	return dir
}

// childTexts extracts the verbatim Text of each semantic child of a sequence
// capture, in order.
func childTexts(c Capture) []string {
	out := make([]string, 0, len(c.Children))
	for _, ch := range c.Children {
		out = append(out, ch.Text)
	}
	return out
}

// TestMatch_CaptureReuse_SequenceChildrenSurvive is the named catcher for fix
// #3 (removing the per-match fresh newCaptures()/nodes alloc in tryMatch). Two
// matches of a sequence-capturing pattern in ONE file now reuse mc.caps across
// both successful matches. If that reuse aliased into the first RawMatch's
// Captures — i.e. if the fresh alloc were actually load-bearing — the second
// match's Children would overwrite the first's in place and this test would go
// red. It asserts both matches keep DISTINCT, INTACT sequence Children on both
// the where==nil and the where!=nil (bindAs) reuse paths.
func TestMatch_CaptureReuse_SequenceChildrenSurvive(t *testing.T) {
	dir := writeReuseCorpus(t)
	lang := treesitter.Language("go")
	pat, err := Parse("f($$$ARGS)")
	if err != nil {
		t.Fatalf("parse pattern: %v", err)
	}
	cp, err := Compile(pat, lang, "")
	if err != nil {
		t.Fatalf("compile pattern: %v", err)
	}
	defer cp.Close()
	scope := Scope{Repo: "corpus"}

	// assertBothIntact checks the two matches carry the two distinct, intact
	// argument lists — regardless of the order the walk emitted them.
	assertBothIntact := func(t *testing.T, matches []RawMatch, label string) {
		t.Helper()
		if len(matches) != 2 {
			t.Fatalf("%s: expected 2 matches, got %d", label, len(matches))
		}
		byArgs := map[string][]string{}
		for _, m := range matches {
			args, ok := m.Captures["ARGS"]
			if !ok {
				t.Fatalf("%s: match at %s has no ARGS capture", label, m.FilePath)
			}
			byArgs[args.Text] = childTexts(args)
		}
		want := map[string][]string{
			"11, 22":     {"11", "22"},
			"33, 44, 55": {"33", "44", "55"},
		}
		for text, wantKids := range want {
			gotKids, ok := byArgs[text]
			if !ok {
				keys := make([]string, 0, len(byArgs))
				for k := range byArgs {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				t.Fatalf("%s: no match with ARGS.Text %q; got ARGS texts %v", label, text, keys)
			}
			if len(gotKids) != len(wantKids) {
				t.Fatalf("%s: ARGS %q Children = %v, want %v", label, text, gotKids, wantKids)
			}
			for i := range wantKids {
				if gotKids[i] != wantKids[i] {
					t.Fatalf("%s: ARGS %q Children = %v, want %v (child %d differs)", label, text, gotKids, wantKids, i)
				}
			}
		}
	}

	// where==nil reuse path.
	matches, _, err := Match(context.Background(), dir, lang, cp, nil, scope)
	if err != nil {
		t.Fatalf("Match (where==nil): %v", err)
	}
	assertBothIntact(t, matches, "where==nil")

	// where!=nil reuse path: a contains_pattern with an `as` binding makes
	// tryMatch take the matchTreeWithNodes + evalWhere branch (which builds a
	// withMatchCaptures scope and bindAs-es a node) for both matches.
	where, err := ParseWhere([]byte(`{"contains_pattern":{"of":"$match","pattern":"$_","as":"inner"}}`))
	if err != nil {
		t.Fatalf("ParseWhere: %v", err)
	}
	wmatches, _, err := Match(context.Background(), dir, lang, cp, where, scope)
	if err != nil {
		t.Fatalf("Match (where!=nil): %v", err)
	}
	assertBothIntact(t, wmatches, "where!=nil bindAs")
}

// TestMatch_PlaceholderRoot_AllocsBounded gates fix #3: dropping the per-match
// fresh newCaptures()/nodes alloc on tryMatch's success path. It asserts the
// SAME placeholder-root where==nil Match measurement TestMatch_WhereNil_
// AllocsBounded uses, with a ceiling locked BETWEEN the post-fix-#1 and
// post-fix-#3 values:
//
//	post fix #1 (fresh alloc still present): ~215071 allocs/op
//	post fix #3 (fresh alloc removed):       ~98276 allocs/op
//
// 130000 sits between them: RED against the fix-#1 tree (~215k > 130k), GREEN
// after fix #3 (~98k < 130k). This is fix #3's attributable allocs/op delta.
func TestMatch_PlaceholderRoot_AllocsBounded(t *testing.T) {
	const ceiling = 130000
	got := placeholderRootMatchAllocs(t)
	t.Logf("placeholder-root where==nil Match (post fix #3): %.0f allocs/op (ceiling %d)", got, ceiling)
	if got > ceiling {
		t.Fatalf("placeholder-root Match allocations regressed: %.0f allocs/op > ceiling %d", got, ceiling)
	}
}
