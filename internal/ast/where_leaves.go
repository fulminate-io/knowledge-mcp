// SPDX-License-Identifier: Apache-2.0

// where_leaves.go — leaf DISPATCH for the JSON where-tree.
//
// The leaf BODIES live with the machinery each one needs: the five capture-local
// evaluators beside the scope chain in where.go, evalSubPattern in
// where_subpattern.go, evalFlowsTo in where_flow.go. Only the dispatch that
// ANDs them lives here, so adding a leaf touches this file plus the one that
// owns its body, and where.go stays the scope-and-tree file it already was.

package ast

import "context"

// evalLeaves evaluates the eight leaf operators on where, ANDing their
// verdicts. Empty leaves are no-ops.
//
// THE TWO HALVES SPLIT WHERE THE CODE ITSELF SPLITS, not to spread lines. The
// five leaves in evalLocalLeaves answer a question about the captured node's
// own kind or text and read nothing outside it; the three in evalWalkingLeaves
// each leave the captured node — two walk ancestors or descendants and take a
// ctx for it, and flows_to walks the enclosing declaration's flow steps. Every
// leaf that needs to look beyond its capture is on one side of that line and
// every leaf that does not is on the other.
//
// NEITHER HALF IS TABLE-DRIVEN, deliberately. A slice of bound closures would
// collapse the repeated short-circuit into one copy, but this runs once per
// candidate node per match, so it would trade a fixed allocation per evaluation
// for the duplication of a two-line block.
func evalLeaves(ctx context.Context, where *WhereNode, scope *evalScope) (bool, error) {
	ok, err := evalLocalLeaves(where, scope)
	if err != nil || !ok {
		return ok, err
	}
	return evalWalkingLeaves(ctx, where, scope)
}

// evalLocalLeaves evaluates the five leaves that read only the captured node —
// its kind, its text, or its identity against another capture. Empty leaves are
// no-ops.
func evalLocalLeaves(where *WhereNode, scope *evalScope) (bool, error) {
	if where.Kind != nil {
		ok, err := evalKind(where.Kind, scope)
		if err != nil || !ok {
			return ok, err
		}
	}
	if where.Matches != nil {
		ok, err := evalMatches(where.Matches, scope)
		if err != nil || !ok {
			return ok, err
		}
	}
	if where.Equals != nil {
		ok, err := evalEquals(where.Equals, scope)
		if err != nil || !ok {
			return ok, err
		}
	}
	if where.SameNode != nil {
		ok, err := evalSameNode(where.SameNode, scope)
		if err != nil || !ok {
			return ok, err
		}
	}
	if where.SameText != nil {
		ok, err := evalSameText(where.SameText, scope)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

// evalWalkingLeaves evaluates the three leaves that leave the captured node:
// the two sub-pattern leaves, which search ancestors and descendants, and
// flows_to, which walks the flow steps of the declaration named by its within.
// Empty leaves are no-ops.
func evalWalkingLeaves(ctx context.Context, where *WhereNode, scope *evalScope) (bool, error) {
	if where.InsidePattern != nil {
		ok, err := evalSubPattern(ctx, where.InsidePattern, scope, ancestorSearch)
		if err != nil || !ok {
			return ok, err
		}
	}
	if where.ContainsPattern != nil {
		ok, err := evalSubPattern(ctx, where.ContainsPattern, scope, descendantSearch)
		if err != nil || !ok {
			return ok, err
		}
	}
	if where.FlowsTo != nil {
		ok, err := evalFlowsTo(where.FlowsTo, scope)
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}
