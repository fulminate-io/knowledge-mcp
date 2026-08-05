// SPDX-License-Identifier: Apache-2.0

// count_test.go — package-level tests for the body-free count path
// (countTally + Count). Handler-path parity and the wire gate live in
// cmd/knowledge/internal/tools/ast_count_wire_test.go.

package ast

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// TestCountTally_ConcurrentRecordsAreRaceFree drives the PRODUCTION accumulator
// directly from many goroutines — the same convention
// parse_health_test.go:TestSkipCounters_EachReasonIncrementsItsOwnAndTheSum
// uses for walkCounters — and asserts the merged totals against
// fixture-derived constants. Under -race it is the catcher for unsynchronized
// map access; the non-zero constant assertions are the known-positive control
// that keeps a probe pointed at nothing from passing.
func TestCountTally_ConcurrentRecordsAreRaceFree(t *testing.T) {
	const (
		workers        = 8
		filesPerWorker = 50
		perFileTotal   = 3
		callsPerFile   = 2 // "call_expression"
		emptyPerFile   = 1 // placeholder-rooted empty kind
	)
	tally := newCountTally()

	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for f := range filesPerWorker {
				path := fmt.Sprintf("w%d/f%d.go", w, f)
				tally.record(path, perFileTotal, map[string]int{
					"call_expression": callsPerFile,
					"":                emptyPerFile,
				})
			}
		})
	}
	wg.Wait()

	var got CountTally
	tally.applyTo(&got)

	files := workers * filesPerWorker
	assert.Equal(t, files*perFileTotal, got.Total, "total must sum every recorded file")
	assert.Len(t, got.ByFile, files, "every distinct path must appear once")
	assert.Equal(t, files*callsPerFile, got.ByKind["call_expression"], "per-kind counts must sum across goroutines")
	assert.Equal(t, files*emptyPerFile, got.ByKind[""], "the empty CompiledKind key must survive the merge")
}

// runBothOver compiles pattern once and runs Match then Count over dir with the
// same CompiledPattern (both only read it), returning both results so a subtest
// can assert Count reproduces Match's numbers over the identical walk.
func runBothOver(t *testing.T, dir, pattern string) ([]RawMatch, WalkStats, CountTally, WalkStats) {
	t.Helper()
	lang := treesitter.Language("go")
	pat, err := Parse(pattern)
	require.NoError(t, err)
	cp, err := Compile(pat, lang, "")
	require.NoError(t, err)
	defer cp.Close()
	raws, mw, merr := Match(context.Background(), dir, lang, cp, nil, Scope{})
	require.NoError(t, merr)
	ct, cw, cerr := Count(context.Background(), dir, lang, cp, nil, Scope{})
	require.NoError(t, cerr)
	return raws, mw, ct, cw
}

// TestCount_TotalsAndMapsMatchTheMatchWalk pins that Count reproduces the match
// walk's numbers exactly — the same total, the same by_file and by_kind maps
// (including the empty-string kind a placeholder-rooted pattern binds), the
// additive property countAll relies on across alternation members, and every
// WalkStats field bar DurationMS.
func TestCount_TotalsAndMapsMatchTheMatchWalk(t *testing.T) {
	dir := benchCountCorpus(t)

	t.Run("totals", func(t *testing.T) {
		raws, _, ct, _ := runBothOver(t, dir, "$_")
		require.NotEmpty(t, raws, "corpus must produce matches or the parity is vacuous")
		assert.Equal(t, len(raws), ct.Total)
	})

	t.Run("by_file", func(t *testing.T) {
		raws, _, ct, _ := runBothOver(t, dir, "$_")
		want := map[string]int{}
		for _, r := range raws {
			want[r.FilePath]++
		}
		require.NotEmpty(t, want)
		assert.Equal(t, want, ct.ByFile)
	})

	t.Run("by_kind_empty_key", func(t *testing.T) {
		raws, _, ct, _ := runBothOver(t, dir, "$_")
		want := map[string]int{}
		for _, r := range raws {
			want[r.CompiledKind]++
		}
		_, hasEmpty := want[""]
		require.True(t, hasEmpty, "placeholder-rooted $_ must bind an empty CompiledKind or this guard is vacuous")
		assert.Equal(t, want, ct.ByKind)
		assert.Positive(t, ct.ByKind[""], "the empty-string kind count must survive the tally")
	})

	t.Run("alternation_sums", func(t *testing.T) {
		// The additive property countAll relies on: summing two Count tallies
		// equals summing two Match result sets, total and key for key.
		rawsA, _, ctA, _ := runBothOver(t, dir, "$_")
		rawsB, _, ctB, _ := runBothOver(t, dir, "func $N($$$A) { $$$B }")
		require.NotEmpty(t, rawsB, "the second arm must match something or the sum is trivial")

		assert.Equal(t, len(rawsA)+len(rawsB), ctA.Total+ctB.Total)

		wantByFile := map[string]int{}
		for _, r := range rawsA {
			wantByFile[r.FilePath]++
		}
		for _, r := range rawsB {
			wantByFile[r.FilePath]++
		}
		gotByFile := map[string]int{}
		for k, v := range ctA.ByFile {
			gotByFile[k] += v
		}
		for k, v := range ctB.ByFile {
			gotByFile[k] += v
		}
		assert.Equal(t, wantByFile, gotByFile)
	})

	t.Run("walk_stats_fields", func(t *testing.T) {
		// Struct-level comparison with DurationMS zeroed on both sides, so a
		// field ADDED to WalkStats later is covered automatically rather than
		// silently escaping the gate.
		_, mw, _, cw := runBothOver(t, dir, "$_")
		mw.DurationMS = 0
		cw.DurationMS = 0
		assert.Equal(t, mw, cw, "Count's WalkStats must match Match's field-for-field (DurationMS aside)")
	})
}

// countRetentionCorpus writes 50 identical Go files of stmtsPerFile statements
// each under a temp dir, so a placeholder-rooted `$_` count over it yields a
// match count linear in stmtsPerFile at a FIXED file count — the knob the
// retention test turns to hold files constant while scaling matches ~10x.
func countRetentionCorpus(tb testing.TB, stmtsPerFile int) string {
	tb.Helper()
	dir := tb.TempDir()
	var b strings.Builder
	b.WriteString("package corpus\n\nfunc f() {\n")
	for i := range stmtsPerFile {
		fmt.Fprintf(&b, "\tx%d := %d + %d\n", i, i, i)
	}
	b.WriteString("}\n")
	content := []byte(b.String())
	for fi := range 50 {
		require.NoError(tb, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d.go", fi)), content, 0o600))
	}
	require.NoError(tb, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module corpus\n\ngo 1.21\n"), 0o600))
	return dir
}

// heapDelta reports the live HeapAlloc growth across fn, holding fn's returned
// value live past the second reading via runtime.KeepAlive. Mirrors the
// GC+ReadMemStats differential idiom of
// cmd/knowledge-server/internal/store/edge_bundle_bulk_test.go (a different
// module, so the idiom is mirrored, not imported).
func heapDelta(fn func() any) uint64 {
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	held := fn()
	runtime.GC()
	runtime.ReadMemStats(&m1)
	runtime.KeepAlive(held)
	if m1.HeapAlloc < m0.HeapAlloc {
		return 0
	}
	return m1.HeapAlloc - m0.HeapAlloc
}

// TestCount_RetainsNoMatchBodies pins that Count's peak retention is flat in
// match count (O(files) plus a per-worker largest-file transient) while Match's
// scales with match count. Two corpus points share the SAME file count and
// differ ~10x in matches, so a fixed-ratio pass cannot be faked by an
// accumulator that is merely smaller-but-still-O(matches): Count must stay flat
// across the jump, and the Match arm — which must, and does, scale — is the
// known-positive control that the probe measures real bytes at all.
func TestCount_RetainsNoMatchBodies(t *testing.T) {
	lang := treesitter.Language("go")
	baseDir := countRetentionCorpus(t, 20)   // ~200 matches/file
	denseDir := countRetentionCorpus(t, 200) // ~2,000 matches/file, same 50 files
	pat, err := Parse("$_")
	require.NoError(t, err)

	compile := func() *CompiledPattern {
		cp, cerr := Compile(pat, lang, "")
		require.NoError(t, cerr)
		return cp
	}
	matchArm := func(dir string) (uint64, int) {
		cp := compile()
		defer cp.Close()
		var n int
		d := heapDelta(func() any {
			raws, _, e := Match(context.Background(), dir, lang, cp, nil, Scope{})
			require.NoError(t, e)
			n = len(raws)
			return raws
		})
		return d, n
	}
	countArm := func(dir string) (uint64, int) {
		cp := compile()
		defer cp.Close()
		var n int
		d := heapDelta(func() any {
			ct, _, e := Count(context.Background(), dir, lang, cp, nil, Scope{})
			require.NoError(t, e)
			n = ct.Total
			return ct
		})
		return d, n
	}

	matchBase, mBaseN := matchArm(baseDir)
	matchDense, mDenseN := matchArm(denseDir)
	countBase, cBaseN := countArm(baseDir)
	countDense, cDenseN := countArm(denseDir)

	require.Equal(t, mBaseN, cBaseN, "Count and Match must agree on the base total")
	require.Equal(t, mDenseN, cDenseN, "Count and Match must agree on the dense total")
	require.InDelta(t, 10.0, float64(cDenseN)/float64(cBaseN), 3.0,
		"the dense corpus must carry ~10x matches at fixed file count (base=%d dense=%d)", cBaseN, cDenseN)

	t.Run("base_point", func(t *testing.T) {
		// Known-positive control: the Match arm must retain real bytes, or a
		// probe pointed at nothing could report a flat Count as a pass.
		assert.Greater(t, matchBase, uint64(1<<20),
			"Match must retain >1MB at the base point (control that the probe measures something); got %d", matchBase)
	})

	t.Run("match_arm_scales", func(t *testing.T) {
		// The O(matches) control: 10x the matches retains far more.
		assert.Greater(t, float64(matchDense), 5.0*float64(matchBase),
			"Match retention must grow with match count: base=%d dense=%d", matchBase, matchDense)
	})

	t.Run("ten_x_matches_same_files", func(t *testing.T) {
		// Count retention must stay within a small constant factor across the
		// 10x match jump at fixed file count — flat in match count. The +1MB
		// slack absorbs measurement noise on two near-constant readings.
		assert.Less(t, float64(countDense), 3.0*float64(countBase)+float64(1<<20),
			"Count retention must stay flat in match count (O(files)): base=%d dense=%d", countBase, countDense)
	})
}
