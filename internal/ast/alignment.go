// SPDX-License-Identifier: Apache-2.0

// alignment.go — the per-match pattern-side records: the pattern-to-source
// token alignment, and the spans a promotion dropped.
//
// WHAT IT RECORDS. For every LITERAL pattern token the walker successfully
// compared against a target node, one TokenAlign entry maps the pattern-side
// byte range to the target-side byte range it matched. Placeholder positions
// are deliberately NOT alignment entries: they already live in Captures with
// their own StartByte/EndByte, and the two records answer different questions.
// A capture says "the user named this span"; an alignment says "this literal
// pattern byte range IS that source byte range".
//
// WHY A CONSUMER NEEDS IT. Reconstructing output from a matched span requires
// telling an unchanged token from a rewritten one. Captures alone cannot: they
// cover only the positions the user named, so everything between them is
// indistinguishable from template text and gets overwritten wholesale.
//
// ONLY LEAF TOKENS ARE RECORDED. An interior pattern node's byte range is the
// union of its children's, so recording it too would produce nested,
// overlapping entries that a positional consumer cannot use. Entries are
// appended during the walker's pre-order descent, which runs left-to-right
// over the pattern, so the accumulated slice arrives in ascending
// pattern-byte order and each pattern token appears at most once.
//
// COORDINATE SPACE. PatStart/PatEnd are offsets into
// PatternTree.SubstitutedSource — the exact bytes handed to the parser,
// wrapper prefix included. A consumer wanting offsets into the user's own DSL
// subtracts PatternTree.PrefixLen. They are NOT offsets into the raw DSL
// source: placeholder substitution rewrites identifiers to reserved-prefix
// names of a different length, so the two spaces diverge after the first
// placeholder. SrcStart/SrcEnd are offsets into the target file's source.
//
// THE SECOND RECORD: DROPPED SPANS. A sequence position promoted through a
// wrapper (seq_shadow.go) throws the wrapper's own tokens away — C's
// `{ $$$B; }` parses its body slot as a statement spanning `<placeholder>;`,
// the placeholder is promoted to consume the target's statements, and the `;`
// goes with the wrapper. Nothing in the target was compared against it, so it
// earns no alignment entry, and at the splice it is indistinguishable from a
// token an alignment bug merely missed. dropped records the pattern-side spans
// those tokens occupy, in the same coordinate space as PatStart/PatEnd, so the
// write side can consume a template token that repeats one instead of emitting
// it beside source that already carries its own.
//
// THE THIRD RECORD: SKIPPED COMMENTS. Where aligns and dropped are both
// PATTERN-side, comments is a TARGET-side record: the source byte spans of the
// comments the ordinary alignment path skipped so a comment-free pattern could
// reach a comment-carrying body (walker.go, matchSiblings). Nothing in the
// pattern was compared against them, so at the splice they are indistinguishable
// from bytes the rewrite may overwrite — and overwriting them is exactly the
// silent comment DELETION this record exists to prevent. The splice reads them
// to keep a skipped comment out of a rewritten region's replaced span.
//
// SPECULATION DISCIPLINE, FOR ALL THREE. The accumulators are append-only during
// a match attempt, so every path that can abandon a partial attempt must truncate
// them back: reset per candidate node, and matchSeqShadow's per-k rollback. A
// speculative alignment surviving a rejected try is the same defect class as a
// leaked seq capture, and just as silent — the entry stays inside the final
// matched span, so a bounds check cannot see it. What catches it is that a
// pattern token would then map to two different source ranges. A dropped span
// surviving a rejected try is worse: it licenses the splice to DELETE a
// template token that no surviving promotion ever dropped. A comment span
// surviving a rejected try is worse still: it names bytes OUTSIDE the final
// match as edge material to preserve, claiming a comment from a different site.

package ast

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// TokenAlign maps one literal pattern token's byte range to the source byte
// range it matched. See the file comment for the coordinate spaces.
type TokenAlign struct {
	PatStart, PatEnd, SrcStart, SrcEnd uint32
}

// alignInitialCap pre-sizes the per-Captures alignment accumulator. Sized for
// a typical pattern's literal-token count so the first few matches do not grow
// the slice; reset truncates rather than reallocating, so the backing array is
// allocated once per Captures and reused across every candidate node after it.
const alignInitialCap = 32

// recordAlign appends the pattern-to-source mapping for one successfully
// compared literal token. Called only from the walker's leaf-compare path, so
// placeholder positions never reach it.
func (c *Captures) recordAlign(p, t *sitter.Node) {
	if c == nil || p == nil || t == nil {
		return
	}
	c.aligns = append(c.aligns, TokenAlign{
		PatStart: p.StartByte(),
		PatEnd:   p.EndByte(),
		SrcStart: t.StartByte(),
		SrcEnd:   t.EndByte(),
	})
}

// alignMark returns the current accumulator length so a speculative try can be
// rolled back to it. Paired with alignRollback.
func (c *Captures) alignMark() int {
	if c == nil {
		return 0
	}
	return len(c.aligns)
}

// alignRollback discards every entry appended since mark. Truncates in place,
// keeping the backing array — a rejected try must cost no allocation.
func (c *Captures) alignRollback(mark int) {
	if c == nil || mark < 0 || mark > len(c.aligns) {
		return
	}
	c.aligns = c.aligns[:mark]
}

// copyAligns returns an independent copy of the accumulated alignment, for
// handing to a RawMatch that outlives the Captures it came from. Nil when
// nothing was recorded, so a match over a pattern of pure placeholders renders
// no empty array.
func (c *Captures) copyAligns() []TokenAlign {
	if c == nil || len(c.aligns) == 0 {
		return nil
	}
	out := make([]TokenAlign, len(c.aligns))
	copy(out, c.aligns)
	return out
}

// dropInitialCap pre-sizes the per-Captures dropped-span accumulator. A pattern
// has as many promotable sequence positions as it has $$$ placeholders and most
// have none, so this is sized for the rare case rather than the common one and
// reset truncates rather than reallocating.
const dropInitialCap = 4

// recordDropped notes the pattern-side spans one promotion threw away: the
// chain top's bytes lying outside its placeholder leaf. A depth-0 chain IS the
// leaf and a zero-width wrapper covers exactly the leaf — both drop no token
// and record nothing, which is why the common promotion costs an append of
// zero entries.
func (c *Captures) recordDropped(top *sitter.Node, chain seqChain) {
	if c == nil || top == nil || chain.depth == 0 {
		return
	}
	if chain.leaf.Start > top.StartByte() {
		c.dropped = append(c.dropped, byteRange{Start: top.StartByte(), End: chain.leaf.Start})
	}
	if chain.leaf.End < top.EndByte() {
		c.dropped = append(c.dropped, byteRange{Start: chain.leaf.End, End: top.EndByte()})
	}
}

// dropMark returns the current dropped-span accumulator length so a speculative
// try can be rolled back to it. Paired with dropRollback.
func (c *Captures) dropMark() int {
	if c == nil {
		return 0
	}
	return len(c.dropped)
}

// dropRollback discards every dropped span appended since mark, truncating in
// place so a rejected try costs no allocation.
func (c *Captures) dropRollback(mark int) {
	if c == nil || mark < 0 || mark > len(c.dropped) {
		return
	}
	c.dropped = c.dropped[:mark]
}

// copyDropped returns an independent copy of the accumulated dropped spans, for
// handing to a RawMatch that outlives the Captures it came from. Nil when
// nothing was dropped — the common case, and the one where the splice keeps its
// pre-existing behavior exactly.
func (c *Captures) copyDropped() []byteRange {
	if c == nil || len(c.dropped) == 0 {
		return nil
	}
	out := make([]byteRange, len(c.dropped))
	copy(out, c.dropped)
	return out
}

// commentInitialCap pre-sizes the per-Captures skipped-comment accumulator. A
// body carries few comments and most matches skip none, so it is sized for the
// rare case and reset truncates rather than reallocating.
const commentInitialCap = 4

// recordComment notes the SOURCE-side byte span of one ignorable comment the
// ordinary alignment path skipped. Unlike aligns and dropped — both pattern-side
// — this is a target-side span: a comment sits inside the matched region that no
// pattern token was compared against, so a rewrite that treated the region as
// template text would delete it. Recorded only on the ordinary path, never for a
// comment a sequence shadow consumed — those live verbatim in the seq capture.
func (c *Captures) recordComment(t *sitter.Node) {
	if c == nil || t == nil {
		return
	}
	c.comments = append(c.comments, byteRange{Start: t.StartByte(), End: t.EndByte()})
}

// commentMark returns the current skipped-comment accumulator length so a
// speculative try can be rolled back to it. Paired with commentRollback.
func (c *Captures) commentMark() int {
	if c == nil {
		return 0
	}
	return len(c.comments)
}

// commentRollback discards every comment span appended since mark, truncating in
// place so a rejected try costs no allocation.
func (c *Captures) commentRollback(mark int) {
	if c == nil || mark < 0 || mark > len(c.comments) {
		return
	}
	c.comments = c.comments[:mark]
}

// copyComments returns an independent copy of the skipped-comment spans that lie
// within [lo, hi) — the matched outer span — for a RawMatch that outlives the
// Captures. Nil when none were skipped, the common case and the one where the
// splice keeps its pre-existing behavior exactly. The spans are recorded in
// source order by the walk, so no sort is needed; the ordering is pinned by a
// test rather than defended on this path.
func (c *Captures) copyComments(lo, hi uint32) []byteRange {
	if c == nil || len(c.comments) == 0 {
		return nil
	}
	out := make([]byteRange, 0, len(c.comments))
	for _, r := range c.comments {
		if r.Start >= lo && r.End <= hi {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
