// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestBuildGoCallGraph runs BuildGoCallGraph on the knowledge repo itself and
// verifies that at least 3 known caller→callee pairs are present.
//
// This test is intentionally slow (SSA + RTA on the full repo) — run with
// -timeout 300s to allow sufficient time.
func TestBuildGoCallGraph(t *testing.T) {
	// Locate the repo root relative to this test file.
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// testFile is .../collector/codegraph/callgraph_go_test.go — repo root
	// is two levels up. Post-BCN9 the parser/cicd/web/cloud/etc. subtrees
	// live under cmd/knowledge/internal/collector/, so the call-graph scan
	// must be repo-wide to surface their edges (parser.Populate etc.).
	rootDir := filepath.Dir(filepath.Dir(filepath.Dir(testFile)))

	t.Logf("running BuildGoCallGraph on %s", rootDir)

	graph, err := BuildGoCallGraph(t.Context(), rootDir)
	if err != nil {
		t.Fatalf("BuildGoCallGraph error: %v", err)
	}
	if len(graph) == 0 {
		t.Fatal("BuildGoCallGraph returned empty graph")
	}
	t.Logf("total callers: %d", len(graph))

	// Helper: check whether caller→callee edge exists.
	hasEdge := func(caller, callee string) bool {
		callees, ok := graph[caller]
		if !ok {
			return false
		}
		return slices.Contains(callees, callee)
	}

	// Helper: check whether caller calls any function whose name contains suffix.
	callerCallsSuffix := func(caller, suffix string) bool {
		for _, callee := range graph[caller] {
			if strings.HasSuffix(callee, suffix) {
				return true
			}
		}
		return false
	}

	// Enumerate known pairs to verify. We require at least 3 to pass.
	type pair struct {
		caller string
		callee string
		// If callee is empty, use calleeSuffix check.
		calleeSuffix string
	}
	candidates := []pair{
		// searcher.Search should call searcher.searchHybridAuto.
		{caller: "search.searcher.Search", calleeSuffix: ".searchHybridAuto"},
		// db.Update should call graph operations.
		{caller: "store.db.Update", calleeSuffix: ".GetNode"},
		// db.Create should call graph.AddNode.
		{caller: "store.db.Create", calleeSuffix: ".AddNode"},
		// resolveEdges calls resolveEdgeID (moved to parser/).
		{caller: "parser.resolveEdges", callee: "parser.resolveEdgeID"},
		// Populate calls DiscoverFiles (moved to parser/).
		{caller: "parser.Populate", callee: "parser.DiscoverFiles"},
		// chunkResultsToPopulate calls resolveEdges.
		{caller: "parser.chunkResultsToPopulate", callee: "parser.resolveEdges"},
	}

	found := 0
	for _, p := range candidates {
		var matched bool
		if p.callee != "" {
			matched = hasEdge(p.caller, p.callee)
		} else {
			matched = callerCallsSuffix(p.caller, p.calleeSuffix)
		}
		if matched {
			t.Logf("FOUND edge: %s → %s%s", p.caller, p.callee, p.calleeSuffix)
			found++
		} else {
			t.Logf("MISSING edge: %s → %s%s", p.caller, p.callee, p.calleeSuffix)
		}
	}

	if found < 3 {
		// Log a sample of the graph to help diagnose.
		n := 0
		for caller, callees := range graph {
			if n >= 20 {
				break
			}
			t.Logf("  %s → %v", caller, callees[:min(3, len(callees))])
			n++
		}
		t.Fatalf("expected at least 3 known caller→callee pairs, found %d", found)
	}
	t.Logf("verified %d/%d known caller→callee pairs", found, len(candidates))
}
