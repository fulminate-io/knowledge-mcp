// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
)

// expr_static_validate.go — the expression half of the pre-walk validation:
// builtin names, arity, literal regexes and literal edge-type arguments.
//
// IT IS REACHED THROUGH validateAgainstSource, so one entry point covers
// everything that needs the source graph or the whole rule list, and both halves
// feed the same collected, sorted refusal.
//
// EMIT IDENTITY IS NOT HERE. An emit carrying neither `name` nor `identity` is a
// PARSE error, in parseEmit. It is decidable from the AST with no source graph,
// and placing it behind Interpret would let recipe.Parse accept a broken shipped
// example — which is all the three documentation gates call.
//
// THE METADATA-KEY CENSUS OVER FIELD PATHS IS validate_source.go's, not this
// file's. Both are reached through the same entry point, so the split is stated
// here: that file owns every field path the recipe reads, on both grammars; this
// one owns builtin names, arity, literal regexes and literal edge-type
// arguments.

// builtinSpec is one builtin's arity contract. wantArgs of -1 means variadic —
// concat is the only one, and it accepts any count including zero.
type builtinSpec struct {
	wantArgs int
	// edgeArg is the index of the argument that names a SOURCE-GRAPH EDGE TYPE,
	// or -1 when the builtin takes none.
	edgeArg int
	// regexArg is the index of the argument that is a regex PATTERN, or -1.
	regexArg int
}

// builtinTable mirrors the four dispatch helpers — evalStringFunc,
// evalGraphFunc, evalBoolFunc and evalRenderFunc — and is the vocabulary an
// unknown-builtin refusal names.
//
// IT IS PINNED TO THE DISPATCH BY A TEST rather than by discipline:
// TestBuiltinTable_CoversEveryDispatchCase parses those four functions and
// requires an entry for every case it finds, so a builtin added later without a
// table entry reds instead of silently escaping validation.
var builtinTable = map[string]builtinSpec{
	// evalStringFunc
	"concat":      {wantArgs: -1, edgeArg: -1, regexArg: -1},
	"trim":        {wantArgs: 1, edgeArg: -1, regexArg: -1},
	"lower":       {wantArgs: 1, edgeArg: -1, regexArg: -1},
	"upper":       {wantArgs: 1, edgeArg: -1, regexArg: -1},
	"length":      {wantArgs: 1, edgeArg: -1, regexArg: -1},
	"slice":       {wantArgs: 3, edgeArg: -1, regexArg: -1},
	"match_group": {wantArgs: 3, edgeArg: -1, regexArg: 1},
	// evalGraphFunc — every one of these takes its edge type first.
	"has_edge":         {wantArgs: 2, edgeArg: 0, regexArg: -1},
	"children_concat":  {wantArgs: 3, edgeArg: 0, regexArg: -1},
	"ancestors_concat": {wantArgs: 3, edgeArg: 0, regexArg: -1},
	"has_ancestor":     {wantArgs: 3, edgeArg: 0, regexArg: 2},
	// evalBoolFunc
	"and": {wantArgs: 2, edgeArg: -1, regexArg: -1},
	"or":  {wantArgs: 2, edgeArg: -1, regexArg: -1},
	"not": {wantArgs: 1, edgeArg: -1, regexArg: -1},
	// evalRenderFunc
	"heading_path":   {wantArgs: 3, edgeArg: 0, regexArg: -1},
	"subtree_concat": {wantArgs: 4, edgeArg: 0, regexArg: -1},
}

// sortedBuiltins renders the builtin vocabulary for a refusal.
func sortedBuiltins() []string {
	out := make([]string, 0, len(builtinTable))
	for name := range builtinTable {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// checkFunc validates one builtin call: its name, its argument count, its
// literal regex argument if it has one, and its edge-type argument if it has
// one.
//
// Today both the unknown-name and the arity errors exist only INSIDE the row
// loop, which is why an empty rowset hides them: a recipe selecting a mistyped
// node type reports rows=0/0 and never reaches the call at all.
func (v *sourceValidator) checkFunc(f ExprFunc) {
	spec, known := builtinTable[f.Name]
	if !known {
		msg := fmt.Sprintf("unknown builtin %q", f.Name)
		if near := ast.ClosestVocabulary(f.Name, sortedBuiltins()); len(near) > 0 {
			msg += fmt.Sprintf("; did you mean %q?", near[0])
		}
		v.add(f.Pos, "%s. The run was refused before the walk rather than answered with zero rows. Builtins: %s",
			msg, joinPlain(sortedBuiltins()))
		return
	}
	if spec.wantArgs >= 0 && len(f.Args) != spec.wantArgs {
		v.add(f.Pos, "%s: expected %d argument(s), got %d", f.Name, spec.wantArgs, len(f.Args))
		return
	}
	if spec.regexArg >= 0 && spec.regexArg < len(f.Args) {
		if lit, ok := f.Args[spec.regexArg].(ExprLit); ok {
			v.checkLiteralRegex(lit.Value, f.Pos, f.Name+" pattern")
		}
	}
	if spec.edgeArg >= 0 && spec.edgeArg < len(f.Args) {
		v.checkEdgeTypeArgument(f, spec.edgeArg)
	}
}

// checkEdgeTypeArgument censuses a builtin's edge-type argument, and REFUSES a
// non-literal one.
//
// THIS IS THE CARRIER THE PROJECTION SIDE RUNS ON. The traverse rule and the
// where-tree's walk leaves are the other two, and neither covers this one: a
// recipe whose traverse is correctly cased can still pass a lowercase edge type
// to heading_path and get an empty render with no error, which is the shipped
// documented example's own defect.
//
// A NON-LITERAL ARGUMENT IS REFUSED, and that narrows the grammar deliberately.
// It costs nothing measurable — every saved recipe and every help example passes
// a literal — and the alternative is a per-row read that can only fail silently,
// which is the class this ticket closes.
func (v *sourceValidator) checkEdgeTypeArgument(f ExprFunc, idx int) {
	lit, ok := f.Args[idx].(ExprLit)
	if !ok {
		v.add(f.Pos,
			"%s: the edge type argument must be a string literal so it can be checked against the source graph's edge types before the walk; got a computed expression",
			f.Name)
		return
	}
	if !contains(v.census.edgeTypes, lit.Value) {
		v.refuseVocabulary(f.Pos, f.Name, censusEdgeType, lit.Value)
	}
}

// checkLiteralRegex compiles a literal pattern once, here, so it lands in the
// process-global cache and the row loop never compiles it — the same discipline
// the rerank predicate's validate already applies for the same reason.
func (v *sourceValidator) checkLiteralRegex(pattern string, pos Position, site string) {
	if _, err := compileRegex(pattern); err != nil {
		v.add(pos, "%s: regex %q does not compile: %v", site, pattern, err)
		return
	}
	regexCompiles.Add(1)
}

// joinPlain renders a vocabulary without quoting, for a builtin list.
func joinPlain(values []string) string {
	var out strings.Builder
	for i, val := range values {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(val)
	}
	return out.String()
}
