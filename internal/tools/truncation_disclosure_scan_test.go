// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// truncation_disclosure_scan_test.go is the go/ast half of the
// truncation-disclosure census: the member rule, the per-function analysis the
// gate reads, and the walk over the two package directories. The declaration
// table and the gate itself live in truncation_disclosure_census_test.go, and
// the classifier's self-check in truncation_disclosure_selfcheck_test.go. The
// three-way split is a file-length consequence, not a design one — the
// pre-commit hook hard-errors above 500 lines on staged *.go with only vendor
// and gen excluded, and test files are not exempt.
//
// THE MEMBER RULE. A SITE is a function declared in
// cmd/knowledge/internal/tools, cmd/knowledge/internal/engine or
// cmd/knowledge/internal/projects/render that returns a kgtools.ToolResult from
// its own signature AND satisfies clause (a1) or (a2):
//
//	(a1) it has a *knowledgev1.ExecuteResponse in scope — as a parameter, or as
//	     the result of calling an engine.ExecuteFn or a GraphCaller Execute
//	     within its own body; or
//	(a2) it RECEIVES A TRUNCATION VERDICT AS A RETURN VALUE from a helper it
//	     calls, without ever holding the response itself.
//
// CLAUSE (a2) IS NOT OPTIONAL. Under (a1) alone, InterceptQueryPlanTree is
// structurally invisible: its body holds no ExecuteResponse and issues no
// Execute, while TraverseDescendantsWithEdges holds the response and returns a
// truncation bool but no ToolResult. The response and the result never meet in
// one function, so an (a1)-only rule reports a clean scan over a live gap. The
// same shape recurs on the examine and by-id edge arms, whose verdicts arrive
// out of a drain-owning helper.
//
// A verdict "arrives from a call" in exactly two spellings a per-function
// scanner can see, and BOTH count because they are the same thing — a value
// produced elsewhere and handed over:
//
//	(i)  a truncation-named value bound in a multi-assign off a call, e.g.
//	     `nodes, edges, truncated, err := TraverseDescendantsWithEdges(...)`;
//	(ii) a truncation-named FIELD read off a value a helper returned, e.g.
//	     `data.EdgesTruncated` — the same verdict carried in a struct instead
//	     of a tuple.
//
// A truncation-named PARAMETER is deliberately NOT clause (a2). A function that
// is HANDED the verdict by its caller has not received it from a helper it
// calls; counting it would make every envelope builder a census member the
// moment the bool is threaded through it, which is bookkeeping rather than
// coverage. Those builders are reached through their callers' rows instead.

const (
	clauseA1 = "a1"
	clauseA2 = "a2"
)

// truncationName matches the identifier spellings a truncation verdict travels
// under. Deliberately loose: a verdict named something this misses is a verdict
// the census cannot see, which is the failure mode worth guarding against.
var truncationName = regexp.MustCompile(`(?i)truncat`)

// truncationGetter matches an accessor that READS a verdict — GetTruncated and
// friends. It exists so a getter call is not mistaken for a disclosure call:
// `resp.GetTruncated()` is reading the verdict, while
// `engine.WithTruncationNotice(res, resp)` is disclosing one, and both sit in
// callee position.
var truncationGetter = regexp.MustCompile(`(?i)^get[a-z0-9_]*truncat`)

// fnFacts is one scanned function: its identity, why it is (or is not) a site,
// and the three facts the json_carriers analysis reads.
type fnFacts struct {
	file      string
	name      string
	pos       token.Position
	isSite    bool
	clause    string
	emitsKey  bool            // its own body WRITES the `truncated` JSON key
	readsVerd bool            // its own body READS a truncation verdict
	callNames map[string]bool // bare names of the functions it calls
}

func (f fnFacts) key() string { return f.file + ":" + f.name }

// typeNameOf returns the trailing type name of an expression, seeing through
// pointers and package qualifiers: `*knowledgev1.ExecuteResponse` →
// "ExecuteResponse".
func typeNameOf(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return typeNameOf(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// resultsToolResult reports whether the signature returns a kgtools.ToolResult.
func resultsToolResult(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, r := range fn.Type.Results.List {
		if typeNameOf(r.Type) == "ToolResult" {
			return true
		}
	}
	return false
}

// execFnParamNames returns the parameter names typed as an ExecuteFn — the
// callable seam every intercept arm issues its reads through.
func execFnParamNames(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	if fn.Type.Params == nil {
		return out
	}
	for _, p := range fn.Type.Params.List {
		if typeNameOf(p.Type) != "ExecuteFn" {
			continue
		}
		for _, n := range p.Names {
			out[n.Name] = true
		}
	}
	return out
}

// holdsExecuteResponse implements clause (a1).
func holdsExecuteResponse(fn *ast.FuncDecl) bool {
	if fn.Type.Params != nil {
		for _, p := range fn.Type.Params.List {
			if typeNameOf(p.Type) == "ExecuteResponse" {
				return true
			}
		}
	}
	execParams := execFnParamNames(fn)
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if execParams[fun.Name] {
				found = true
			}
		case *ast.SelectorExpr:
			if fun.Sel.Name == "Execute" {
				found = true
			}
		}
		return true
	})
	return found
}

// verdictArrivesFromCall implements clause (a2) — spellings (i) and (ii) above.
func verdictArrivesFromCall(body *ast.BlockStmt) bool {
	callees := calleeExprs(body)
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			if !containsCall(v.Rhs) {
				return true
			}
			for _, lhs := range v.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && truncationName.MatchString(id.Name) {
					found = true
				}
			}
		case *ast.SelectorExpr:
			// A field read, never a call target: `data.EdgesTruncated` counts,
			// `engine.WithTruncationNotice(...)` does not.
			if !callees[ast.Node(v)] && truncationName.MatchString(v.Sel.Name) {
				found = true
			}
		}
		return true
	})
	return found
}

// containsCall reports whether any expression in the list contains a call.
func containsCall(exprs []ast.Expr) bool {
	for _, e := range exprs {
		found := false
		ast.Inspect(e, func(n ast.Node) bool {
			if _, ok := n.(*ast.CallExpr); ok {
				found = true
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// readsTruncationVerdict reports whether the body READS a verdict at all — a
// getter call, or any truncation-named value in non-callee position, parameters
// included. Distinct from clause (a2), which asks where the verdict CAME FROM;
// this asks only whether the body has one in hand, which is what the
// json_carriers analysis needs.
func readsTruncationVerdict(body *ast.BlockStmt) bool {
	callees := calleeExprs(body)
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if truncationGetter.MatchString(v.Sel.Name) ||
				(!callees[ast.Node(v)] && truncationName.MatchString(v.Sel.Name)) {
				found = true
			}
			// The .Sel of a CALLEE selector is handled by calleeExprs marking it, not
			// by stopping the descent here — descent still has to reach a verdict
			// nested inside a larger expression. Without that marking the Ident arm
			// below scored `engine.WithTruncationNotice(...)` as a verdict READ,
			// because the walk reaches the Sel as a bare Ident carrying the
			// FUNCTION's name. That is the exact mechanism that made the CONSTANT BY
			// CONSTRUCTION escape undemandable: every disclosing site looked like a
			// reader, so no site could ever be asked to justify a constant key.
		case *ast.Ident:
			if !callees[ast.Node(v)] && truncationName.MatchString(v.Name) {
				found = true
			}
		}
		return true
	})
	return found
}

// calleeExprs collects the expression nodes sitting in callee position, INCLUDING
// the .Sel of a qualified callee. Both spellings are recorded because a caller
// may reach either node first depending on how the walk descends.
func calleeExprs(body *ast.BlockStmt) map[ast.Node]bool {
	out := map[ast.Node]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			out[ast.Node(call.Fun)] = true
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				out[ast.Node(sel.Sel)] = true
			}
		}
		return true
	})
	return out
}

// bodyCallNames returns the bare names of every function the body calls.
func bodyCallNames(body *ast.BlockStmt) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			out[fun.Name] = true
		case *ast.SelectorExpr:
			out[fun.Sel.Name] = true
		}
		return true
	})
	return out
}

// emitsTruncatedKey reports whether the body WRITES the `truncated` JSON key —
// as a map-key literal, as a struct field tag on a locally declared envelope, or
// by composite-literalling a named struct type whose declaration carries the
// tag. READING the verdict is not emitting it: renderTraversalResponse read
// resp.GetTruncated() for years while its JSON payload carried no such key,
// which is exactly the gap this census exists to catch.
func emitsTruncatedKey(body *ast.BlockStmt, taggedTypes map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BasicLit:
			if v.Kind == token.STRING && strings.Trim(v.Value, "`\"") == "truncated" {
				found = true
			}
		case *ast.StructType:
			if structTagsTruncated(v) {
				found = true
			}
		case *ast.CompositeLit:
			if taggedTypes[typeNameOf(v.Type)] {
				found = true
			}
		}
		return true
	})
	return found
}

// structTagsTruncated reports whether a struct type declares a field tagged
// json:"truncated".
func structTagsTruncated(st *ast.StructType) bool {
	for _, f := range st.Fields.List {
		if f.Tag != nil && strings.Contains(f.Tag.Value, `json:"truncated"`) {
			return true
		}
	}
	return false
}

// censusRoots are the three package directories, relative to the tools package
// directory the test runs in. projects/render is in scope because it hosts the
// tree-rendering disclosure sentence (AppendTruncationNotice) and the assemble
// arms that call it — exactly the population this gate governs.
var censusRoots = []string{".", "../engine", "../projects/render"}

// goFilesForCensus returns every non-test .go file directly under root. Go
// packages are flat, so the walk never descends into a subdirectory.
func goFilesForCensus(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// scanCensusTree parses both package directories and returns every function's
// facts. The tagged-type set is collected in a FIRST PASS because a struct
// declared at file scope is emitted by whichever function composite-literals
// it — the search envelope's shape, which no single function body declares.
func scanCensusTree(t *testing.T) []fnFacts {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	var bases []string
	for _, root := range censusRoots {
		for _, path := range goFilesForCensus(t, root) {
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			files = append(files, f)
			bases = append(bases, filepath.Base(path))
		}
	}
	tagged := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if st, ok := ts.Type.(*ast.StructType); ok && structTagsTruncated(st) {
				tagged[ts.Name.Name] = true
			}
			return true
		})
	}
	var out []fnFacts
	for i, f := range files {
		out = append(out, scanFileForDisclosureSites(fset, f, bases[i], tagged)...)
	}
	return out
}

// scanFileForDisclosureSites classifies every function declared in one file.
func scanFileForDisclosureSites(fset *token.FileSet, file *ast.File, base string, tagged map[string]bool) []fnFacts {
	var out []fnFacts
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		facts := fnFacts{
			file:      base,
			name:      fn.Name.Name,
			pos:       fset.Position(fn.Pos()),
			emitsKey:  emitsTruncatedKey(fn.Body, tagged),
			readsVerd: readsTruncationVerdict(fn.Body),
			callNames: bodyCallNames(fn.Body),
		}
		if resultsToolResult(fn) {
			switch {
			case holdsExecuteResponse(fn):
				facts.isSite, facts.clause = true, clauseA1
			case verdictArrivesFromCall(fn.Body):
				facts.isSite, facts.clause = true, clauseA2
			}
		}
		out = append(out, facts)
	}
	return out
}

// transitiveEmitters returns the set of function names that write the
// `truncated` key, directly or through a function they call.
//
// The closure runs over BARE names because a cross-package call reaches the
// engine through a package qualifier the per-file scan does not resolve. That
// widens the set at the margins; the discriminating power sits in the ROOT set,
// which is only those functions whose own bodies write the key — today, before
// this ticket, that root set is EMPTY, so every json_carrier row fails.
func transitiveEmitters(all []fnFacts) map[string]bool {
	callers := map[string][]string{}
	out := map[string]bool{}
	for _, f := range all {
		if f.emitsKey {
			out[f.name] = true
		}
		for callee := range f.callNames {
			callers[callee] = append(callers[callee], f.name)
		}
	}
	frontier := make([]string, 0, len(out))
	for name := range out {
		frontier = append(frontier, name)
	}
	for len(frontier) > 0 {
		name := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		for _, caller := range callers[name] {
			if !out[caller] {
				out[caller] = true
				frontier = append(frontier, caller)
			}
		}
	}
	return out
}
