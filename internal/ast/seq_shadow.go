// SPDX-License-Identifier: Apache-2.0

// seq_shadow.go — $$$SEQ sibling-sequence semantics: which pattern child
// becomes a sequence shadow, and how that shadow consumes target siblings.
//
// A $$$SEQ placeholder binds a RUN OF SIBLINGS. The pattern position hosting it
// is rarely the placeholder leaf itself, though: substituting a bare identifier
// into a body or an argument list makes tree-sitter wrap it. `{ $$$B }` parses
// as block → expression_statement → identifier in Go, `def f():\n  $$$B` as
// block → expression_statement → identifier in Python, and `f() { $$$B; }` as
// compound_statement → command → command_name → word in bash. Every sequence
// position is therefore a CHAIN of single-named-child wrappers ending at the
// leaf, and the whole question is which link of the chain the sequence lives at.
//
// THE TWO READINGS. Take the chain's top node W, a child of the pattern node
// whose children are being aligned:
//
//   - PROMOTE W. Drop W and let the placeholder consume W's TARGET SIBLINGS.
//     Right when W is a member of a repeated list: Go's expression_statement
//     inside a block, C's expression_statement inside a compound statement,
//     Go's parameter_declaration inside a parameter list. The target carries no
//     W level of its own — it carries N statements, or N parameters.
//
//   - DESCEND INTO W. Match W as an ordinary node and let the sequence resolve
//     one level lower, among W's own children. Right when W is a structural
//     container the target carries too: an argument list, a Rust block, a Ruby
//     body_statement, a Python block, a Groovy class body. Promoting one of
//     those captures the CONTAINER, delimiters and all — `handler($$$A)`
//     binding "(alpha, beta)" instead of "alpha, beta", and an identity rewrite
//     then emitting a second pair of parens around it.
//
// THE FIRST DISCRIMINATOR IS THE TARGET ITSELF. When the target sibling facing
// W is of a different kind — or there is no target sibling left at all — the
// target simply has no W level here and W is an artifact of substituting an
// identifier into the pattern. C's `{ $$$B; }` parses its body slot as an
// expression_statement while the target holds declarations; JavaScript's
// `class $N { $$$B }` parses as a field_definition while the target holds
// method_definitions. Both promote, and the empty sequence falls out of the
// same test: with no sibling to face, W is promoted and consumes nothing.
//
// THE SECOND DISCRIMINATOR IS THE GRAMMAR'S OWN FIELD NAME, and it settles the
// remaining case — a target sibling of exactly W's kind, where promoting and
// descending are both structurally possible and mean different things.
// tree-sitter names the singular structural slots of a node — body, parameters,
// arguments, consequence — so a field-named child is a slot to descend into and
// an unnamed child is a list member to promote. That is what separates the two
// positions that are otherwise identical from the pattern side: Go's
// expression_statement child of a block is unnamed (promote, so $$$B binds
// statements) while Ruby's body_statement child of a method is field `body`
// (descend, so $$$B binds the statements inside it rather than the body node).
// The name is not a reliable "is this repeated" signal on its own — JavaScript
// names its repeated class members `member` — which is why the target-kind test
// runs first and why the two are read together rather than either alone.
//
// A SECOND, INDEPENDENT GUARD backs it up: a chain whose top node starts before
// its placeholder leaf carries a leading delimiter — an argument list's "(", a
// block's "{" — and is never promoted whatever the grammar calls it. The two
// guards agree on every promoted position and overlap on most refused ones, so
// a grammar that forgets to name a bracketed slot still cannot leak its
// delimiters into a capture.
//
// ORDERED ALTERNATION, ONE WAY ONLY. A promotable position that fails to align
// falls back to matching W as an ordinary node, since a grammar may leave a
// genuine container both unnamed and undelimited. The reverse fallback does NOT
// exist: a field-named slot that fails to align fails the match, because
// falling back there is exactly how a container capture would be reintroduced.
// So a sequence position costs at most two branches, and a pattern has as many
// sequence positions as it has $$$ placeholders — the alternation is bounded by
// the pattern, never by the size of the target tree.

package ast

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// seqChain describes a pattern child that reaches a sequence placeholder
// through a run of single-named-child wrappers.
type seqChain struct {
	// ph is the sequence placeholder the chain ends at.
	ph Placeholder
	// kind is the chain's top node kind — what a facing target sibling is
	// compared against.
	kind string
	// depth counts the wrapper levels between the pattern child and the
	// placeholder leaf. Zero means the child IS the leaf — the one case with
	// no second reading, because a bare placeholder among siblings can only
	// mean those siblings.
	depth int
	// leaf is the placeholder leaf's own pattern-side byte range. The chain
	// top's bytes OUTSIDE it are the tokens a promotion throws away — C's
	// `{ $$$B; }` has a top spanning `<placeholder>;` and a leaf spanning the
	// placeholder, leaving the `;`. Recorded so the splice can tell a dropped
	// pattern token from one an alignment bug merely missed.
	leaf byteRange
	// leadAligned is false when the chain's top node starts before the leaf,
	// i.e. it opens with a delimiter that belongs to a container the target
	// carries in its own right.
	leadAligned bool
}

// findSeqChain reports whether the subtree rooted at p is a run of
// single-named-child wrappers ending at a sequence-placeholder leaf, and
// describes the chain when it is. A non-sequence placeholder, or a node with
// anything other than exactly one named child on the way down, ends the walk
// with ok=false. Patterns carrying no sequence placeholder at all short-circuit
// on the tree's hasSeq flag and pay nothing.
func findSeqChain(pt *PatternTree, p *sitter.Node) (seqChain, bool) {
	if pt == nil || !pt.hasSeq || p == nil {
		return seqChain{}, false
	}
	cur := p
	for depth := 0; cur != nil; depth++ {
		if ph, ok := lookupPlaceholder(pt, cur); ok {
			if ph.Kind != KindSeq && ph.Kind != KindSeqWild {
				return seqChain{}, false
			}
			return seqChain{
				ph:          ph,
				kind:        p.Type(),
				depth:       depth,
				leadAligned: p.StartByte() == cur.StartByte(),
				leaf:        byteRange{Start: cur.StartByte(), End: cur.EndByte()},
			}, true
		}
		if cur.NamedChildCount() != 1 {
			return seqChain{}, false
		}
		cur = cur.NamedChild(0)
	}
	return seqChain{}, false
}

// promotes reports whether the chain's top node should be dropped so the
// placeholder consumes that node's target siblings, rather than matched as an
// ordinary node so the sequence resolves among its children. pParent is the
// pattern node whose children are being aligned and idx is the chain's index
// among ALL of pParent's children — the coordinates the grammar's field name is
// asked for. tHead is the target sibling the chain faces, nil when the target
// has none left. See the file comment for why the two discriminators are read
// in this order.
func (c seqChain) promotes(pParent *sitter.Node, idx int, tHead *sitter.Node) bool {
	if c.depth == 0 {
		return true
	}
	if !c.leadAligned {
		return false
	}
	if tHead == nil || tHead.Type() != c.kind {
		return true
	}
	return pParent == nil || pParent.FieldNameForChild(idx) == ""
}

// matchSeqShadow greedy-matches 0..len(tChildren) target siblings to the
// sequence-shadow placeholder, then recurses on the rest of the pattern. Tries
// the largest k first (greedy) and backs off until either the rest of the
// pattern matches or k reaches -1 (no valid alignment). pParent and pRestIdx
// describe where pRest sits in the pattern so a later sequence position can
// still reach its own field name.
func matchSeqShadow(
	pt *PatternTree,
	ph Placeholder,
	pParent *sitter.Node,
	pRest []*sitter.Node,
	pRestIdx int,
	tChildren []*sitter.Node,
	patSrc, src []byte,
	caps *Captures,
) bool {
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
		// after a failed pRest match. Both append-only accumulators are rolled
		// back by the same mechanism: a rejected try may have aligned some
		// of pRest's literal tokens before failing, and leaving those behind
		// would map one pattern token to two different source ranges — and it
		// may have recorded a dropped span for a nested sequence position that
		// the abandoned try promoted, which would license the splice to consume
		// a template token no surviving promotion ever dropped.
		saved := saveBinding(caps, ph.Name)
		mark := caps.alignMark()
		dmark := caps.dropMark()
		cmark := caps.commentMark()
		caps.bindSeq(ph.Name, consumed, src)
		if matchSiblings(pt, pParent, pRest, pRestIdx, tChildren[k:], patSrc, src, caps) {
			return true
		}
		restoreBinding(caps, ph.Name, saved)
		caps.alignRollback(mark)
		caps.dropRollback(dmark)
		caps.commentRollback(cmark)
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
