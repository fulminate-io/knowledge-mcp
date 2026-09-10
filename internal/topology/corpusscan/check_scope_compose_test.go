// SPDX-License-Identifier: Apache-2.0

// check_scope_compose_test.go — the per-check scope where it meets the OTHER
// narrowings: the caller's file list, the caller's path_prefix, the graph
// executor, and the shared symbols the practice side reads the same scope with.
//
// IT IS A SECOND FILE BECAUSE COMPOSITION IS A SECOND PROPERTY. check_scope_test.go
// asks whether a declared scope is honored at all; these rows ask what happens
// when TWO narrowings are in force at once, which is where a "last one wins"
// implementation passes every row of the first file and widens or drops a scan
// here.

package corpusscan

import (
	"maps"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// TestCheckScope_ComposesWithTheFileListScope is requirement 3's composition
// leg: the two narrowings INTERSECT rather than either replacing the other.
func TestCheckScope_ComposesWithTheFileListScope(t *testing.T) {
	root := scopeRepo(t)
	gc := astCorpus(scopedCheckNode("chk-1", scopeMeta("", "lib")))

	// CONTROL A: the file list alone reaches both named files.
	open := astCorpus(scopedCheckNode("chk-1", nil))
	if got := siteFiles(runScan(t, withFiles(scanRequest(open, "knowledge", root), "lib/a.go", "other/d.go"))); len(got) != 2 {
		t.Fatalf("control: an unscoped check over both named files flags both, got %v", got)
	}
	// CONTROL B: the check scope alone reaches both files under lib.
	if got := siteFiles(runScan(t, scanRequest(gc, "knowledge", root))); len(got) != 2 {
		t.Fatalf("control: the scoped check alone flags both files under lib, got %v", got)
	}

	got := siteFiles(runScan(t, withFiles(scanRequest(gc, "knowledge", root), "lib/a.go", "other/d.go")))
	if len(got) != 1 || got[0] != "lib/a.go" {
		t.Fatalf("the intersection of the named files and the check's scope is lib/a.go alone, got %v", got)
	}

	// THE SIBLING-PREFIX ROW, on the named-file side of the intersection. A named
	// file under libextra is NOT under a check scoped to lib, and a bare string
	// prefix says it is — this row is where the two answers differ, and the run is
	// then refused rather than reporting a clean verdict over an unwalked file.
	if err := runScanErr(withFiles(scanRequest(gc, "knowledge", root), "libextra/c.go")); err == nil {
		t.Fatal("a named file under a SIBLING of the check's prefix is outside its scope, which leaves the run nothing to execute")
	}
}

// TestCheckScope_ComposesWithPathPrefix is the same composition through the
// other caller channel, because a feature that reaches one channel and not its
// sibling is a silent drop in the other.
func TestCheckScope_ComposesWithPathPrefix(t *testing.T) {
	root := scopeRepo(t)
	gc := astCorpus(scopedCheckNode("chk-1", scopeMeta("", "lib")))

	req := scanRequest(gc, "knowledge", root)
	req.PathPrefix = "lib/nested"
	got := siteFiles(runScan(t, req))
	if len(got) != 1 || got[0] != "lib/nested/b.go" {
		t.Fatalf("a path_prefix deeper than the check's scope intersects to the deeper one, got %v", got)
	}

	// The other direction: the check's scope is the deeper of the two.
	deep := astCorpus(scopedCheckNode("chk-1", scopeMeta("", "lib/nested")))
	req2 := scanRequest(deep, "knowledge", root)
	req2.PathPrefix = "lib"
	if got := siteFiles(runScan(t, req2)); len(got) != 1 || got[0] != "lib/nested/b.go" {
		t.Fatalf("a check scope deeper than the path_prefix intersects to the check's, got %v", got)
	}

	// A DISJOINT PAIR IS OUT OF SCOPE, not a widened walk: it is the case a
	// union or a "last one wins" composition would render as a full scan. The
	// SIBLING-PREFIX row is the one that also tells the two matchers apart: under
	// a bare string prefix `libextra` reads as "inside lib" and the check runs.
	for _, scope := range []string{"other", "libextra"} {
		t.Run("disjoint from "+scope, func(t *testing.T) {
			req3 := scanRequest(astCorpus(scopedCheckNode("chk-1", scopeMeta("", scope))), "knowledge", root)
			req3.PathPrefix = "lib"
			if err := runScanErr(req3); err == nil {
				t.Fatal("a check scope disjoint from the run's own scope leaves nothing to run, which is refused rather than widened")
			}
		})
	}
}

// TestCheckScope_UsesTheSharedDecoderAndTheSharedMatcher is requirement 3's
// one-implementation leg, and it is asserted BEHAVIORALLY rather than by a claim
// that two files import the same package.
//
// THE ORACLE IS THE SHARED SYMBOL ITSELF. Each row's expectation is not a
// hand-written list of files; it is computed in the test by asking
// parser.MatchesPathPrefixes — the predicate the corpus walk, the discovery
// report and the practice-side style index all narrow by — which file of the
// tree is under the scope. A check side that grew its own notion of "under this
// path" disagrees with the oracle on the row where the two differ, which is
// exactly the sibling-prefix row a strings.HasPrefix implementation gets wrong.
//
// THE DECODER LEG IS THE SAME SHAPE: the metadata is decoded in the test by
// kgtypes.StyleScopeFromMetadata, the function the practice-side index reads a
// style rule's scope with, and the scan is asserted to narrow by what THAT
// decode returned. A second decoder on the check side would have to agree with
// this one on every row, which is the drift the shared symbol removes.
func TestCheckScope_UsesTheSharedDecoderAndTheSharedMatcher(t *testing.T) {
	tree := []string{"lib/a.go", "lib/nested/b.go", "libextra/c.go", "other/d.go"}
	root := scopeRepo(t)

	for _, scope := range []string{"lib", "lib/nested", "libextra", "other", "lib/a.go"} {
		t.Run(scope, func(t *testing.T) {
			md := scopeMeta("", scope)

			// THE DECODER: the practice side's own, reading the same metadata.
			sc, err := kgtypes.StyleScopeFromMetadata(md)
			if err != nil {
				t.Fatalf("the shared decoder must read this scope: %v", err)
			}
			if !sc.PathsSet || len(sc.Paths) != 1 || sc.Paths[0] != scope {
				t.Fatalf("the shared decoder read %+v from %v", sc, md)
			}

			// THE MATCHER: the walk's own predicate, over what it decoded.
			want := []string{}
			for _, p := range tree {
				if parser.MatchesPathPrefixes(p, sc.Paths) {
					want = append(want, p)
				}
			}
			if len(want) == 0 {
				t.Fatalf("control: every row must have a known positive, %q selected nothing", scope)
			}

			got := siteFiles(runScan(t, scanRequest(astCorpus(scopedCheckNode("chk-1", md)), "knowledge", root)))
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("the scan must flag exactly the files the SHARED predicate admits under %q: want %v, got %v",
					scope, want, got)
			}
		})
	}

	// THE NEGATIVE LEG, on the same instruments: a value the shared decoder
	// REFUSES is a value the scan refuses, rather than one it reads as a wider
	// scope. Without this row the two sides could agree on every legal value and
	// disagree on every illegal one, which is where a silent widening lives.
	bad := rawScopeMeta(kgtypes.MetaKeyStyleScopePaths, "lib")
	if _, err := kgtypes.StyleScopeFromMetadata(bad); err == nil {
		t.Fatal("control: the shared decoder must refuse a non-array scope")
	}
	findings := runScan(t, scanRequest(astCorpus(
		scopedCheckNode("chk-bad", bad), scopedCheckNode("chk-open", nil)), "knowledge", root))
	if len(leadTitled(findings, RefusalPrefixUnvalidated)) != 1 {
		t.Error("a value the shared decoder refuses must refuse the check, never widen its walk")
	}
}

// TestCheckScope_NarrowsAGraphCheckToo is the SIBLING-ARM leg. A scoped check
// that kept every candidate node on the graph arm while its ast siblings walked
// a subtree would report sites in files its author declared it does not govern:
// the requirement is about the check, not about one executor.
func TestCheckScope_NarrowsAGraphCheckToo(t *testing.T) {
	corpusFor := scopedGraphCorpus

	// CONTROL: unscoped, both violating nodes are flagged.
	whole := matchFindings(runScan(t, scanRequest(corpusFor(nil), "repo", t.TempDir())))
	if len(whole) != 2 {
		t.Fatalf("control: an unscoped graph check flags the violator in each package, got %d", len(whole))
	}

	scoped := runScan(t, scanRequest(corpusFor(scopeMeta("", "lib")), "repo", t.TempDir()))
	sites := matchFindings(scoped)
	if len(sites) != 1 || sites[0].Evidence[0] != "lib/a.go:caller" {
		t.Fatalf("a graph check scoped to %q flags only the violator under it — and never the sibling prefix libextra — got %+v", "lib", sites)
	}
	if len(leadTitled(scoped, DisclosurePrefixScopeApplied)) != 1 {
		t.Error("the graph arm states the scope it applied, exactly as the ast arm does")
	}

	// A SCOPE THAT LEAVES NO CANDIDATE IS DISCLOSED, NOT ERRORED. The empty-graph
	// control exists to catch an uncollected repo; a check whose own paths hold no
	// node of this graph is a different fact and must not be reported as one.
	empty := runScan(t, scanRequest(corpusFor(scopeMeta("", "nowhere")), "repo", t.TempDir()))
	if got := len(matchFindings(empty)); got != 0 {
		t.Fatalf("a graph check scoped outside every candidate flags nothing, got %d", got)
	}
	out := leadTitled(empty, DisclosurePrefixOutOfScope)
	if len(out) != 1 || !strings.Contains(out[0].Summary, "function_declaration") {
		t.Fatalf("it must be disclosed by name, saying no candidate node is inside its paths, got %+v", out)
	}
}

// TestCheckScope_DisjointExclusionIsREADRatherThanCounted closes the branch the
// other rows only step through: scopeExclusionReason's DISJOINT-PATH leg.
//
// EVERY EXISTING ROW THAT REACHES IT SKIPS EVERY CHECK, so the run is refused
// and no finding is ever rendered — the branch runs and its words go nowhere. A
// disclosure nobody reads is a disclosure that can say anything, which is why
// this corpus keeps an UNSCOPED check beside the disjoint one: the run executes,
// the disclosure is emitted, and the sentence is asserted.
//
// THE TWO LEGS ARE READ APART, not just present. A single "it was excluded"
// assertion is satisfied by either branch, so the repo row asserts the repo
// sentence and the path row asserts the path sentence, and each denies the
// other's — which is what makes the branch observed rather than merely covered.
func TestCheckScope_DisjointExclusionIsREADRatherThanCounted(t *testing.T) {
	root := scopeRepo(t)

	t.Run("the disjoint-path leg", func(t *testing.T) {
		req := scanRequest(astCorpus(
			scopedCheckNode("chk-disjoint", scopeMeta("", "other")),
			scopedCheckNode("chk-open", nil)), "knowledge", root)
		req.PathPrefix = "lib"

		findings := runScan(t, req)
		// CONTROL: the run executed, or the disclosure below would not exist.
		if got := len(matchFindings(findings)); got != 2 {
			t.Fatalf("control: the unscoped check must still flag both files under %q, got %d", "lib", got)
		}

		out := leadTitled(findings, DisclosurePrefixOutOfScope)
		if len(out) != 1 {
			t.Fatalf("the disjoint check must be disclosed by name, got %d", len(out))
		}
		if !strings.Contains(out[0].Summary, "none of its paths falls inside this run's own scope") {
			t.Errorf("the disclosure must say WHICH leg excluded it — the path leg — got %q", out[0].Summary)
		}
		if strings.Contains(out[0].Summary, "this run scans repo") {
			t.Errorf("and must not give the repo leg's reason for a path exclusion, got %q", out[0].Summary)
		}
	})

	t.Run("the repo leg", func(t *testing.T) {
		findings := runScan(t, scanRequest(astCorpus(
			scopedCheckNode("chk-elsewhere", scopeMeta("some-other-repo")),
			scopedCheckNode("chk-open", nil)), "knowledge", root))

		out := leadTitled(findings, DisclosurePrefixOutOfScope)
		if len(out) != 1 {
			t.Fatalf("the repo-scoped check must be disclosed by name, got %d", len(out))
		}
		if !strings.Contains(out[0].Summary, `this run scans repo "knowledge"`) {
			t.Errorf("the disclosure must name the repo the run IS scanning, got %q", out[0].Summary)
		}
		if strings.Contains(out[0].Summary, "none of its paths") {
			t.Errorf("and must not give the path leg's reason for a repo exclusion, got %q", out[0].Summary)
		}
	})
}

// TestCheckScope_RefusedAndOutOfScopeTogetherRefuseTheRun is the mixed
// all-skipped run: one check refused for a malformed scope, one out of scope,
// and nothing left to execute.
//
// IT IS ITS OWN ROW BECAUSE THE TWO COUNTERS ARE SEPARATE and the error names
// both. A closer that reported only the out-of-scope count would tell an
// operator their run was mis-aimed when half the reason is a corpus they must
// fix, and one that reported only refusals would say the opposite.
func TestCheckScope_RefusedAndOutOfScopeTogetherRefuseTheRun(t *testing.T) {
	root := scopeRepo(t)
	gc := astCorpus(
		scopedCheckNode("chk-bad", rawScopeMeta(kgtypes.MetaKeyStyleScopePaths, "lib")),
		scopedCheckNode("chk-elsewhere", scopeMeta("some-other-repo")),
	)

	// CONTROL: with the two defects removed, this same corpus shape runs and
	// flags — so the refusal below is the two checks and not an empty corpus.
	if got := len(matchFindings(runScan(t, scanRequest(astCorpus(
		scopedCheckNode("chk-bad", nil), scopedCheckNode("chk-elsewhere", nil)), "knowledge", root)))); got != 8 {
		t.Fatalf("control: the same two checks unscoped flag four sites each, got %d", got)
	}

	err := runScanErr(scanRequest(gc, "knowledge", root))
	if err == nil {
		t.Fatal("one refused check and one out-of-scope check leave nothing executed, which is not a clean run")
	}
	for _, want := range []string{"1 out of scope", "1 refused", "not one of the 2 check(s)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must carry %q so an operator can tell the two causes apart; got %q", want, err)
		}
	}
}

// scopedGraphCorpus seeds a one-check graph corpus carrying the given scope
// metadata, over a code graph with one violator under lib and one under the
// SIBLING prefix libextra.
//
// THE SIBLING IS THE DISCRIMINATOR HERE TOO. A graph-arm narrowing written with
// a bare string prefix keeps the libextra violator under a scope of lib, and
// every other assertion in these rows passes under that implementation.
func scopedGraphCorpus(scope map[string]string, nodeFiles ...string) *fakeCaller {
	md := graphCheckMeta(corpus.CheckGraphAssertion, noOutboundCalls, "warning", "fx-graph-bad", "fx-graph-good")
	maps.Copy(md, scope)
	if len(nodeFiles) == 0 {
		nodeFiles = []string{"lib/a.go", "libextra/c.go"}
	}
	var nodes []*knowledgev1.Node
	var edges []*knowledgev1.Edge
	for _, f := range nodeFiles {
		nodes = append(nodes, codeNode(f+":caller"), codeNode(f+":helper"))
		edges = append(edges, &knowledgev1.Edge{
			Type: string(kgtypes.EdgeCalls), FromId: f + ":caller", ToId: f + ":helper"})
	}
	return newFakeCaller().
		seed("checks", []*knowledgev1.Node{
			checkNode("chk-graph", "no function calls another", "keep the call graph flat", md),
			exampleNode("fx-graph-bad", callsBad),
			exampleNode("fx-graph-good", callsGood),
		}, nil).
		seed("code/repo", nodes, edges)
}

// TestCheckScope_GraphArmComposesWithBothCallerChannels fills the two cells the
// graph row left empty: a check scope meeting a caller FILE LIST, and the same
// meeting a caller PATH_PREFIX.
//
// THE GRAPH ARM APPLIES THE TWO NARROWINGS THROUGH DIFFERENT CODE, which is why
// the cells are not implied by the ast arm's. The caller's channel filters
// candidate nodes in filterByPathPrefix and the check's own paths in
// filterByCheckScope, so a composition that worked on the walk could still drop
// or keep the wrong nodes here.
//
// THE SEEDED FILES EXIST SO THE FILE LIST RESOLVES, and are never walked: a
// graph check reads the code graph and not the working tree, but the file-list
// scope is resolved against the tree before any check runs.
func TestCheckScope_GraphArmComposesWithBothCallerChannels(t *testing.T) {
	root := seedRepo(t, map[string]string{
		"lib/a.go":        deferCloseBad,
		"lib/nested/b.go": deferCloseBad,
		"libextra/c.go":   deferCloseBad,
	})

	t.Run("with a caller file list", func(t *testing.T) {
		// CONTROL: the file list ALONE keeps only the node in the named file.
		open := siteEvidence(runScan(t, withFiles(scanRequest(scopedGraphCorpus(nil), "repo", root), "lib/a.go")))
		if len(open) != 1 || open[0] != "lib/a.go:caller" {
			t.Fatalf("control: an unscoped graph check under a one-file list flags only that file's violator, got %v", open)
		}

		// The check's scope AGREES with the list: the same one node survives.
		agree := runScan(t, withFiles(scanRequest(scopedGraphCorpus(scopeMeta("", "lib")), "repo", root), "lib/a.go"))
		if got := siteEvidence(agree); len(got) != 1 || got[0] != "lib/a.go:caller" {
			t.Fatalf("a check scoped to lib over a list naming lib/a.go keeps that violator, got %v", got)
		}
		if len(leadTitled(agree, DisclosurePrefixScopeApplied)) != 1 {
			t.Error("and it states the scope it applied")
		}

		// The check's scope and the named file are DISJOINT, so the check never
		// reaches the graph at all and the run has nothing to execute.
		disagree := withFiles(scanRequest(scopedGraphCorpus(scopeMeta("", "libextra")), "repo", root), "lib/a.go")
		if err := runScanErr(disagree); err == nil {
			t.Fatal("a check scoped away from every named file leaves nothing to execute, which is refused rather than reported clean")
		}
	})

	t.Run("the blame for an emptied candidate set is measured, not inferred", func(t *testing.T) {
		// BOTH NARROWINGS ARE IN FORCE IN BOTH ROWS, which is the whole point:
		// an arm that inferred the cause from "the check carried a scope" gives
		// the same answer to both, and is wrong on one of them.

		// ROW 1 — THE CHECK'S PATHS EMPTY IT. The caller names both files and the
		// graph holds nodes only under libextra, so the caller's filter keeps
		// candidates and the check's scope of lib takes the last one.
		byCheck := runScan(t, withFiles(
			scanRequest(scopedGraphCorpus(scopeMeta("", "lib"), "libextra/c.go"), "repo", root),
			"lib/a.go", "libextra/c.go"))
		out := leadTitled(byCheck, DisclosurePrefixOutOfScope)
		if len(out) != 1 {
			t.Fatalf("the check's own scope emptied the candidates, so the CHECK is named; got %d out-of-scope lines", len(out))
		}
		if len(leadTitled(byCheck, DisclosurePrefixGraphNotRun)) != 0 {
			t.Error("and the caller's channel is not blamed for it")
		}

		// ROW 2 — THE CALLER'S LIST EMPTIES IT. The check's scope of lib admits
		// the named file, so the intersection is non-empty and the check runs;
		// the named file simply holds no node of this graph.
		byCaller := runScan(t, withFiles(
			scanRequest(scopedGraphCorpus(scopeMeta("", "lib")), "repo", root), "lib/nested/b.go"))
		if got := len(leadTitled(byCaller, DisclosurePrefixGraphNotRun)); got != 1 {
			t.Fatalf("the caller's file list emptied the candidates, so the FILE SCOPE is named; got %d graph-not-run lines", got)
		}
		if len(leadTitled(byCaller, DisclosurePrefixOutOfScope)) != 0 {
			t.Error("and the check's scope is not blamed for the caller's narrowing")
		}
	})

	t.Run("with a caller path prefix", func(t *testing.T) {
		req := scanRequest(scopedGraphCorpus(scopeMeta("", "lib")), "repo", root)
		req.PathPrefix = "lib"
		if got := siteEvidence(runScan(t, req)); len(got) != 1 || got[0] != "lib/a.go:caller" {
			t.Fatalf("a check scoped to lib under path_prefix lib flags that violator and never the sibling, got %v", got)
		}

		// THE SIBLING CELL: the caller narrows to lib and the check to libextra,
		// which have nothing in common, so the check does not run at all.
		disjoint := scanRequest(scopedGraphCorpus(scopeMeta("", "libextra")), "repo", root)
		disjoint.PathPrefix = "lib"
		if err := runScanErr(disjoint); err == nil {
			t.Fatal("a graph check scoped disjointly from the run's own prefix leaves nothing to execute, which is refused")
		}
	})
}

// siteEvidence lists the dedup key of every flagged site, which for a graph site
// is the violating node id.
func siteEvidence(findings []foundation.Finding) []string {
	out := []string{}
	for _, f := range matchFindings(findings) {
		out = append(out, firstEvidence(f))
	}
	return out
}
