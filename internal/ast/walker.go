// SPDX-License-Identifier: Apache-2.0

// walker.go — manual ast-grep-style matcher.
//
// matchTree(pat, target, src, caps) recursively compares a compiled
// PatternTree against a target tree-sitter node, dispatching by case:
// kind-equality, named-field children, single-node placeholder bind,
// sequence-placeholder greedy bind, and wildcard. No tree-sitter queries
// at runtime — every match decision is a Go-side AST walk.
//
// Sequence-placeholder semantics:
//
//   - A pattern subtree shaped as "linear chain of single-named-child nodes
//     ending at a substituted seq-placeholder identifier" is treated as a
//     SEQ SHADOW at its parent level. The shadow consumes 0..N consecutive
//     target siblings (greedy match with backtracking when followed by
//     more pattern siblings).
//
//   - The shadow's collected target siblings populate the seq capture's
//     .text (verbatim source slice from start_byte of first matched
//     sibling to end_byte of last matched sibling) and .children (per-
//     sibling Capture views). Empty match (zero siblings) yields .text=""
//     and .children=[].
//
// Wrapper-stripping (Q3): matchTree starts at PatternTree.Root, which
// compilePattern set to the smallest descendant fully covering the user's
// substituted source. That makes positions report against the user's
// pattern, not the wrapper.

package ast

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// Captures is the per-match name → Capture binding accumulator. Sequence
// captures populate Children + StartByte + EndByte; single captures leave
// Children nil.
type Captures struct {
	byName map[string]Capture
}

// newCaptures returns an empty Captures binding accumulator.
func newCaptures() *Captures {
	return &Captures{byName: make(map[string]Capture)}
}

// reset clears all bindings so the Captures can be reused for a new
// match attempt without allocating a new map.
func (c *Captures) reset() {
	for k := range c.byName {
		delete(c.byName, k)
	}
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
//  3. Iterate named children of P and T together with seq-shadow handling.
//
// inherent to the sibling-alignment logic.
//
//nolint:gocognit // four-case dispatch over a small grammar; complexity is
func matchNode(pt *PatternTree, p, t *sitter.Node, patSrc, src []byte, caps *Captures) bool {
	pOrig := p
	p = effectivePatternNode(p)
	if ph, ok := lookupPlaceholder(pt, p); ok {
		switch ph.Kind {
		case KindNode:
			caps.bindNode(ph.Name, effectiveTargetNode(t), src)
			return true
		case KindNodeWild:
			return true
		case KindSeq, KindSeqWild:
			// Seq placeholders should be handled at the parent's child-
			// iteration level. If we reach this case the pattern's outer
			// shape is a bare seq placeholder, which we treat as a single
			// node here (degenerate — bind the whole target as a single-
			// element sequence).
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
	// Leaf nodes (no named children) carry meaning in their text — type
	// names, keywords, literals. Compare verbatim. tree-sitter still walks
	// punctuation and operator tokens as anonymous children of the parent,
	// so leaf-text comparison only matters for structurally-meaningful
	// leaves where the named child count is zero.
	if p.NamedChildCount() == 0 && t.NamedChildCount() == 0 {
		if p.Content(patSrc) != t.Content(src) {
			return false
		}
	}
	return matchChildren(pt, p, t, patSrc, src, caps)
}

// matchChildren walks the named children of p and t, handling seq-shadow
// children with greedy backtracking when followed by more pattern
// siblings.
func matchChildren(pt *PatternTree, p, t *sitter.Node, patSrc, src []byte, caps *Captures) bool {
	pChildren := namedChildren(p)
	tChildren := namedChildren(t)
	return matchSiblings(pt, pChildren, tChildren, patSrc, src, caps)
}

// matchSiblings aligns ordered named children of pattern and target.
// Each iteration consumes one pattern child and zero-or-more target
// children (the latter only for seq-shadow positions).
func matchSiblings(pt *PatternTree, pChildren, tChildren []*sitter.Node, patSrc, src []byte, caps *Captures) bool {
	if len(pChildren) == 0 {
		return len(tChildren) == 0
	}
	pHead := pChildren[0]
	pRest := pChildren[1:]

	if shadow, ok := findSeqShadow(pt, pHead); ok {
		return matchSeqShadow(pt, shadow, pRest, tChildren, patSrc, src, caps)
	}
	if len(tChildren) == 0 {
		return false
	}
	if !matchNode(pt, pHead, tChildren[0], patSrc, src, caps) {
		return false
	}
	return matchSiblings(pt, pRest, tChildren[1:], patSrc, src, caps)
}

// matchSeqShadow greedy-matches 0..len(tChildren) target siblings to the
// seq-shadow placeholder, then recurses on the rest of the pattern. Tries
// the largest k first (greedy) and backs off until either the rest of the
// pattern matches or k reaches -1 (no valid alignment).
func matchSeqShadow(pt *PatternTree, ph Placeholder, pRest []*sitter.Node, tChildren []*sitter.Node, patSrc, src []byte, caps *Captures) bool {
	// Greedy: try consuming as many target children as possible first,
	// then back off. Must leave at least len(pRest) target children to
	// satisfy the rest of the pattern (since each non-seq pattern child
	// consumes at least one target child; if any of pRest is itself a
	// seq-shadow it can consume zero, but the simple lower bound is
	// "at most len(pRest) is reserved").
	maxK := len(tChildren)
	for k := maxK; k >= 0; k-- {
		consumed := tChildren[:k]
		// Snapshot caps in case we need to roll back the seq capture
		// after a failed pRest match.
		saved := saveBinding(caps, ph.Name)
		caps.bindSeq(ph.Name, consumed, src)
		if matchSiblings(pt, pRest, tChildren[k:], patSrc, src, caps) {
			return true
		}
		restoreBinding(caps, ph.Name, saved)
	}
	return false
}

// bindingSnapshot records a Capture under a name plus a "did this binding
// exist before the try" flag. Used by matchSeqShadow to roll back a
// failed try without leaking the speculative seq capture.
type bindingSnapshot struct {
	cap   Capture
	exist bool
}

// saveBinding snapshots a capture entry so a failed try can roll it back.
func saveBinding(caps *Captures, name string) bindingSnapshot {
	if caps == nil || name == "" {
		return bindingSnapshot{}
	}
	prev, ok := caps.byName[name]
	return bindingSnapshot{cap: prev, exist: ok}
}

// restoreBinding reverts the named entry to its pre-try value (or deletes
// it when the pre-try value didn't exist).
func restoreBinding(caps *Captures, name string, saved bindingSnapshot) {
	if caps == nil || name == "" {
		return
	}
	if !saved.exist {
		delete(caps.byName, name)
		return
	}
	caps.byName[name] = saved.cap
}

// seqShadowMaxDepth caps how far findSeqShadow descends through single-
// named-child wrappers to find a seq-placeholder leaf. The cap exists to
// prevent the heuristic from "shadowing" structural container nodes that
// happen to chain to a seq placeholder at deeper levels — e.g.,
// function_declaration's `parameters` child (parameter_list →
// parameter_declaration → placeholder, depth 2) should NOT shadow at
// function_declaration's level because parameter_list is structurally a
// container of parameters, not a seq slot itself. Depth 1 covers the
// "(parameter_declaration containing placeholder)" pattern at
// parameter_list level (1 step from parameter_declaration to the leaf
// placeholder identifier) and the "expression_statement containing
// placeholder" inside a block (1 step). It explicitly does NOT cover the
// 2-step descent from function_declaration's parameter_list child to the
// placeholder — that would over-shadow.
const seqShadowMaxDepth = 1

// findSeqShadow returns the seq placeholder hosted by the subtree rooted
// at p when the subtree is a short linear chain (≤ seqShadowMaxDepth
// single-named-child wrappers) ending at a seq-placeholder identifier.
// Otherwise returns ok=false and the caller treats p as a normal pattern
// node.
//
// The shallow-chain test prevents over-shadowing: at function_declaration
// level we iterate [name, parameters, body] — parameters chains through
// parameter_list → parameter_declaration → placeholder (depth 2), which
// is rejected here. At parameter_list level we iterate
// [parameter_declaration] — depth 1, which IS recognized.
func findSeqShadow(pt *PatternTree, p *sitter.Node) (Placeholder, bool) {
	cur := p
	for depth := 0; cur != nil && depth <= seqShadowMaxDepth; depth++ {
		if ph, ok := lookupPlaceholder(pt, cur); ok {
			if ph.Kind == KindSeq || ph.Kind == KindSeqWild {
				return ph, true
			}
			return Placeholder{}, false
		}
		if cur.NamedChildCount() != 1 {
			return Placeholder{}, false
		}
		cur = cur.NamedChild(0)
	}
	return Placeholder{}, false
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

// namedChildren returns n's named children as a slice. Anonymous
// punctuation (parens, commas, braces) is dropped — structural matching
// operates on named-child shape only.
func namedChildren(n *sitter.Node) []*sitter.Node {
	if n == nil {
		return nil
	}
	count := int(n.NamedChildCount())
	if count == 0 {
		return nil
	}
	out := make([]*sitter.Node, 0, count)
	for i := range count {
		out = append(out, n.NamedChild(i))
	}
	return out
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
