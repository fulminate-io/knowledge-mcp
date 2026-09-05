// SPDX-License-Identifier: Apache-2.0

package segmentdist

// measurement_census_size_test.go answers the census's second question: given an
// expression that reaches a leg, HOW MANY DOCUMENTS is it, and does it come from
// this declaration or from its caller.
//
// The two answers are different kinds. A size resolved HERE (a generator called with
// a constant, a composite literal, a local built from either) makes this declaration
// itself heavy. A size that traces back to a PARAMETER makes the parameter a leg of
// this declaration, so every caller passing a large corpus into it becomes heavy
// instead — which is how a fixture helper's callers are found without walking a
// caller list.

import (
	"go/ast"
	"maps"
	"slices"
)

// paramTaint maps each local identifier to the parameter indices its value derives
// from. It is a forward pass over the declaration in source order, which is enough
// for test code: a helper's corpus is assigned once and used after.
func (a *censusAnalysis) paramTaint(u *censusUnit) map[string][]int {
	taint := map[string][]int{}
	for i, name := range u.params {
		if name != "" && name != "_" {
			taint[name] = append(taint[name], i)
		}
	}
	for range 3 {
		ast.Inspect(u.body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			var from []int
			for _, rhs := range as.Rhs {
				from = append(from, a.taintOf(rhs, taint)...)
			}
			if len(from) == 0 {
				return true
			}
			// A multi-value builder taking a corpus hands that corpus's provenance to
			// every result: bucketGroups(docs) returns the same documents regrouped,
			// and the regrouping is what a rebuild stages.
			for _, lhs := range as.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
					taint[id.Name] = censusMergeInts(taint[id.Name], from)
				}
			}
			return true
		})
	}
	return taint
}

// taintOf returns the parameter indices an expression derives from.
func (a *censusAnalysis) taintOf(e ast.Expr, taint map[string][]int) []int {
	var out []int
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			out = censusMergeInts(out, taint[id.Name])
		}
		return true
	})
	return out
}

// originParams reports which of this declaration's parameters a leg expression
// derives from, and is what turns a helper into a leg of its own.
func (a *censusAnalysis) originParams(e ast.Expr, u *censusUnit, taint map[string][]int) []int {
	var out []int
	for _, idx := range a.taintOf(e, taint) {
		if u.docParams[idx] {
			out = censusMergeInts(out, []int{idx})
		}
	}
	return out
}

// docLocals sizes the document slices a declaration binds to local names, seeded
// with the sizes its caller bound to its parameters.
//
// ONE IMPLEMENTATION FOR BOTH PATHS, DELIBERATELY. An earlier split had a scoped
// variant that handled only one-to-one assignment, so a MULTI-RESULT fixture —
// `docs, targetID, _, term := searchCorpus(3)` — bound nothing on the scoped path
// and its corpus resolved to no size at all.
func (a *censusAnalysis) docLocals(u *censusUnit, seed map[string]int, seen map[string]bool) map[string]int {
	out := map[string]int{}
	maps.Copy(out, seed)
	for range 3 {
		ast.Inspect(u.body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				a.bindDocLocals(u, out, s.Lhs, s.Rhs, seen)
			case *ast.ValueSpec:
				// `var docs []searchengine.Document` starts at zero, so the appends
				// that follow accumulate from a known base instead of resolving to
				// nothing.
				if len(s.Values) == 0 && censusTypeString(s.Type) == "[]searchengine.Document" {
					for _, name := range s.Names {
						if _, bound := out[name.Name]; !bound {
							out[name.Name] = 0
						}
					}
					return true
				}
				lhs := make([]ast.Expr, 0, len(s.Names))
				for _, name := range s.Names {
					lhs = append(lhs, name)
				}
				a.bindDocLocals(u, out, lhs, s.Values, seen)
			}
			return true
		})
	}
	return out
}

func (a *censusAnalysis) bindDocLocals(u *censusUnit, out map[string]int, lhs, rhs []ast.Expr, seen map[string]bool) {
	if len(rhs) == 1 && len(lhs) > 1 {
		// A multi-result fixture: size every document-typed result the same, since
		// the resolver reads the returned corpus rather than the tuple position.
		if n, ok := a.docSize(rhs[0], u, out, seen); ok {
			for _, l := range lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
					out[id.Name] = max(out[id.Name], n)
				}
			}
		}
		return
	}
	if len(lhs) != len(rhs) {
		return
	}
	for i, l := range lhs {
		id, ok := l.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		if n, ok := a.docSize(rhs[i], u, out, seen); ok {
			out[id.Name] = max(out[id.Name], n)
		}
	}
}

// docSize resolves how many documents an expression carries, as an upper bound.
//
// AN INDEX OR SLICE OF A CORPUS RESOLVES TO THE WHOLE CORPUS. That is deliberate
// over-approximation: a sub-slice handed to a leg can only be smaller, so reporting
// the parent size can make the census flag a test that need not be gated — visible
// to whoever reads the failure — and can never hide one that must be.
func (a *censusAnalysis) docSize(e ast.Expr, u *censusUnit, docs map[string]int, seen map[string]bool) (int, bool) {
	switch x := e.(type) {
	case *ast.CompositeLit:
		if censusTypeString(x.Type) == "[]searchengine.Document" {
			return len(x.Elts), true
		}
	case *ast.Ident:
		if x.Name == "nil" {
			return 0, true
		}
		if n, ok := docs[x.Name]; ok {
			return n, true
		}
	case *ast.ParenExpr:
		return a.docSize(x.X, u, docs, seen)
	case *ast.SliceExpr:
		return a.docSize(x.X, u, docs, seen)
	case *ast.IndexExpr:
		return a.docSize(x.X, u, docs, seen)
	case *ast.CallExpr:
		if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "make" && len(x.Args) >= 2 &&
			censusTypeString(x.Args[0]) == "[]searchengine.Document" {
			return a.inlineMakeSize(x, u, docs, seen)
		}
		if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "append" && len(x.Args) > 0 {
			total, ok := a.docSize(x.Args[0], u, docs, seen)
			if !ok {
				return 0, false
			}
			for _, arg := range x.Args[1:] {
				n, nok := a.docSize(arg, u, docs, seen)
				if !nok {
					n = 1 // a single appended element
				}
				total += n
			}
			return total, true
		}
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Clone" && len(x.Args) == 1 {
			return a.docSize(x.Args[0], u, docs, seen)
		}
		if callee := a.calleeOf(x); callee != nil {
			intBind, docBind := a.bindArgs(callee, x.Args, u, docs, seen)
			return a.generatorSize(callee, intBind, docBind, seen)
		}
	}
	return 0, false
}

// inlineMakeSize sizes a `make([]searchengine.Document, ...)` written in place. A
// zero length with a capacity is this package's filtered-corpus shape, so the CAP is
// the bound; `len(x)` over a resolvable slice is the per-document rewrite shape.
func (a *censusAnalysis) inlineMakeSize(x *ast.CallExpr, u *censusUnit, docs map[string]int, seen map[string]bool) (int, bool) {
	sizeArg := x.Args[1]
	if v, ok := a.pkg.constFold(sizeArg, u); ok && v == 0 && len(x.Args) > 2 {
		sizeArg = x.Args[2]
	}
	if v, ok := a.pkg.censusUpperBound(sizeArg, u); ok {
		return v, true
	}
	if lc, ok := sizeArg.(*ast.CallExpr); ok {
		if fn, ok := lc.Fun.(*ast.Ident); ok && fn.Name == "len" && len(lc.Args) == 1 {
			if v, ok := a.docSize(lc.Args[0], u, docs, seen); ok {
				return v, true
			}
			if v, ok := a.sliceLen(lc.Args[0], u); ok {
				return v, true
			}
		}
	}
	return 0, false
}

// bindArgs evaluates a call's arguments in the CALLER's scope, so the callee's size
// expression can be evaluated against real values. A GENERATOR'S PARAMETER IS NOT
// ALWAYS A SIZE — searchCorpus takes a target index and builds searchCorpusN
// documents whatever it is passed — so both bindings are made and the callee's own
// size expression decides which, if either, it uses.
func (a *censusAnalysis) bindArgs(
	callee *censusUnit, args []ast.Expr, u *censusUnit, docs map[string]int, seen map[string]bool,
) (map[string]int, map[string]int) {
	intBind, docBind := map[string]int{}, map[string]int{}
	for i, arg := range args {
		if i >= len(callee.params) || callee.params[i] == "" || callee.params[i] == "_" {
			continue
		}
		if v, ok := a.pkg.censusUpperBound(arg, u); ok {
			intBind[callee.params[i]] = v
		}
		if n, ok := a.docSize(arg, u, docs, seen); ok {
			docBind[callee.params[i]] = n
		}
	}
	return intBind, docBind
}

// generatorSize resolves how many documents a declaration produces, given bindings
// for its parameters.
//
// BUILDERS RETURN THE SLICE AMONG SEVERAL RESULTS, so this reads every return
// operand rather than a single-result signature; a resolver keyed on
// []searchengine.Document as the SOLE result misses eight of this package's helpers.
func (a *censusAnalysis) generatorSize(
	callee *censusUnit, intBind, docBind map[string]int, seen map[string]bool,
) (int, bool) {
	if seen[callee.key] {
		return 0, false
	}
	seen[callee.key] = true
	defer delete(seen, callee.key)

	scoped := *callee
	scoped.locals = map[string]int{}
	maps.Copy(scoped.locals, callee.locals)
	maps.Copy(scoped.locals, intBind)
	scopedDocs := map[string]int{}
	maps.Copy(scopedDocs, docBind)

	// The declared corpus: `make([]searchengine.Document, LEN)`, or the CAP of an
	// append-filled one, which is how this package writes a filtered corpus.
	if n, ok := a.makeSize(&scoped, scopedDocs, seen); ok {
		if a.generatorsResolved == nil {
			a.generatorsResolved = map[string]bool{}
		}
		a.generatorsResolved[callee.key] = true
		return n, true
	}
	// Otherwise a delegating or pass-through generator: read what it returns.
	local := a.docLocals(&scoped, scopedDocs, seen)
	best, found := 0, false
	ast.Inspect(callee.body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, r := range ret.Results {
			if v, ok := a.docSize(r, &scoped, local, seen); ok {
				best, found = max(best, v), true
			}
		}
		return true
	})
	if found {
		if a.generatorsResolved == nil {
			a.generatorsResolved = map[string]bool{}
		}
		a.generatorsResolved[callee.key] = true
		return best, found
	}
	// A REGROUPING RATHER THAN A GENERATOR: bucketGroups returns the same corpus
	// keyed by partition, so its result is bounded by the corpus it was handed. The
	// bound is what the rebuild stages, which is the whole layer rather than a bucket.
	if len(callee.docParams) == 1 {
		for idx := range callee.docParams {
			if n, ok := scopedDocs[callee.params[idx]]; ok {
				return n, true
			}
		}
	}
	return best, found
}

// makeSize reads a declaration's own `make([]searchengine.Document, ...)`.
func (a *censusAnalysis) makeSize(u *censusUnit, docs map[string]int, seen map[string]bool) (int, bool) {
	best, found := 0, false
	ast.Inspect(u.body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "make" || len(call.Args) < 2 {
			return true
		}
		if censusTypeString(call.Args[0]) != "[]searchengine.Document" {
			return true
		}
		// The length argument, or the CAPACITY when the length is zero and the slice
		// is filled by append.
		sizeArg := call.Args[1]
		if v, ok := a.pkg.constFold(sizeArg, u); ok && v == 0 && len(call.Args) > 2 {
			sizeArg = call.Args[2]
		}
		if v, ok := a.pkg.censusUpperBound(sizeArg, u); ok {
			best, found = max(best, v), true
			return true
		}
		// `make([]searchengine.Document, len(docs))` — a per-document rewrite whose
		// size is the input corpus's.
		if lc, ok := sizeArg.(*ast.CallExpr); ok {
			if fn, ok := lc.Fun.(*ast.Ident); ok && fn.Name == "len" && len(lc.Args) == 1 {
				if v, ok := a.docSize(lc.Args[0], u, docs, seen); ok {
					best, found = max(best, v), true
				}
			}
		}
		return true
	})
	return best, found
}

// censusDocOperands returns the document-carrying sub-expressions of an assignment's
// right-hand side — the corpus inside `append(staged.hnsw, BucketWork{Docs: ...})`.
func censusDocOperands(e ast.Expr, u *censusUnit) []ast.Expr {
	var out []ast.Expr
	names := map[string]bool{}
	for i, p := range u.params {
		if u.docParams[i] && p != "" {
			names[p] = true
		}
	}
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && names[id.Name] {
			out = append(out, id)
		}
		return true
	})
	return out
}

// censusMergeInts unions two small int sets, order-stable.
func censusMergeInts(dst, src []int) []int {
	for _, v := range src {
		found := slices.Contains(dst, v)
		if !found {
			dst = append(dst, v)
		}
	}
	return dst
}
