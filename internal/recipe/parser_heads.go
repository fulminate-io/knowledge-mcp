// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"sort"
	"strings"
)

// parser_heads.go holds the parse-time validation of BARE field-path heads.
//
// It is a narrow, deliberate reversal of the rest of the DSL's fail-soft
// design, and the narrowness is the point. A recipe that reads a type it never
// selected — asking for a page while selecting a section — evaluates to empty
// at every row and emits nodes with blank fields, which looks like a source
// graph with no content rather than a recipe with a typo. That failure is
// silent, and it is silent in the one place an author cannot inspect: the
// recipe body lives in the graph, not in a file they can open.
//
// UNKNOWN METADATA KEYS STAY FAIL-SOFT. The reversal is HEADS ONLY. A head that
// is legal followed by a key nothing carries still parses and still evaluates
// to empty, because source graphs legitimately differ in which keys they stamp
// and a recipe written against one should degrade rather than refuse.

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
		return checkExprHeads(r.Where, scope)
	case RuleTraverse:
		scope.add(r.As)
		return nil
	case RuleFilter:
		return checkExprHeads(r.Pred, scope)
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
