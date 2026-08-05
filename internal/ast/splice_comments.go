// SPDX-License-Identifier: Apache-2.0

// splice_comments.go — the source-side comment-edge recovery for the splice.
//
// Phase 3's comment-transparent alignment lets a comment-free pattern match a
// comment-carrying body, so a comment the walker skipped can land inside the
// source range the splice's template overwrites (splice.go). RawMatch.CommentSpans
// records those comments' source-side byte ranges, and the helpers here re-emit
// any of them that sits at an EDGE of the rewritten region — a comment strictly
// interior to it is left to the caller's template like any other interior byte.
// This is the source-side analog of splice_dropped.go's pattern-side budget.

package ast

// recoverEdgeComments splits the replaced core region src[left:right) into the
// runs the splice handles specially: the whitespace and the recovered COMMENT
// block at each edge. Walking in from the left, a run alternates whitespace and
// whole recorded comment spans and stops at the first byte that is neither; the
// right end walks the same way over whatever the left run did not claim, so the
// two never overlap. It is the comment-aware generalization of edgeSpace, and
// with no comment in the region it returns exactly what edgeSpace did — an
// all-whitespace seam still reports the SAME run for both edges, which is what
// puts a pure insertion on its own line.
//
// Comments are CONTENT the caller never asked to delete, so leadComments and
// trailComments are re-emitted UNCONDITIONALLY by the caller; leadWS/trailWS are
// returned separately so the whitespace keeps preferSourceSpace's rule (the
// template decides WHETHER there is whitespace, the source decides WHAT it is).
// leadComments runs from the first leading comment through the last byte of the
// leading run — comments plus the whitespace that separates and follows them —
// so a line comment keeps the newline that terminates it and cannot swallow the
// body that follows.
//
// ONLY EDGE COMMENTS ARE RECOVERED. A comment strictly interior to the core sits
// between two tokens the caller is actively rewriting, and is inside the
// caller's explicit rewrite exactly as any other interior byte is; recovering it
// would second-guess a template that deleted the code on both sides of it. That
// boundary is pinned by the interior_comment_is_inside_the_caller_rewrite test.
func recoverEdgeComments(src []byte, left, right uint32, spans []byteRange) (leadWS, leadComments, trailComments, trailWS string) {
	if !anyCommentIn(spans, left, right) {
		l, t := edgeSpace(string(src[left:right]))
		return l, "", "", t
	}

	// Leading run: whitespace and whole comment spans, in from the left.
	p := left
	firstLead, haveLead := left, false
	for p < right {
		if isSpliceSpace(src[p]) {
			p++
			continue
		}
		if c, ok := spanStartingAt(spans, p, right); ok {
			if !haveLead {
				firstLead, haveLead = p, true
			}
			p = c.End
			continue
		}
		break
	}
	leadEnd := p
	if haveLead {
		leadWS = string(src[left:firstLead])
		leadComments = string(src[firstLead:leadEnd])
	} else {
		leadWS = string(src[left:leadEnd])
	}

	// Trailing run: the mirror, over only what the leading run did not claim.
	q := right
	lastTrail, haveTrail := right, false
	for q > leadEnd {
		if isSpliceSpace(src[q-1]) {
			q--
			continue
		}
		if c, ok := spanEndingAt(spans, q, leadEnd); ok {
			if !haveTrail {
				lastTrail, haveTrail = q, true
			}
			q = c.Start
			continue
		}
		break
	}
	if haveTrail {
		trailComments = string(src[q:lastTrail])
		trailWS = string(src[lastTrail:right])
	} else {
		trailWS = string(src[q:right])
	}
	return leadWS, leadComments, trailComments, trailWS
}

// anyCommentIn reports whether any recorded comment span lies fully within
// [lo, hi). The no-comment answer is what keeps every pre-Phase-3 splice
// byte-identical: it takes the edgeSpace fast path unchanged.
func anyCommentIn(spans []byteRange, lo, hi uint32) bool {
	for _, s := range spans {
		if s.End > s.Start && s.Start >= lo && s.End <= hi {
			return true
		}
	}
	return false
}

// spanStartingAt returns a recorded comment span that begins exactly at `at` and
// ends at or before `hi`. The scan is linear because the span list is short and
// arrives sorted; a starts-at match is unique because the spans are disjoint.
func spanStartingAt(spans []byteRange, at, hi uint32) (byteRange, bool) {
	for _, s := range spans {
		if s.Start == at && s.End <= hi && s.End > s.Start {
			return s, true
		}
	}
	return byteRange{}, false
}

// spanEndingAt returns a recorded comment span that ends exactly at `at` and
// begins at or after `lo`. The tail-scan mirror of spanStartingAt.
func spanEndingAt(spans []byteRange, at, lo uint32) (byteRange, bool) {
	for _, s := range spans {
		if s.End == at && s.Start >= lo && s.End > s.Start {
			return s, true
		}
	}
	return byteRange{}, false
}
