// SPDX-License-Identifier: Apache-2.0

package segmentdist

// measurement_census_scope_test.go is the RESOLVER half of the measurement-gate
// census: it parses this package, builds the scope tables, and answers "how many
// documents is this expression". measurement_census_test.go is the gate that uses
// it. They are two files only because lefthook caps a staged Go file at 500 lines.
//
// EVERY RESOLVER RULE HERE EXISTS BECAUSE A LANE GOT IT WRONG on this package. The
// blind spots are named at their implementations rather than in a list, so the next
// author meets each one where it bites.

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// censusDiagBuildTag names the one build constraint whose files this census does not
// classify. SCOPE DECISIONS ARE STATED, NOT ASSUMED: files behind it are not part of
// an ordinary `go test`, so a gate on them would be unobservable.
const censusDiagBuildTag = "segmentdist_diag"

// censusArm is what a document slice reaching a given position actually builds.
type censusArm int

const (
	censusArmNone censusArm = iota
	// censusArmHNSW: the slice is sealed into an HNSW segment by this call.
	censusArmHNSW
	// censusArmBM25: the slice reaches the field engine only.
	censusArmBM25
	// censusArmStagedHNSW: the slice is APPENDED to staged rebuild work and builds
	// nothing until a finalize takes it. KEYING ON FINALIZE RATHER THAN ON STAGE is
	// what keeps TestStageRebuildPartitionStagesWithoutShipping out of the set.
	censusArmStagedHNSW
	censusArmStagedBM25
)

func (a censusArm) String() string {
	switch a {
	case censusArmHNSW:
		return "hnsw"
	case censusArmBM25:
		return "bm25"
	case censusArmStagedHNSW:
		return "staged-hnsw"
	case censusArmStagedBM25:
		return "staged-bm25"
	default:
		return "unclassified"
	}
}

// censusBuildSink is a function whose call BUILDS into the arm of its manager
// argument, with the index of that argument and of the corpus.
//
// THE SET IS DERIVED, NOT LISTED, AND THE DIFFERENCE IS NOT COSMETIC. A first draft
// of this census named four sinks by hand and missed replaceBucketGroups, which is
// the only build route one of this package's tests has; the leg table looked
// complete and the test looked light. A sink is generic over the engine's type
// parameters and takes an ALREADY-ARMED manager, so no walk over methods of *Manager
// can find one — which is the same gap class as a build sitting one call past the
// Manager entirely.
type censusBuildSink struct{ mgrArg, docsArg int }

// censusManagerType is the armed per-format manager every build sink takes.
const censusManagerType = "distManager"

// censusCorpusTypes are the two shapes a corpus crosses a sink boundary in: the
// documents themselves, and the staged rebuild work that carries them.
var censusCorpusTypes = map[string]bool{
	"[]searchengine.Document":   true,
	"[]searchengine.BucketWork": true,
}

// deriveBuildSinks computes the sink set from the package's own source: a function
// that takes an armed manager and a corpus, and reaches that engine either directly
// or through another sink. The fixpoint is what resolves sealPerPartition, whose own
// body only calls sealOne.
func (p *censusPkg) deriveBuildSinks() map[string]censusBuildSink {
	type candidate struct {
		unit *censusUnit
		sink censusBuildSink
	}
	var candidates []candidate
	for _, u := range p.units {
		if u.recv != "" {
			continue // a method on a named type is reached through the leg table
		}
		mgrArg, docsArg := -1, -1
		for i, typ := range u.paramTypes {
			if mgrArg < 0 && strings.Contains(typ, censusManagerType) {
				mgrArg = i
			}
			if docsArg < 0 && censusCorpusTypes[typ] {
				docsArg = i
			}
		}
		if mgrArg >= 0 && docsArg >= 0 {
			candidates = append(candidates, candidate{u, censusBuildSink{mgrArg: mgrArg, docsArg: docsArg}})
		}
	}
	out := map[string]censusBuildSink{}
	for range 8 {
		grew := false
		for _, c := range candidates {
			if _, done := out[c.unit.key]; done {
				continue
			}
			if censusReachesEngine(c.unit, out) {
				out[c.unit.key] = c.sink
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	return out
}

// censusReachesEngine reports whether a declaration touches an engine directly or
// calls a sink already known to.
func censusReachesEngine(u *censusUnit, known map[string]censusBuildSink) bool {
	found := false
	ast.Inspect(u.body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "engine" {
				found = true
			}
		}
		name := ""
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.IndexExpr:
			if id, ok := fn.X.(*ast.Ident); ok {
				name = id.Name
			}
		}
		if _, isSink := known[name]; isSink {
			found = true
		}
		return true
	})
	return found
}

// censusEnginePrimitives are the engine calls a test can make PAST the Manager
// entirely, spelled `<mgr>.engine.<name>(docs)`. They are the same gap class as
// stage-and-finalize: the build sits one call past every method the Manager walk
// can see.
var censusEnginePrimitives = map[string]bool{"Add": true, "AddSealAndSupersede": true}

// censusStagedFields maps the staged-rebuild field names to their arms. The staging
// method names its two document parameters after these fields, but the census reads
// the ASSIGNMENT rather than the parameter name, because a helper's name lies.
var censusStagedFields = map[string]censusArm{"hnsw": censusArmStagedHNSW, "bm25": censusArmStagedBM25}

// censusExternalConsts are the constants from other packages that this package sizes
// corpora with. The rule cites the CONSTANT, never the literal, so this binds the
// real Go value rather than a copy of its digits.
var censusExternalConsts = map[string]int{
	"searchengine.DefaultMinSegmentDocs": searchengine.DefaultMinSegmentDocs,
}

// censusUnit is one TOP-LEVEL DECLARATION and everything nested inside it. A
// function literal is part of its enclosing declaration rather than a unit of its
// own, which is what makes the package's shared 8192-document fixture visible: the
// build lives inside a closure inside a sync.OnceValue inside a package-level var.
type censusUnit struct {
	key        string // func name, "Recv.Method", or package-level var name
	file       string
	line       int
	params     []string // parameter names in order; "" for _ and for a var unit
	paramTypes []string // parameter types in the same order, as written
	body       ast.Node
	recv       string // receiver type as written, e.g. "*Manager"
	isTest     bool

	// docParams holds the indices of parameters typed []searchengine.Document.
	docParams map[int]bool
	// locals holds function-scoped integer const/var bindings. Keying them PER UNIT
	// rather than per file is what resolves seedN correctly: one file declares it
	// twice, 5000 in one test and 2040 inside a grouped const block in another.
	locals map[string]int
	// ambiguous names got two different constant values inside one unit; evaluating
	// one fails loudly rather than picking a winner.
	ambiguous map[string]bool
	// bounds holds function-scoped integers that are NOT compile-time constants but
	// do have a derivable ceiling, so a corpus sized by a bounded pseudo-random draw
	// resolves instead of fataling.
	bounds map[string]int
	// lens holds the element count of non-document slice parameters a caller bound,
	// so a corpus sized by len(ids) over a literal argument resolves.
	lens map[string]int
	// closures are the function literals this declaration binds to a NAME. They are
	// resolved at their own call sites with arguments bound, exactly like a package
	// function; an ANONYMOUS literal (a t.Run body, a defer, a goroutine) has no call
	// site to bind and stays part of this declaration.
	closures map[string]*censusUnit

	// Derived by the fixpoint in measurement_census_test.go.
	legParams map[int]censusArm
	finalizes bool
}

// censusPkg is the parsed package plus its package-level scope.
type censusPkg struct {
	fset  *token.FileSet
	units map[string]*censusUnit
	// methods maps a method NAME to every declaration that carries it. A name can be
	// declared on more than one receiver — FinalizeRebuild is on *Manager and on a
	// test adapter — and dropping an ambiguous name resolves the call to NOTHING,
	// which is how the build half of the stage-and-finalize pair went missing from an
	// earlier draft of this census and took sixteen tests with it.
	methods   map[string][]*censusUnit
	pkgConsts map[string]int
	testFiles int
	skipped   []string
}

// censusParsePackage parses every .go file in the package directory, excluding the
// diagnostic build tag. The test runs with CWD = this package directory.
func censusParsePackage(t *testing.T) *censusPkg {
	t.Helper()
	p := &censusPkg{
		fset:      token.NewFileSet(),
		units:     map[string]*censusUnit{},
		methods:   map[string][]*censusUnit{},
		pkgConsts: map[string]int{},
	}
	var files []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == "." {
				return nil
			}
			return fs.SkipDir // a single-package scan; nested dirs are siblings
		}
		if strings.HasSuffix(d.Name(), ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk package directory: %v", err)
	}
	for _, path := range files {
		file, perr := parser.ParseFile(p.fset, path, nil, parser.ParseComments)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		if censusHasDiagTag(file) {
			p.skipped = append(p.skipped, path)
			continue
		}
		if strings.HasSuffix(path, "_test.go") {
			p.testFiles++
		}
		p.collectFile(t, path, file)
	}
	return p
}

// censusHasDiagTag reports whether a file is behind the diagnostic build tag.
func censusHasDiagTag(file *ast.File) bool {
	for _, group := range file.Comments {
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				continue
			}
			if expr.Eval(func(tag string) bool { return tag == censusDiagBuildTag }) {
				return true
			}
		}
	}
	return false
}

// collectFile records one file's package-level constants and declaration units.
func (p *censusPkg) collectFile(t *testing.T, path string, file *ast.File) {
	t.Helper()
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if v, ok := p.constFold(vs.Values[i], nil); ok {
							p.pkgConsts[name.Name] = v
						}
					}
					// A package-level var whose value is a function literal (directly
					// or wrapped, as sync.OnceValue wraps this package's shared
					// two-layer corpus) is a unit: the build lives in its closure.
					if i < len(vs.Values) && censusContainsFuncLit(vs.Values[i]) {
						p.addUnit(&censusUnit{
							key: name.Name, file: path, line: p.fset.Position(name.Pos()).Line,
							body: vs.Values[i], docParams: map[int]bool{},
							locals: map[string]int{}, ambiguous: map[string]bool{},
							legParams: map[int]censusArm{}, lens: map[string]int{},
							bounds: map[string]int{}, closures: map[string]*censusUnit{},
						})
					}
				}
			}
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			u := &censusUnit{
				file: path, line: p.fset.Position(d.Pos()).Line, body: d.Body,
				docParams: map[int]bool{}, locals: map[string]int{},
				ambiguous: map[string]bool{}, legParams: map[int]censusArm{},
				lens: map[string]int{}, bounds: map[string]int{},
				closures: map[string]*censusUnit{},
			}
			u.key = d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				u.recv = censusTypeString(d.Recv.List[0].Type)
				u.key = strings.TrimPrefix(u.recv, "*") + "." + d.Name.Name
				p.methods[d.Name.Name] = append(p.methods[d.Name.Name], u)
			}
			u.isTest = strings.HasPrefix(d.Name.Name, "Test") && d.Recv == nil &&
				strings.HasSuffix(path, "_test.go") && censusTakesTestingT(d)
			censusCollectParams(u, d.Type.Params)
			p.addUnit(u)
		}
	}
}

func (p *censusPkg) addUnit(u *censusUnit) { p.units[u.key] = u }

// censusTakesTestingT keeps Benchmark, Fuzz and Example shapes out of the test set.
// SCOPE DECISION STATED: Benchmark functions build corpora at and above the
// threshold and are excluded because `go test` does not run them without -bench.
func censusTakesTestingT(d *ast.FuncDecl) bool {
	if d.Type.Params == nil || len(d.Type.Params.List) != 1 {
		return false
	}
	return censusTypeString(d.Type.Params.List[0].Type) == "*testing.T"
}

// censusCollectParams flattens a parameter list, recording which parameters are
// document slices. Grouped parameters (`hnswDocs, bm25Docs []searchengine.Document`)
// expand to one index each, which is the whole point: TWO LEGS TAKE BOTH SLICES.
func censusCollectParams(u *censusUnit, list *ast.FieldList) {
	if list == nil {
		return
	}
	for _, f := range list.List {
		isDocs := censusTypeString(f.Type) == "[]searchengine.Document"
		typ := censusTypeString(f.Type)
		if len(f.Names) == 0 {
			if isDocs {
				u.docParams[len(u.params)] = true
			}
			u.params = append(u.params, "")
			u.paramTypes = append(u.paramTypes, typ)
			continue
		}
		for _, n := range f.Names {
			if isDocs {
				u.docParams[len(u.params)] = true
			}
			u.params = append(u.params, n.Name)
			u.paramTypes = append(u.paramTypes, typ)
		}
	}
}

// censusCollectClosures registers the function literals a declaration binds to a
// name. A NAMED CLOSURE IS A CALLABLE, and treating it as one is what resolves the
// package's shared two-layer corpus (a build inside `build := func(salt byte)`
// inside a sync.OnceValue) and the small per-subtest fixtures whose corpus size is
// the length of an argument their caller wrote out.
func (p *censusPkg) censusCollectClosures(u *censusUnit) {
	ast.Inspect(u.body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, rhs := range as.Rhs {
			lit, ok := rhs.(*ast.FuncLit)
			if !ok {
				continue
			}
			id, ok := as.Lhs[i].(*ast.Ident)
			if !ok || id.Name == "_" {
				continue
			}
			c := &censusUnit{
				key: u.key + "." + id.Name, file: u.file,
				line: p.fset.Position(lit.Pos()).Line, body: lit.Body,
				docParams: map[int]bool{}, locals: map[string]int{},
				ambiguous: map[string]bool{}, legParams: map[int]censusArm{},
				lens: map[string]int{}, bounds: map[string]int{},
				closures: map[string]*censusUnit{},
			}
			censusCollectParams(c, lit.Type.Params)
			p.censusCollectLocals(c)
			p.censusCollectClosures(c)
			u.closures[id.Name] = c
		}
		return true
	})
}
