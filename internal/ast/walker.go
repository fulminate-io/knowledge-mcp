// SPDX-License-Identifier: Apache-2.0

// walker.go — manual ast-grep-style matcher.
//
// matchTree(pat, target, src, caps) recursively compares a compiled
// PatternTree against a target tree-sitter node, dispatching by case:
// kind-equality, child-list alignment, single-node placeholder bind,
// sequence-placeholder greedy bind, and wildcard. No tree-sitter queries
// at runtime — every match decision is a Go-side AST walk.
//
// The child-list alignment compares ALL children, anonymous tokens included,
// so operators, modifiers and declaration keywords constrain a match in both
// directions. Two classes of child are exempt, and the two are dropped in
// different places on purpose. A grammar's declared PURE-LAYOUT tokens
// (LangConfig.LayoutTokens) — Go's intra-block newline terminator is the only
// one the census found — are dropped from BOTH child lists by allChildren, so a
// pattern constrains what the source SAYS and not how it was spelled across
// lines. A grammar's declared COMMENT kinds (LangConfig.CommentKinds) are
// skipped on the ordinary alignment path only: matchSiblings advances past a
// target-side comment a comment-free pattern did not ask for and records its
// source span so the splice can preserve it, while a SEQUENCE shadow still
// consumes comments verbatim into its capture — which is what keeps a $$$ body
// re-interpolating as valid source. The comment skip is a TARGET-side act only:
// a comment written into a pattern is deliberate and is compared as an ordinary
// node. Three records come out of a successful match: the named Captures, the
// literal-token alignment, and the skipped-comment spans — all in alignment.go.
//
// Sequence-placeholder semantics:
//
//   - A pattern subtree shaped as "linear chain of single-named-child nodes
//     ending at a substituted seq-placeholder identifier" is a candidate SEQ
//     SHADOW at its parent level. Whether the chain's top node is promoted to
//     that shadow — so the placeholder consumes the node's TARGET SIBLINGS —
//     or matched as an ordinary node so the sequence resolves among its own
//     children is decided in seq_shadow.go, from the kind of the target sibling
//     it faces and the grammar's field name for that child. The shadow consumes
//     0..N consecutive target siblings (greedy match with backtracking when
//     followed by more pattern siblings).
//
//   - The shadow's collected target siblings populate the seq capture's
//     .text (verbatim source slice from start_byte of first matched
//     sibling to end_byte of last matched sibling) and .children (per-
//     sibling Capture views). Empty match (zero siblings) yields .text=""
//     and .children=[]. Container delimiters are never in either: the
//     container is matched as a node, so its parens or braces align against
//     the pattern's own.
//
// Wrapper-stripping (Q3): matchTree starts at PatternTree.Root, which
// compilePattern set to the smallest descendant fully covering the user's
// substituted source. That makes positions report against the user's
// pattern, not the wrapper.

package ast

import (
	"slices"

	sitter "github.com/smacker/go-tree-sitter"
)

// Captures is the per-match name → Capture binding accumulator. Sequence
// captures populate Children + StartByte + EndByte; single captures leave
// Children nil. aligns is the ordered literal-token alignment record for the
// same match attempt and dropped holds the pattern spans its promotions threw
// away — see alignment.go.
type Captures struct {
	byName   map[string]Capture
	aligns   []TokenAlign
	dropped  []byteRange
	comments []byteRange
}

// newCaptures returns an empty Captures binding accumulator.
func newCaptures() *Captures {
	return &Captures{
		byName:   make(map[string]Capture),
		aligns:   make([]TokenAlign, 0, alignInitialCap),
		dropped:  make([]byteRange, 0, dropInitialCap),
		comments: make([]byteRange, 0, commentInitialCap),
	}
}

// reset clears all bindings so the Captures can be reused for a new
// match attempt without allocating a new map. Both pattern-side accumulators
// are truncated rather than dropped: reset runs once per candidate node, and
// either record leaked from a rejected candidate would corrupt the next match.
func (c *Captures) reset() {
	for k := range c.byName {
		delete(c.byName, k)
	}
	c.aligns = c.aligns[:0]
	c.dropped = c.dropped[:0]
	c.comments = c.comments[:0]
}

// clearNodeMap removes all entries from a node map for reuse.
func clearNodeMap(m map[string]*sitter.Node) {
	for k := range m {
		delete(m, k)
	}
}

// bindNode records a single-node capture under name (or appends a fresh
// per-occurrence binding when the name is empty / wildcard). Multiple
// captures with the same name OVERWRITE — the v2 design rejects implicit
// binding equality, so named-collision is a callsite concern (B.4
// same_node leaf is the explicit identity check).
func (c *Captures) bindNode(name string, n *sitter.Node, src []byte) {
	if name == "" || n == nil {
		return
	}
	c.byName[name] = nodeToCapture(n, src)
}

// bindSeq records a sequence capture: name → Capture with Children
// populated, Text spanning [first.StartByte, last.EndByte). Empty seq
// (no siblings) records {Text: "", Children: []}.
//
// THE SEPARATOR RULE. A seq shadow consumes whole sibling spans, anonymous
// tokens included — it has to, because the pattern siblings that follow it
// align against the target's own anonymous tokens. So the commas between
// parameters and the semicolons between statements arrive here. Text KEEPS
// them: it is the verbatim source span, which is what makes a seq capture
// re-interpolate as valid source. Children DROPS them: it carries semantic
// siblings only, so a two-parameter capture reads as two parameters rather
// than two parameters and a comma.
func (c *Captures) bindSeq(name string, siblings []*sitter.Node, src []byte) {
	if name == "" {
		return
	}
	cap := Capture{
		Children: make([]Capture, 0, len(siblings)),
	}
	if len(siblings) > 0 {
		first := siblings[0]
		last := siblings[len(siblings)-1]
		cap.StartByte = first.StartByte()
		cap.EndByte = last.EndByte()
		cap.Line = int(first.StartPoint().Row) + 1
		cap.Text = string(src[first.StartByte():last.EndByte()])
		for _, s := range siblings {
			if !s.IsNamed() {
				continue
			}
			cap.Children = append(cap.Children, nodeToCapture(s, src))
		}
		// Sequence captures don't carry a Kind — the children carry kinds.
	}
	c.byName[name] = cap
}

// matchTree compares pat at its current root position against target. src
// is the target file's source bytes — the placeholder lookup uses the
// PatternTree's own SubstitutedSource bytes for pattern-side recognition,
// but every captured target node reads from src.
//
// Returns true when the pattern matches; caps holds the bindings in that
// case. Returns false on any structural mismatch.
func matchTree(pt *PatternTree, target *sitter.Node, src []byte, caps *Captures) bool {
	if pt == nil || pt.Root == nil || target == nil {
		return false
	}
	patSrc := []byte(pt.SubstitutedSource)
	return matchNode(pt, pt.Root, target, patSrc, src, caps)
}

// effectivePatternNode descends through single-named-child wrappers that
// share p's outer byte range. Tree-sitter sometimes wraps a single
// identifier in a redundant container (e.g. `return $ERR` parses as
// return_statement → expression_list → identifier, where expression_list
// has the SAME outer byte range as its only child). Without this descent
// our placeholder lookup hits at the wrapper level and binds Kind=
// expression_list rather than Kind=identifier. Descending fixes the
// binding at the structurally-meaningful leaf.
func effectivePatternNode(p *sitter.Node) *sitter.Node {
	cur := p
	for cur.NamedChildCount() == 1 {
		child := cur.NamedChild(0)
		if child == nil {
			break
		}
		if child.StartByte() != cur.StartByte() || child.EndByte() != cur.EndByte() {
			break
		}
		cur = child
	}
	return cur
}

// effectiveTargetNode mirrors effectivePatternNode on the target side: if
// the placeholder pattern leaf is reached after descending through a
// same-byte-range wrapper on the pattern, descend the target through the
// SAME wrapper kind sequence so the bound capture reflects the structural
// leaf rather than the wrapper. Used only for single-node bindings.
func effectiveTargetNode(t *sitter.Node) *sitter.Node {
	cur := t
	for cur.NamedChildCount() == 1 {
		child := cur.NamedChild(0)
		if child == nil {
			break
		}
		if child.StartByte() != cur.StartByte() || child.EndByte() != cur.EndByte() {
			break
		}
		cur = child
	}
	return cur
}

// matchNode is the recursive matcher. Pattern node P is checked against
// target T:
//
//  1. If P's byte range hits a single-node placeholder, bind T (when not a
//     wildcard) and return true — wildcards match anything.
//  2. Otherwise P.Type() must equal T.Type().
//  3. When both are childless tokens, their source text must match.
//  4. Otherwise iterate all children of P and T together with seq-shadow
//     handling.
//
//nolint:gocognit // four-case dispatch over a small grammar; the complexity is inherent to the sibling-alignment logic
func matchNode(pt *PatternTree, p, t *sitter.Node, patSrc, src []byte, caps *Captures) bool {
	pOrig := p
	// The wrapper-strip is refused for a subtree that chains down to a
	// sequence placeholder. Those chains are frequently zero-width — Python's
	// block, Ruby's body_statement and Go's expression_statement each share
	// their leaf's byte range exactly — so descending would collapse the very
	// container the sequence has to resolve INSIDE, hand the placeholder a
	// single node, and bind a whole multi-statement body as one element.
	// matchChildren reaches the same leaf one level down, where the chain is a
	// sibling position and seq_shadow.go can read the grammar's field name.
	if _, hostsSeq := findSeqChain(pt, p); !hostsSeq {
		p = effectivePatternNode(p)
	}
	if ph, ok := lookupPlaceholder(pt, p); ok {
		switch ph.Kind {
		case KindNode:
			caps.bindNode(ph.Name, effectiveTargetNode(t), src)
			return true
		case KindNodeWild:
			return true
		case KindSeq, KindSeqWild:
			// Sequence placeholders are handled at the parent's child-
			// iteration level, so the only way to arrive here is a pattern
			// whose entire outer shape is a bare seq placeholder. There are no
			// siblings to consume at the root, so the target binds as a
			// single-element sequence.
			caps.bindSeq(ph.Name, []*sitter.Node{effectiveTargetNode(t)}, src)
			return true
		}
	}
	// Lockstep target descent: when effectivePatternNode descended the
	// pattern through zero-width single-named-child wrappers (e.g. Python's
	// with_clause → with_item → as_pattern, all sharing the same byte
	// range), descend the target through the same chain so the type check
	// compares structurally-meaningful peers. Gated on `p was descended`
	// so the top-level walker doesn't over-match by stripping target
	// wrappers (visiting `expression_statement` would otherwise re-match
	// the inner `call_expression` pattern at the outer level).
	if p != pOrig {
		t = effectiveTargetNode(t)
	}
	if p.Type() != t.Type() {
		return false
	}
	// TOKEN EQUALITY IS DECIDED HERE AND NOWHERE ELSE. A node with no
	// children of ANY kind is a token, and two tokens match when their source
	// text matches — that covers identifiers, type names, keywords and
	// literals alike. The gate counts ALL children because that is what
	// "leaf" means: a node holding only anonymous children is not a token, it
	// is a node whose children the matcher compares one by one. A Java
	// `modifiers` node of bare keywords and the same node carrying an
	// annotation are then treated identically, instead of the first being
	// text-compared and the second waved through.
	//
	// ONE EXCEPTION, applied to BOTH sides here: a grammar that absorbs layout
	// whitespace into its leading anonymous tokens (LangConfig.
	// TrimsAnonTokenWhitespace) has those tokens compared whitespace-trimmed,
	// or the source's line breaks become a constraint the pattern has to
	// reproduce byte for byte. It is gated on both nodes being ANONYMOUS so a
	// named leaf — a string literal, an identifier — keeps discriminating on
	// the whitespace it carries. The config check comes first because it is the
	// cheapest, and node source is sliced rather than copied via Content()
	// because this path runs for every childless token of every candidate.
	if p.ChildCount() == 0 && t.ChildCount() == 0 {
		trim := pt.LangCfg.TrimsAnonTokenWhitespace && !p.IsNamed() && !t.IsNamed()
		if !tokenTextMatches(patSrc[p.StartByte():p.EndByte()], src[t.StartByte():t.EndByte()], trim) {
			return false
		}
		as, ae := alignedTokenRange(src, t.StartByte(), t.EndByte(), trim)
		caps.recordAlignRange(p, as, ae)
		return true
	}
	return matchChildren(pt, p, t, patSrc, src, caps)
}

// matchChildren walks ALL children of p and t — anonymous tokens included —
// handling seq-shadow children with greedy backtracking when followed by more
// pattern siblings. Comparing anonymous tokens is what makes a pattern
// constrain in both directions: an operator, modifier or declaration keyword
// in the PATTERN excludes targets that lack it, and the same token in the
// SOURCE excludes patterns that do not carry it.
//
// One exception is applied HERE: the grammar's declared pure-layout tokens,
// dropped by allChildren from BOTH lists before alignment. Both sides matter
// equally: a one-line pattern must reach a multi-line body, and a multi-line
// pattern must reach a one-line body. Applying the drop in the single call both
// sides go through is what makes the two lists provably comparable — a skip
// applied on one side only would fix one direction and corrupt the other. The
// OTHER exception, declared comment kinds, is NOT applied here: it is a
// target-side-only skip that matchSiblings performs on its ordinary path, so a
// sequence shadow still consumes comments verbatim from the raw target list.
func matchChildren(pt *PatternTree, p, t *sitter.Node, patSrc, src []byte, caps *Captures) bool {
	layout := pt.LangCfg.LayoutTokens
	return matchSiblings(pt, p, allChildren(p, patSrc, layout), 0, allChildren(t, src, layout), patSrc, src, caps)
}

// matchSiblings aligns the ordered children of pattern and target. pChildren
// is the not-yet-consumed tail of pParent's child list and pIdx is the index of
// pChildren[0] among ALL of pParent's children — the coordinates a sequence
// position needs to ask the grammar for its field name. Each iteration consumes
// one pattern child and zero-or-more target children (the latter only for
// seq-shadow positions).
//
// A promotable sequence position that fails to align falls back to the ordinary
// node match below; the reverse fallback deliberately does not exist. Both
// directions are argued in seq_shadow.go.
func matchSiblings(
	pt *PatternTree,
	pParent *sitter.Node,
	pChildren []*sitter.Node,
	pIdx int,
	tChildren []*sitter.Node,
	patSrc, src []byte,
	caps *Captures,
) bool {
	comments := pt.LangCfg.CommentKinds
	if len(pChildren) == 0 {
		// The pattern is exhausted. It still matches when every remaining target
		// child is an ignorable comment — this is what admits a TRAILING comment
		// inside a literal body. Verify before recording so a doomed tail leaks
		// no span into a caps a later sibling might still be reusing.
		for _, tc := range tChildren {
			if len(comments) == 0 || !isIgnorableComment(tc, comments) {
				return false
			}
		}
		for _, tc := range tChildren {
			caps.recordComment(tc)
		}
		return true
	}
	pHead := pChildren[0]
	pRest := pChildren[1:]

	var tHead *sitter.Node
	if len(tChildren) > 0 {
		tHead = tChildren[0]
	}
	// The seq-chain test runs FIRST and against the RAW target head: chain.promotes
	// reads tHead's kind to decide promotion and matchSeqShadow consumes from the
	// raw list, so the comment skip below must not have touched it yet. A sequence
	// shadow that faces a leading comment therefore still spans it verbatim.
	if chain, ok := findSeqChain(pt, pHead); ok && chain.promotes(pParent, pIdx, tHead) {
		if matchSeqShadow(pt, chain.ph, pParent, pRest, pIdx+1, tChildren, patSrc, src, caps) {
			// The promotion succeeded, so pHead's own tokens — everything in
			// its span outside the placeholder leaf — were never compared
			// against anything and earn no alignment entry. Record them, so the
			// splice can tell a template token repeating one of them from a
			// token the caller wrote. Recorded only on success: a try that
			// fails here falls through to the ordinary-node reading below, and
			// a span from an abandoned try would license a deletion.
			caps.recordDropped(pHead, chain)
			return true
		}
		if chain.depth == 0 {
			// The pattern child IS the placeholder leaf — there is no
			// ordinary-node reading of it to fall back to.
			return false
		}
	}
	// ORDINARY PATH ONLY, reached once the sequence branch has declined: advance
	// past leading target-side comments a comment-free pattern did not ask for,
	// recording each source span so the splice preserves it. A MEANINGFUL extra —
	// a #region, a heredoc body — is not in CommentKinds and so is not skipped
	// here; it still constrains the alignment, which is the whole point of the
	// declared list over a blanket IsExtra predicate.
	for len(comments) > 0 && len(tChildren) > 0 && isIgnorableComment(tChildren[0], comments) {
		caps.recordComment(tChildren[0])
		tChildren = tChildren[1:]
	}
	if len(tChildren) == 0 {
		return false
	}
	if !matchNode(pt, pHead, tChildren[0], patSrc, src, caps) {
		return false
	}
	return matchSiblings(pt, pParent, pRest, pIdx+1, tChildren[1:], patSrc, src, caps)
}

// lookupPlaceholder returns the Placeholder bound to p's byte range in pt.
// Only LEAF nodes (NamedChildCount == 0) hit — tree-sitter sometimes
// wraps a placeholder identifier in zero-width container kinds
// (parameter_declaration around a single type_identifier, expression_list
// around a single identifier in a return slot) which share the inner
// node's outer byte range. Limiting hits to leaves keeps the walker from
// confusing those wrappers with the placeholder itself; the
// effectivePatternNode descent in matchNode handles the wrapper-strip
// before the lookup.
func lookupPlaceholder(pt *PatternTree, p *sitter.Node) (Placeholder, bool) {
	if pt == nil || p == nil {
		return Placeholder{}, false
	}
	if p.NamedChildCount() != 0 {
		return Placeholder{}, false
	}
	ph, ok := pt.Placeholders[byteRange{Start: p.StartByte(), End: p.EndByte()}]
	return ph, ok
}

// allChildren returns every child of n as a slice — named children AND the
// anonymous tokens between them (parens, commas, braces, operators, modifier
// and declaration keywords). The matcher compares them all, so an anonymous
// token is as load-bearing as a named child.
//
// The exception is layout: a child matching one of the grammar's declared
// LayoutTokens is dropped, because it records how the source was spelled
// rather than what it says. src is the byte source n was parsed from — the
// pattern's SubstitutedSource on the pattern side, the target file's bytes on
// the target side. Almost every grammar declares no layout token at all and
// pays one length check for the whole list.
func allChildren(n *sitter.Node, src []byte, layout []string) []*sitter.Node {
	if n == nil {
		return nil
	}
	count := int(n.ChildCount())
	if count == 0 {
		return nil
	}
	out := make([]*sitter.Node, 0, count)
	for i := range count {
		c := n.Child(i)
		if len(layout) > 0 && isLayoutToken(c, src, layout) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// isLayoutToken reports whether c is an anonymous token whose source text the
// grammar declared as pure layout. Named nodes and interior nodes can never
// qualify: layout is a property of a terminal the parse surfaced, and a node
// with children carries whatever its children carry.
func isLayoutToken(c *sitter.Node, src []byte, layout []string) bool {
	if c == nil || c.IsNamed() || c.ChildCount() != 0 {
		return false
	}
	return slices.Contains(layout, c.Content(src))
}

// isIgnorableComment reports whether c is one of the grammar's declared comment
// kinds — the ordinary-path mirror of isLayoutToken, and the difference is the
// point. isLayoutToken requires !c.IsNamed() because layout is a property of an
// anonymous terminal; isIgnorableComment requires c.IsNamed() because every
// comment kind in every one of the 21 grammars is a NAMED node, which is exactly
// why LayoutTokens structurally cannot reach them. The kind is compared against
// the DECLARED LIST, never the source text and NEVER c.IsExtra(): IsExtra is
// true for meaningful extras too (heredoc bodies, preprocessor regions), and
// skipping those would corrupt a match — the declared list is what separates a
// comment from meaning. The guards are ordered cheapest-first so a non-comment
// child pays only the length check and IsNamed() before the slices.Contains.
func isIgnorableComment(c *sitter.Node, comments []string) bool {
	if len(comments) == 0 || c == nil || !c.IsNamed() {
		return false
	}
	return slices.Contains(comments, c.Type())
}

// nodeToCapture builds a Capture from a single tree-sitter node: text,
// kind, line, byte range. Sequence captures call bindSeq, which builds
// the outer-span Capture itself and uses nodeToCapture only for child
// entries.
func nodeToCapture(n *sitter.Node, src []byte) Capture {
	return Capture{
		Text:      n.Content(src),
		Kind:      n.Type(),
		Line:      int(n.StartPoint().Row) + 1,
		StartByte: n.StartByte(),
		EndByte:   n.EndByte(),
	}
}
