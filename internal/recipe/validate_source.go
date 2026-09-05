// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// validate_source.go — everything a recipe names, checked against the loaded
// source graph BEFORE the row loop.
//
// WHY BEFORE THE LOOP, measured rather than argued: every expression in this
// interpreter is evaluated per row, so a rowset of ZERO evaluates nothing and
// reports nothing. One typo in a select type therefore silences every other
// mistake in the recipe — an unknown builtin and an uncompilable regex both
// returned rows=0/0 with no error. Validating once, up front, is what makes each
// of them a message an author can repair from.
//
// THE COMPARISON IS EXACT-CASE EVERYWHERE AND NOTHING HERE FOLDS. `contains` is
// refused against a graph carrying only `CONTAINS`. The census's suggest helper
// runs a case-insensitive pass to WORD the near-miss, and that pass is prose
// only: it is never consulted to decide membership, and nothing in this file
// calls it for anything but the message.
//
// Cost: one pass over the rule list reading the memoized census. Nothing here
// runs per row and nothing here issues an Execute RPC.

// sourceViolation is one refusal, carrying the position it is ordered by.
type sourceViolation struct {
	pos Position
	msg string
}

// sourceValidator collects every violation across the whole rule list.
//
// IT COLLECTS RATHER THAN RETURNING ON THE FIRST, and that is a correctness
// property rather than a courtesy. RuleEmit.Fields is a Go map and Go randomizes
// map iteration, so a first-error-wins census over an emit carrying two
// offending arguments names one of them AT RANDOM — measured at roughly 425 to
// 75 over 500 runs of identical input. A refusal that varies run to run cannot
// be asserted by any gate, and an unassertable refusal cannot be gated at all.
// It also serves the author better: every site to repair arrives in one pass.
type sourceValidator struct {
	sv         *sourceView
	census     *sourceCensus
	violations []sourceViolation

	// compiled collects this run's where-tree regexes, keyed by the leaf each
	// belongs to. It is handed to the Env so the evaluator can read it without
	// the cached, shared AST ever being written to.
	compiled map[*MatchesLeaf]*regexp.Regexp

	// compares collects this run's resolved compare leaves — operator and parsed
	// operand — keyed by the leaf each belongs to, and is handed to the Env on
	// the same terms and for the same reason as compiled.
	compares map[*CompareLeaf]compareResolution
}

// validateAgainstSource refuses a recipe that names anything the loaded source
// graph does not carry, before any row exists.
//
// THE SIGNATURE TAKES NO GRAPH IDENTITY, and that is decided rather than
// omitted: the graph's type and name travel on the sourceView, set by
// loadSourceView, so the vocabulary and the identity of the graph it was drawn
// from cannot drift apart — and Interpret's signature stays unchanged.
//
// THE VOCABULARY IS REACHED ONLY THROUGH sv.census(). No function here ranges
// byType, outEdges, inEdges or byID to answer a membership question. A validator
// that walked the index maps inline at each site would return identical correct
// answers on every behavioral test while re-walking the whole graph once per
// site; the census walk counter is what makes that difference countable.
func validateAgainstSource(r *Recipe, sv *sourceView) (
	map[*MatchesLeaf]*regexp.Regexp, map[*CompareLeaf]compareResolution, error,
) {
	if r == nil || sv == nil {
		return nil, nil, nil
	}
	v := &sourceValidator{
		sv:       sv,
		census:   sv.census(),
		compiled: map[*MatchesLeaf]*regexp.Regexp{},
		compares: map[*CompareLeaf]compareResolution{},
	}
	for _, rule := range r.Rules {
		v.checkRule(rule)
	}
	if err := v.result(); err != nil {
		return nil, nil, err
	}
	return v.compiled, v.compares, nil
}

// result renders the collected violations as one error, ordered by line, then
// column, then message text.
//
// THE MESSAGE TIEBREAK IS NOT DECORATION. Two violations at the same position —
// which is exactly what two mis-cased arguments inside one emit block are —
// would otherwise swap freely, so a validator that collects everything and
// reports it in map order still names every site while ordering them at random.
// That is a different wrong-but-compiling implementation from first-error-wins,
// and the sort is what rejects it.
func (v *sourceValidator) result() error {
	if len(v.violations) == 0 {
		return nil
	}
	sort.Slice(v.violations, func(i, j int) bool {
		a, b := v.violations[i], v.violations[j]
		if a.pos.Line != b.pos.Line {
			return a.pos.Line < b.pos.Line
		}
		if a.pos.Col != b.pos.Col {
			return a.pos.Col < b.pos.Col
		}
		return a.msg < b.msg
	})
	parts := make([]string, 0, len(v.violations))
	for _, viol := range v.violations {
		parts = append(parts, fmt.Sprintf("at %d:%d: %s", viol.pos.Line, viol.pos.Col, viol.msg))
	}
	return fmt.Errorf("recipe refused before the walk, checked against %s/%s: %s",
		v.sv.graphType, v.sv.name, strings.Join(parts, "; "))
}

// add records one violation.
func (v *sourceValidator) add(pos Position, format string, args ...any) {
	v.violations = append(v.violations, sourceViolation{pos: pos, msg: fmt.Sprintf(format, args...)})
}

// refuseVocabulary records the standard refusal for a value outside one of the
// three source vocabularies.
//
// It carries, in order: the offending value, the graph it was checked against
// with its node count, the near-miss clause when one exists, why the run was
// refused BEFORE the walk instead of answered with zero rows, the observed
// vocabulary sorted, and where that vocabulary came from. Each part exists
// because the recipe body lives in the graph rather than in a file the author
// can open, so the message is the only repair surface they have.
func (v *sourceValidator) refuseVocabulary(pos Position, site, kind, value string) {
	observed, _ := v.census.vocabulary(kind)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s: %s %q is not in the source graph %s/%s (%d nodes)",
		site, kind, value, v.sv.graphType, v.sv.name, len(v.sv.byID))
	clause := v.census.suggest(kind, value)
	if clause != "" {
		fmt.Fprintf(&sb, "; %s", clause)
	}
	// The exact-casing sentence is added for edge types because the measured
	// failure is a casing mismatch, and an author told only "unknown edge type
	// contains" while the graph carries CONTAINS will not see it. The
	// case-differing suggestion already says so, so it is not repeated.
	if kind == censusEdgeType && !strings.Contains(clause, "matched exactly") {
		sb.WriteString("; edge types are matched exactly, including case")
	}
	fmt.Fprintf(&sb, ". The run was refused before the walk rather than answered with zero rows. Observed %ss in the loaded source graph: %s",
		kind, renderVocabulary(observed))
	v.violations = append(v.violations, sourceViolation{pos: pos, msg: sb.String()})
}

// renderVocabulary quotes and joins an observed vocabulary, or says plainly
// that the graph carries none.
func renderVocabulary(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	quoted := make([]string, 0, len(values))
	for _, val := range values {
		quoted = append(quoted, fmt.Sprintf("%q", val))
	}
	return strings.Join(quoted, ", ")
}

// checkRule censuses one rule.
//
// WHAT IS DELIBERATELY NOT CENSUSED, because this census describes the SOURCE
// graph only: RuleEmit.NodeType and RuleLookup.NodeType are TARGET-graph types,
// RuleLink.Rel is a target-graph edge type, and an emit field NAME is a name in
// the target node rather than a value read from the source. Applying the source
// census to any of them would refuse correct recipes.
func (v *sourceValidator) checkRule(rule Rule) {
	switch r := rule.(type) {
	case RuleSelect:
		if !contains(v.census.nodeTypes, r.NodeType) {
			v.refuseVocabulary(r.Pos, "select", censusNodeType, r.NodeType)
		}
		v.checkWhereTree(r.Where, r.Pos)
	case RuleTraverse:
		if !contains(v.census.edgeTypes, r.EdgeType) {
			v.refuseVocabulary(r.Pos, "traverse", censusEdgeType, r.EdgeType)
		}
	case RuleWalk:
		// The twin of the arm above. The default arm below would now catch a
		// RuleWalk that fell through, but as a MISSING ARM rather than censusing
		// the edge type — so this arm is what makes `walk contains` on a CONTAINS
		// graph a named refusal instead of zero rows with no error.
		if !contains(v.census.edgeTypes, r.EdgeType) {
			v.refuseVocabulary(r.Pos, "walk", censusEdgeType, r.EdgeType)
		}
	case RuleFilter:
		v.checkWhereTree(r.Where, r.Pos)
	case RuleBind:
		v.checkExpr(r.Value)
	case RuleGroupBy:
		v.checkExpr(r.Key)
	case RuleEmit:
		// Sorted, because Fields is a Go map: an unsorted walk would produce the
		// same set of violations in a different order every run.
		for _, name := range sortedKeys(r.Fields) {
			v.checkExpr(r.Fields[name])
		}
	case RuleLookup:
		v.checkExpr(r.Identity)
	case RuleLink:
		v.checkExpr(r.From)
		v.checkExpr(r.To)
	case RuleSourceRef:
		v.checkExpr(r.Ref)
	default:
		// UNREACHABLE FOR EVERY RULE TYPE THAT EXISTS, and that is the point: each
		// of the ten declared above has an arm, so this fires only for a rule type
		// added later whose author wired the dispatch and forgot the census.
		//
		// THE DISPATCHER'S OWN FALLTHROUGH CANNOT COVER THIS. dispatchRule ends in
		// an "unknown rule type" error, but that fires when the DISPATCH arm is
		// missing — the one arm nobody forgets, because without it the rule does
		// nothing at all and is noticed on the first run. The shape that actually
		// ships is the opposite one: a rule that evaluates correctly and is never
		// censused, so its edge type escapes the vocabulary check and a mis-cased
		// or absent type answers zero rows in silence. That is how the walk rule
		// shipped, and closing it here makes the class unreachable rather than
		// merely detectable.
		v.add(r.Position(), "rule type %T has no source-census arm in checkRule, so nothing it "+
			"names was checked against the source graph. Add an arm — censusing its edge type "+
			"if it carries one, its expressions if it reads any, and an empty arm if it "+
			"legitimately reads nothing from the source", r)
	}
}

// checkWhereTree censuses a where-tree: node types on kind leaves, edge types on
// both walk leaves, and every `of` field path. It also runs the tree's ONE regex
// compile pass and its ONE compare-leaf resolve pass, so every matches leaf is
// compiled and every compare leaf is resolved before the row loop and the
// evaluator never has to do either.
func (v *sourceValidator) checkWhereTree(w *WhereNode, pos Position) {
	if w == nil {
		return
	}
	if err := compileWhereTree(w, pos, v.compiled); err != nil {
		v.add(pos, "%s", err.Error())
	}
	v.checkWhereNode(w, pos)
}

// checkWhereNode walks the tree without re-running the compile pass.
func (v *sourceValidator) checkWhereNode(w *WhereNode, pos Position) {
	if w == nil {
		return
	}
	for _, child := range w.All {
		v.checkWhereNode(child, pos)
	}
	for _, child := range w.Any {
		v.checkWhereNode(child, pos)
	}
	v.checkWhereNode(w.Not, pos)

	if w.Kind != nil {
		for _, want := range w.Kind.Is {
			if !contains(v.census.nodeTypes, want) {
				v.refuseVocabulary(pos, "kind leaf", censusNodeType, want)
			}
		}
	}
	for _, path := range whereNodeOwnPaths(w) {
		v.checkFieldPath(path, pos, "where-tree")
	}
	for name, leaf := range map[string]*EdgeLeaf{"ancestor leaf": w.Ancestor, "descendant leaf": w.Descendant} {
		if leaf == nil {
			continue
		}
		if !contains(v.census.edgeTypes, leaf.Edge) {
			v.refuseVocabulary(pos, name, censusEdgeType, leaf.Edge)
		}
		v.checkWhereNode(leaf.Where, pos)
	}
	if w.Compare != nil {
		v.checkCompareLeaf(w.Compare, pos)
	}
}

// checkCompareLeaf resolves one compare leaf's operator and parses its literal
// operand, BEFORE any row exists, recording the result for the run's evaluator.
//
// IT RECORDS VIOLATIONS RATHER THAN RETURNING ON THE FIRST, because
// sourceValidator collects: a recipe carrying a bad operator AND a mis-cased
// edge type must report both, in one deterministic pass, and an early return
// here would hide the second.
//
// The refusals name the offender, the near-miss clause when there is one, that
// the run was refused BEFORE the walk rather than answered with zero rows, and
// the admitted operators — every part of which exists because the recipe body
// lives in the graph rather than in a file the author can open.
func (v *sourceValidator) checkCompareLeaf(leaf *CompareLeaf, pos Position) {
	op, ok := compareOp(leaf.Op)
	if !ok {
		msg := fmt.Sprintf("compare leaf on %q: operator %q is not one this leaf applies", leaf.Of, leaf.Op)
		if clause := suggestCompareOp(leaf.Op); clause != "" {
			msg += "; " + clause
		}
		msg += fmt.Sprintf(". The run was refused before the walk rather than answered with zero rows. "+
			"Admitted operators: %s", joinPlain(compareOpVocabulary()))
		v.add(pos, "%s", msg)
		return
	}
	value, err := parseNumericOperand(leaf.Value)
	if err != nil {
		v.add(pos, "compare leaf on %q: operand %q is not a number, and nothing is coerced or trimmed: %v. "+
			"The run was refused before the walk rather than answered with zero rows. Admitted operators: %s",
			leaf.Of, leaf.Value, err, joinPlain(compareOpVocabulary()))
		return
	}
	v.compares[leaf] = compareResolution{op: op, value: value}
	compareLiteralParses.Add(1)
}

// checkExpr censuses one expression tree's field paths, recursing exactly where
// checkExprHeads already does so the two cannot disagree about what a recipe
// reads. Builtin names, arity, literal regexes and literal edge-type arguments
// are checked by expr_static_validate.go, reached from here.
//
// It takes NO position parameter, and that is deliberate rather than an
// omission: every diagnostic below is reported at the EXPRESSION's own Pos,
// which parser_expr.go stamps from the head token on every ExprField, ExprRegex
// and ExprFunc it builds. A rule-level position threaded in alongside would be
// a second, coarser answer to the same question that nothing ever consults —
// which is exactly what it had become, and what unparam reported.
func (v *sourceValidator) checkExpr(e Expr) {
	switch x := e.(type) {
	case nil:
		return
	case ExprField:
		v.checkFieldPath(strings.Join(x.Path, "."), x.Pos, "field")
	case ExprRegex:
		v.checkExpr(x.LHS)
		v.checkLiteralRegex(x.Pattern, x.Pos, "regex literal")
	case ExprFunc:
		v.checkFunc(x)
		for _, arg := range x.Args {
			v.checkExpr(arg)
		}
	}
}

// contains reports membership EXACTLY, with no case folding anywhere on this
// path. It is the accept/reject decision the whole ticket turns on.
func contains(vocabulary []string, value string) bool {
	return slices.Contains(vocabulary, value)
}

// sortedKeys renders a field map's keys deterministically.
func sortedKeys(fields map[string]Expr) []string {
	out := make([]string, 0, len(fields))
	for name := range fields {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
