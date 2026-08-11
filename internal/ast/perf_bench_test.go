// SPDX-License-Identifier: Apache-2.0

package ast

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// perf_bench_test.go holds the two benchmarks the ast perf work uses to
// produce numbers for the record — NOT gates. A `go test -bench` invocation
// exits 0 when its selector matches nothing and prints no `--- PASS:` line to
// anchor, so a benchmark cannot be an honest gate; the gates elsewhere are
// allocation and retention assertions instead. The proof these benchmarks
// actually RAN is testdata/perf_baseline.txt: a benchmark that never ran
// contributes no allocs/op line to record. Both call b.ReportAllocs().

// benchSpliceCorpus builds a ~4,000-line synthetic buffer carrying 400 disjoint
// token spans and returns it alongside 400 fileEdit values over those spans,
// DESC-sorted by Start and pairwise non-overlapping — the exact shape
// buildFileEdits guarantees reaches applyEditsToSource. spliceEdits runs no
// re-parse, so the buffer need not be valid Go; only the byte spans matter.
func benchSpliceCorpus() ([]byte, []fileEdit) {
	const edits = 400
	var b strings.Builder
	type span struct{ start, end int }
	spans := make([]span, 0, edits)
	for i := range edits {
		// ~9 filler lines per edit lands the buffer around 4,000 lines.
		for range 9 {
			b.WriteString("// filler filler filler filler filler\n")
		}
		b.WriteString("token_")
		start := b.Len()
		fmt.Fprintf(&b, "%08d", i)
		end := b.Len()
		b.WriteString(" = 0\n")
		spans = append(spans, span{start: start, end: end})
	}
	src := []byte(b.String())
	// spans are in ascending Start order; walk them in reverse to emit the
	// DESC-by-Start slice the right-to-left splice consumes.
	fe := make([]fileEdit, 0, len(spans))
	for _, v := range slices.Backward(spans) {
		fe = append(fe, fileEdit{
			Start:       uint32(v.start),
			End:         uint32(v.end),
			Replacement: "REPLACED",
		})
	}
	return src, fe
}

// BenchmarkSpliceEdits_ManyEdits measures the splice-assembly loop in
// isolation — NOT applyEditsToSource, whose re-parse gate would dominate and
// hide the thing being measured. The BEFORE number is taken against the
// right-to-left whole-buffer-rebuild shape extracted in Phase 1; Phase 2
// rewrites the same function to a single forward pass and re-measures here.
func BenchmarkSpliceEdits_ManyEdits(b *testing.B) {
	src, edits := benchSpliceCorpus()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := spliceEdits(src, edits); err != nil {
			b.Fatalf("spliceEdits: %v", err)
		}
	}
}

// benchCountCorpus writes 40 small Go files under a temp dir, each carrying a
// few hundred AST nodes so a placeholder-rooted `$_` count yields roughly 200
// matches per file. Returns the corpus root.
func benchCountCorpus(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	var b strings.Builder
	b.WriteString("package corpus\n\nfunc f() {\n")
	for i := range 40 {
		fmt.Fprintf(&b, "\tx%d := %d + %d\n\t_ = x%d\n", i, i, i, i)
	}
	b.WriteString("}\n")
	content := []byte(b.String())
	for fi := range 40 {
		path := filepath.Join(dir, fmt.Sprintf("file%02d.go", fi))
		if err := os.WriteFile(path, content, 0o600); err != nil {
			tb.Fatalf("write corpus file: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module corpus\n\ngo 1.21\n"), 0o600); err != nil {
		tb.Fatalf("write go.mod: %v", err)
	}
	return dir
}

// BenchmarkCountWalk_PlaceholderRoot measures the count path over a fixed-file,
// many-match corpus. Phase 3 re-pointed its body from ast.Match to ast.Count:
// it now measures the AFTER shape — the body-free walk that retains only the
// per-file tally rather than every RawMatch — which is the whole point of the
// count path change. The BEFORE numbers (against the retaining Match walk) are
// recorded in testdata/perf_baseline.txt.
func BenchmarkCountWalk_PlaceholderRoot(b *testing.B) {
	dir := benchCountCorpus(b)
	lang := treesitter.Language("go")
	pat, err := Parse("$_")
	if err != nil {
		b.Fatalf("parse pattern: %v", err)
	}
	cp, err := Compile(pat, lang, "")
	if err != nil {
		b.Fatalf("compile pattern: %v", err)
	}
	defer cp.Close()
	scope := Scope{Repo: "corpus"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := Count(context.Background(), dir, lang, cp, nil, scope); err != nil {
			b.Fatalf("Count: %v", err)
		}
	}
}

// BenchmarkMatchWalk_PlaceholderRoot measures the MATCH path over the same
// fixed-file, many-match corpus BenchmarkCountWalk_PlaceholderRoot uses, with a
// placeholder-rooted `$_` and where==nil. It is the record benchmark for the
// alloc-storm fixes to the placeholder-rooted walk: fix #1 skips the dead
// $match/nodes population on the where==nil path, fix #3 drops the per-match
// fresh capture allocation. Both attribute their allocs/op delta here; the gates
// themselves are the AllocsBounded assertions (record-only benchmark, per the
// file convention above). It reuses benchCountCorpus verbatim.
func BenchmarkMatchWalk_PlaceholderRoot(b *testing.B) {
	dir := benchCountCorpus(b)
	lang := treesitter.Language("go")
	pat, err := Parse("$_")
	if err != nil {
		b.Fatalf("parse pattern: %v", err)
	}
	cp, err := Compile(pat, lang, "")
	if err != nil {
		b.Fatalf("compile pattern: %v", err)
	}
	defer cp.Close()
	scope := Scope{Repo: "corpus"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := Match(context.Background(), dir, lang, cp, nil, scope); err != nil {
			b.Fatalf("Match: %v", err)
		}
	}
}

// benchJSXCorpus writes 40 small .tsx files under a temp dir, each carrying 40
// multi-line JSX elements in JSX child position — the shape whose leading
// anonymous token absorbs the preceding newline. Returns the corpus root.
//
// It is a SEPARATE corpus from benchCountCorpus because that one is Go, and a
// tsx walk over Go files scans nothing.
func benchJSXCorpus(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	var b strings.Builder
	b.WriteString("export function App() {\n  return <div>\n")
	for i := range 40 {
		fmt.Fprintf(&b, "    <CodeBlock code={sample%d} />\n", i)
	}
	b.WriteString("  </div>;\n}\n")
	content := []byte(b.String())
	for fi := range 40 {
		path := filepath.Join(dir, fmt.Sprintf("file%02d.tsx", fi))
		if err := os.WriteFile(path, content, 0o600); err != nil {
			tb.Fatalf("write corpus file: %v", err)
		}
	}
	return dir
}

// BenchmarkMatchWalk_JSXConcreteRoot measures the MATCH path with a
// CONCRETE-ROOTED JSX pattern over a multi-line tsx corpus. It is the record
// benchmark for the JSX token-comparison work, and its pre-fix reading is what
// that work's allocation ceiling is measured against.
//
// WHY NOT REUSE BenchmarkMatchWalk_PlaceholderRoot: that benchmark cannot reach
// the childless-token comparison at all. Its `$_` pattern returns at the
// node-wildcard arm before any child alignment or leaf comparison runs, and its
// corpus is Go. It would report identical allocations whether or not a
// token-text comparison allocates, so a ceiling built on it is green in every
// state of the code.
//
// The corpus deliberately holds the shape that does NOT match before the fix.
// The walk and its leaf comparisons execute either way — a comparison that
// REJECTS costs the same work as one that accepts, minus the match bookkeeping —
// so a pre-fix reading measures the same walk without the trim, which is exactly
// the ceiling a trim regression must be caught against.
func BenchmarkMatchWalk_JSXConcreteRoot(b *testing.B) {
	dir := benchJSXCorpus(b)
	lang := treesitter.LangTSX
	pat, err := Parse("<CodeBlock code={$C} />")
	if err != nil {
		b.Fatalf("parse pattern: %v", err)
	}
	cp, err := Compile(pat, lang, "")
	if err != nil {
		b.Fatalf("compile pattern: %v", err)
	}
	defer cp.Close()
	scope := Scope{Repo: "corpus"}

	// RUN PROOF, not a match assertion: before the fix the correct match count
	// over this corpus is zero, so asserting matches would assert the defect.
	// Asserting the walk reached its files distinguishes "measured a walk" from
	// "measured an empty directory", which would otherwise report a fast,
	// low-alloc, entirely meaningless baseline.
	_, stats, err := Match(context.Background(), dir, lang, cp, nil, scope)
	if err != nil {
		b.Fatalf("Match: %v", err)
	}
	if stats.FilesScanned == 0 {
		b.Fatalf("corpus walk scanned no files; the baseline would measure nothing")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := Match(context.Background(), dir, lang, cp, nil, scope); err != nil {
			b.Fatalf("Match: %v", err)
		}
	}
}

// BenchmarkMatchWalk_JSXNoMatch is the GATED arm for the JSX token-comparison
// work, and the only one of the two that can bound an allocation ceiling.
//
// WHY A SECOND JSX BENCHMARK. A before/after ceiling is only meaningful when
// both runs perform the SAME work. The pre-fix JSX baseline was taken over a walk
// that produced ZERO matches — nothing matching was the defect — so comparing a
// post-fix MATCHING run against it compares thousands of materialized matches
// against none, and goes red precisely because the fix worked. This arm produces
// zero matches in BOTH states, so the two sides differ only by the trim.
//
// It is concrete-rooted for the same reason BenchmarkMatchWalk_JSXConcreteRoot
// is: a placeholder root returns at the node-wildcard arm before any leaf
// comparison and would measure nothing about this path. The element name simply
// does not occur in the corpus, so every candidate is rejected — after the
// comparison the trim governs has already run on its leading token.
//
// EXPECT THE FIGURE TO SIT SLIGHTLY ABOVE THE PRE-FIX BASELINE. Once the leading
// token compares equal, each candidate proceeds one child deeper before the name
// rejects it, which is legitimate work the defect used to skip — not the trim
// allocating. The trim itself allocates nothing: it reslices.
func BenchmarkMatchWalk_JSXNoMatch(b *testing.B) {
	dir := benchJSXCorpus(b)
	lang := treesitter.LangTSX
	pat, err := Parse("<NoSuchElement code={$C} />")
	if err != nil {
		b.Fatalf("parse pattern: %v", err)
	}
	cp, err := Compile(pat, lang, "")
	if err != nil {
		b.Fatalf("compile pattern: %v", err)
	}
	defer cp.Close()
	scope := Scope{Repo: "corpus"}

	// The arm's defining property, asserted rather than assumed: it must scan
	// the corpus and match nothing. A version of this that started matching
	// would silently reintroduce the cross-boundary comparison this benchmark
	// exists to avoid.
	raws, stats, err := Match(context.Background(), dir, lang, cp, nil, scope)
	if err != nil {
		b.Fatalf("Match: %v", err)
	}
	if stats.FilesScanned == 0 {
		b.Fatalf("corpus walk scanned no files; the reading would measure nothing")
	}
	if len(raws) != 0 {
		b.Fatalf("no-match arm matched %d time(s); it must match nothing in both states or it cannot bound a ceiling", len(raws))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := Match(context.Background(), dir, lang, cp, nil, scope); err != nil {
			b.Fatalf("Match: %v", err)
		}
	}
}
