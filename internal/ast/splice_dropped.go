// SPDX-License-Identifier: Apache-2.0

// splice_dropped.go — the write-side rule for pattern tokens the MATCH threw
// away, split out of splice.go for the file-size cap.
//
// A sequence position promoted through a wrapper (seq_shadow.go) drops the
// wrapper's own tokens: C's `{ $$$B; }` matches a body of self-terminating
// statements and the pattern's `;` goes with the wrapper. A dropped token was
// never compared against anything, so it earns no alignment entry and the
// splice's anchor list cannot see it — the identity template still carries it,
// and re-emitting it turns `g(a);` into `g(a);;`.
//
// THE RULE IS SCOPED, NOT BLANKET, and the scoping is the whole point. Treating
// every unanchored template token as a repeat would delete text the caller
// explicitly wrote, which is strictly worse than the duplication being fixed: a
// duplication shows up in the diff and a deletion does not. So a token is
// consumed only when the pattern-side window it sits in overlaps a span the
// matcher recorded as dropped, and only for as many bytes as that span holds.

package ast

// droppedBudget is how much of the pattern the MATCH threw away, and therefore
// how much of it the template may repeat without the splice emitting anything.
//
// Each span is a pattern-side range recorded at a promotion (walker.go). left
// tracks the bytes of each span still unclaimed, which is what makes the rule
// SCOPED rather than blanket: a template repeating the pattern's dropped `;`
// spends the span, and a template that wrote a SECOND `;` finds nothing left to
// attribute it to, so that one is emitted. The caller asked for it.
//
// A NO-DROP MATCH GETS A NIL BUDGET and every method refuses, so a pattern with
// no promoted sequence position behaves exactly as it did before this record
// existed — including keeping a latent alignment gap a LOUD duplication in the
// diff rather than turning it into a silent deletion.
type droppedBudget struct {
	spans []byteRange
	left  []uint32
}

// newDroppedBudget prepares the per-match claim state. Nil when the match
// dropped nothing, which is the common case.
func newDroppedBudget(spans []byteRange) *droppedBudget {
	if len(spans) == 0 {
		return nil
	}
	b := &droppedBudget{spans: spans, left: make([]uint32, len(spans))}
	for i, s := range spans {
		if s.End > s.Start {
			b.left[i] = s.End - s.Start
		}
	}
	return b
}

// claim spends one byte of a dropped span on the template token tok, sitting in
// the pattern-side window (low, high). It reports whether the budget covered it.
//
// IDENTIFIER BYTES ARE NEVER CLAIMED. A promotion drops a wrapper's separators
// and delimiters, so an identifier-looking token in an unanchored position is
// the caller's own text far more often than it is a repeat of the pattern. The
// two ways to be wrong are not symmetric: refusing a genuine repeat leaves a
// duplicated token, which the diff shows, while claiming a genuine rewrite
// deletes text nobody can see was dropped.
func (b *droppedBudget) claim(tok byte, low, high uint32) bool {
	if b == nil || isIdentCont(tok) {
		return false
	}
	for i, s := range b.spans {
		if b.left[i] == 0 || s.End <= low || s.Start >= high {
			continue
		}
		b.left[i]--
		return true
	}
	return false
}

// clone copies the claim state, leaving the spans shared. It lets a scan spend
// budget speculatively and throw the result away — a probe that does not pan
// out must not leave tokens marked spent for the scans that follow it.
func (b *droppedBudget) clone() *droppedBudget {
	if b == nil {
		return nil
	}
	return &droppedBudget{spans: b.spans, left: append([]uint32(nil), b.left...)}
}

// consumeDroppedTail spends trailing template bytes against the budget once the
// forward scan has consumed every anchor, and returns the offset it reached.
//
// WHY THE FORWARD SCAN CANNOT DO THIS ITSELF. scanTemplateHead's loop is
// bounded by the anchor list, so it exits the moment the last anchor is
// consumed and never looks at what follows. A dropped token in the MIDDLE of a
// pattern is therefore claimed on the way past, but one at the very END is
// not — the scan has already stopped. That is exactly where a class member's
// absorbed separator sits: tree-sitter keeps the `;` in the class_body list, so
// the compiled root ends one token short of the pattern and the dropped span is
// the pattern's final byte.
//
// The tail scan does not cover it either: its loop is bounded by the anchors
// the head scan did NOT consume, so it does not run at all when the head
// consumed them all.
func consumeDroppedTail(template string, anchors []spliceAnchor, head templateScan, drop *droppedBudget) int {
	if drop == nil {
		return head.offset
	}
	low, high := patWindow(anchors, head.anchors-1, head.anchors)
	ti := head.offset
	for {
		j := ti
		for j < len(template) && isSpliceSpace(template[j]) {
			j++
		}
		if j >= len(template) {
			return j
		}
		if !drop.claim(template[j], low, high) {
			return ti
		}
		ti = j + 1
	}
}

// patWindow bounds the pattern-side region an unanchored template token can
// have come from: the pattern end of the nearest literal anchor at or below
// loIdx, and the pattern start of the nearest one at or above hiIdx.
//
// Placeholder anchors are skipped because Captures carries no pattern
// coordinates for them. That widens the window to the literal tokens on either
// side of the placeholder — which is exactly the region a promoted wrapper's
// dropped tokens occupy, since the wrapper is what the placeholder sits inside.
func patWindow(anchors []spliceAnchor, loIdx, hiIdx int) (uint32, uint32) {
	low, high := uint32(0), ^uint32(0)
	for i := loIdx; i >= 0 && i < len(anchors); i-- {
		if anchors[i].name == "" {
			low = anchors[i].patEnd
			break
		}
	}
	for i := hiIdx; i >= 0 && i < len(anchors); i++ {
		if anchors[i].name == "" {
			high = anchors[i].patStart
			break
		}
	}
	return low, high
}
