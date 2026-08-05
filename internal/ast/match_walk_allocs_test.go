// SPDX-License-Identifier: Apache-2.0

package ast

import (
	"context"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// placeholderRootMatchAllocs measures allocations per ast.Match call over
// benchCountCorpus with a placeholder-rooted `$_` and where==nil — the dominant
// alloc-storm shape. It is the shared probe behind the two AllocsBounded gates
// on this path (fix #1's TestMatch_WhereNil_AllocsBounded and fix #3's
// TestMatch_PlaceholderRoot_AllocsBounded), so both assert against the exact
// same measurement at their respective fix stages.
func placeholderRootMatchAllocs(t *testing.T) float64 {
	t.Helper()
	dir := benchCountCorpus(t)
	lang := treesitter.Language("go")
	pat, err := Parse("$_")
	if err != nil {
		t.Fatalf("parse pattern: %v", err)
	}
	cp, err := Compile(pat, lang, "")
	if err != nil {
		t.Fatalf("compile pattern: %v", err)
	}
	defer cp.Close()
	scope := Scope{Repo: "corpus"}
	ctx := context.Background()
	return testing.AllocsPerRun(3, func() {
		if _, _, err := Match(ctx, dir, lang, cp, nil, scope); err != nil {
			t.Fatalf("Match: %v", err)
		}
	})
}

// placeholderRootCountAllocs measures allocations per ast.Count call over the
// same placeholder-rooted `$_` corpus the Match probe uses — the light-match
// counterpart, isolating the count path's per-match cost.
func placeholderRootCountAllocs(t *testing.T) float64 {
	t.Helper()
	dir := benchCountCorpus(t)
	lang := treesitter.Language("go")
	pat, err := Parse("$_")
	if err != nil {
		t.Fatalf("parse pattern: %v", err)
	}
	cp, err := Compile(pat, lang, "")
	if err != nil {
		t.Fatalf("compile pattern: %v", err)
	}
	defer cp.Close()
	scope := Scope{Repo: "corpus"}
	ctx := context.Background()
	return testing.AllocsPerRun(3, func() {
		if _, _, err := Count(ctx, dir, lang, cp, nil, scope); err != nil {
			t.Fatalf("Count: %v", err)
		}
	})
}

// TestCount_LightMatch_AllocsBounded gates fix #2 (the count light-match path):
// count must stop building a RawMatch per match. It asserts the SAME
// placeholder-root Count measurement, with a TIGHT ceiling just above the
// measured post-fix value — mirroring the two sibling Match gates, which lock
// their ceiling between the pre- and post-fix numbers rather than leaving loose
// headroom on the largest fix:
//
//	pre-fix (count builds every RawMatch): ~289167 allocs/op
//	post-fix (body-free light match):      ~24466 allocs/op
//
// 30000 sits just above post-fix and an order of magnitude below pre-fix: RED
// against the RawMatch-building count (~289k > 30k), GREEN after the light path
// (~24k < 30k). Count is the biggest fix; a loose gate here would let a partial
// regression pass green.
func TestCount_LightMatch_AllocsBounded(t *testing.T) {
	const ceiling = 30000
	got := placeholderRootCountAllocs(t)
	t.Logf("placeholder-root Count (light match): %.0f allocs/op (ceiling %d)", got, ceiling)
	if got > ceiling {
		t.Fatalf("count light-match allocations regressed: %.0f allocs/op > ceiling %d", got, ceiling)
	}
}

// TestMatch_WhereNil_AllocsBounded gates fix #1: on the where==nil placeholder
// path, tryMatch must delegate to matchTree, skipping the dead $match
// nodeToCapture Content copy and the findNodeBySpan nodes-population loop that
// matchTreeWithNodes does. The ceiling is locked BETWEEN the measured pre-fix
// and post-fix allocs/op:
//
//	pre-fix (matchTreeWithNodes unconditionally): ~289040 allocs/op
//	post-fix (matchTree on where==nil):           ~215155 allocs/op
//
// 230000 sits between them, so this assertion goes RED against the unfixed tree
// (~289k > 230k) and GREEN after the fix (~215k < 230k) — a genuine gate, not a
// vacuous ceiling. Fix #3 tightens the same measurement further in
// TestMatch_PlaceholderRoot_AllocsBounded.
func TestMatch_WhereNil_AllocsBounded(t *testing.T) {
	const ceiling = 230000
	got := placeholderRootMatchAllocs(t)
	t.Logf("placeholder-root where==nil Match: %.0f allocs/op (ceiling %d)", got, ceiling)
	if got > ceiling {
		t.Fatalf("where==nil Match allocations regressed: %.0f allocs/op > ceiling %d", got, ceiling)
	}
}
