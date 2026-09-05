// SPDX-License-Identifier: Apache-2.0

package segmentdist

// measurement_census_calls_test.go holds the CALL-RESOLUTION predicates of the
// measurement-gate census — which call is a build sink, which is an engine
// primitive, which expression is staged work — plus the gate's first-statement
// check and the fourth, cross-checking arm's recorded set.

import (
	"go/ast"
	"maps"
	"sort"
	"strings"
	"testing"
)

// censusBind is one call's argument values, seen from inside the callee.
type censusBind struct {
	ints map[string]int // integer parameters, exact or upper-bounded
	docs map[string]int // document-slice parameters, in documents
	lens map[string]int // any other slice parameter, in elements
}

// censusReach is what one declaration builds once its arguments are bound.
type censusReach struct {
	direct, staged int
	finalizes      bool
}

func (r censusReach) merge(o censusReach) censusReach {
	return censusReach{
		direct:    max(r.direct, o.direct),
		staged:    max(r.staged, o.staged),
		finalizes: r.finalizes || o.finalizes,
	}
}

// censusMaxDepth bounds the recursion. The package's deepest chain is a test into a
// fixture into a seed helper into a Manager method into a build sink; a limit an
// order of magnitude above that turns a cycle the seen-set missed into a bounded
// walk rather than a stack overflow.
const censusMaxDepth = 40

// scopedUnit clones a declaration with its caller's argument values in scope.
func (a *censusAnalysis) scopedUnit(u *censusUnit, bind censusBind) *censusUnit {
	if len(bind.ints) == 0 && len(bind.lens) == 0 {
		return u
	}
	scoped := *u
	scoped.locals = map[string]int{}
	maps.Copy(scoped.locals, u.locals)
	maps.Copy(scoped.locals, bind.ints)
	scoped.lens = map[string]int{}
	maps.Copy(scoped.lens, u.lens)
	maps.Copy(scoped.lens, bind.lens)
	return &scoped
}

// bindCall evaluates a call's arguments in the caller's scope.
func (a *censusAnalysis) bindCall(callee *censusUnit, args []ast.Expr, caller *censusUnit, docs map[string]int) censusBind {
	bind := censusBind{ints: map[string]int{}, docs: map[string]int{}, lens: map[string]int{}}
	for i, arg := range args {
		if i >= len(callee.params) || callee.params[i] == "" || callee.params[i] == "_" {
			continue
		}
		name := callee.params[i]
		if v, ok := a.pkg.censusUpperBound(arg, caller); ok {
			bind.ints[name] = v
		}
		if n, ok := a.docSize(arg, caller, docs, map[string]bool{}); ok {
			bind.docs[name] = n
		}
		if n, ok := a.sliceLen(arg, caller); ok {
			bind.lens[name] = n
		}
	}
	return bind
}

// sliceLen counts the elements of a literal slice argument, so a corpus sized by
// `len(ids)` over an argument the caller wrote out resolves rather than fataling.
func (a *censusAnalysis) sliceLen(e ast.Expr, u *censusUnit) (int, bool) {
	switch x := e.(type) {
	case *ast.CompositeLit:
		return len(x.Elts), true
	case *ast.Ident:
		if n, ok := u.lens[x.Name]; ok {
			return n, true
		}
	}
	return 0, false
}

// callsIn lists the call expressions in a declaration, skipping the bodies of named
// closures: those are resolved at their own call sites, with arguments bound.
func (a *censusAnalysis) callsIn(u *censusUnit) []*ast.CallExpr {
	var out []*ast.CallExpr
	skip := map[ast.Node]bool{}
	for _, c := range u.closures {
		skip[c.body] = true
	}
	var walk func(n ast.Node) bool
	walk = func(n ast.Node) bool {
		if n == nil || skip[n] {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			out = append(out, call)
		}
		return true
	}
	ast.Inspect(u.body, walk)
	return out
}

// callSink resolves a call to a DERIVED build sink.
func (a *censusAnalysis) callSink(call *ast.CallExpr) (censusBuildSink, bool) {
	name := ""
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		name = fn.Name
	case *ast.IndexExpr:
		if id, ok := fn.X.(*ast.Ident); ok {
			name = id.Name
		}
	}
	s, ok := a.sinks[name]
	return s, ok
}

// censusEngineCall resolves `<x>.engine.Add(...)` and its supersede sibling,
// returning the manager expression's text.
func censusEngineCall(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !censusEnginePrimitives[sel.Sel.Name] {
		return "", false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	if !ok || inner.Sel.Name != "engine" {
		return "", false
	}
	return censusExprString(inner.X), true
}

// censusIsStagedWork reports whether an expression is the staged record's own
// document field — the finalize's input rather than a corpus with a static size.
func censusIsStagedWork(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	_, staged := censusStagedFields[sel.Sel.Name]
	return staged
}

// censusRoutesOnABool reports whether a declaration selects between the hnsw and
// bm25 fields on a boolean parameter — the shared sink shape, which has no static
// arm and is therefore not a leg in its own right.
func censusRoutesOnABool(u *censusUnit) bool {
	hasBool := false
	for _, p := range u.params {
		if p == "fields" {
			hasBool = true
		}
	}
	seen := map[string]bool{}
	ast.Inspect(u.body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if _, staged := censusStagedFields[sel.Sel.Name]; staged {
				seen[sel.Sel.Name] = true
			}
		}
		return true
	})
	return hasBool && seen["hnsw"] && seen["bm25"]
}

// censusRequireGateIsAHelper is the census's control (e).
//
// make measure-segmentdist decides whether a measurement really ran by counting gate
// skip messages ATTRIBUTED TO A GATED TEST'S OWN FILE, and that attribution exists
// only because the gate helper declares itself one. Drop the t.Helper() call and go
// test blames the gate's own file for all 78 skips, the recipe's guard counts zero,
// and the target prints a duration table and exits 0 having measured nothing — with
// every test in this package still green, which is why this is a control and not a
// comment.
func censusRequireGateIsAHelper(t *testing.T, p *censusPkg) {
	t.Helper()
	gate := p.units[censusGateHelper]
	if gate == nil {
		t.Fatalf("%s is not declared in this package — the gate this census enforces does not exist", censusGateHelper)
	}
	if !censusOpensWithTHelper(gate) {
		t.Fatalf("%s does not call t.Helper() as its first statement: go test would blame the gate's own file for "+
			"every gated test's skip, so make measure-segmentdist's MEASUREMENT DID NOT RUN guard — which counts "+
			"skips attributed to a gated test's own file — would count zero and pass a run in which nothing was measured",
			censusGateHelper)
	}
}

// censusOpensWithTHelper reports whether a declaration's FIRST statement is a
// t.Helper() call. It is the gate's half of the attribution contract the
// measure-segmentdist recipe's guard reads.
func censusOpensWithTHelper(u *censusUnit) bool {
	body, ok := u.body.(*ast.BlockStmt)
	if !ok || len(body.List) == 0 {
		return false
	}
	es, ok := body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Helper"
}

// censusOpensWithGate reports whether the gate helper is the FIRST statement.
func censusOpensWithGate(u *censusUnit) bool {
	body, ok := u.body.(*ast.BlockStmt)
	if !ok || len(body.List) == 0 {
		return false
	}
	es, ok := body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == censusGateHelper
}

// censusDiffNames compares the derivation's membership with the recorded set.
func censusDiffNames(t *testing.T, selected []string) {
	t.Helper()
	recorded := map[string]bool{}
	for _, n := range censusHeavySet {
		recorded[n] = true
	}
	got := map[string]bool{}
	for _, n := range selected {
		got[n] = true
	}
	var added, dropped []string
	for n := range got {
		if !recorded[n] {
			added = append(added, n)
		}
	}
	for n := range recorded {
		if !got[n] {
			dropped = append(dropped, n)
		}
	}
	sort.Strings(added)
	sort.Strings(dropped)
	if len(added) > 0 || len(dropped) > 0 {
		t.Errorf("THE DERIVATION AND THE RECORDED SET DISAGREE (derivation selected %d, record holds %d).\n"+
			"  selected but not recorded (%d): %s\n"+
			"  recorded but not selected (%d): %s\n"+
			"  The derivation is the authority: it re-reads the tree, the list does not.\n"+
			"  Establish which moved — a new heavy test, a corpus resized, a leg added —\n"+
			"  then update censusHeavySet and the ticket's landing notes together.",
			len(selected), len(censusHeavySet),
			len(added), strings.Join(added, ", "),
			len(dropped), strings.Join(dropped, ", "))
	}
}

// censusHeavySet is the fourth arm's RECORD: the membership the derivation returned
// when a human last read it, held here so a silent drift between the rule and the
// tree fails a test instead of going unnoticed. IT IS NOT AN INPUT to the
// derivation, and it cannot substitute for control (c), because it is seeded from
// the same leg table that control guards.
var censusHeavySet = []string{
	"TestAssertLiveSetBackedByL2SelfTest",
	"TestBM25ResetRetiresPriorLayer",
	"TestBranchSeedAbortsWhenACopiedBlobCannotBePersisted",
	"TestBranchSeedDoesNotResurrectRetiredDocs",
	"TestClosureRebuildSetStaysBounded",
	"TestDeferredDeleteIdsSurviveARebuildThatDidNotEmitTheirPartition",
	"TestDeferredDrainTrimsExactlyThePartitionsItEmitted",
	"TestDeferredReEmitConvergesAndThenStops",
	"TestDeferredReEmitIDsServesWholePartitionsWithinTheBudget",
	"TestDeferredReEmitSaysNothingAboutAConvergedGraph",
	"TestDeferredSelectorDeclinesBelowTheResidencyFloor",
	"TestDeferredTrimDoesNotFireWhenTheDrainsPersistFails",
	"TestDeleteAbsorbsATransientL2WriteFailure",
	"TestDeleteAcrossACountChangeKeepsCorpusExact",
	"TestDeleteFromBuckets_NeverHeldIdsDirtyNoPartitions",
	"TestDeleteRemovesResultsFromSearchWithoutAnHNSWReEmit",
	"TestDeleteWalksOnlySpanningConstituents",
	"TestDeletionIsDurableAcrossReloadBeforeAnyReEmit",
	"TestDeterministicConvergenceAcrossWriters",
	"TestDeterministicConvergenceRepeated",
	"TestDeterministicShipKeepsBothFormatsResolvable",
	"TestDrainDerivesCountFromTrueCorpus",
	"TestDrainOnAnUnloadedPoolLeavesTheDurableRecordUntouched",
	"TestDuplicateIdMergeKeepsEveryDistinctMember",
	"TestEmbedDrainAfterRebuildDropsRetiredLayer",
	"TestEmbedDrainCoalescesMergesOntoReconcileTick",
	"TestEmbedShipKeepsBothFormatsResolvable",
	"TestFinalizeRebuildReportsCompletedSwap",
	"TestFinalizeReportsPerFormatRetirement",
	"TestFreshProcessCannotRetireAPriorCorpus",
	"TestGroupRebuildDiagnosticEmitsBeforeTheExpensiveCall",
	"TestHNSWResetRoutesThroughReplaceLayer",
	"TestInvalidateLocalEvictsFromTheServingCache",
	"TestLoadReadsL2InEveryBranch",
	"TestManagerAddAndMarkDirtySealsOneBlob",
	"TestManagerFor_BranchGraphSeedsFromBaseOnce",
	"TestManagerResidentDocCount",
	"TestManagerRoutesPerGraph",
	"TestManagerSearchFusesBothEngines",
	"TestManagerSearchTextOnlyArm",
	"TestManagerVectorByIDResolvesShippedVector",
	"TestMergedPayloadIsMappingBackedNotHeapBacked",
	"TestMultiIDDeleteDefersAtTheSeam",
	"TestPartialLayerNeverRetiresAGoodLayer",
	"TestPartialRebuildSetAcrossACountChangeLosesNothing",
	"TestPostLoadMergeLeakIsReclaimedByTheNextRebuild",
	"TestPruneCacheColdStartStillLoadsTheWholeCorpus",
	"TestPruneCacheCoversTheRebuiltLayer",
	"TestPruneCacheDriverDryRun",
	"TestPruneCacheDriverExecute",
	"TestPruneCacheLiveSearchAfterPrune",
	"TestReBucketTriggerFiresOnlyWhenADoublingBehind",
	"TestReBucketTriggerSuppressedDuringPartialRealignment",
	"TestReEmitAcrossACountChangeKeepsCorpusExact",
	"TestReEmitKeepsPartitionsPure",
	"TestReEmitKeepsPartitionsPureAcrossACountChange",
	"TestRebuildDeltaReEmitsOwningBucketOnly",
	"TestRebuildSwapReclaimsThenInvalidateLocalBackstops",
	"TestRebuiltDeltaCorpusExactAcrossCountChange",
	"TestRecallSurvivesDuplicateIdMerge",
	"TestRecallSurvivesWindowScaleDuplicateMerge",
	"TestReclaimBoundedGrowth",
	"TestRepartitionConvergesToOnePerPartition",
	"TestReplaceLayerShipsBeforeSwapping",
	"TestResetRebuildPublishesOnlyThisRunsLayer",
	"TestResetSwapAbsorbsBuildWindowSurvivors",
	"TestResetSwapPreservesBuildWindowSurvivorsAsDuplicates",
	"TestResetThenDeltaThenDrainKeepsCardinality",
	"TestResidentObservationsIsolatePerArmErrors",
	"TestRestartLoadImportsTheFullL2Corpus",
	"TestSearchAcrossACountChangeReturnsKDistinct",
	"TestSearchOverlayFusesHNSWArmAcrossPools",
	"TestSeedBranchBucketFromBase_CopiesPublishedPartitions",
	"TestSeedBranchBucketFromBase_RefusesToOverflowTheBudget",
	"TestSeedBranchBucket_CopiesRebuildRecord",
	"TestSegmentPayloadStripsAnEnvelopeTheFormatWouldRefuse",
	"TestSupersededBlobsAreEvictedFromL2",
	"TestTickDerivesTheTrueCorpusCount",
}
