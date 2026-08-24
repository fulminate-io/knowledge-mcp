// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_branch_test.go — tests for the coverage table's BRANCH
// GRAPH rows: the per-base overlay enumeration that discovers them, the composed
// key they are addressed by, and the declined segment probe that keeps their band
// honest. Split from the two sibling coverage test files for the 500-line cap,
// mirroring the production split. The shared fixtures (coverageFake,
// coverageSegReader, coverageDeps) live in manage_status_coverage_test.go and are
// used from here — same package, so nothing is duplicated.
package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// branchFixture is the two-base fixture the branch tests share: base "agent" whose
// overlay arrives in the CLOUD form (the full composed catalog key) and base
// "knowledge" whose overlay arrives in the OSS form (the bare overlay name), plus a
// third base with no overlays at all.
//
// The two FORMS in one pass are the point. A single base carrying both forms would
// catch a resolver that never normalizes and one that strips nothing, but it cannot
// catch the two properties this enumeration actually adds: that each key normalizes
// against the base THAT PRODUCED IT rather than against some other base in the same
// batch, and that the concurrent round lands its results by base index so row order
// survives. With one base there is no wrong base to normalize against and no order to
// preserve, and both assertions go vacuous.
func branchFixture() *coverageFake {
	return &coverageFake{
		baseNamesByType: map[string][]string{"code": {"agent", "knowledge", "plain"}},
		overlayKeysByBase: map[string][]string{
			"agent":     {"agent@launch-fixes"},
			"knowledge": {"fix-a"},
		},
		statsByKey: map[string]*knowledgev1.GraphStats{
			"code/agent":              {NonProxyNodeCount: 51967, SummarizedCount: 51967, BinaryVectorCount: 51967},
			"code/agent@launch-fixes": {NonProxyNodeCount: 59314, SummarizedCount: 59314, BinaryVectorCount: 59314},
			"code/knowledge":          {NonProxyNodeCount: 100, SummarizedCount: 100, BinaryVectorCount: 100},
			"code/knowledge@fix-a":    {NonProxyNodeCount: 42, SummarizedCount: 42, BinaryVectorCount: 42},
			"code/plain":              {NonProxyNodeCount: 7, SummarizedCount: 7, BinaryVectorCount: 7},
			"knowledge":               {NonProxyNodeCount: 10, SummarizedCount: 10, BinaryVectorCount: 10},
			"practice/go":             {NonProxyNodeCount: 20, SummarizedCount: 20, BinaryVectorCount: 20},
		},
	}
}

// targetsByLabel indexes an enumeration by row label so a test can assert about one
// row without depending on the position of every other row.
func targetsByLabel(targets []coverageTarget) map[string]coverageTarget {
	byLabel := make(map[string]coverageTarget, len(targets))
	for _, t := range targets {
		byLabel[t.label] = t
	}
	return byLabel
}

// TestCoverageTargets_BranchGraphs is the enumeration criterion: a code base graph's
// branch overlays become coverage rows of their own, addressed by the composed
// "base@overlay" key, whichever form the backend reported them in.
//
// THE EMPTY-BRANCH LEG IS THE ADDRESSING DECISION, not a detail. The two wire shapes
// that can name a branch resolve to DIFFERENT corpora: a selector carrying Repo=base
// plus Branch=overlay resolves the COMPOSITE of base and overlay, while the composed
// key with an empty Branch resolves the branch graph's OWN rows — which is what an
// inventory row must report, and what the adjacent delete control reclaims. A test
// asserting only Repo would pass against a selector that also set Branch and silently
// reported the composite under a per-graph label.
func TestCoverageTargets_BranchGraphs(t *testing.T) {
	deps := &coverageDeps{gc: branchFixture()}

	targets := coverageTargets(context.Background(), deps)
	byLabel := targetsByLabel(targets)

	for _, tc := range []struct{ label, key string }{
		// Reported by the cloud backend as the full composed key.
		{"code/agent@launch-fixes", "agent@launch-fixes"},
		// Reported by the OSS backend bare — the base prefix already stripped.
		{"code/knowledge@fix-a", "knowledge@fix-a"},
	} {
		got, ok := byLabel[tc.label]
		require.True(t, ok, "both backend key forms must reduce to the composed row label %q", tc.label)
		assert.Equal(t, tc.key, got.name, "the row's instance name is the composed branch graph key")
		assert.Equal(t, kgtypes.GraphCode, got.gt)
		assert.True(t, got.overlay, "a branch row is marked as coming from an overlay enumeration")
		assert.Equal(t, tc.key, got.target.GetRepo(),
			"a branch graph is addressed by composed Repo, so the count describes the graph the delete removes")
		assert.Empty(t, got.target.GetBranch(),
			"Branch must stay EMPTY — a Repo+Branch selector resolves the base+overlay COMPOSITE instead of the branch graph's own rows")
	}

	// Base order survives the concurrent round: results land by base index, never by
	// completion order. "agent" precedes "knowledge" in the base list, so its branch
	// row precedes the other's.
	var branchLabels []string
	for _, tgt := range targets {
		if tgt.overlay {
			branchLabels = append(branchLabels, tgt.label)
		}
	}
	assert.Equal(t, []string{"code/agent@launch-fixes", "code/knowledge@fix-a"}, branchLabels,
		"branch rows are emitted in base order, then enumeration order")

	// The base rows are still present and unmarked — a branch row is ADDITIVE, and
	// nothing about the enumeration replaces the base it belongs to.
	base, ok := byLabel["code/agent"]
	require.True(t, ok)
	assert.False(t, base.overlay)
	assert.Equal(t, "agent", base.target.GetRepo())
}

// TestCoverageTargets_NoPhantomRows pins the acceptance clause that a graph with no
// branches shows no phantom rows: a code base the backend reports no overlays for
// contributes exactly its own row.
func TestCoverageTargets_NoPhantomRows(t *testing.T) {
	deps := &coverageDeps{gc: branchFixture()}

	targets := coverageTargets(context.Background(), deps)

	var forPlain []string
	for _, tgt := range targets {
		if tgt.gt == kgtypes.GraphCode && (tgt.name == "plain" || strings.HasPrefix(tgt.name, "plain@")) {
			forPlain = append(forPlain, tgt.label)
		}
	}
	assert.Equal(t, []string{"code/plain"}, forPlain,
		"a base with no overlays contributes only its own row — no empty-named or self-composed phantom")

	// KNOWN POSITIVE in the same enumeration: the bases that DO have overlays got
	// their branch rows, so the assertion above cannot be satisfied by an
	// enumeration that emitted no branch rows at all.
	byLabel := targetsByLabel(targets)
	assert.Contains(t, byLabel, "code/agent@launch-fixes")
	assert.Contains(t, byLabel, "code/knowledge@fix-a")
}

// TestCoverageTargets_CodeOnly pins that the overlay enumeration is asked ONLY about
// code graphs. Overlays of the other families are knowledge session overlays —
// ephemeral working state rather than inventory — and the server's selector
// validation rejects a knowledge selector whose name is not a root alias, so such a
// request would do the work and then drop its own row on an error.
func TestCoverageTargets_CodeOnly(t *testing.T) {
	fake := branchFixture()
	deps := &coverageDeps{gc: fake}

	coverageTargets(context.Background(), deps)

	var overlayAsks []string
	for _, req := range fake.execReqs {
		if base := req.GetQuery().GetOverlayOf(); base != "" {
			graph := req.GetTarget().GetGraph()
			assert.Equal(t, string(kgtypes.GraphCode), graph,
				"overlay_of was sent for graph %q — only the code family has inventory overlays", graph)
			overlayAsks = append(overlayAsks, base)
		}
	}

	// THE KNOWN POSITIVE. Without it a walk that issued no overlay enumeration at
	// all — the pre-change behavior, and the exact defect — satisfies the loop above
	// vacuously.
	assert.ElementsMatch(t, []string{"agent", "knowledge", "plain"}, overlayAsks,
		"every code base is asked about its overlays exactly once")
}

// TestCoverageRows_BranchNoSegProbe is the declined-probe criterion, and the
// probed-key leg is the one that matters. Asserting only that the row reports no
// segments would pass against an implementation that probed the branch key and
// merely discarded the answer — which leaves the real hazard live, because the
// production reader LAZILY CONSTRUCTS a per-graph manager and its on-disk directory
// for whatever key it is handed. Asserting the branch key was never probed closes
// that at the caller, where the wrong key would originate.
func TestCoverageRows_BranchNoSegProbe(t *testing.T) {
	seg := &coverageSegReader{
		coveredByKey:  map[string]int{"code/agent": 40000},
		residentByKey: map[string]int{"code/agent": 40000},
	}
	deps := &coverageDeps{gc: branchFixture(), segCov: seg}

	rows := collectCoverageRows(context.Background(), deps)
	byGraph := make(map[string]CoverageRow, len(rows))
	for _, r := range rows {
		byGraph[r.Graph] = r
	}

	branch, ok := byGraph["code/agent@launch-fixes"]
	require.True(t, ok, "the branch graph must have a row at all")
	assert.False(t, branch.HasSegments,
		"a branch graph has no segment pool of its own, so the row must not claim one")
	assert.Equal(t, DispositionNoSegments, branch.SegDisposition,
		"the honest band is the dash: every named band asserts an arm is servicing this graph, and none is")

	assert.NotContains(t, seg.probed, "code/agent@launch-fixes",
		"the branch key must never be probed — the production reader lazily constructs a manager "+
			"and a directory for whatever key it is handed")
	assert.Contains(t, seg.probed, "code/agent",
		"the BASE key is still probed, so the branch row's silence is a declined probe rather than a dead reader")

	// KNOWN POSITIVE on the same measurement: the base row reports the real pool the
	// same reader served. Without it, a reader returning nothing for every key would
	// satisfy every assertion above.
	base, ok := byGraph["code/agent"]
	require.True(t, ok)
	assert.True(t, base.HasSegments)
	assert.Equal(t, 40000, base.SegCovered)
}

// TestRenderLLMCoverage_BranchRow is the CLI/MCP half: the markdown coverage table
// carries the branch graph's own row, under the composed row label, with the bare
// dash in its segment cell.
func TestRenderLLMCoverage_BranchRow(t *testing.T) {
	seg := &coverageSegReader{
		coveredByKey:  map[string]int{"code/agent": 40000},
		residentByKey: map[string]int{"code/agent": 40000},
	}

	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: branchFixture(), segCov: seg})

	assert.Contains(t, out, "| code/agent@launch-fixes | 59314 | 59314 of 59314 | 59314 of 59314 | — | 0 | 0 |",
		"the branch graph renders its own row, under the composed key, with the bare dash segment cell")

	// KNOWN POSITIVE in the same render: the base row carries a real segment cell, so
	// the dash above is this row's declined probe rather than a renderer that lost the
	// column for every row. Its band is cache-aged because the fixture programs no
	// backstop verification, which is the arm the default branch takes.
	assert.Contains(t, out, "| code/agent | 51967 | 51967 of 51967 | 51967 of 51967 | shipped 40000 · live 40000 [cache-aged] | 0 | 0 |",
		"the base row still renders its real segment coverage")
}
