// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_census_ast_test.go holds the go/ast MECHANICS the query claim-surface
// census runs on: the package parse, the intercept-signature predicate, the
// tool-claim predicate, the payload-carrier propagation and the payload-struct
// collection. The assertions that consume them — and every registered set they
// are checked against — stay in query_arm_census_test.go.
//
// The split is a file-length concern only (the repo caps a source file at 500
// lines): there is ONE census, and this is its walker half. The sibling registry
// tables use the same arrangement for the same reason.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// toolsPackageAST is the parsed non-test source of this package, indexed the two
// ways the census needs: by func name and by named type.
type toolsPackageAST struct {
	fset      *token.FileSet
	files     map[string]*ast.File
	funcs     map[string]*ast.FuncDecl
	structs   map[string]*ast.StructType
	funcFiles map[string]string
}

// parseToolsPackage parses every non-test .go file in this package directory.
// Test sources are excluded on purpose: the census is about the PRODUCTION claim
// surface, and a fixture in a _test.go file is not a claim point.
func parseToolsPackage(t *testing.T) *toolsPackageAST {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	pkg := &toolsPackageAST{
		fset:      token.NewFileSet(),
		files:     map[string]*ast.File{},
		funcs:     map[string]*ast.FuncDecl{},
		structs:   map[string]*ast.StructType{},
		funcFiles: map[string]string{},
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(pkg.fset, filepath.Join(".", name), nil, parser.SkipObjectResolution)
		require.NoErrorf(t, perr, "parsing %s", name)
		pkg.files[name] = file
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil || d.Body == nil {
					continue
				}
				pkg.funcs[d.Name.Name] = d
				pkg.funcFiles[d.Name.Name] = name
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if st, isStruct := ts.Type.(*ast.StructType); isStruct {
						pkg.structs[ts.Name.Name] = st
					}
				}
			}
		}
	}
	require.NotEmpty(t, pkg.files, "the census must parse at least one source file")
	return pkg
}

// renderType renders the type expressions the census compares against, which are
// all plain idents, qualified idents, pointers and slices. Anything else renders
// empty and therefore never matches a signature the predicate accepts.
func renderType(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return renderType(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + renderType(t.X)
	case *ast.ArrayType:
		return "[]" + renderType(t.Elt)
	}
	return ""
}

// paramNames flattens a field list into one name per parameter, so a grouped
// signature (a, b string) indexes the same way the call site does.
func paramNames(fl *ast.FieldList) []string {
	var names []string
	if fl == nil {
		return names
	}
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			names = append(names, "")
			continue
		}
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

// paramTypes flattens a field list into one rendered type per parameter,
// mirroring paramNames so the two index together.
func paramTypes(fl *ast.FieldList) []string {
	var types []string
	if fl == nil {
		return types
	}
	for _, f := range fl.List {
		rendered := renderType(f.Type)
		count := len(f.Names)
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			types = append(types, rendered)
		}
	}
	return types
}

// interceptSignatureParams reports the name of the kgtools.CallToolParams
// parameter when fd has the client intercept signature, and "" otherwise. The
// signature is the structural definition of "a chain step": every claimant has
// it, and nothing else in the package does.
func interceptSignatureParams(fd *ast.FuncDecl) string {
	inTypes := paramTypes(fd.Type.Params)
	outTypes := paramTypes(fd.Type.Results)
	want := []string{"context.Context", "ClientDeps", "kgtools.CallToolParams"}
	if len(inTypes) != len(want) || len(outTypes) != 2 {
		return ""
	}
	for i, w := range want {
		if inTypes[i] != w {
			return ""
		}
	}
	if outTypes[0] != "bool" || outTypes[1] != "kgtools.ToolResult" {
		return ""
	}
	names := paramNames(fd.Type.Params)
	return names[2]
}

// stringLit reports the unquoted value of a string literal expression.
func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	val, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return val, true
}

// claimsTool reports whether fd's body tests its params.Name against tool, in
// EITHER of the two shapes the chain uses: a comparison (== / !=) or a switch
// case. Both are load-bearing — see queryArmBearingDelegates for why a
// comparison-only predicate under-counts by exactly one.
func claimsTool(fd *ast.FuncDecl, paramsName, tool string) bool {
	selector := paramsName + ".Name"
	found := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.BinaryExpr:
			if node.Op != token.EQL && node.Op != token.NEQ {
				return true
			}
			for _, pair := range [][2]ast.Expr{{node.X, node.Y}, {node.Y, node.X}} {
				if renderType(pair[0]) != selector {
					continue
				}
				if val, ok := stringLit(pair[1]); ok && val == tool {
					found = true
				}
			}
		case *ast.SwitchStmt:
			if node.Tag == nil || renderType(node.Tag) != selector {
				return true
			}
			for _, stmt := range node.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range clause.List {
					if val, ok := stringLit(expr); ok && val == tool {
						found = true
					}
				}
			}
		}
		return true
	})
	return found
}

// queryEntryPoints derives the claim-surface entry-point set from source.
func (p *toolsPackageAST) queryEntryPoints() []string {
	var found []string
	for name, fd := range p.funcs {
		paramsName := interceptSignatureParams(fd)
		if paramsName == "" {
			continue
		}
		if claimsTool(fd, paramsName, "query") {
			found = append(found, name)
		}
	}
	sort.Strings(found)
	return found
}

// payloadCarriers propagates the raw query payload from each seed func through
// the package's own call graph, returning funcName → the set of expressions in
// that func's body that hold the payload.
//
// PROPAGATION IS THROUGH THE json.RawMessage VALUE ONLY, never through a whole
// kgtools.CallToolParams value. That narrowness is the contract, not a
// shortcut: several handlers are handed the original params (thought.go's
// simulate arm, for one) and decode their OWN tool's arg struct out of it, so a
// whole-params walk would collect structs that are not query payload carriers at
// all. A genuine query-claim delegation is recorded in queryArmBearingDelegates
// and seeded explicitly, which is also what keeps that record honest.
func (p *toolsPackageAST) payloadCarriers(seeds []string) map[string]map[string]bool {
	carriers := map[string]map[string]bool{}
	for _, seed := range seeds {
		fd, ok := p.funcs[seed]
		if !ok {
			continue
		}
		paramsName := interceptSignatureParams(fd)
		if paramsName == "" {
			continue
		}
		carriers[seed] = map[string]bool{paramsName + ".Arguments": true}
	}
	for changed := true; changed; {
		changed = false
		for fn, held := range carriers {
			fd := p.funcs[fn]
			if fd == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				target := p.funcs[callee.Name]
				if target == nil {
					return true
				}
				names := paramNames(target.Type.Params)
				for i, arg := range call.Args {
					if i >= len(names) || names[i] == "" || !held[renderType(arg)] {
						continue
					}
					if carriers[callee.Name] == nil {
						carriers[callee.Name] = map[string]bool{}
					}
					if !carriers[callee.Name][names[i]] {
						carriers[callee.Name][names[i]] = true
						changed = true
					}
				}
				return true
			})
		}
	}
	return carriers
}

// localVarType finds the type expression of a `var name T` declaration inside
// fd's body. Returns nil when the identifier is not declared that way.
func localVarType(fd *ast.FuncDecl, name string) ast.Expr {
	var found ast.Expr
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.VAR {
			return true
		}
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || vs.Type == nil {
				continue
			}
			for _, ident := range vs.Names {
				if ident.Name == name {
					found = vs.Type
				}
			}
		}
		return true
	})
	return found
}

// payloadStruct is one struct type a query payload is decoded into.
type payloadStruct struct {
	// label is the type name for a named struct, or a source-located
	// "anonymous struct at file:line" for a literal at the unmarshal site.
	label     string
	anonymous bool
	tags      []string
}

// structTags returns the json tag names declared on a struct type, dropping the
// ",omitempty"-style options and any field without a usable tag.
func structTags(st *ast.StructType) []string {
	var tags []string
	for _, field := range st.Fields.List {
		if field.Tag == nil {
			continue
		}
		raw, err := strconv.Unquote(field.Tag.Value)
		if err != nil {
			continue
		}
		key, _, _ := strings.Cut(reflect.StructTag(raw).Get("json"), ",")
		if key == "" || key == "-" {
			continue
		}
		tags = append(tags, key)
	}
	return tags
}

// queryPayloadStructs walks every carrier-bearing func for
// `json.Unmarshal(<carrier>, &target)` and resolves each target to the struct
// type it was declared as. Anonymous struct literals at the unmarshal site are
// collected exactly like named ones — the whole point, since `scope` lives only
// on one.
func (p *toolsPackageAST) queryPayloadStructs(carriers map[string]map[string]bool) []payloadStruct {
	byLabel := map[string]payloadStruct{}
	for fn, held := range carriers {
		fd := p.funcs[fn]
		if fd == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || renderType(call.Fun) != "json.Unmarshal" || len(call.Args) != 2 {
				return true
			}
			if !held[renderType(call.Args[0])] {
				return true
			}
			unary, ok := call.Args[1].(*ast.UnaryExpr)
			if !ok || unary.Op != token.AND {
				return true
			}
			target, ok := unary.X.(*ast.Ident)
			if !ok {
				return true
			}
			typeExpr := localVarType(fd, target.Name)
			if typeExpr == nil {
				return true
			}
			switch decl := typeExpr.(type) {
			case *ast.Ident:
				st, isStruct := p.structs[decl.Name]
				if !isStruct {
					return true
				}
				byLabel[decl.Name] = payloadStruct{label: decl.Name, tags: structTags(st)}
			case *ast.StructType:
				pos := p.fset.Position(decl.Pos())
				label := fmt.Sprintf("anonymous struct at %s:%d", filepath.Base(pos.Filename), pos.Line)
				byLabel[label] = payloadStruct{label: label, anonymous: true, tags: structTags(decl)}
			}
			return true
		})
	}
	found := make([]payloadStruct, 0, len(byLabel))
	for _, ps := range byLabel {
		found = append(found, ps)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].label < found[j].label })
	return found
}
