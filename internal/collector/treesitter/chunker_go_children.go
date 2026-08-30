// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// Direct-named-child scans over a Go parse node, shared by the three Go arms.
//
// THEY ALL RETURN COUNTS OR NODES, NEVER SLICES, and that is the point of the
// file. Each replaced a helper that collected matching children into a fresh
// []*sitter.Node so the caller could take its length, index [0], or range it —
// one allocation per call, on paths the qualifier arm walks for every parameter
// of every declaration and the flow arm walks for every short declaration,
// assignment, call and return in every body. Re-reading a child by index is
// free: the tree memoizes each node's Go wrapper on first access
// (Tree.cachedNode), so the second read returns the same pointer.
//
// They live in their own file because all three arms call them — the qualifier
// binder, the flow-step walker, and (through goShortVarSides) both — so filing
// them under any one arm would misplace them.

// goTwoNamedChildrenOfKind returns a node's first two direct named children of
// one kind class, and declines — returning two nils — unless there are EXACTLY
// two.
//
// THE EXACT-TWO RULE IS THE CALLERS' DECLINE RULE, not a convenience. Both the
// qualifier binder's `:=` form and the flow arm's assignment form read a
// left-hand and a right-hand expression_list, and a THIRD list means the shape
// is not the one either reads. Binding the first two of three would be a
// confident wrong answer where declining is an honest one, and it is what the
// slice form both callers used to build enforced with `len(lists) != 2`.
func goTwoNamedChildrenOfKind(node *sitter.Node, kind uint8) (first, second *sitter.Node) {
	classes := goKinds()
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		if classes.class(child.Symbol()) != kind {
			continue
		}
		switch {
		case first == nil:
			first = child
		case second == nil:
			second = child
		default:
			return nil, nil
		}
	}
	if second == nil {
		return nil, nil
	}
	return first, second
}

// goFirstNamedChildOfKind returns a node's first direct named child of one kind
// class, or nil when it has none — the slice-free reading of a caller that built
// the whole list only to index [0] after a len check.
func goFirstNamedChildOfKind(node *sitter.Node, kind uint8) *sitter.Node {
	classes := goKinds()
	for i := range int(node.NamedChildCount()) {
		if child := node.NamedChild(i); classes.class(child.Symbol()) == kind {
			return child
		}
	}
	return nil
}

// goCountNamedChildrenOfKind counts a node's direct named children of one kind
// class.
func goCountNamedChildrenOfKind(node *sitter.Node, kind uint8) int {
	classes := goKinds()
	count := 0
	for i := range int(node.NamedChildCount()) {
		if classes.class(node.NamedChild(i).Symbol()) == kind {
			count++
		}
	}
	return count
}

// goEachNamedChildOfKind visits a node's direct named children of one kind
// class, passing each its ORDINAL AMONG THAT KIND rather than its child index —
// which is the index the slice form's `for i, name := range names` handed out,
// and the one the right-hand expression list is paired by.
func goEachNamedChildOfKind(node *sitter.Node, kind uint8, visit func(ordinal int, child *sitter.Node)) {
	classes := goKinds()
	ordinal := 0
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		if classes.class(child.Symbol()) != kind {
			continue
		}
		visit(ordinal, child)
		ordinal++
	}
}
