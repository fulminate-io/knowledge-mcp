// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// bounded_reads_census_test.go is the standing structural gate against unbounded
// full-hydration reads. It walks every non-test .go file under
// cmd/knowledge/internal with go/ast, classifies every knowledgev1.QueryPlan
// composite literal, and FAILS on any site that reads a whole graph or a whole
// node type in one Execute without appearing on a survivor list with a reason.
//
// WHY STRUCTURAL RATHER THAN A LINE GREP: a grep census reports a comfortable,
// wrong zero on the shapes this tree actually holds — a Selection assigned to a
// variable before use, a multi-line literal whose type key sits on the next line,
// and a literal keyed on the PLURAL NodeTypes when the pattern was anchored on
// NodeType. It is also not an `ast`-tool census: that tool has no CLI surface, so
// a checked-in script cannot invoke it. A go/ast guard TEST is the in-tree
// mechanism, and it rides `make test` for free. Precedent, copied deliberately:
// local_routing_guard_test.go in this package.
//
// TWO EXEMPTIONS, applied BEFORE classification:
//  1. A plan carrying ById, Ids, or Selection.FromId is a by-id, bulk-hydrate or
//     traversal shape — not a browse.
//  2. A plan whose ReturnMode is RETURN_MODE_EDGES is not a node browse: it
//     returns edge rows, its cost model is the edge table, and its boundedness
//     belongs to the match-all edge primitive. Without this, the classifier
//     false-positives on deliberate by-design match-all edge reads.
//
// RESIDUAL BOUNDARY (documented, not a gap to fix here): the Selection alias scan
// is INTRA-FUNCTION, the same depth local_routing_guard_test.go uses. A Selection
// returned from a helper or threaded through a struct field is not resolved — such
// a site is reported under the unresolved_selection kind rather than silently
// passing, so the boundary is visible rather than a hole.

// censusKind names the five verdicts. They are NOT mutually exclusive, and that
// is deliberate: one site can legitimately be both an unbounded_type_browse (on
// one branch) and an ambiguous_selection (because its alias is assigned twice).
// A classifier that forced one verdict per site would be choosing a branch, which
// is the exact failure ambiguous_selection exists to surface.
const (
	kindUnboundedMatchAll   = "unbounded_match_all"
	kindUnboundedTypeBrowse = "unbounded_type_browse"
	kindBrowseNoSkipTotal   = "browse_no_skip_total"
	kindAmbiguousSelection  = "ambiguous_selection"
	kindUnresolvedSelection = "unresolved_selection"
)

var censusKinds = []string{
	kindUnboundedMatchAll,
	kindUnboundedTypeBrowse,
	kindBrowseNoSkipTotal,
	kindAmbiguousSelection,
	kindUnresolvedSelection,
}

// censusSite is one classified QueryPlan literal. The survivor list is keyed on
// (file, function) rather than a line number deliberately: line numbers rot on
// every edit above the site, and a stale key silently turns a survivor entry into
// a missed violation.
type censusSite struct {
	file string // repo-relative-ish path as scanned
	fn   string // enclosing function or method name
	pos  token.Position
	kind string
}

// survivorKey identifies an allowed site.
type survivorKey struct{ file, fn, kind string }

// censusSurvivors are the sites that legitimately read without a bound, each with
// the reason it is allowed. Adding an entry here is one of only two legitimate
// responses to a census failure; the other is to bound the read with a keyset
// drain. Anything else is the gate being talked out of its job.
var censusSurvivors = map[survivorKey]string{
	// --- unbounded_match_all ---
	// (none: both former entries — the graph-wide JSON node enumeration and the
	// topology whole-graph node fetch — are keyset drains now.)

	// --- unbounded_type_browse ---
	// Each "predicate-bounded" reason below was read in current source, not
	// inherited from the ticket's prose. All four hivemonitor sites now hold up:
	// reaperHives was the one genuinely unbounded read and was narrowed to a
	// per-session predicate.
	{"engine/dispatch_byid.go", "findLinkageProxies", kindUnboundedTypeBrowse}:               "narrowed by a foreign_id OP_EQ metadata predicate on the requested node id — bounded in practice",
	{"tools/cross_graph_migrate.go", "scanSlugLessPracticeProxies", kindUnboundedTypeBrowse}: "narrowed by a foreign_graph=practice OP_EQ metadata predicate — bounded in practice",
	{"hivemonitor/monitor.go", "banEvictedMembers", kindUnboundedTypeBrowse}:                 "narrowed by a hive OP_EQ metadata predicate plus a status filter — bounded in practice",
	{"hivemonitor/monitor_heartbeat.go", "memberHivesFor", kindUnboundedTypeBrowse}:          "narrowed by a session OP_EQ metadata predicate — bounded in practice",
	{"hivemonitor/hive_reaper.go", "sweepHive", kindUnboundedTypeBrowse}:                     "narrowed by a hive OP_EQ metadata predicate — bounded in practice",
	// The entry stays even though the read is now narrowed: classify() cannot see
	// MetadataPredicates, so a predicate-bounded browse still lands here as
	// unbounded_type_browse. Deleting the entry turns the gate red.
	{"hivemonitor/hive_reaper.go", "reaperHives", kindUnboundedTypeBrowse}: "narrowed by a session OP_EQ metadata predicate — one browse per live local session, memberHivesFor's shape — bounded in practice",
	{"bootstrap/hive_loops.go", "hasLiveMember", kindUnboundedTypeBrowse}:  "narrowed by a session OP_EQ metadata predicate — memberHivesFor's shape, one browse per session resolved inside the boot re-detection window and none at all outside it — bounded in practice",

	// --- ambiguous_selection ---
	// (none: the recent-browse arm's double Selection assignment was collapsed to
	// a single literal plus a field set when it became a keyset drain.)

	// --- unresolved_selection ---
	// All three Compile browse arms live in one function, so one entry covers them.
	{"engine/compile_query.go", "buildDefaultModePlan", kindUnresolvedSelection}:        "the Compile browse arms: they build Selection via browseSelection/browsePluralSelection and set Limit AFTERWARDS via applyBrowseLimitOffset, so an absent Limit key in the literal is correct here",
	{"engine/dispatch_delete_preview.go", "deletePreviewPlan", kindUnresolvedSelection}: "Selection comes from the pruneSelection helper — outside the intra-function scan by construction",
}

// planFields is the decoded shape of one QueryPlan composite literal.
type planFields struct {
	hasByID, hasIDs        bool
	hasLimit, hasSkipTotal bool
	returnMode             string
	selection              *selectionInfo
	selectionUnresolved    bool
	selectionAmbiguous     bool
}

// selectionInfo is the decoded shape of the plan's Selection value.
type selectionInfo struct {
	elems   int
	hasType bool // NodeType or NodeTypes
	hasFrom bool // FromId — a traversal anchor, not a browse
}

// keyName returns the field name of a keyed composite-literal element.
func keyName(e ast.Expr) (string, ast.Expr, bool) {
	kv, ok := e.(*ast.KeyValueExpr)
	if !ok {
		return "", nil, false
	}
	id, ok := kv.Key.(*ast.Ident)
	if !ok {
		return "", nil, false
	}
	return id.Name, kv.Value, true
}

// litTypeName returns the trailing type name of a composite literal, seeing
// through the &T{...} address-of form: `&knowledgev1.QueryPlan{}` → "QueryPlan".
func litTypeName(cl *ast.CompositeLit) string {
	switch t := cl.Type.(type) {
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// unwrap strips a leading & so `&knowledgev1.Selection{...}` yields its literal.
func unwrap(e ast.Expr) ast.Expr {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		return u.X
	}
	return e
}

// describeSelection decodes a Selection composite literal.
func describeSelection(cl *ast.CompositeLit) *selectionInfo {
	info := &selectionInfo{elems: len(cl.Elts)}
	for _, el := range cl.Elts {
		name, _, ok := keyName(el)
		if !ok {
			continue
		}
		switch name {
		case "NodeType", "NodeTypes":
			info.hasType = true
		case "FromId":
			info.hasFrom = true
		}
	}
	return info
}

// selectionAssignments collects, per identifier, every Selection composite
// literal assigned to it anywhere in the function. More than one DISTINCT
// assignment makes the site ambiguous: resolving by assignment order would
// silently pick a branch.
func selectionAssignments(body *ast.BlockStmt) map[string][]*ast.CompositeLit {
	out := map[string][]*ast.CompositeLit{}
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range as.Rhs {
			if i >= len(as.Lhs) {
				break
			}
			cl, ok := unwrap(rhs).(*ast.CompositeLit)
			if !ok || litTypeName(cl) != "Selection" {
				continue
			}
			if id, ok := as.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
				out[id.Name] = append(out[id.Name], cl)
			}
		}
		return true
	})
	return out
}

// describePlan decodes one QueryPlan literal, resolving its Selection either
// inline or through a same-function alias.
func describePlan(cl *ast.CompositeLit, assigns map[string][]*ast.CompositeLit) planFields {
	var p planFields
	for _, el := range cl.Elts {
		name, val, ok := keyName(el)
		if !ok {
			continue
		}
		switch name {
		case "ById":
			p.hasByID = true
		case "Ids":
			p.hasIDs = true
		case "Limit":
			p.hasLimit = true
		case "SkipTotal":
			p.hasSkipTotal = true
		case "ReturnMode":
			if sel, ok := val.(*ast.SelectorExpr); ok {
				p.returnMode = sel.Sel.Name
			}
		case "Selection":
			switch v := unwrap(val).(type) {
			case *ast.CompositeLit:
				p.selection = describeSelection(v)
			case *ast.Ident:
				lits := assigns[v.Name]
				switch {
				case len(lits) == 1:
					p.selection = describeSelection(lits[0])
				case len(lits) > 1:
					p.selectionAmbiguous = true
					// Still classify the widest branch so an ambiguous site cannot
					// hide an unbounded read behind its ambiguity.
					p.selection = describeSelection(lits[0])
					for _, l := range lits[1:] {
						if si := describeSelection(l); si.hasType {
							p.selection.hasType = true
						}
					}
				default:
					p.selectionUnresolved = true
				}
			default:
				p.selectionUnresolved = true
			}
		}
	}
	return p
}

// classify returns every kind the plan trips. Exemptions are applied first.
func classify(p planFields) []string {
	if p.hasByID || p.hasIDs || (p.selection != nil && p.selection.hasFrom) {
		return nil // by-id / bulk-hydrate / traversal — not a browse
	}
	if p.returnMode == "ReturnMode_RETURN_MODE_EDGES" {
		return nil // an edges read is not a node browse
	}
	var kinds []string
	if p.selectionAmbiguous {
		kinds = append(kinds, kindAmbiguousSelection)
	}
	if p.selectionUnresolved {
		kinds = append(kinds, kindUnresolvedSelection)
	}
	if p.selection != nil {
		switch {
		case p.selection.hasType && !p.hasLimit:
			kinds = append(kinds, kindUnboundedTypeBrowse)
		case p.selection.elems == 0 && !p.hasLimit:
			kinds = append(kinds, kindUnboundedMatchAll)
		}
	}
	if p.hasLimit && !p.hasSkipTotal {
		kinds = append(kinds, kindBrowseNoSkipTotal)
	}
	return kinds
}

// scanFileForPlans classifies every QueryPlan literal in one parsed file.
func scanFileForPlans(fset *token.FileSet, file *ast.File, rel string) []censusSite {
	var out []censusSite
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		assigns := selectionAssignments(fn.Body)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok || litTypeName(cl) != "QueryPlan" {
				return true
			}
			for _, kind := range classify(describePlan(cl, assigns)) {
				out = append(out, censusSite{file: rel, fn: fn.Name.Name, pos: fset.Position(cl.Pos()), kind: kind})
			}
			return true
		})
	}
	return out
}

// goFilesUnder returns every non-test .go file under root.
func goFilesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// scanInternalTree walks every non-test .go file under cmd/knowledge/internal.
// The test runs with CWD = the bootstrap package dir, so the root is "..".
func scanInternalTree(t *testing.T) []censusSite {
	t.Helper()
	const root = ".."
	fset := token.NewFileSet()
	var out []censusSite
	for _, path := range goFilesUnder(t, root) {
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		out = append(out, scanFileForPlans(fset, file, rel)...)
	}
	return out
}

// TestBoundedReadsCensus is the live gate: every classified site must appear on
// the survivor list with a written reason.
func TestBoundedReadsCensus(t *testing.T) {
	sites := scanInternalTree(t)
	byKind := map[string][]censusSite{}
	for _, s := range sites {
		byKind[s.kind] = append(byKind[s.kind], s)
	}
	for _, kind := range censusKinds {
		t.Run(kind, func(t *testing.T) {
			for _, s := range byKind[kind] {
				if _, ok := censusSurvivors[survivorKey{s.file, s.fn, s.kind}]; ok {
					continue
				}
				t.Errorf("unbounded read (%s) at %s in %s:%s\n"+
					"  Only two responses are legitimate: bound the read with a keyset drain\n"+
					"  (engine.DrainKeysetPages / DrainKeysetIDs), or add it to censusSurvivors with a written reason.",
					kind, s.pos, s.file, s.fn)
			}
		})
	}
}

// TestBoundedReadsCensus_SelfCheck PROVES the classifier fires rather than being
// green because it matches nothing. It parses synthetic in-memory snippets —
// including every shape a line-grep census misses and both exemptions — and
// asserts the verdict on each, without any scratch edit of real source.
func TestBoundedReadsCensus_SelfCheck(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{{
		name: "variable-assigned empty Selection (a line grep misses this)",
		body: "sel := &knowledgev1.Selection{}\n\t_ = &knowledgev1.QueryPlan{Selection: sel}",
		want: []string{kindUnboundedMatchAll},
	}, {
		name: "inline empty Selection",
		body: `_ = &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{}}`,
		want: []string{kindUnboundedMatchAll},
	}, {
		name: "multi-line NodeTypes literal (a line grep anchored on the type key misses this)",
		body: "_ = &knowledgev1.QueryPlan{\n\t\tSelection: &knowledgev1.Selection{\n\t\t\tNodeTypes: []string{\"thought\"},\n\t\t},\n\t}",
		want: []string{kindUnboundedTypeBrowse},
	}, {
		name: "single-line PLURAL NodeTypes (a pattern anchored on NodeType: misses this)",
		body: `_ = &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{NodeTypes: []string{"hive_member"}}}`,
		want: []string{kindUnboundedTypeBrowse},
	}, {
		name: "singular NodeType with no limit",
		body: `_ = &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{NodeType: "file"}}`,
		want: []string{kindUnboundedTypeBrowse},
	}, {
		name: "Limit without SkipTotal",
		body: `_ = &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{NodeType: "file"}, Limit: 500}`,
		want: []string{kindBrowseNoSkipTotal},
	}, {
		name: "two-assignment alias is ambiguous",
		body: "sel := &knowledgev1.Selection{}\n\tsel = &knowledgev1.Selection{NodeTypes: []string{\"x\"}}\n\t_ = &knowledgev1.QueryPlan{Selection: sel}",
		want: []string{kindAmbiguousSelection, kindUnboundedTypeBrowse},
	}, {
		name: "Selection from a helper is unresolved, not silently passed",
		body: `_ = &knowledgev1.QueryPlan{Selection: pruneSelection(x)}`,
		want: []string{kindUnresolvedSelection},
	}, {
		name: "by-id plan is exempt",
		body: `_ = &knowledgev1.QueryPlan{ById: "f.go"}`,
		want: nil,
	}, {
		name: "bulk ids[] hydrate is exempt",
		body: `_ = &knowledgev1.QueryPlan{Ids: symIDs}`,
		want: nil,
	}, {
		name: "traversal anchored on FromId is exempt",
		body: `_ = &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{FromId: []string{"a"}, EdgeTypes: []string{"CONTAINS"}}}`,
		want: nil,
	}, {
		name: "match-all RETURN_MODE_EDGES is exempt (an edges read is not a node browse)",
		body: `_ = &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{}, ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES}`,
		want: nil,
	}, {
		name: "bounded browse carrying both Limit and SkipTotal is clean",
		body: `_ = &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{NodeType: "file"}, Limit: 500, SkipTotal: true}`,
		want: nil,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			src := "package p\nfunc f() {\n\t" + tc.body + "\n}\n"
			file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
			if err != nil {
				t.Fatalf("parse synthetic snippet: %v", err)
			}
			var got []string
			for _, s := range scanFileForPlans(fset, file, "synthetic.go") {
				got = append(got, s.kind)
			}
			if !sameKinds(got, tc.want) {
				t.Errorf("classifier verdict = %v, want %v for body %q", got, tc.want, tc.body)
			}
		})
	}
}

// sameKinds compares two verdict sets order-insensitively.
func sameKinds(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, k := range a {
		seen[k]++
	}
	for _, k := range b {
		seen[k]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
