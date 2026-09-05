// SPDX-License-Identifier: Apache-2.0

package segmentdist

// measurement_census_test.go is the LIVE GATE for the classification rule stated in
// measurement_gate_test.go: it re-derives, from this package's own source on every
// run, which tests build an HNSW corpus at or above the threshold, and fails naming
// any that do not carry requireMeasurementRun as their first statement.
//
// IT IS RULE-DERIVED, NOT LIST-DERIVED, AND THAT DISTINCTION IS THE WHOLE POINT. An
// earlier round specified this census as a comparison between the gated set and a
// hard-coded list of names — an identity check whose answer key comes from the thing
// under test, which reports clean over every test the list forgot. Here the leg
// table, the corpus sizes and the membership are all computed; the name list survives
// only as a FOURTH cross-checking arm that fails when it and the derivation disagree.
//
// THE THREE DERIVATIONS, in order:
//
//	(i)   THE LEG TABLE, from the package's own non-test source. Every method on
//	      *Manager taking a document slice is enumerated, and each of its document
//	      parameters is classified by where that parameter's value FLOWS: into a
//	      build sink under an hnsw-armed manager, under a bm25-armed one, or into
//	      staged rebuild work that builds nothing until a finalize takes it.
//	(ii)  THE HELPER TABLE, by the same flow analysis over every declaration in the
//	      package, test files included. A helper that passes its own parameter into
//	      a leg exports that parameter as a leg of its own, so seedShipped,
//	      stageRebuildRun and stageBM25Reset classify themselves and the naming trap
//	      resolves without being named.
//	(iii) THE GATE, per test: the maximum corpus reaching an HNSW leg, against
//	      searchengine.DefaultMinSegmentDocs.
//
// FIVE ANTI-VACUITY CONTROLS, each of which has caught something:
//
//	(a) a scan that read zero test files FATALS;
//	(b) a helper table that resolved zero corpus generators FATALS;
//	(c) a *Manager method that consumes a document slice and is left UNCLASSIFIED
//	    FATALS, naming the method — without it the specification can be restated
//	    wrongly and the census stays green, and the name-list arm cannot substitute
//	    because it is seeded from this same table's output;
//	(d) a derived table carrying a staged-hnsw leg but NO finalize row FATALS — the
//	    staging half of a pair is not a build, so a table that lost the build half
//	    would gate on nothing;
//	(e) a gate helper that is not a t.Helper FATALS. This one guards an artifact
//	    outside Go: make measure-segmentdist decides whether a measurement really
//	    ran by counting gate skip messages ATTRIBUTED TO A GATED TEST'S OWN FILE,
//	    and that attribution exists only because the helper declares itself one.
//	    Without the declaration go test blames the gate's own file for every skip,
//	    the recipe's guard counts zero, and the target prints a duration table and
//	    exits 0 having measured nothing.
//
// AND ONE LOUDNESS RULE: a leg site whose corpus size resolves to neither a constant
// nor a derivable upper bound FATALS naming the site, rather than being guessed at.

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// censusGateHelper is the call every heavy test must open with.
const censusGateHelper = "requireMeasurementRun"

// censusLegSite is one place a document slice reaches a leg.
type censusLegSite struct {
	arm  censusArm
	docs ast.Expr
	pos  token.Position
}

// censusAnalysis is the whole derivation.
type censusAnalysis struct {
	pkg *censusPkg
	// unresolved maps each unbounded leg site to a declaration that reaches it.
	unresolved map[string]string
	// generatorsResolved names every corpus generator whose size rule the resolver
	// actually evaluated. It backs control (b): a helper table that resolved none of
	// them would report every test sub-threshold and stay green.
	generatorsResolved map[string]bool
	// reach holds derivation (iii)'s answer per test.
	reach map[string]censusReach
	// sinks is the derived build-sink set — see deriveBuildSinks.
	sinks map[string]censusBuildSink
	// memo caches one declaration's result per distinct argument binding. Without it
	// the walk re-derives a shared fixture once per caller and the census costs more
	// than the tests it is classifying.
	memo map[string]censusReach
}

// censusAnalyze runs the three derivations.
//
// THE LEG TABLE IS A FIXPOINT over parameter flow, because a helper exports a leg
// only once its callee's legs are known. THE REACHABLE CORPORA ARE A RECURSIVE
// EVALUATION rooted at each test, because a size is only meaningful with the
// caller's arguments bound: deleteRetryFixtureOfSize builds whatever its caller
// asked for, and a table of per-declaration constants cannot say what that was.
func censusAnalyze(t *testing.T, p *censusPkg) *censusAnalysis {
	t.Helper()
	a := &censusAnalysis{pkg: p, reach: map[string]censusReach{}, unresolved: map[string]string{}, memo: map[string]censusReach{}}
	for _, u := range p.units {
		p.censusCollectLocals(u)
		p.censusCollectClosures(u)
	}
	a.sinks = p.deriveBuildSinks()
	for range 12 {
		changed := false
		for _, u := range p.units {
			if a.deriveLegParams(u) {
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	// Derivation (iii) is rooted at the TESTS. A leg site no test can reach is not a
	// membership this census has to decide, which is also what keeps benchmarks and
	// dead helpers out of the unresolved report by construction rather than by name.
	for _, u := range p.units {
		if u.isTest {
			a.reach[u.key] = a.reachOf(u, censusBind{}, map[string]bool{}, 0, u.key)
		}
	}
	return a
}

// deriveLegParams recomputes one declaration's leg parameters — derivation (i) for
// the Manager methods and (ii) for every helper — returning whether anything moved.
func (a *censusAnalysis) deriveLegParams(u *censusUnit) bool {
	before := fmt.Sprint(u.legParams)
	arms := a.armBindings(u)
	taint := a.paramTaint(u)
	for _, site := range a.legSites(u, arms, true) {
		for _, o := range a.originParams(site.docs, u, taint) {
			if u.legParams[o] < site.arm || u.legParams[o] == censusArmNone {
				u.legParams[o] = site.arm
			}
		}
	}
	if a.isFinalizer(u, arms) {
		u.finalizes = true
	}
	return before != fmt.Sprint(u.legParams)
}

// reachOf evaluates what a declaration builds with its parameters bound, inlining
// every call it makes. It also records the leg sites whose size it could not bound.
func (a *censusAnalysis) reachOf(u *censusUnit, bind censusBind, seen map[string]bool, depth int, path string) (out censusReach) {
	if depth > censusMaxDepth || seen[u.key] {
		return out
	}
	key := u.key + "|" + fmt.Sprint(bind.ints, bind.docs, bind.lens)
	if r, ok := a.memo[key]; ok {
		return r
	}
	seen[u.key] = true
	defer delete(seen, u.key)
	if path == "" {
		path = u.key
	}
	defer func() { a.memo[key] = out }()

	scoped := a.scopedUnit(u, bind)
	arms := a.armBindings(scoped)
	docs := a.docLocals(scoped, bind.docs, map[string]bool{})

	for _, site := range a.legSites(scoped, arms, false) {
		if site.arm != censusArmHNSW && site.arm != censusArmStagedHNSW {
			continue
		}
		n, ok := a.docSize(site.docs, scoped, docs, map[string]bool{})
		if !ok {
			// Deduped by site: one unbounded expression reached down many call paths
			// is one defect to fix, not one per path.
			a.unresolved[fmt.Sprintf("%s: %s leg carries %s, whose size resolves to no constant and no upper bound",
				site.pos, site.arm, censusExprString(site.docs))] = path
			continue
		}
		if site.arm == censusArmHNSW {
			out.direct = max(out.direct, n)
		} else {
			out.staged = max(out.staged, n)
		}
	}
	if a.isFinalizer(scoped, arms) {
		out.finalizes = true
	}
	for _, call := range a.callsIn(scoped) {
		candidates := a.calleesOf(call)
		if len(candidates) == 0 {
			if c := scoped.closures[censusExprString(call.Fun)]; c != nil {
				candidates = []*censusUnit{c}
			}
		}
		for _, callee := range candidates {
			// A BUILD SINK IS A LEAF OF THE DERIVATION. Its call was already counted
			// above with the corpus the caller handed it; walking inside re-derives
			// the same build from the partitioned locals it works through, which carry
			// less information than the argument does.
			if _, isSink := a.sinks[callee.key]; isSink {
				continue
			}
			out = out.merge(a.reachOf(callee, a.bindCall(callee, call.Args, scoped, docs), seen, depth+1,
				path+" -> "+callee.key))
		}
	}
	return out
}

// armBindings maps local identifiers to the engine arm they were selected for.
// `dm := m.managerFor(...)` is the hnsw arm; `bm := m.bm25ManagerFor(...)` the bm25
// one. This is what lets one method route two slices to two arms.
func (a *censusAnalysis) armBindings(u *censusUnit) map[string]censusArm {
	out := map[string]censusArm{}
	ast.Inspect(u.body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, rhs := range as.Rhs {
			call, ok := rhs.(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			id, ok := as.Lhs[i].(*ast.Ident)
			if !ok {
				continue
			}
			// A NAME BOUND TO BOTH ARMS IS THE VECTOR ARM. Sibling subtests reuse
			// one name for two managers, and these bindings are per-declaration
			// rather than per-block, so a later bm25 binding must not erase an
			// earlier hnsw one: some site under that name really does build.
			switch sel.Sel.Name {
			case "managerFor":
				out[id.Name] = censusArmHNSW
			case "bm25ManagerFor":
				if out[id.Name] != censusArmHNSW {
					out[id.Name] = censusArmBM25
				}
			}
		}
		return true
	})
	return out
}

// legSites finds every place inside one declaration where a document slice reaches
// a leg: a build sink, an engine primitive, a staged-rebuild append, or a call to
// another declaration at a parameter index already known to be a leg.
func (a *censusAnalysis) legSites(u *censusUnit, arms map[string]censusArm, withCalleeLegs bool) []censusLegSite {
	var out []censusLegSite
	add := func(arm censusArm, docs ast.Expr, pos token.Pos) {
		if arm == censusArmHNSW || arm == censusArmStagedHNSW || arm == censusArmBM25 || arm == censusArmStagedBM25 {
			out = append(out, censusLegSite{arm: arm, docs: docs, pos: a.pkg.fset.Position(pos)})
		}
	}
	skip := map[ast.Node]bool{}
	for _, c := range u.closures {
		skip[c.body] = true
	}
	ast.Inspect(u.body, func(n ast.Node) bool {
		if n == nil || skip[n] {
			return false
		}
		switch x := n.(type) {
		case *ast.AssignStmt:
			// Staged rebuild work: an assignment into the `.hnsw` or `.bm25` field of
			// the staged record. Read from the ASSIGNMENT, never the parameter name.
			for _, lhs := range x.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				arm, ok := censusStagedFields[sel.Sel.Name]
				if !ok {
					continue
				}
				for _, rhs := range x.Rhs {
					for _, e := range censusDocOperands(rhs, u) {
						add(arm, e, x.Pos())
					}
				}
			}
		case *ast.CallExpr:
			if sink, ok := a.callSink(x); ok {
				if sink.docsArg < len(x.Args) && sink.mgrArg < len(x.Args) {
					arm := arms[censusExprString(x.Args[sink.mgrArg])]
					if arm == censusArmNone {
						// An unbound manager expression is treated as the VECTOR arm.
						// Over-reporting a leg makes the census flag a test that need
						// not be gated, which an author sees; under-reporting hides one
						// that must be, which nobody sees.
						arm = censusArmHNSW
					}
					if !censusIsStagedWork(x.Args[sink.docsArg]) {
						add(arm, x.Args[sink.docsArg], x.Pos())
					}
				}
			}
			if name, ok := censusEngineCall(x); ok && len(x.Args) > 0 {
				arm := arms[name]
				if arm == censusArmNone {
					arm = censusArmHNSW
				}
				add(arm, x.Args[0], x.Pos())
			}
			if withCalleeLegs {
				for _, callee := range a.calleesOf(x) {
					for idx, arm := range callee.legParams {
						if idx < len(x.Args) {
							add(arm, x.Args[idx], x.Pos())
						}
					}
				}
			}
		}
		return true
	})
	return out
}

// isFinalizer reports whether this declaration BUILDS previously staged work: a
// build sink under an hnsw-armed manager whose documents are the staged record's own
// field rather than anything this declaration was passed.
func (a *censusAnalysis) isFinalizer(u *censusUnit, arms map[string]censusArm) bool {
	found := false
	ast.Inspect(u.body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sink, ok := a.callSink(call)
		if !ok || sink.docsArg >= len(call.Args) || sink.mgrArg >= len(call.Args) {
			return true
		}
		if !censusIsStagedWork(call.Args[sink.docsArg]) {
			return true
		}
		if arms[censusExprString(call.Args[sink.mgrArg])] == censusArmHNSW {
			found = true
		}
		return true
	})
	return found
}

// calleesOf resolves one call expression to the declarations in this package it
// could reach. A method name carried by several receivers resolves to ALL of them
// and their results are merged, because over-reporting a build is a failure an
// author reads and under-reporting one is a failure nobody sees.
func (a *censusAnalysis) calleesOf(call *ast.CallExpr) []*censusUnit {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if u := a.pkg.units[fn.Name]; u != nil {
			return []*censusUnit{u}
		}
	case *ast.SelectorExpr:
		return a.pkg.methods[fn.Sel.Name]
	case *ast.IndexExpr: // a generic instantiation, e.g. helper[T](...)
		if id, ok := fn.X.(*ast.Ident); ok {
			if u := a.pkg.units[id.Name]; u != nil {
				return []*censusUnit{u}
			}
		}
	}
	return nil
}

// calleeOf is calleesOf when exactly one declaration answers, which is what a size
// resolution needs: a generator with two candidate declarations is not a size.
func (a *censusAnalysis) calleeOf(call *ast.CallExpr) *censusUnit {
	if c := a.calleesOf(call); len(c) == 1 {
		return c[0]
	}
	return nil
}

// TestMeasurementGateCensus is the live gate.
func TestMeasurementGateCensus(t *testing.T) {
	t.Parallel()

	p := censusParsePackage(t)
	// CONTROL (a).
	if p.testFiles == 0 {
		t.Fatalf("scanned zero _test.go files — the census would be vacuously green")
	}
	a := censusAnalyze(t, p)
	// CONTROL (b), in both of its halves: a table that resolved no corpus generator,
	// or a leg table that derived no build sink, would report every test in the
	// package sub-threshold and stay green having classified nothing.
	if len(a.generatorsResolved) == 0 {
		t.Fatalf("resolved zero corpus generators — the helper table would be vacuously green")
	}
	if len(a.sinks) == 0 {
		t.Fatalf("derived zero build sinks — the leg table would be vacuously green")
	}
	// CONTROL (c): every *Manager method consuming a document slice is classified.
	var unclassified []string
	staged, finalizers := 0, 0
	for _, u := range p.units {
		// The finalize half of the staged pair takes NO document slice — it takes the
		// staged record — so it is counted over every declaration rather than over the
		// document-consuming ones. Counting it inside the loop below is exactly how a
		// table can lose the build half and still report itself complete.
		if u.finalizes {
			finalizers++
		}
		if strings.TrimPrefix(u.recv, "*") != "Manager" || len(u.docParams) == 0 {
			continue
		}
		for idx := range u.docParams {
			switch u.legParams[idx] {
			case censusArmStagedHNSW:
				staged++
			case censusArmNone:
				if !censusRoutesOnABool(u) {
					unclassified = append(unclassified, fmt.Sprintf("%s (%s:%d) parameter %d (%s)",
						u.key, u.file, u.line, idx, u.params[idx]))
				}
			}
		}
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		t.Fatalf("LEG TABLE INCOMPLETE — %d Manager document parameter(s) reached no build sink and no staged field:\n  %s\n"+
			"  A leg the table cannot see is a leg the gate cannot enforce. Classify it by\n"+
			"  following where the parameter flows, or add the sink it reaches to\n"+
			"  censusBuildSinks; never add the test to a list.",
			len(unclassified), strings.Join(unclassified, "\n  "))
	}
	// CONTROL (d): staging without a finalize is not a build.
	if staged > 0 && finalizers == 0 {
		t.Fatalf("LEG TABLE CARRIES %d staged-hnsw parameter(s) and NO finalize row — staging only appends "+
			"BucketWork under mu, so a table that lost the build half of the pair would gate on nothing", staged)
	}
	// CONTROL (e): the gate helper must be a t.Helper. Stated where its predicate
	// lives, because the reason is about attribution rather than about this walk.
	censusRequireGateIsAHelper(t, p)
	// LOUDNESS RULE: no guessed sizes.
	if len(a.unresolved) > 0 {
		lines := make([]string, 0, len(a.unresolved))
		for site, from := range a.unresolved {
			lines = append(lines, site+"\n    via "+from)
		}
		sort.Strings(lines)
		t.Fatalf("UNRESOLVED CORPUS SIZE at %d leg site(s):\n  %s\n"+
			"  The census decides membership on size, so a size it cannot bound is a\n"+
			"  membership it cannot decide. Resolve it, or bound it.",
			len(a.unresolved), strings.Join(lines, "\n  "))
	}

	// DERIVATION (iii): the gate.
	var selected, ungated []string
	for _, u := range p.units {
		if !u.isTest {
			continue
		}
		r := a.reach[u.key]
		heavy := r.direct >= searchengine.DefaultMinSegmentDocs ||
			(r.staged >= searchengine.DefaultMinSegmentDocs && r.finalizes)
		if !heavy {
			continue
		}
		selected = append(selected, u.key)
		if !censusOpensWithGate(u) {
			ungated = append(ungated, fmt.Sprintf("%s (%s:%d) reaches %d documents on an HNSW leg",
				u.key, u.file, u.line, max(r.direct, r.staged)))
		}
	}
	sort.Strings(selected)
	sort.Strings(ungated)
	if len(ungated) > 0 {
		t.Errorf("%d test(s) build an HNSW corpus of %d documents or more without %s(t) as the first statement:\n  %s\n"+
			"  Benchmarks and files behind //go:build %s are out of scope by decision and were not scanned.",
			len(ungated), searchengine.DefaultMinSegmentDocs, censusGateHelper,
			strings.Join(ungated, "\n  "), censusDiagBuildTag)
	}

	// THE FOURTH ARM, and it is a CROSS-CHECK rather than the answer key: the
	// derivation above decides membership, and this list is what the derivation
	// returned when it was last read by a human. A disagreement in either direction
	// is a failure, because it means the rule and the record have drifted apart.
	censusDiffNames(t, selected)
}
