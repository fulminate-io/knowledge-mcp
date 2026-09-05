// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"sort"
	"strings"
)

// parser_heads.go holds the parse-time validation of BARE field-path heads.
//
// A recipe that reads a type it never selected — asking for a page while
// selecting a section — would evaluate to empty at every row and emit nodes
// with blank fields, which looks like a source graph with no content rather
// than a recipe with a typo. That failure is silent, and it is silent in the
// one place an author cannot inspect: the recipe body lives in the graph, not
// in a file they can open.
//
// HEAD LEGALITY IS CHECKED HERE BECAUSE IT NEEDS NO SOURCE GRAPH. It is
// decidable from the recipe text alone — the legal set comes from the rules
// that precede the read — so it belongs at parse time, where recipe.Parse
// alone rejects it and every documentation gate that parses a shipped example
// catches one for free.
//
// THE REST OF THE VOCABULARY IS CHECKED AGAINST THE LOADED SOURCE GRAPH, in
// validate_source.go, before the row loop: node types, edge types on all three
// of their carriers, and metadata keys on every field path. Unknown metadata
// keys no longer read as empty. The two halves are findable from each other on
// purpose — head legality here, corpus vocabulary there, and nothing in the DSL
// degrades to an empty result for a value the author got wrong.

// bareHeadScope tracks the heads that are legal at the current point of a
// recipe. A select RESETS it; a traverse binding ADDS to it.
type bareHeadScope struct {
	legal    map[string]bool
	selected bool
}

// universalHeads are legal under every select.
//
// `group` is the group-by rule's own pseudo-variable namespace. `node` is this
// DSL's spelling for "the current row" regardless of its type, so it names no
// type at all — which is why outlawing it would be the rule reaching past its
// purpose. Both appear in the error message below, so both are vocabulary an
// operator repairs a recipe from.
var universalHeads = []string{"group", "node"}

// newBareHeadScope starts with nothing legal: a bare head before any select has
// no row to be a field of.
func newBareHeadScope() *bareHeadScope {
	return &bareHeadScope{legal: map[string]bool{}}
}

// reset installs the legal set a select establishes.
func (s *bareHeadScope) reset(nodeType string) {
	s.legal = map[string]bool{nodeType: true}
	for _, h := range universalHeads {
		s.legal[h] = true
	}
	s.selected = true
}

// add admits a traverse alias.
func (s *bareHeadScope) add(alias string) {
	if alias != "" {
		s.legal[alias] = true
	}
}

// sorted renders the legal set for the error message, deterministically.
func (s *bareHeadScope) sorted() []string {
	out := make([]string, 0, len(s.legal))
	for h := range s.legal {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// validateBareHeads walks a parsed recipe in rule order and rejects any bare
// field-path head that is not legal at that point.
//
// It returns the parser's own error type so callers still handle exactly one,
// and it is called from the grammar entry rather than from Parse so Parse's
// signature is untouched.
func validateBareHeads(r *Recipe) *ParseError {
	if r == nil {
		return nil
	}
	scope := newBareHeadScope()
	for _, rule := range r.Rules {
		if err := validateRuleHeads(rule, scope); err != nil {
			return err
		}
	}
	return nil
}

// validateRuleHeads validates one rule's expressions and applies that rule's
// effect on the scope.
//
// A select RESETS the scope BEFORE its own Where is validated: the where-expr
// is evaluated against candidate rows of the newly selected type, so it reads
// that type's fields and not the previous one's.
func validateRuleHeads(rule Rule, scope *bareHeadScope) *ParseError {
	switch r := rule.(type) {
	case RuleSelect:
		scope.reset(r.NodeType)
		return checkWhereTreeHeads(r.Where, scope, r.Pos)
	case RuleTraverse:
		scope.add(r.As)
		// A traverse is what puts an edge under the row, so it is what makes
		// `edge` legal. It is added HERE rather than to universalHeads because a
		// select establishes rows that walked no edge: `select block where
		// {"compare":{"of":"edge.position",…}}` reads an edge that does not
		// exist, and scope.reset dropping the head is what refuses it — with no
		// source graph at all, so every documentation parse gate catches one.
		scope.add(edgeHead)
		return nil
	case RuleWalk:
		// Both the alias and the rule's own pseudo-variable namespace become
		// legal heads: `walk.depth` and `walk.position` are row-scoped values
		// this rule stamps, not fields of any node.
		scope.add(r.As)
		scope.add("walk")
		// A WALK IS AN EDGE STEP, so it makes `edge` legal on the same terms a
		// traverse does — every walked row was reached along exactly one edge of
		// the named type, and evalWalk carries it. Without this the head is
		// dropped by the preceding select's reset and `edge.position` on a walked
		// row is refused at parse time, which is how this was found.
		scope.add(edgeHead)
		return nil
	case RuleFilter:
		return checkWhereTreeHeads(r.Where, scope, r.Pos)
	case RuleBind:
		return checkExprHeads(r.Value, scope)
	case RuleGroupBy:
		return checkExprHeads(r.Key, scope)
	case RuleEmit:
		// Fields is a Go map, so the keys are walked in SORTED order — without
		// that, the same bad recipe would name a different offending head from
		// one run to the next.
		names := make([]string, 0, len(r.Fields))
		for name := range r.Fields {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := checkExprHeads(r.Fields[name], scope); err != nil {
				return err
			}
		}
		return nil
	case RuleLookup:
		return checkExprHeads(r.Identity, scope)
	case RuleLink:
		if err := checkExprHeads(r.From, scope); err != nil {
			return err
		}
		return checkExprHeads(r.To, scope)
	case RuleSourceRef:
		return checkExprHeads(r.Ref, scope)
	}
	return nil
}

// checkWhereTreeHeads validates every bare head a where-tree reads, recursing
// through composers and into both edge leaves' sub-trees.
//
// THE SUB-TREE OF AN ancestor OR descendant LEAF HAS ITS OWN SCOPE, AND `node`
// IS THE ONLY BARE HEAD LEGAL IN IT. That asymmetry is deliberate and is the one
// place a sub-tree's scope differs from its parent's: the row a sub-tree tests
// is a WALKED NEIGHBOR, not the selected row, so the select type and every
// traverse alias name something the neighbor is not. A reader who "simplifies"
// this by passing the parent scope down re-admits `section.symbol_name` inside
// an ancestor leaf, where it reads a field of the wrong row and evaluates empty
// on every neighbor. A `$var` reference stays legal there — the sigil check in
// checkFieldHead skips it — and resolves against the outer row's bindings, which
// the evaluator carries into the synthetic neighbor row.
//
// pos is the position of the RULE carrying the tree. The where-tree is decoded
// by encoding/json, which reports offsets into its own payload rather than
// recipe line:col, so the rule's own position is the finest one available; the
// collected refusal's message tiebreak is what keeps two violations at the same
// position deterministically ordered.
func checkWhereTreeHeads(w *WhereNode, scope *bareHeadScope, pos Position) *ParseError {
	if w == nil {
		return nil
	}
	for _, child := range w.All {
		if err := checkWhereTreeHeads(child, scope, pos); err != nil {
			return err
		}
	}
	for _, child := range w.Any {
		if err := checkWhereTreeHeads(child, scope, pos); err != nil {
			return err
		}
	}
	if err := checkWhereTreeHeads(w.Not, scope, pos); err != nil {
		return err
	}

	ofs := whereNodeOwnPaths(w)
	for _, of := range ofs {
		if err := checkFieldHead(ExprField{Path: splitFieldPath(of), Pos: pos}, scope); err != nil {
			return err
		}
	}

	for _, leaf := range []*EdgeLeaf{w.Ancestor, w.Descendant} {
		if leaf == nil {
			continue
		}
		// `edge` is legal in the sub-tree and names the WALKED edge — the one this
		// leaf stepped along to reach the candidate — shadowing any outer
		// traverse's, because the candidate is a different row reached along a
		// different edge. That is why it is admitted here rather than inherited:
		// the sub-tree's scope is built fresh, and the walk leaf is itself an
		// edge step.
		neighborScope := &bareHeadScope{legal: map[string]bool{"node": true, edgeHead: true}, selected: true}
		if err := checkWhereTreeHeads(leaf.Where, neighborScope, pos); err != nil {
			return err
		}
	}
	return nil
}

// whereNodeOwnPaths returns the `of` values carried by this node's OWN leaves,
// in a fixed order, without descending into composers or edge sub-trees.
//
// It is shared by the head validator here and by the source-vocabulary
// validator, so the two cannot disagree about which paths a where-tree reads.
func whereNodeOwnPaths(w *WhereNode) []string {
	if w == nil {
		return nil
	}
	var out []string
	if w.Kind != nil {
		out = append(out, w.Kind.Of)
	}
	if w.Matches != nil {
		out = append(out, w.Matches.Of)
	}
	if w.Equals != nil {
		out = append(out, w.Equals.Of)
	}
	if w.Exists != nil {
		out = append(out, w.Exists.Of)
	}
	if w.Compare != nil {
		out = append(out, w.Compare.Of)
	}
	return out
}

// splitFieldPath splits a where-tree `of` value on "." into the same shape
// ExprField.Path carries and evalField consumes, so one reader serves both
// grammars.
func splitFieldPath(of string) []string {
	return strings.Split(of, ".")
}

// checkExprHeads validates one expression tree.
//
// The recursion into call arguments and regex left-hand sides is not optional:
// saved recipes bind regex expressions over bare heads and nest calls inside
// calls with bare heads in sibling arguments, so a validator that only looked
// at top-level expressions would be silent exactly where authors write the
// hardest expressions.
func checkExprHeads(e Expr, scope *bareHeadScope) *ParseError {
	switch x := e.(type) {
	case nil:
		return nil
	case ExprField:
		return checkFieldHead(x, scope)
	case ExprRegex:
		return checkExprHeads(x.LHS, scope)
	case ExprFunc:
		for _, arg := range x.Args {
			if err := checkExprHeads(arg, scope); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

// checkFieldHead rejects one field path's head when it is not legal.
//
// A dollar-prefixed path is a variable reference, not a bare head — the parser
// preserves the sigil on the first segment and the evaluator branches on it.
func checkFieldHead(f ExprField, scope *bareHeadScope) *ParseError {
	if len(f.Path) == 0 {
		return nil
	}
	head := f.Path[0]
	if strings.HasPrefix(head, "$") {
		return nil
	}
	if !scope.selected {
		return &ParseError{
			Line: f.Pos.Line, Col: f.Pos.Col,
			Msg: fmt.Sprintf("field %q has no row to read from: no select rule appears before it", head),
		}
	}
	if scope.legal[head] {
		return nil
	}
	// The message lists the legal set because the recipe body lives in the
	// graph: an operator hitting this has to repair it from the message alone.
	return &ParseError{
		Line: f.Pos.Line, Col: f.Pos.Col,
		Msg: fmt.Sprintf("unknown field head %q; legal heads here are: %s",
			head, strings.Join(scope.sorted(), ", ")),
	}
}
