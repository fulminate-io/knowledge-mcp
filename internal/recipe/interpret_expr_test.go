// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// seededView builds a small in-memory sourceView for the expression-evaluator
// tests: a section with a child paragraph and an ancestor part, wired with
// contains edges, so field access, regex, has_edge and has_ancestor can all be
// exercised against the same graph.
func seededView() *sourceView {
	part := &knowledgev1.Node{Id: "part1", Type: "section", SymbolName: "Foreword"}
	sec := &knowledgev1.Node{
		Id: "sec1", Type: "section", SymbolName: "Message Router",
		Description: "routes a message to one of several channels",
		Metadata:    map[string]string{"kind": "pattern"},
	}
	para := &knowledgev1.Node{Id: "p1", Type: "paragraph", SymbolName: "body text"}
	return &sourceView{
		byID: map[string]*knowledgev1.Node{"part1": part, "sec1": sec, "p1": para},
		byType: map[string][]*knowledgev1.Node{
			"section":   {part, sec},
			"paragraph": {para},
		},
		outEdges: map[string][]*knowledgev1.Edge{
			"part1": {{FromId: "part1", ToId: "sec1", Type: "contains"}},
			"sec1":  {{FromId: "sec1", ToId: "p1", Type: "contains"}},
		},
		inEdges: map[string][]*knowledgev1.Edge{
			"sec1": {{FromId: "part1", ToId: "sec1", Type: "contains"}},
			"p1":   {{FromId: "sec1", ToId: "p1", Type: "contains"}},
		},
	}
}

func mustEval(t *testing.T, env *Env, row *Row, e Expr, sv *sourceView) string {
	t.Helper()
	v, err := evalExpr(context.Background(), env, row, e, sv)
	require.NoError(t, err)
	return v
}

func TestEvalExpr_FieldAccess(t *testing.T) {
	sv := seededView()
	row := &Row{NodeID: "sec1", Node: sv.byID["sec1"], Vars: map[string]string{}}
	env := newEnv()

	// Bare node name → SymbolName.
	assert.Equal(t, "Message Router",
		mustEval(t, env, row, ExprField{Path: []string{"section"}}, sv))
	// Dotted field → description.
	assert.Equal(t, "routes a message to one of several channels",
		mustEval(t, env, row, ExprField{Path: []string{"node", "description"}}, sv))
	// Metadata key access.
	assert.Equal(t, "pattern",
		mustEval(t, env, row, ExprField{Path: []string{"node", "metadata", "kind"}}, sv))
	// Default-segment metadata access (kgtypes.Value fall-through).
	assert.Equal(t, "pattern",
		mustEval(t, env, row, ExprField{Path: []string{"node", "kind"}}, sv))
}

func TestEvalExpr_Regex(t *testing.T) {
	sv := seededView()
	row := &Row{NodeID: "sec1", Node: sv.byID["sec1"], Vars: map[string]string{}}
	env := newEnv()

	// ~= matches anywhere → returns the match text (truthy).
	got := mustEval(t, env, row, ExprRegex{
		LHS:     ExprField{Path: []string{"node", "symbol_name"}},
		Pattern: "Router",
	}, sv)
	assert.Equal(t, "Router", got)

	// Non-match → "".
	got = mustEval(t, env, row, ExprRegex{
		LHS:     ExprField{Path: []string{"node", "symbol_name"}},
		Pattern: "Aggregator",
	}, sv)
	assert.Empty(t, got)

	// Negated non-match → truthy sentinel "1".
	got = mustEval(t, env, row, ExprRegex{
		LHS:     ExprField{Path: []string{"node", "symbol_name"}},
		Pattern: "Aggregator",
		Negate:  true,
	}, sv)
	assert.Equal(t, "1", got)
}

func TestEvalExpr_HasEdge(t *testing.T) {
	sv := seededView()
	env := newEnv()

	secRow := &Row{NodeID: "sec1", Node: sv.byID["sec1"], Vars: map[string]string{}}
	// sec1 --contains--> p1 (outgoing) → true.
	assert.Equal(t, "true", mustEval(t, env, secRow, ExprFunc{
		Name: "has_edge", Args: []Expr{ExprLit{Value: "contains"}, ExprLit{Value: "out"}},
	}, sv))
	// sec1 has no outgoing "relates-to" → "".
	assert.Empty(t, mustEval(t, env, secRow, ExprFunc{
		Name: "has_edge", Args: []Expr{ExprLit{Value: "relates-to"}, ExprLit{Value: "out"}},
	}, sv))
	// sec1 has an incoming contains (from part1) → true.
	assert.Equal(t, "true", mustEval(t, env, secRow, ExprFunc{
		Name: "has_edge", Args: []Expr{ExprLit{Value: "contains"}, ExprLit{Value: "in"}},
	}, sv))
}

func TestEvalExpr_HasAncestor(t *testing.T) {
	sv := seededView()
	env := newEnv()

	// p1's transitive incoming-contains ancestors are sec1 then part1
	// (Foreword). has_ancestor matches the Foreword name.
	pRow := &Row{NodeID: "p1", Node: sv.byID["p1"], Vars: map[string]string{}}
	assert.Equal(t, "1", mustEval(t, env, pRow, ExprFunc{
		Name: "has_ancestor",
		Args: []Expr{ExprLit{Value: "contains"}, ExprLit{Value: "symbol_name"}, ExprLit{Value: "^Foreword$"}},
	}, sv))

	// No ancestor named "Appendix" → "".
	assert.Empty(t, mustEval(t, env, pRow, ExprFunc{
		Name: "has_ancestor",
		Args: []Expr{ExprLit{Value: "contains"}, ExprLit{Value: "symbol_name"}, ExprLit{Value: "^Appendix$"}},
	}, sv))
}

func TestEvalExpr_ChildrenConcat(t *testing.T) {
	sv := seededView()
	env := newEnv()
	secRow := &Row{NodeID: "sec1", Node: sv.byID["sec1"], Vars: map[string]string{}}
	// children_concat over contains → the child paragraph's symbol_name.
	assert.Equal(t, "body text", mustEval(t, env, secRow, ExprFunc{
		Name: "children_concat",
		Args: []Expr{ExprLit{Value: "contains"}, ExprLit{Value: "symbol_name"}, ExprLit{Value: " "}},
	}, sv))
}

// lit is a tiny helper that wraps a string in an ExprLit so the builtin tests
// read as call(arg, arg, ...) without the struct-literal noise.
func lit(v string) Expr { return ExprLit{Value: v} }

// fn builds an ExprFunc with literal string arguments — the common shape for
// the pure-string and boolean builtin tests below.
func fn(name string, args ...string) ExprFunc {
	a := make([]Expr, 0, len(args))
	for _, s := range args {
		a = append(a, lit(s))
	}
	return ExprFunc{Name: name, Args: a}
}

// TestEvalExpr_StringBuiltins pins the exact output of every pure-string builtin
// (concat/trim/lower/upper/length/slice/match_group) against literal inputs,
// including the byte-clamped out-of-range slice and the 1-indexed match_group
// extraction. A mutation flipping any builtin's semantics fails an assertion.
func TestEvalExpr_StringBuiltins(t *testing.T) {
	sv := seededView()
	env := newEnv()
	row := &Row{NodeID: "sec1", Node: sv.byID["sec1"], Vars: map[string]string{}}

	// concat joins all args with no separator (variadic, zero-arity-safe).
	assert.Equal(t, "abc", mustEval(t, env, row, fn("concat", "a", "b", "c"), sv))
	assert.Empty(t, mustEval(t, env, row, fn("concat"), sv))
	// trim strips surrounding whitespace only.
	assert.Equal(t, "mid space", mustEval(t, env, row, fn("trim", "  mid space  "), sv))
	// lower / upper case-fold the whole string.
	assert.Equal(t, "router", mustEval(t, env, row, fn("lower", "RoUtEr"), sv))
	assert.Equal(t, "ROUTER", mustEval(t, env, row, fn("upper", "RoUtEr"), sv))
	// length is the BYTE length rendered as a decimal string.
	assert.Equal(t, "5", mustEval(t, env, row, fn("length", "hello"), sv))
	assert.Equal(t, "0", mustEval(t, env, row, fn("length", ""), sv))

	// slice is input[start:end] with byte offsets.
	assert.Equal(t, "el", mustEval(t, env, row, fn("slice", "hello", "1", "3"), sv))
	// end clamps to len(input) when out of range — no panic, no error.
	assert.Equal(t, "llo", mustEval(t, env, row, fn("slice", "hello", "2", "99"), sv))
	// start<0 clamps to 0.
	assert.Equal(t, "he", mustEval(t, env, row, fn("slice", "hello", "-4", "2"), sv))
	// start>end yields "" (not an error).
	assert.Empty(t, mustEval(t, env, row, fn("slice", "hello", "3", "1"), sv))

	// match_group is 1-indexed for capture groups; group 0 is the whole match.
	assert.Equal(t, "1", mustEval(t, env, row, fn("match_group", "a1b2", "([a-z])([0-9])", "2"), sv))
	assert.Equal(t, "a", mustEval(t, env, row, fn("match_group", "a1b2", "([a-z])([0-9])", "1"), sv))
	assert.Equal(t, "a1", mustEval(t, env, row, fn("match_group", "a1b2", "([a-z])([0-9])", "0"), sv))
	// No match OR an out-of-range group index → "" (no error).
	assert.Empty(t, mustEval(t, env, row, fn("match_group", "zzz", "([a-z])([0-9])", "1"), sv))
	assert.Empty(t, mustEval(t, env, row, fn("match_group", "a1b2", "([a-z])([0-9])", "9"), sv))

	t.Run("NonIntegerIndicesError", func(t *testing.T) {
		// strconv.Atoi failures in sliceString and matchGroup bubble up as
		// interpreter errors — proving those error paths are reached.
		_, err := evalExpr(context.Background(), env, row, fn("slice", "hello", "x", "3"), sv)
		require.Error(t, err)
		_, err = evalExpr(context.Background(), env, row, fn("slice", "hello", "1", "y"), sv)
		require.Error(t, err)
		_, err = evalExpr(context.Background(), env, row, fn("match_group", "a1", "(a)", "z"), sv)
		require.Error(t, err)
	})
}

// TestEvalExpr_BoolBuiltins pins the string-truthiness contract of and/or/not:
// they return SPECIFIC operand values, not a collapsed "true"/"". A regression
// that reduces them to bare booleans is caught by the value assertions.
func TestEvalExpr_BoolBuiltins(t *testing.T) {
	sv := seededView()
	env := newEnv()
	row := &Row{NodeID: "sec1", Node: sv.byID["sec1"], Vars: map[string]string{}}

	// and → "" if either operand empty, else the SECOND operand.
	assert.Equal(t, "second", mustEval(t, env, row, fn("and", "first", "second"), sv))
	assert.Empty(t, mustEval(t, env, row, fn("and", "", "second"), sv))
	assert.Empty(t, mustEval(t, env, row, fn("and", "first", ""), sv))

	// or → the FIRST non-empty operand.
	assert.Equal(t, "first", mustEval(t, env, row, fn("or", "first", "second"), sv))
	assert.Equal(t, "second", mustEval(t, env, row, fn("or", "", "second"), sv))
	assert.Empty(t, mustEval(t, env, row, fn("or", "", ""), sv))

	// not → sentinel "1" for empty (falsy) input, "" for any non-empty input.
	assert.Equal(t, "1", mustEval(t, env, row, fn("not", ""), sv))
	assert.Empty(t, mustEval(t, env, row, fn("not", "anything"), sv))

	t.Run("Arity", func(t *testing.T) {
		// checkArity gates the bool builtins: and wants 2, not wants 1.
		_, err := evalExpr(context.Background(), env, row, fn("and", "only-one"), sv)
		require.Error(t, err)
		_, err = evalExpr(context.Background(), env, row, fn("not", "a", "b"), sv)
		require.Error(t, err)
	})
}

// TestEvalExpr_VarLookup pins lookupVar's precedence: a row-scoped binding
// shadows the same-named env binding, env resolves when the row lacks the var,
// and an unbound var returns "".
func TestEvalExpr_VarLookup(t *testing.T) {
	sv := seededView()
	env := newEnv()
	env.Vars["x"] = "env-x"
	env.Vars["y"] = "env-y"
	row := &Row{NodeID: "sec1", Node: sv.byID["sec1"], Vars: map[string]string{"x": "row-x"}}

	// Row binding shadows env binding for the same name.
	assert.Equal(t, "row-x", mustEval(t, env, row, ExprVar{Name: "x"}, sv))
	// Var present only on env → env fallback resolves.
	assert.Equal(t, "env-y", mustEval(t, env, row, ExprVar{Name: "y"}, sv))
	// Unbound var → "".
	assert.Empty(t, mustEval(t, env, row, ExprVar{Name: "z"}, sv))
}

// TestEvalExpr_AncestorsConcat pins ancestors_concat: it aggregates the named
// field over ONE-HOP INCOMING neighbors (the sibling of children_concat), not a
// transitive ancestor walk. sec1's only incoming-contains neighbor is part1
// ("Foreword"), so the result is exactly that one name.
func TestEvalExpr_AncestorsConcat(t *testing.T) {
	sv := seededView()
	env := newEnv()
	secRow := &Row{NodeID: "sec1", Node: sv.byID["sec1"], Vars: map[string]string{}}
	assert.Equal(t, "Foreword", mustEval(t, env, secRow, ExprFunc{
		Name: "ancestors_concat",
		Args: []Expr{lit("contains"), lit("symbol_name"), lit(" ")},
	}, sv))
}

// TestEvalExpr_HasEdgeBoth exercises the bothEdges union branch not covered by
// the existing out/in-only has_edge test: sec1 has BOTH an outgoing contains
// (→p1) and an incoming contains (part1→), so has_edge('contains','both') is
// truthy.
func TestEvalExpr_HasEdgeBoth(t *testing.T) {
	sv := seededView()
	env := newEnv()
	secRow := &Row{NodeID: "sec1", Node: sv.byID["sec1"], Vars: map[string]string{}}
	assert.Equal(t, "true", mustEval(t, env, secRow, ExprFunc{
		Name: "has_edge", Args: []Expr{lit("contains"), lit("both")},
	}, sv))
}

// TestEvalExpr_RegexErrorAndEmptyLHS pins evalRegex's two non-happy paths: an
// invalid pattern is an interpreter error (not a silent zero-match), and an
// empty LHS yields "" with no error.
func TestEvalExpr_RegexErrorAndEmptyLHS(t *testing.T) {
	sv := seededView()
	env := newEnv()
	row := &Row{NodeID: "sec1", Node: sv.byID["sec1"], Vars: map[string]string{}}

	// Invalid regex "(" → compile error bubbles up.
	_, err := evalExpr(context.Background(), env, row, ExprRegex{
		LHS:     lit("anything"),
		Pattern: "(",
	}, sv)
	require.Error(t, err)

	// Empty LHS against a valid pattern → "" (no match, no error).
	got, err := evalExpr(context.Background(), env, row, ExprRegex{
		LHS:     lit(""),
		Pattern: "x+",
	}, sv)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestEvalExpr_HasAncestorCycleGuard builds a cyclic incoming-edge graph and
// asserts has_ancestor returns "" and the test completes (does not hang),
// proving the visited-set + maxDepth=32 cycle guard. a→b→a via contains
// incoming edges; no node is named "Zzz".
func TestEvalExpr_HasAncestorCycleGuard(t *testing.T) {
	a := &knowledgev1.Node{Id: "a", Type: "section", SymbolName: "A"}
	b := &knowledgev1.Node{Id: "b", Type: "section", SymbolName: "B"}
	sv := &sourceView{
		byID:   map[string]*knowledgev1.Node{"a": a, "b": b},
		byType: map[string][]*knowledgev1.Node{"section": {a, b}},
		// Cyclic incoming-contains edges: a's incoming is from b, b's from a.
		inEdges: map[string][]*knowledgev1.Edge{
			"a": {{FromId: "b", ToId: "a", Type: "contains"}},
			"b": {{FromId: "a", ToId: "b", Type: "contains"}},
		},
	}
	env := newEnv()
	row := &Row{NodeID: "a", Node: a, Vars: map[string]string{}}
	got, err := evalExpr(context.Background(), env, row, ExprFunc{
		Name: "has_ancestor",
		Args: []Expr{lit("contains"), lit("symbol_name"), lit("^Zzz$")},
	}, sv)
	require.NoError(t, err)
	assert.Empty(t, got, "no ancestor matches and the cycle guard prevents a hang")
}

// TestEvalExpr_UnknownFunction pins evalFunc's final fallthrough: an
// unrecognized builtin name returns an error containing 'unknown function'.
func TestEvalExpr_UnknownFunction(t *testing.T) {
	sv := seededView()
	env := newEnv()
	row := &Row{NodeID: "sec1", Node: sv.byID["sec1"], Vars: map[string]string{}}
	_, err := evalExpr(context.Background(), env, row, fn("no_such_builtin", "x"), sv)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown function")
}
