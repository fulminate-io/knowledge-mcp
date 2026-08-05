// SPDX-License-Identifier: Apache-2.0

// alignment_test.go — the speculation discipline for the two pattern-side
// records: the literal-token alignment, and the spans a promotion dropped.
//
// WHAT CAN GO WRONG AND WHY IT IS INVISIBLE. The accumulator is append-only
// during a match attempt. Two paths abandon a partial attempt: reset, once per
// candidate node, and matchSeqShadow's greedy backoff, once per rejected k. An
// entry left behind by either one still points INSIDE the final matched span,
// so a bounds check cannot see it. What it does break is the record's meaning:
// one literal pattern token would map to two different source ranges, and a
// consumer walking the alignment positionally would emit the abandoned try's
// bytes.
//
// So the assertion is functionality, not bounds: every pattern token appears
// at most once, entries ascend in pattern order, and the mapping is checked by
// rendering both sides back to source text. The bounds check is kept as the
// weaker companion.
//
// THE FIXTURE FORCES A BACKOFF THAT APPENDS, WHICH TOOK SOME DOING. A seq
// shadow followed by ONE pattern sibling fails every rejected try on its first
// comparison and appends nothing, so the obvious fixture cannot catch a missing
// rollback at all. The shadow here is followed by four siblings and the target
// repeats the trailing anchor, so the greedy pass reaches a k where the leading
// comma and then a placeholder match — appending an entry — before the sibling
// after them mismatches. The successful alignment is found two steps further
// down, which is what leaves the abandoned entry visible in the final record.
//
// THE DROPPED-SPAN ROLLBACK IS THE SAME DISCIPLINE ON A HARSHER FAILURE. A
// dropped span left behind by a rejected try tells the splice a template token
// repeats a pattern token nothing actually dropped — and the splice's answer to
// that is to DELETE the token. Its fixture needs a rejected try that reaches a
// nested promotion before failing, which the else-clause target below produces:
// the nested `if ($D) { $$$B; }` aligns far enough to promote its body and
// record the wrapper's `;`, then fails because the target carries an else the
// pattern does not.

package ast

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// backtrackPattern's seq shadow is followed by four siblings, so a rejected
// greedy try can align some of them before the rest fails to fit.
const backtrackPattern = "handler($$$A, $Z, tail)"

// backtrackTarget repeats `tail` so the greedy pass reaches an alignment that
// looks right for two siblings and then runs out of arguments.
const backtrackTarget = "package p\n" +
	"func f() { handler(a, b, tail, c, d, tail) }\n"

// alignEntry renders one TokenAlign back to the text on both sides, which is
// what makes a mismatched mapping readable in a failure message.
type alignEntry struct {
	patText string
	srcText string
	align   TokenAlign
}

// renderAligns projects the accumulator onto both source buffers.
func renderAligns(caps *Captures, patSrc, src []byte) []alignEntry {
	out := make([]alignEntry, 0, len(caps.aligns))
	for _, a := range caps.aligns {
		out = append(out, alignEntry{
			patText: string(patSrc[a.PatStart:a.PatEnd]),
			srcText: string(src[a.SrcStart:a.SrcEnd]),
			align:   a,
		})
	}
	return out
}

// matchBacktrackFixture compiles backtrackPattern, walks backtrackTarget, and
// returns the Captures plus the outer node of the first match.
func matchBacktrackFixture(t *testing.T) (*Captures, *sitter.Node, []byte, []byte, func()) {
	t.Helper()
	pt, err := compilePattern(context.Background(), mustParse(t, backtrackPattern), goLangConfig)
	if err != nil {
		t.Fatalf("compilePattern(%q): %v", backtrackPattern, err)
	}
	parser := treesitter.NewParser()
	tree, err := parser.Parse(context.Background(), []byte(backtrackTarget), treesitter.LangGo)
	if err != nil {
		pt.Close()
		parser.Close()
		t.Fatalf("parse target: %v", err)
	}
	cleanup := func() {
		tree.Close()
		parser.Close()
		pt.Close()
	}

	src := []byte(backtrackTarget)
	caps := newCaptures()
	var outer *sitter.Node
	walkAll(tree.RootNode(), func(n *sitter.Node) {
		if outer != nil {
			return
		}
		caps.reset()
		if matchTree(pt, n, src, caps) {
			outer = n
		}
	})
	if outer == nil {
		cleanup()
		t.Fatalf("fixture did not match — the rollback cannot be exercised without a successful backtracking match")
	}
	return caps, outer, []byte(pt.SubstitutedSource), src, cleanup
}

func TestAlignmentRollback(t *testing.T) {
	t.Run("no_speculative_entry_survives_a_rejected_try", func(t *testing.T) {
		caps, outer, patSrc, src, cleanup := matchBacktrackFixture(t)
		defer cleanup()

		entries := renderAligns(caps, patSrc, src)
		if len(entries) == 0 {
			t.Fatalf("alignment is empty — a pattern with six literal tokens must record them, " +
				"and an empty record would satisfy every check below vacuously")
		}

		// Known positive: the mapping is real, not a slice of coincidences.
		wantPairs := map[string]string{"handler": "handler", "tail": "tail", "(": "(", ")": ")"}
		got := map[string]string{}
		for _, e := range entries {
			got[e.patText] = e.srcText
		}
		for pat, want := range wantPairs {
			if got[pat] != want {
				t.Errorf("pattern token %q aligned to %q, want %q (all entries: %+v)", pat, got[pat], want, entries)
			}
		}

		// The record must be a FUNCTION of pattern position. This is the check
		// that fails when a rejected greedy try leaves its comma behind.
		seen := map[uint32]TokenAlign{}
		for _, e := range entries {
			if prev, dup := seen[e.align.PatStart]; dup {
				t.Errorf("pattern token %q at PatStart=%d aligned to TWO source ranges "+
					"([%d,%d) and [%d,%d)) — a speculative entry from a rejected seq-shadow try survived",
					e.patText, e.align.PatStart, prev.SrcStart, prev.SrcEnd, e.align.SrcStart, e.align.SrcEnd)
			}
			seen[e.align.PatStart] = e.align
		}

		// Ordering: the walker descends the pattern left to right, so a record
		// that is out of order is a record that has an entry from elsewhere.
		for i := 1; i < len(entries); i++ {
			if entries[i].align.PatStart <= entries[i-1].align.PatStart {
				t.Errorf("entry %d (%q, PatStart=%d) does not follow entry %d (%q, PatStart=%d)",
					i, entries[i].patText, entries[i].align.PatStart,
					i-1, entries[i-1].patText, entries[i-1].align.PatStart)
			}
		}

		// Bounds: nothing outside the final matched span.
		for _, e := range entries {
			if e.align.SrcStart < outer.StartByte() || e.align.SrcEnd > outer.EndByte() {
				t.Errorf("entry %q maps to [%d,%d), outside the matched span [%d,%d)",
					e.patText, e.align.SrcStart, e.align.SrcEnd, outer.StartByte(), outer.EndByte())
			}
		}
	})

	t.Run("reset_truncates_between_candidates", func(t *testing.T) {
		caps, _, _, _, cleanup := matchBacktrackFixture(t)
		defer cleanup()

		if len(caps.aligns) == 0 {
			t.Fatalf("fixture recorded no alignment; the truncation check would be vacuous")
		}
		caps.reset()
		if len(caps.aligns) != 0 {
			t.Errorf("reset left %d alignment entries; a leak here corrupts the NEXT candidate's record", len(caps.aligns))
		}
	})
}

// dropRollbackPattern nests one promotable sequence position inside another, so
// a rejected try of the outer one can reach the inner promotion before failing.
const dropRollbackPattern = "if ($C) { $$$A; if ($D) { $$$B; } }"

// dropRollbackElseTarget carries an else clause the pattern does not, so the
// nested if fails AFTER its body promoted and recorded the wrapper's `;`. No
// candidate node matches — the record must not outlive the try that made it.
const dropRollbackElseTarget = "void f(int c, int w) {\n" +
	"    if (c) {\n        if (w) { m(); } else { n(); }\n    }\n}\n"

// dropRollbackPlainTarget is the same fixture minus the else. It MATCHES, and
// its dropped spans are the known-positive control: they prove the else fixture
// reaches the same recording path rather than silently never getting there.
const dropRollbackPlainTarget = "void f(int c, int w) {\n" +
	"    if (c) {\n        if (w) { m(); }\n    }\n}\n"

// runDropFixture walks every candidate node of a C target with pattern, and
// reports whether any matched, that match's dropped spans, and the largest
// number of spans left behind by a FAILED attempt.
func runDropFixture(t *testing.T, pattern, target string) (matched bool, dropped []byteRange, leaked int) {
	t.Helper()
	cfg, ok := langConfigFor(treesitter.LangC)
	if !ok {
		t.Fatalf("no LangConfig registered for C")
	}
	pt, err := compilePattern(context.Background(), mustParse(t, pattern), cfg)
	if err != nil {
		t.Fatalf("compilePattern(%q): %v", pattern, err)
	}
	defer pt.Close()

	parser := treesitter.NewParser()
	defer parser.Close()
	tree, err := parser.Parse(context.Background(), []byte(target), treesitter.LangC)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	defer tree.Close()

	src := []byte(target)
	caps := newCaptures()
	walkAll(tree.RootNode(), func(n *sitter.Node) {
		if matched {
			return
		}
		caps.reset()
		if matchTree(pt, n, src, caps) {
			matched = true
			dropped = caps.copyDropped()
			return
		}
		if len(caps.dropped) > leaked {
			leaked = len(caps.dropped)
		}
	})
	return matched, dropped, leaked
}

// TestDroppedSpanRollback pins the harsher half of the speculation discipline:
// a span recorded by a promotion inside a REJECTED try must not survive it.
func TestDroppedSpanRollback(t *testing.T) {
	t.Run("known_positive_a_surviving_promotion_records_its_spans", func(t *testing.T) {
		matched, dropped, _ := runDropFixture(t, dropRollbackPattern, dropRollbackPlainTarget)
		if !matched {
			t.Fatalf("plain fixture did not match — every assertion below it would be vacuous")
		}
		if len(dropped) == 0 {
			t.Fatalf("a match over two promoted `$$$X;` positions recorded no dropped span; " +
				"the else fixture's zero would then prove nothing")
		}
	})

	t.Run("no_span_survives_a_rejected_try", func(t *testing.T) {
		matched, _, leaked := runDropFixture(t, dropRollbackPattern, dropRollbackElseTarget)
		if matched {
			t.Fatalf("else fixture matched; it is chosen precisely because the nested if fails " +
				"after its body promotes, and a match here means no try was ever rejected")
		}
		if leaked != 0 {
			t.Errorf("%d dropped span(s) survived a rejected try — the splice would read them as "+
				"license to DELETE a template token no surviving promotion ever dropped", leaked)
		}
	})
}
