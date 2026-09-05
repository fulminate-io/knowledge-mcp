// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// regexCache is the process-global cache of compiled regex patterns.
// Recipe bodies use a bounded set of patterns, so caching them across
// Interpret invocations is a pure win — no eviction needed in v1.
var regexCache sync.Map // pattern string → *regexp.Regexp

// evalExpr evaluates e against the current row and env using the
// in-memory sourceView for edge lookups (has_edge). Returns a string because v1 has no
// non-string values — regex matches, has_edge, and bool-ish
// predicates all resolve to "" (false) or a non-empty string (true,
// typically the match text or the literal "true").
func evalExpr(
	ctx context.Context,
	env *Env,
	row *Row,
	e Expr,
	sv *sourceView,
) (string, error) {
	switch x := e.(type) {
	case ExprLit:
		return x.Value, nil
	case ExprVar:
		return lookupVar(env, row, x.Name), nil
	case ExprField:
		return evalField(env, row, x.Path)
	case ExprRegex:
		return evalRegex(ctx, env, row, x, sv)
	case ExprFunc:
		return evalFunc(ctx, env, row, x, sv)
	}
	return "", fmt.Errorf("unsupported expression type %T", e)
}

// lookupVar returns the value of a bare $var reference. Row-scoped
// bindings shadow env-wide bindings, matching traverse-as semantics
// (each row gets its own $target, but a global `bind $slug := ...`
// could stash a value on env.Vars as well).
func lookupVar(env *Env, row *Row, name string) string {
	if row != nil && row.Vars != nil {
		if v, ok := row.Vars[name]; ok {
			return v
		}
	}
	if env != nil && env.Vars != nil {
		if v, ok := env.Vars[name]; ok {
			return v
		}
	}
	return ""
}

// evalRegex compiles and applies the pattern. Empty LHS matches
// nothing (returns ""); errors during compilation bubble up as
// interpreter errors so recipe authors learn about bad regex syntax
// immediately rather than silently getting zero matches.
func evalRegex(
	ctx context.Context,
	env *Env,
	row *Row,
	r ExprRegex,
	sv *sourceView,
) (string, error) {
	lhs, err := evalExpr(ctx, env, row, r.LHS, sv)
	if err != nil {
		return "", err
	}
	re, err := compileRegex(r.Pattern)
	if err != nil {
		return "", fmt.Errorf("regex %q at %d:%d: %w", r.Pattern, r.Pos.Line, r.Pos.Col, err)
	}
	match := re.FindString(lhs)
	if r.Negate {
		// `!~` inverts truthiness. An unmatched LHS becomes a sentinel
		// non-empty string so filter rules treat it as truthy; a match
		// becomes empty so filter drops the row.
		if match == "" {
			return "1", nil
		}
		return "", nil
	}
	return match, nil
}

// compileRegex memoizes compiled patterns across Interpret runs.
func compileRegex(pattern string) (*regexp.Regexp, error) {
	if v, ok := regexCache.Load(pattern); ok {
		re, ok := v.(*regexp.Regexp)
		if !ok {
			return nil, fmt.Errorf("compileRegex: cache entry for %q has unexpected type %T", pattern, v)
		}
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	actual, _ := regexCache.LoadOrStore(pattern, re)
	cached, ok := actual.(*regexp.Regexp)
	if !ok {
		return nil, fmt.Errorf("compileRegex: cache race produced unexpected type %T", actual)
	}
	return cached, nil
}

// evalFunc dispatches on function name. Unknown names produce a
// parse-time-flavored error at runtime — the parser does not
// currently validate builtin names against the known list so this is
// the last line of defense.
//
// The dispatch is split across four category-specific helpers
// (string ops, graph ops, boolean ops, render ops) to keep this top
// function short. Each helper returns (value, handled, error); when
// handled is false the next helper gets a turn.
func evalFunc(
	ctx context.Context,
	env *Env,
	row *Row,
	f ExprFunc,
	sv *sourceView,
) (string, error) {
	args, err := evalArgs(ctx, env, row, f.Args, sv)
	if err != nil {
		return "", err
	}
	if v, ok, err := evalStringFunc(f.Name, args); ok {
		return v, err
	}
	if v, ok, err := evalGraphFunc(row, f.Name, args, sv); ok {
		return v, err
	}
	if v, ok, err := evalBoolFunc(f.Name, args); ok {
		return v, err
	}
	if v, ok, err := evalRenderFunc(row, f.Name, args, sv); ok {
		return v, err
	}
	return "", fmt.Errorf("unknown function %q at %d:%d", f.Name, f.Pos.Line, f.Pos.Col)
}

// sliceString returns input[start:end] using Go's standard slice
// semantics, with bounds clamped to the string's actual byte length
// (so an out-of-range index doesn't panic). Indices are byte offsets,
// not rune offsets — matches Go's str[i:j].
func sliceString(input, startStr, endStr string) (string, error) {
	start, err := strconv.Atoi(startStr)
	if err != nil {
		return "", fmt.Errorf("slice: start must be an integer, got %q", startStr)
	}
	end, err := strconv.Atoi(endStr)
	if err != nil {
		return "", fmt.Errorf("slice: end must be an integer, got %q", endStr)
	}
	if start < 0 {
		start = 0
	}
	if end > len(input) {
		end = len(input)
	}
	if start > end {
		return "", nil
	}
	return input[start:end], nil
}

// andBool / orBool / notBool implement string-as-truthiness boolean
// composition for the v1 grammar's filter / where predicates. Empty
// = false; any non-empty = true. The truthy result returns the first
// non-empty operand (so the value remains useful for downstream
// emits) when possible.
func andBool(a, b string) string {
	if a == "" || b == "" {
		return ""
	}
	return b
}

func orBool(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func notBool(a string) string {
	if a == "" {
		return "1"
	}
	return ""
}

// evalStringFunc handles the pure-string builtins (no DB access, no
// row context). Returns (value, true, err) when the function name is
// recognized; (\"\", false, nil) for unknown names so the caller can
// continue dispatch to the other category helpers.
func evalStringFunc(name string, args []string) (string, bool, error) {
	switch name {
	case "concat":
		return strings.Join(args, ""), true, nil
	case "trim":
		if err := checkArity("trim", args, 1); err != nil {
			return "", true, err
		}
		return strings.TrimSpace(args[0]), true, nil
	case "lower":
		if err := checkArity("lower", args, 1); err != nil {
			return "", true, err
		}
		return strings.ToLower(args[0]), true, nil
	case "upper":
		if err := checkArity("upper", args, 1); err != nil {
			return "", true, err
		}
		return strings.ToUpper(args[0]), true, nil
	case "length":
		if err := checkArity("length", args, 1); err != nil {
			return "", true, err
		}
		return strconv.Itoa(len(args[0])), true, nil
	case "slice":
		if err := checkArity("slice", args, 3); err != nil {
			return "", true, err
		}
		v, err := sliceString(args[0], args[1], args[2])
		return v, true, err
	case "match_group":
		if err := checkArity("match_group", args, 3); err != nil {
			return "", true, err
		}
		v, err := matchGroup(args[0], args[1], args[2])
		return v, true, err
	}
	return "", false, nil
}

// evalGraphFunc handles builtins that query the source graph. Same
// (value, handled, err) return shape as evalStringFunc.
func evalGraphFunc(
	row *Row,
	name string,
	args []string,
	sv *sourceView,
) (string, bool, error) {
	switch name {
	case "has_edge":
		if err := checkArity("has_edge", args, 2); err != nil {
			return "", true, err
		}
		v, err := hasEdge(row, args[0], args[1], sv)
		return v, true, err
	case "children_concat":
		if err := checkArity("children_concat", args, 3); err != nil {
			return "", true, err
		}
		return childrenConcat(row, args[0], args[1], args[2], sv), true, nil
	case "ancestors_concat":
		if err := checkArity("ancestors_concat", args, 3); err != nil {
			return "", true, err
		}
		return ancestorsConcat(row, args[0], args[1], args[2], sv), true, nil
	case "has_ancestor":
		if err := checkArity("has_ancestor", args, 3); err != nil {
			return "", true, err
		}
		v, err := hasAncestor(row, args[0], args[1], args[2], sv)
		return v, true, err
	}
	return "", false, nil
}

// evalBoolFunc handles boolean composition over the v1 string-
// truthiness convention. Same (value, handled, err) return shape.
func evalBoolFunc(name string, args []string) (string, bool, error) {
	switch name {
	case "and":
		if err := checkArity("and", args, 2); err != nil {
			return "", true, err
		}
		return andBool(args[0], args[1]), true, nil
	case "or":
		if err := checkArity("or", args, 2); err != nil {
			return "", true, err
		}
		return orBool(args[0], args[1]), true, nil
	case "not":
		if err := checkArity("not", args, 1); err != nil {
			return "", true, err
		}
		return notBool(args[0]), true, nil
	}
	return "", false, nil
}

// matchGroup runs the regex pattern against input and returns the
// n-th capture group (1-indexed; 0 is the full match, matching Go's
// regexp.FindStringSubmatch convention). Returns "" when the regex
// doesn't match OR n is out of range. Used by recipes that need to
// extract clean labels from messy source identifiers — e.g. peel a
// method name out of "package/file.go:ClassName.MethodName".
//
// Args are strings (not raw regex literals) because the DSL grammar
// reserves regex literals for the `~=` / `!~` operators. The pattern
// string is compiled via the same memoizing cache as `~=`.
func matchGroup(input, pattern, nStr string) (string, error) {
	n, err := strconv.Atoi(nStr)
	if err != nil {
		return "", fmt.Errorf("match_group: index must be an integer, got %q", nStr)
	}
	if n < 0 {
		return "", fmt.Errorf("match_group: index must be >= 0, got %d", n)
	}
	re, err := compileRegex(pattern)
	if err != nil {
		return "", fmt.Errorf("match_group: compile %q: %w", pattern, err)
	}
	matches := re.FindStringSubmatch(input)
	if matches == nil || n >= len(matches) {
		return "", nil
	}
	return matches[n], nil
}

// evalArgs evaluates each argument expression in turn, failing fast on
// the first error.
func evalArgs(
	ctx context.Context,
	env *Env,
	row *Row,
	args []Expr,
	sv *sourceView,
) ([]string, error) {
	out := make([]string, 0, len(args))
	for _, a := range args {
		v, err := evalExpr(ctx, env, row, a, sv)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// checkArity validates the argument count for a builtin.
func checkArity(name string, args []string, want int) error {
	if len(args) != want {
		return fmt.Errorf("%s: expected %d argument(s), got %d", name, want, len(args))
	}
	return nil
}

// hasEdge and parseDirection live in interpret_funcs.go.
