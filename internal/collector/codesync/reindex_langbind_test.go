// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestAugmentWithPreciseCallGraph_BindsByDeclaringFileAndKeepsNonGoCalls runs the
// full Populate + RTA-merge path over a small polyglot fixture and pins the
// post-merge CALLS set exactly.
//
// It exists to prove three separate properties of the merge at once:
//
//  1. every merged CALLS edge binds to the declaration it was actually derived
//     from, so no edge ever crosses a language boundary;
//  2. call edges the Go call graph can never produce (bash, TypeScript) survive
//     the merge;
//  3. a method is analyzed as a CALLER, not only as a callee.
//
// EVERY GO EDGE IN THE EXPECTED SET IS THE PRECISE CALL GRAPH'S, BY
// CONSTRUCTION: the merge removes every Go-caller tree-sitter CALLS edge before
// it re-adds anything, so the three go edges present afterwards can only have
// come from the precise call graph. That is why assertion (1) is not satisfiable
// by tree-sitter output that happens to agree with it.
//
// The fixture's zz_gen.go / Dispatch / genOnly trio is what makes the failure
// deterministic rather than racy — see writeLangBindFixture.
//
// The test prints its post-merge edge set on both pass and fail as
// `POSTCALLS <from> -> <to> w=<weight>` lines with no leading whitespace, plus a
// `POSTPAIRS <fromLang>-><toLang>=<n> ...` summary. testdata/ful1335_redfirst.txt
// is a frozen capture of that output from before the fix. Failure messages
// deliberately use `want:` / `got:` and never the POSTCALLS form, because the
// artifact's negative legs grep raw `go test -v` output: a failure message
// echoing an edge in POSTCALLS form would satisfy a leg that exists to prove
// that edge is absent.
func TestAugmentWithPreciseCallGraph_BindsByDeclaringFileAndKeepsNonGoCalls(t *testing.T) {
	root := writeLangBindFixture(t)

	pop, err := parser.Populate(t.Context(), "fx", root)
	require.NoError(t, err)

	// (0) GENERATED-FILE PRECONDITION. zz_gen.go is excluded from discovery by
	// the generated-Go rule (parser/indexer_discover.go isIndexable: names ending
	// _gen.go are skipped), so no node exists for genOnly on the Go side, while
	// go/packages compiles it normally. That asymmetry is what gives the
	// mis-bind a single deterministic writer instead of a last-write-wins race.
	// Asserted directly so a broken fixture reports itself instead of surfacing
	// as a puzzling absence inside the merge assertions.
	genFileNodes := 0
	for _, n := range pop.Nodes {
		if n.FilePath == "svc/api/zz_gen.go" {
			genFileNodes++
		}
	}
	fmt.Printf("GENFILE_NODES %d\n", genFileNodes)
	assert.Zero(t, genFileNodes,
		"fixture precondition broken: svc/api/zz_gen.go produced nodes, but the "+
			"generated-Go discovery exclusion should have kept it out of the parse")

	out := augmentWithPreciseCallGraph(t.Context(), pop, root)

	// Node.Language is set for every chunk node (parser/populate.go appendChunkNode).
	language := make(map[string]string, len(out.Nodes))
	for _, n := range out.Nodes {
		language[n.Id] = n.Language
	}

	var calls []*knowledgev1.Edge
	for _, e := range out.Edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeCalls {
			calls = append(calls, e)
		}
	}

	got := make([]string, 0, len(calls))
	pairCounts := make(map[string]int)
	callerLangCounts := make(map[string]int)
	for _, e := range calls {
		got = append(got, fmt.Sprintf("%s -> %s w=%g", e.FromId, e.ToId, e.Weight))
		pairCounts[language[e.FromId]+"->"+language[e.ToId]]++
		callerLangCounts[language[e.FromId]]++
	}
	slices.Sort(got)

	pairNames := make([]string, 0, len(pairCounts))
	for name, n := range pairCounts {
		if n > 0 {
			pairNames = append(pairNames, name)
		}
	}
	slices.Sort(pairNames)
	pairParts := make([]string, 0, len(pairNames))
	for _, name := range pairNames {
		pairParts = append(pairParts, fmt.Sprintf("%s=%d", name, pairCounts[name]))
	}
	fmt.Printf("POSTPAIRS %s\n", strings.Join(pairParts, " "))
	for _, line := range got {
		fmt.Printf("POSTCALLS %s\n", line)
	}

	// (1) EXACT SET — no more and no fewer.
	//
	// Dispatch contributes NO edge: genOnly's declaring file is svc/api/zz_gen.go,
	// so its decl key is svc/api/zz_gen.go:genOnly and no node carries that ID, so
	// the lookup misses. That absence IS the test of "an unindexed declaration
	// contributes no binding" — it is correct, not a gap to repair.
	want := []string{
		"scripts/run.sh:main -> scripts/run.sh:helper w=1",
		"svc/api/handler.go:Reach -> svc/api/handler.go:Server.Handle w=1",
		"svc/api/handler.go:Serve -> svc/api/handler.go:goOnlyTarget w=2",
		"svc/api/handler.go:Server.Handle -> svc/api/handler.go:goOnlyTarget w=1",
		"web/api/client.ts:Render -> web/api/client.ts:Helper w=1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("post-merge CALLS set mismatch\nwant: %s\ngot:  %s",
			strings.Join(want, " | "), strings.Join(got, " | "))
	}

	// (2) CROSS-LANGUAGE INVARIANT, stated separately so the failure names the
	// violating edge rather than drowning it in a set diff.
	for _, e := range calls {
		if language[e.FromId] != language[e.ToId] {
			t.Errorf("cross-language CALLS edge: from %s (%s) to %s (%s)",
				e.FromId, language[e.FromId], e.ToId, language[e.ToId])
		}
	}

	// (3) PER-LANGUAGE SURVIVAL CENSUS by caller language.
	assert.Equal(t, map[string]int{"go": 3, "typescript": 1, "bash": 1}, callerLangCounts,
		"per-caller-language CALLS census changed")

	// (4) WEIGHT RE-ATTACHMENT: Serve calls goOnlyTarget twice, so the pair both
	// layers saw must still carry the tree-sitter weight 2, not the RTA default 1.
	// This is the proof captureCallEdgeWeights still functions after the merge
	// rework.
	var serveWeight float64
	for _, e := range calls {
		if e.FromId == "svc/api/handler.go:Serve" && e.ToId == "svc/api/handler.go:goOnlyTarget" {
			serveWeight = e.Weight
		}
	}
	assert.InDelta(t, 2.0, serveWeight, 0,
		"want: Serve->goOnlyTarget weight 2 (tree-sitter saw two call sites); got: %v", serveWeight)

	// (5) METHOD CALLER SURVIVES THE MERGE. Asserted separately from the exact
	// set because it catches a dangerous interaction between the two production
	// changes: the drop guard removes every Go-caller tree-sitter CALLS edge, so
	// a function set regressed to package members would drop this edge and never
	// replace it — silently, with the cross-language invariant still green.
	var methodIsCaller bool
	for _, e := range calls {
		if e.FromId == "svc/api/handler.go:Server.Handle" {
			methodIsCaller = true
		}
	}
	assert.True(t, methodIsCaller,
		"no CALLS edge has svc/api/handler.go:Server.Handle as its FROM endpoint — "+
			"methods are not being analyzed as callers")
}

// TestBuildNodeIndex_DuplicateDeclKeyRefusesBothAndCounts drives the
// duplicate-decl-key alarm non-zero on purpose.
//
// The alarm guards an invariant rather than serving a case — under the decl-key
// derivation the key IS the node ID, and parser.DeduplicateChunks exists to make
// duplicate node IDs impossible — so without a test that actually fires it, a
// counter that was never wired and a corpus that is genuinely clean would be
// indistinguishable.
//
// The third node is a CONTROL: it proves the withheld key is the guard firing
// rather than the index coming back empty.
func TestBuildNodeIndex_DuplicateDeclKeyRefusesBothAndCounts(t *testing.T) {
	const dupKey = "svc/api/handler.go:Serve"
	const controlKey = "svc/api/handler.go:Reach"

	idx := buildNodeIndex([]*knowledgev1.Node{
		{Id: dupKey, FilePath: "svc/api/handler.go", SymbolName: "Serve", Language: "go"},
		{Id: dupKey, FilePath: "svc/api/handler.go", SymbolName: "Serve", Language: "go"},
		{Id: controlKey, FilePath: "svc/api/handler.go", SymbolName: "Reach", Language: "go"},
	})

	assert.Equal(t, 1, idx.collisions, "want: 1 counted duplicate decl key; got: %d", idx.collisions)

	_, bound := idx.declToID[dupKey]
	assert.False(t, bound,
		"want: the duplicated decl key withdrawn so NEITHER declaration binds; got: still bound")

	_, control := idx.declToID[controlKey]
	assert.True(t, control,
		"want: the control decl key present; got: absent — the index is empty, so the "+
			"absence above proves nothing")
}

// TestDropGoCallerCallEdges_ReplacesOnlyGoCallerEdges pins the drop rule to the
// FROM endpoint.
//
// The typescript->go case is the falsifier: a both-endpoints reading of
// "Go-endpoint edge" would drop it, and nothing would ever re-add it, because
// the precise Go call graph only ever emits edges whose caller is a Go
// declaration. That is exactly the tsx/js/bash/python loss this change exists to
// end.
func TestDropGoCallerCallEdges_ReplacesOnlyGoCallerEdges(t *testing.T) {
	langByID := map[string]string{
		"a.go:GoA":     "go",
		"b.go:GoB":     "go",
		"c.ts:TsA":     "typescript",
		"d.sh:ShA":     "bash",
		"d.sh:ShB":     "bash",
		"e.py:Unknown": "python",
	}
	calls := string(kgtypes.EdgeCalls)
	other := string(kgtypes.EdgeLanguage)

	edges := []*knowledgev1.Edge{
		{FromId: "a.go:GoA", ToId: "b.go:GoB", Type: calls},  // go->go: dropped
		{FromId: "a.go:GoA", ToId: "c.ts:TsA", Type: calls},  // go->typescript: dropped
		{FromId: "c.ts:TsA", ToId: "a.go:GoA", Type: calls},  // typescript->go: SURVIVES
		{FromId: "d.sh:ShA", ToId: "d.sh:ShB", Type: calls},  // bash->bash: survives
		{FromId: "unlisted", ToId: "a.go:GoA", Type: calls},  // unknown caller reads non-Go: survives
		{FromId: "a.go:GoA", ToId: "lang:x:go", Type: other}, // non-CALLS from a Go node: untouched
	}

	// Both Go callers live in files the analysis covered, so the coverage gate is
	// wide open here and the drop rule under test is the LANGUAGE one, exactly as
	// it was before the gate existed. TestDropGoCallerCallEdges_UncoveredSurvives
	// is where the gate itself is exercised.
	idx := nodeIndex{
		langByID: langByID,
		fileByID: map[string]string{
			"a.go:GoA":     "a.go",
			"b.go:GoB":     "b.go",
			"c.ts:TsA":     "c.ts",
			"d.sh:ShA":     "d.sh",
			"d.sh:ShB":     "d.sh",
			"e.py:Unknown": "e.py",
		},
	}
	covered := map[string]bool{"a.go": true, "b.go": true}

	filtered, removed, keptNonGo, keptUncovered := dropGoCallerCallEdges(edges, idx, covered)

	assert.Equal(t, 2, removed, "want: 2 Go-caller CALLS edges removed; got: %d", removed)
	assert.Equal(t, 3, keptNonGo, "want: 3 CALLS edges kept; got: %d", keptNonGo)
	assert.Equal(t, 0, keptUncovered,
		"want: 0 CALLS edges kept for lack of coverage, since both Go callers are in covered files; got: %d", keptUncovered)

	got := make([]string, 0, len(filtered))
	for _, e := range filtered {
		got = append(got, fmt.Sprintf("%s->%s/%s", e.FromId, e.ToId, e.Type))
	}
	want := []string{
		"c.ts:TsA->a.go:GoA/" + calls,
		"d.sh:ShA->d.sh:ShB/" + calls,
		"unlisted->a.go:GoA/" + calls,
		"a.go:GoA->lang:x:go/" + other,
	}
	assert.Equal(t, want, got, "retained edge set changed")
}

// TestDropGoCallerCallEdges_UncoveredSurvives pins the COVERAGE half of the drop
// rule: a Go caller's tree-sitter CALLS edge is deleted only when the precise
// analysis actually loaded the file that declares it.
//
// The covered case and the uncovered case are BOTH present in one run on
// purpose. A covered-only fixture makes the survival assertion unfalsifiable and
// an uncovered-only fixture makes the drop assertion unfalsifiable; neither half
// is evidence without the other. The two Go callers therefore carry different
// concrete file paths and different node IDs, so no single field collapses the
// distinction.
//
// NAMED CATCHER for the harness detail this depends on: if fileByID is declared
// but never populated, every lookup returns "", covered[""] is false, and
// NOTHING is ever dropped — the removed == 1 assertion below is what goes red,
// alongside the landed drop guard. If the gate is inverted instead, the survival
// assertion goes red alone. Neither failure mode is silent.
func TestDropGoCallerCallEdges_UncoveredSurvives(t *testing.T) {
	calls := string(kgtypes.EdgeCalls)

	idx := nodeIndex{
		langByID: map[string]string{
			"covered.go:CovA":    "go",
			"covered.go:CovB":    "go",
			"uncovered.go:UncA":  "go",
			"uncovered.go:UncB":  "go",
			"scripts/run.sh:ShA": "bash",
			"scripts/run.sh:ShB": "bash",
		},
		fileByID: map[string]string{
			"covered.go:CovA":    "covered.go",
			"covered.go:CovB":    "covered.go",
			"uncovered.go:UncA":  "uncovered.go",
			"uncovered.go:UncB":  "uncovered.go",
			"scripts/run.sh:ShA": "scripts/run.sh",
			"scripts/run.sh:ShB": "scripts/run.sh",
		},
	}
	// uncovered.go is deliberately absent: it is the build-tag-excluded /
	// testdata / failed-to-type-check class, which no analysis will ever cover.
	covered := map[string]bool{"covered.go": true}

	edges := []*knowledgev1.Edge{
		{FromId: "covered.go:CovA", ToId: "covered.go:CovB", Type: calls},
		{FromId: "uncovered.go:UncA", ToId: "uncovered.go:UncB", Type: calls},
		{FromId: "scripts/run.sh:ShA", ToId: "scripts/run.sh:ShB", Type: calls},
	}

	filtered, removed, keptNonGo, keptUncovered := dropGoCallerCallEdges(edges, idx, covered)

	assert.Equal(t, 1, removed,
		"want: the covered Go caller's edge dropped, because the precise graph will re-add it; got: %d removed", removed)
	assert.Equal(t, 1, keptUncovered,
		"want: the UNCOVERED Go caller's edge kept, because nothing can replace it; got: %d kept", keptUncovered)
	assert.Equal(t, 1, keptNonGo,
		"want: the bash caller's edge kept as before; got: %d — the control is broken, so neither assertion above proves anything", keptNonGo)

	got := make([]string, 0, len(filtered))
	for _, e := range filtered {
		got = append(got, e.FromId+"->"+e.ToId)
	}
	assert.Equal(t, []string{
		"uncovered.go:UncA->uncovered.go:UncB",
		"scripts/run.sh:ShA->scripts/run.sh:ShB",
	}, got, "retained edge set changed")
}

// writeLangBindFixture writes the polyglot binding fixture and returns its root.
//
// The shared basename "api" across svc/api and web/api is the whole point of the
// layout — it is the directory-basename collision the merge used to key on. Do
// not "tidy" the directory names.
//
// WHY THE zz_gen.go / Dispatch / genOnly TRIO. Contesting "Serve" between the two
// "api" directories reproduces the cross-language mis-bind only when the
// TypeScript node happens to win a last-write-wins race whose winner follows
// nondeterministic chunking completion order. zz_gen.go instead is a generated Go
// file, so parser discovery excludes it and NO node exists for genOnly on the Go
// side, while go/packages compiles it normally so the analysis still sees
// Dispatch -> genOnly. The contested key therefore has exactly one writer, the
// TypeScript node, and the mis-bind is deterministic.
//
// The Serve contest is kept anyway because it is the ticket's literal "api"
// shape; it simply is not what the deterministic failure depends on.
//
// Each remaining piece earns its place: goOnlyTarget exists only in Go, so a
// mis-bound Serve edge is a mixed-language edge; Render/Helper is a genuine
// TypeScript call edge that must survive the merge; run.sh is the non-Go,
// non-TypeScript survivor; Reach->Handle proves a receiver-qualified decl key
// resolves as a CALLEE; Server.Handle->goOnlyTarget proves a method is analyzed
// as a CALLER.
func writeLangBindFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	writePopulateFixtureFile(t, filepath.Join(root, "go.mod"), "module example.com/fx\n\ngo 1.24\n")

	writePopulateFixtureFile(t, filepath.Join(root, "svc", "api", "handler.go"), `package api

type Server struct{}

func Serve() int {
	return goOnlyTarget() + goOnlyTarget()
}

func goOnlyTarget() int {
	return 1
}

func (s Server) Handle() int {
	return goOnlyTarget()
}

func Reach() int {
	var s Server
	return s.Handle()
}

func Dispatch() int {
	return genOnly()
}
`)

	writePopulateFixtureFile(t, filepath.Join(root, "svc", "api", "zz_gen.go"), `package api

func genOnly() int {
	return 7
}
`)

	writePopulateFixtureFile(t, filepath.Join(root, "web", "api", "client.ts"), `export function Serve(): number {
  return 1;
}

export function genOnly(): number {
  return 7;
}

export function Render(): number {
  return Helper();
}

export function Helper(): number {
  return 2;
}
`)

	writePopulateFixtureFile(t, filepath.Join(root, "scripts", "run.sh"), `#!/usr/bin/env bash

helper() {
  echo "helper"
}

main() {
  helper
}
`)

	return root
}
