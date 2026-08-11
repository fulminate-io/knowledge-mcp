// SPDX-License-Identifier: Apache-2.0

// splice.go — the source-anchored replacement builder.
//
// WHAT IT FIXES. Splicing an interpolated template over the whole matched byte
// range destroys every byte the template did not reproduce exactly: the
// newlines and indentation between tokens, and the anonymous tokens the
// pattern never named. A template written to be byte-identical to its pattern
// still collapsed a three-line function onto one line, and an interface-
// property pattern re-emitted `o?: number` as `o: number` — silently turning
// an optional property into a required one. The re-parse gate cannot see this
// class: reflowed and de-indented output is still grammatical.
//
// THE MECHANISM. A match carries two records that together locate every
// pattern token in the source: Alignment, one entry per LITERAL pattern token
// (alignment.go), and Captures, one entry per named placeholder. Sorted by
// source position they form the ANCHOR LIST — an ordered, disjoint tiling of
// the pattern's tokens onto source byte ranges.
//
// A THIRD RECORD COVERS THE TOKENS THAT ARE IN NEITHER. A sequence position
// promoted through a wrapper drops the wrapper's own tokens, and a dropped
// token was never compared against anything, so it earns no alignment entry:
// C's `{ $$$B; }` matches a body of self-terminating statements and the
// pattern's `;` simply goes away. The template still carries it, so without the
// record the splice sees an unanchored template token and emits it — writing
// `g(a);` back as `g(a);;`. RawMatch.DroppedSpans holds those tokens'
// pattern-side ranges; splice_dropped.go turns them into the budget the two
// scans below spend, one token at a time.
//
// A FOURTH RECORD IS OF A DIFFERENT KIND: SOURCE BYTES ANCHORED BY NOTHING. The
// ordinary alignment path skips a grammar's declared comment kinds so a
// comment-free pattern reaches a comment-carrying body (walker.go), and a
// skipped comment then sits inside the matched span covered by no alignment
// entry and no capture. Where the three records above locate PATTERN tokens,
// this one — RawMatch.CommentSpans, source-side ranges — locates comments the
// caller did NOT ask to delete. recoverEdgeComments re-emits any comment at the
// EDGE of the rewritten region so the replaced core cannot swallow it, while a
// comment strictly INTERIOR to the core is left to the caller's template like
// any other interior byte — a boundary a test pins rather than prose.
//
// The splice then reads the template from both ends against that list. A
// literal anchor agrees when its text appears verbatim at that point in the
// template; a placeholder anchor agrees when the template names it as $NAME or
// $$$NAME. What the two scans consume is the part of the template that repeats
// the pattern; what is left in the middle is the caller's explicit rewrite.
// Output is then src[matchStart : lastHeadAnchor.end] + the interpolated
// middle + src[firstTailAnchor.start : matchEnd]. The two source slices are
// taken as CONTIGUOUS BYTE RANGES rather than reassembled token by token,
// which is precisely why every inter-token byte inside them survives.
//
// WHY NO PATTERN ARGUMENT IS NEEDED. A literal anchor's SOURCE text is also its
// PATTERN text, so the anchor list is a complete stand-in for the pattern; and
// matching is order-preserving, so source order is pattern order. The walker
// upholds that equality on both of its comparison paths: it records an alignment
// only for a token whose texts matched, and where a grammar's absorbed layout
// whitespace made them match on TRIMMED text, it records the trimmed source
// range rather than the token's full span (alignment.go recordAlignRange,
// layout_jsx.go alignedTokenRange). The absorbed bytes are then anchored by
// nothing and survive inside the contiguous source slices below, exactly as any
// other inter-anchor byte does.
//
// IDENTITY IS BY CONSTRUCTION, NOT BY SPECIAL CASE. When the forward scan
// consumes every anchor and nothing but whitespace remains, the template said
// nothing the pattern did not, and the matched span is returned verbatim. The
// general path is source-anchored too — a one-token rewrite leaves every other
// byte alone — so this is a short-circuit, not the only thing holding the
// invariant up.
//
// INHERITED LIMITATION. A pattern naming the same capture twice ($A + $A)
// binds ONE capture (walker.go:81-86 overwrites by design), so the anchor list
// is short an entry and the reconstruction is unreliable — as the whole-span
// splice already was, re-emitting the surviving binding in both positions.
//
// BYTE OFFSETS ARE TRUE BYTE OFFSETS throughout: every bound here comes from a
// tree-sitter StartByte/EndByte and indexes the source slice directly. No rune
// index and no line/column round trip enters the computation, which is what
// keeps multibyte UTF-8, a leading BOM and CRLF line endings exact.

package ast

import (
	"sort"
	"strings"
)

// outerCaptureName is the reserved Captures key holding the outer matched
// node — the span a replacement stands in for.
const outerCaptureName = "match"

// spliceAnchor is one pattern token whose position in the source is known.
// Exactly one of name / text is set: name for a placeholder anchor (from
// Captures), text for a literal anchor (from Alignment, holding the source
// bytes it matched, which are also the pattern token's bytes).
//
// patStart/patEnd are the token's PATTERN-side range, carried on literal
// anchors only — Captures records no pattern coordinates for a placeholder.
// They exist so a template token that anchors nothing can be located in pattern
// space, between the literal tokens bracketing it, and tested against the
// dropped spans.
type spliceAnchor struct {
	name     string
	text     string
	srcStart uint32
	srcEnd   uint32
	patStart uint32
	patEnd   uint32
}

// templateScan is how far one end of the template repeated the pattern:
// anchors consumed, and the template byte offset agreement stopped at.
type templateScan struct {
	anchors int
	offset  int
}

// spliceFromSource builds the replacement text for one match's outer span,
// taking every byte the template did not explicitly rewrite from src.
//
// It falls back to a bare interpolateTemplate — the whole-span behavior —
// whenever the match cannot be anchored: no outer capture, no source bytes
// (buildFileEdits' callers may have none), a span outside the source, or a
// pattern of pure wildcards that produced no anchors at all.
func spliceFromSource(m RawMatch, template string, src []byte) (string, error) {
	outer, ok := m.Captures[outerCaptureName]
	if !ok || outer.EndByte <= outer.StartByte || int(outer.EndByte) > len(src) {
		return interpolateTemplate(template, m.Captures)
	}
	anchors := spliceAnchors(m, src, outer)
	if len(anchors) == 0 {
		return interpolateTemplate(template, m.Captures)
	}

	// One budget for both scans: a dropped token repeats ONCE in the template,
	// so whichever end reaches it spends it and the other end finds it spent.
	drop := newDroppedBudget(m.DroppedSpans)

	head := scanTemplateHead(template, anchors, m.Captures, drop)
	if head.anchors == len(anchors) {
		// The template repeated the pattern and said nothing more: an
		// identity replacement is the matched span, byte for byte. "Nothing
		// more" includes a trailing token the MATCH dropped — a class
		// member's absorbed `;` is pattern text that never reached the
		// anchor list, so a template repeating it is still saying nothing
		// new. The probe runs on a copy: budget is only spent if the
		// identity actually holds.
		probe := drop.clone()
		if end := consumeDroppedTail(template, anchors, head, probe); strings.TrimSpace(template[end:]) == "" {
			return string(src[outer.StartByte:outer.EndByte]), nil
		}
	}
	tail := scanTemplateTail(template, anchors, m.Captures, head, drop)
	if tail.offset < head.offset {
		return interpolateTemplate(template, m.Captures)
	}

	left, right := spliceBounds(anchors, outer, head, tail)
	if left > right || left < outer.StartByte || right > outer.EndByte {
		return interpolateTemplate(template, m.Captures)
	}

	lead, core, trail := splitEdgeSpace(template[head.offset:tail.offset])
	body, err := interpolateTemplate(core, m.Captures)
	if err != nil {
		return "", err
	}
	leadWS, leadComments, trailComments, trailWS := recoverEdgeComments(src, left, right, m.CommentSpans)

	var b strings.Builder
	b.Grow(int(outer.EndByte-outer.StartByte) + len(template))
	b.Write(src[outer.StartByte:left])
	b.WriteString(preferSourceSpace(lead, leadWS))
	b.WriteString(leadComments)
	b.WriteString(body)
	b.WriteString(trailComments)
	b.WriteString(preferSourceSpace(trail, trailWS))
	b.Write(src[right:outer.EndByte])
	return b.String(), nil
}

// spliceAnchors builds the ordered, disjoint anchor list for one match:
// literal tokens from Alignment plus named placeholders from Captures, sorted
// by source position.
//
// Zero-width captures are dropped — that is how a sequence placeholder that
// matched no siblings presents itself, and it has no position to anchor. The
// scans handle it separately, by consuming its template reference without
// consuming an anchor. Anything outside the matched span is dropped too, and
// an overlapping entry is skipped: a positional consumer needs a strictly
// ascending tiling, and two anchors claiming the same bytes would produce one.
func spliceAnchors(m RawMatch, src []byte, outer Capture) []spliceAnchor {
	out := make([]spliceAnchor, 0, len(m.Alignment)+len(m.Captures))
	for _, a := range m.Alignment {
		if a.SrcEnd <= a.SrcStart || a.SrcStart < outer.StartByte || a.SrcEnd > outer.EndByte {
			continue
		}
		out = append(out, spliceAnchor{
			text:     string(src[a.SrcStart:a.SrcEnd]),
			srcStart: a.SrcStart,
			srcEnd:   a.SrcEnd,
			patStart: a.PatStart,
			patEnd:   a.PatEnd,
		})
	}
	for name, c := range m.Captures {
		if name == "" || name == outerCaptureName {
			continue
		}
		if c.EndByte <= c.StartByte || c.StartByte < outer.StartByte || c.EndByte > outer.EndByte {
			continue
		}
		out = append(out, spliceAnchor{name: name, srcStart: c.StartByte, srcEnd: c.EndByte})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].srcStart != out[j].srcStart {
			return out[i].srcStart < out[j].srcStart
		}
		return out[i].srcEnd < out[j].srcEnd
	})

	kept := out[:0]
	var end uint32
	for _, a := range out {
		if len(kept) > 0 && a.srcStart < end {
			continue
		}
		kept = append(kept, a)
		end = a.srcEnd
	}
	return kept
}

// scanTemplateHead walks the template forward, consuming anchors for as long
// as it keeps repeating the pattern.
func scanTemplateHead(template string, anchors []spliceAnchor, caps map[string]Capture, drop *droppedBudget) templateScan {
	ti, ai := 0, 0
	for ai < len(anchors) {
		j := ti
		for j < len(template) && isSpliceSpace(template[j]) {
			j++
		}
		if j >= len(template) {
			break
		}
		if name, end, ok := placeholderHead(template, j); ok {
			// A sequence that matched no siblings has no anchor, but the
			// template still names it; consuming the reference keeps the two
			// sides aligned.
			if isEmptySeqCapture(caps, name) {
				ti = end
				continue
			}
			if anchors[ai].name != name {
				break
			}
			ti, ai = end, ai+1
			continue
		}
		a := anchors[ai]
		if a.name == "" && a.text != "" && strings.HasPrefix(template[j:], a.text) &&
			!breaksIdentRight(a.text, template, j+len(a.text)) {
			ti, ai = j+len(a.text), ai+1
			continue
		}
		// The token anchors nothing. Consume it only if it repeats a pattern
		// token this match DROPPED; otherwise the scan stops and the token
		// becomes part of the caller's rewrite, as it always has.
		low, high := patWindow(anchors, ai-1, ai)
		if drop.claim(template[j], low, high) {
			ti = j + 1
			continue
		}
		break
	}
	return templateScan{anchors: ai, offset: ti}
}

// scanTemplateTail walks the template backward the same way, never crossing
// the offset the forward scan reached.
func scanTemplateTail(template string, anchors []spliceAnchor, caps map[string]Capture, head templateScan, drop *droppedBudget) templateScan {
	tj, aj := len(template), 0
	for head.anchors+aj < len(anchors) {
		j := tj
		for j > head.offset && isSpliceSpace(template[j-1]) {
			j--
		}
		if j <= head.offset {
			break
		}
		ai := len(anchors) - 1 - aj
		a := anchors[ai]
		if name, start, ok := placeholderTail(template, j); ok {
			if start < head.offset {
				break
			}
			if isEmptySeqCapture(caps, name) {
				tj = start
				continue
			}
			if a.name != name {
				break
			}
			tj, aj = start, aj+1
			continue
		}
		if a.name == "" && a.text != "" && strings.HasSuffix(template[:j], a.text) {
			start := j - len(a.text)
			if start >= head.offset && !breaksIdentLeft(a.text, template, start) {
				tj, aj = start, aj+1
				continue
			}
		}
		// The mirror of the head scan's dropped-token claim. This end reaches
		// the trailing side of a promoted position — C's `{ $$$B; }` presents
		// its dropped `;` here whenever the head scan stopped earlier, which is
		// every template that rewrites something before it.
		low, high := patWindow(anchors, ai, ai+1)
		if j-1 >= head.offset && drop.claim(template[j-1], low, high) {
			tj = j - 1
			continue
		}
		break
	}
	return templateScan{anchors: aj, offset: tj}
}

// spliceBounds resolves the source byte range the template's middle stands in
// for: the span of the anchors NEITHER scan consumed.
//
// When both scans together consumed every anchor, no pattern token is being
// rewritten and the middle is a pure insertion — a zero-width seam rather than
// a range to overwrite, placed at the boundary the surrounding agreement pins.
func spliceBounds(anchors []spliceAnchor, outer Capture, head, tail templateScan) (uint32, uint32) {
	left, right := outer.StartByte, outer.EndByte
	if head.anchors > 0 {
		left = anchors[head.anchors-1].srcEnd
	}
	if tail.anchors > 0 {
		right = anchors[len(anchors)-tail.anchors].srcStart
	}
	if head.anchors+tail.anchors == len(anchors) {
		if tail.anchors == 0 {
			right = left
		}
		if head.anchors == 0 {
			left = right
		}
	}
	return left, right
}

// placeholderHead reads a `$NAME` / `$$$NAME` reference starting at s[i],
// returning the capture name and the offset just past it. A two-dollar run is
// the literal-'$' escape, not a reference.
func placeholderHead(s string, i int) (string, int, bool) {
	if i >= len(s) || s[i] != '$' {
		return "", 0, false
	}
	d := 0
	for i+d < len(s) && s[i+d] == '$' {
		d++
	}
	if d != 1 && d != 3 {
		return "", 0, false
	}
	k := i + d
	if k >= len(s) || !isIdentStart(s[k]) {
		return "", 0, false
	}
	e := k
	for e < len(s) && isIdentCont(s[e]) {
		e++
	}
	return s[k:e], e, true
}

// placeholderTail reads a `$NAME` / `$$$NAME` reference ENDING at s[j],
// returning the capture name and the offset of its leading '$'.
func placeholderTail(s string, j int) (string, int, bool) {
	k := j
	for k > 0 && isIdentCont(s[k-1]) {
		k--
	}
	if k == j || !isIdentStart(s[k]) {
		return "", 0, false
	}
	d := 0
	for k-d > 0 && s[k-d-1] == '$' {
		d++
	}
	if d != 1 && d != 3 {
		return "", 0, false
	}
	return s[k:j], k - d, true
}

// breaksIdentRight reports whether a literal token matched at the head of a
// template region runs into an identifier character, which would mean the
// template names a LONGER identifier rather than repeating the pattern's token
// (`foo` matched inside `foobar`).
func breaksIdentRight(token, s string, end int) bool {
	return isIdentCont(token[len(token)-1]) && end < len(s) && isIdentCont(s[end])
}

// breaksIdentLeft is the mirror check for a literal token matched at the tail.
func breaksIdentLeft(token, s string, start int) bool {
	return isIdentCont(token[0]) && start > 0 && isIdentCont(s[start-1])
}

// isEmptySeqCapture reports whether name is bound to a sequence that matched
// zero siblings: no text and no source position of its own.
func isEmptySeqCapture(caps map[string]Capture, name string) bool {
	c, ok := caps[name]
	return ok && c.Text == "" && c.EndByte <= c.StartByte
}

// isSpliceSpace reports whether b is inter-token whitespace.
func isSpliceSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}

// splitEdgeSpace splits a template region into its leading whitespace, its
// trimmed core, and its trailing whitespace. An all-whitespace region is
// reported as lead only, so the two halves never double-count the same bytes.
func splitEdgeSpace(s string) (lead, core, trail string) {
	start, end := 0, len(s)
	for start < end && isSpliceSpace(s[start]) {
		start++
	}
	for end > start && isSpliceSpace(s[end-1]) {
		end--
	}
	if start == end {
		return s, "", ""
	}
	return s[:start], s[start:end], s[end:]
}

// edgeSpace returns the leading and trailing whitespace runs of a source
// region. An all-whitespace region reports the SAME run for both: it is the
// seam between two preserved tokens, and an insertion there wants the source's
// own line break and indentation on each side of it.
func edgeSpace(s string) (lead, trail string) {
	start, end := 0, len(s)
	for start < end && isSpliceSpace(s[start]) {
		start++
	}
	for end > start && isSpliceSpace(s[end-1]) {
		end--
	}
	if start == end {
		return s, s
	}
	return s[:start], s[end:]
}

// preferSourceSpace picks the whitespace to emit at one edge of a rewritten
// region. The template decides WHETHER there is whitespace there — a template
// attaching directly, as `renamed_$N` does, must not have any inserted — and
// the source decides WHAT it is, so an inserted statement lands on its own
// line at the surrounding indentation rather than at the template's.
func preferSourceSpace(templateSpace, sourceSpace string) string {
	if templateSpace == "" {
		return ""
	}
	if sourceSpace != "" {
		return sourceSpace
	}
	return templateSpace
}
