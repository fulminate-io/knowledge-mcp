// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// centrality_test.go covers the headline behavioral assertions for PageRank
// and the two HITS analyzers over the shared 10-node star fixture, plus the
// degree, community, scc and cycles analyzers. The fixture has finding-0 as
// the star center (out-degree 4 to finding-1..4) plus an incoming edge from
// step-2 — the shape lets us assert HITS distinguishes hubs from authorities
// and that PageRank concentrates mass on heavily-pointed-at receivers.
//
// The fixture is wire-backed (graphFixture) rather than store-backed; the
// behavioral assertions are identical to the prior store-backed tests because
// only the data-access layer moved.

// findingID / stepID give the deterministic synthetic IDs used by the star
// fixture below, matching the prior fixture's "finding-N" / "step-N" labels.
func findingID(i int) string { return "finding-" + string(rune('0'+i)) }
func stepID(i int) string    { return "step-" + string(rune('0'+i)) }

// buildStarFixture reproduces the prior store-backed BuildTestFixture star
// shape over the wire: 5 findings + 5 steps; star (finding-0 -> finding-1..4
// via relates-to); chain (step-0..4 via contains); cross-edge (step-2 ->
// finding-0 via informed-by).
func buildStarFixture() *graphFixture {
	f := newGraphFixture()
	const n = 5
	for i := range n {
		f.AddNodeFull(findingID(i), findingID(i), kgtypes.NodeFinding, "", nil)
	}
	for i := range n {
		f.AddNodeFull(stepID(i), stepID(i), kgtypes.NodeStep, "", nil)
	}
	for i := range n - 1 {
		f.AddEdge(stepID(i), stepID(i+1), kgtypes.EdgeKGContains)
	}
	for i := 1; i < n; i++ {
		f.AddEdge(findingID(0), findingID(i), kgtypes.EdgeRelatesTo)
	}
	f.AddEdge(stepID(2), findingID(0), kgtypes.EdgeInformedBy)
	return f
}

func starReq(f *graphFixture, topK int) foundation.Request {
	return f.req(kgtypes.GraphKnowledge, "default", topK)
}

// TestPageRankAnalyzer_Run verifies the analyzer produces findings, that
// scores form a non-trivial distribution, and that every finding carries
// the expected metric keys.
func TestPageRankAnalyzer_Run(t *testing.T) {
	f := buildStarFixture()
	findings, err := PageRankAnalyzer{}.Run(newTestCtx(t), starReq(f, 20))
	if err != nil {
		t.Fatalf("PageRankAnalyzer.Run: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected at least one finding")
	}

	topScore := findings[0].Metrics["pagerank"]
	bottomScore := findings[len(findings)-1].Metrics["pagerank"]
	if topScore <= bottomScore {
		t.Errorf("expected top > bottom, got top=%g bottom=%g", topScore, bottomScore)
	}

	for i, fnd := range findings {
		for _, k := range []string{"pagerank", "rank", "percentile", "damping", "tolerance"} {
			if _, ok := fnd.Metrics[k]; !ok {
				t.Errorf("finding[%d] missing metric %q", i, k)
			}
		}
		if fnd.Algorithm != "pagerank" {
			t.Errorf("finding[%d] algorithm = %q, want pagerank", i, fnd.Algorithm)
		}
		if len(fnd.Evidence) == 0 {
			t.Errorf("finding[%d] empty Evidence", i)
		}
	}
}

// TestPageRankAnalyzer_Registered guards that pagerank self-registers under
// its stable name in the foundation registry.
func TestPageRankAnalyzer_Registered(t *testing.T) {
	a, ok := foundation.Get("pagerank")
	if !ok {
		t.Fatal("pagerank analyzer must be registered at package init")
	}
	if a.Name() != "pagerank" {
		t.Errorf("Name() = %q, want pagerank", a.Name())
	}
}

// TestHITSHubAnalyzer_Run verifies hubs run on the fixture and that the
// star-center finding-0 (out-degree 4) ranks #1 by hub score.
func TestHITSHubAnalyzer_Run(t *testing.T) {
	f := buildStarFixture()
	findings, err := HITSHubAnalyzer{}.Run(newTestCtx(t), starReq(f, 20))
	if err != nil {
		t.Fatalf("HITSHubAnalyzer.Run: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected at least one finding")
	}
	if findings[0].Evidence[0] != findingID(0) {
		t.Errorf("top hub = %s, want finding-0 = %s", findings[0].Evidence[0], findingID(0))
	}
	for _, fnd := range findings {
		if fnd.Algorithm != "hits_hubs" {
			t.Errorf("algorithm = %q, want hits_hubs", fnd.Algorithm)
		}
		for _, k := range []string{"score", "rank", "percentile", "tolerance"} {
			if _, ok := fnd.Metrics[k]; !ok {
				t.Errorf("finding[%s] missing metric %q", fnd.Title, k)
			}
		}
	}
}

// TestHITSAnalyzers_HubVsAuthority verifies hubs and authorities produce
// different rankings: finding-0 outranks finding-1 on hubs (out-degree 4),
// and finding-1 outranks finding-0 on the authority axis.
func TestHITSAnalyzers_HubVsAuthority(t *testing.T) {
	f := buildStarFixture()
	hubs, err := HITSHubAnalyzer{}.Run(newTestCtx(t), starReq(f, 20))
	if err != nil {
		t.Fatalf("hubs: %v", err)
	}
	auths, err := HITSAuthorityAnalyzer{}.Run(newTestCtx(t), starReq(f, 20))
	if err != nil {
		t.Fatalf("authorities: %v", err)
	}
	if len(hubs) == 0 || len(auths) == 0 {
		t.Fatal("expected non-empty results from both analyzers")
	}

	hubScore := make(map[string]float64, len(hubs))
	for _, fnd := range hubs {
		hubScore[fnd.Evidence[0]] = fnd.Metrics["score"]
	}
	authScore := make(map[string]float64, len(auths))
	for _, fnd := range auths {
		authScore[fnd.Evidence[0]] = fnd.Metrics["score"]
	}

	if hubScore[findingID(0)] <= hubScore[findingID(1)] {
		t.Errorf("hub: finding-0 (%g) should outrank finding-1 (%g)",
			hubScore[findingID(0)], hubScore[findingID(1)])
	}
	if authScore[findingID(1)] <= authScore[findingID(0)] {
		t.Errorf("authority: finding-1 (%g) should outrank finding-0 (%g)",
			authScore[findingID(1)], authScore[findingID(0)])
	}
}

// TestWeightedPageRank_Registered + smoke run on the star fixture.
func TestWeightedPageRank_Run(t *testing.T) {
	f := buildStarFixture()
	findings, err := WeightedPageRankAnalyzer{}.Run(newTestCtx(t), starReq(f, 5))
	if err != nil {
		t.Fatalf("WeightedPageRankAnalyzer.Run: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected at least one weighted-pagerank finding")
	}
	for _, fnd := range findings {
		if fnd.Algorithm != "pagerank_weighted" {
			t.Errorf("algorithm = %q, want pagerank_weighted", fnd.Algorithm)
		}
	}
}

// TestDegreeAnalyzers_Run verifies fan_in / fan_out / degree_total run on the
// star fixture and that finding-0 (out-degree 4) tops fan_out.
func TestDegreeAnalyzers_Run(t *testing.T) {
	f := buildStarFixture()
	fanOut, err := FanOutAnalyzer{}.Run(newTestCtx(t), starReq(f, 20))
	if err != nil {
		t.Fatalf("FanOutAnalyzer.Run: %v", err)
	}
	if len(fanOut) == 0 {
		t.Fatal("expected fan_out findings")
	}
	if fanOut[0].Evidence[0] != findingID(0) {
		t.Errorf("top fan_out = %s, want finding-0 (out-degree 4)", fanOut[0].Evidence[0])
	}
	if got := fanOut[0].Metrics["fan_out"]; got != 4 {
		t.Errorf("finding-0 fan_out = %g, want 4", got)
	}

	for _, name := range []string{"fan_in", "fan_out", "degree_total"} {
		a, ok := foundation.Get(name)
		if !ok {
			t.Errorf("degree analyzer %q not registered", name)
			continue
		}
		findings, derr := a.Run(newTestCtx(t), starReq(f, 20))
		if derr != nil {
			t.Errorf("%s.Run: %v", name, derr)
		}
		if len(findings) == 0 {
			t.Errorf("%s produced no findings", name)
		}
	}
}

// TestSCCAnalyzer_NoCycle verifies the acyclic star fixture yields no SCC
// findings (every SCC is a singleton).
func TestSCCAnalyzer_NoCycle(t *testing.T) {
	f := buildStarFixture()
	findings, err := SCCAnalyzer{}.Run(newTestCtx(t), starReq(f, 0))
	if err != nil {
		t.Fatalf("SCCAnalyzer.Run: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("acyclic fixture should produce no SCC findings, got %d", len(findings))
	}
}

// TestSCCAnalyzer_WithCycle verifies a 3-node cycle surfaces one SCC finding.
func TestSCCAnalyzer_WithCycle(t *testing.T) {
	f := newGraphFixture()
	for _, id := range []string{"a", "b", "c"} {
		f.AddNode(id, kgtypes.NodeFinding)
	}
	f.AddEdge("a", "b", kgtypes.EdgeRelatesTo)
	f.AddEdge("b", "c", kgtypes.EdgeRelatesTo)
	f.AddEdge("c", "a", kgtypes.EdgeRelatesTo)

	findings, err := SCCAnalyzer{}.Run(newTestCtx(t), f.req(kgtypes.GraphKnowledge, "default", 0))
	if err != nil {
		t.Fatalf("SCCAnalyzer.Run: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("3-node cycle should produce exactly 1 SCC finding, got %d", len(findings))
	}
	if got := findings[0].Metrics["size"]; got != 3 {
		t.Errorf("SCC size = %g, want 3", got)
	}
	if got := sortedStrings(findings[0].Evidence); len(got) != 3 {
		t.Errorf("SCC evidence should hold 3 members, got %v", got)
	}
}

// TestCyclesAnalyzer_WithCycle verifies a 2-node mutual reference surfaces one
// elementary cycle finding of length 2.
func TestCyclesAnalyzer_WithCycle(t *testing.T) {
	f := newGraphFixture()
	f.AddNode("a", kgtypes.NodeFinding)
	f.AddNode("b", kgtypes.NodeFinding)
	f.AddEdge("a", "b", kgtypes.EdgeRelatesTo)
	f.AddEdge("b", "a", kgtypes.EdgeRelatesTo)

	findings, err := CyclesAnalyzer{}.Run(newTestCtx(t), f.req(kgtypes.GraphKnowledge, "default", 20))
	if err != nil {
		t.Fatalf("CyclesAnalyzer.Run: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("a 2-cycle should produce exactly 1 cycle finding, got %d", len(findings))
	}
	if got := findings[0].Metrics["length"]; got != 2 {
		t.Errorf("cycle length = %g, want 2", got)
	}
}

// TestCommunityAnalyzer_TwoClique verifies the community analyzer surfaces the
// two cliques as communities (each clique has >= communityMinSize members).
func TestCommunityAnalyzer_TwoClique(t *testing.T) {
	f := newGraphFixture()
	const per = 6
	mk := func(prefix string, i int) string { return prefix + string(rune('0'+i)) }
	// Two cliques of `per` nodes each, fully connected internally, one bridge.
	for c := range 2 {
		prefix := "x"
		if c == 1 {
			prefix = "y"
		}
		for i := range per {
			f.AddNode(mk(prefix, i), kgtypes.NodeFinding)
		}
		for i := range per {
			for j := range per {
				if i != j {
					f.AddEdge(mk(prefix, i), mk(prefix, j), kgtypes.EdgeRelatesTo)
				}
			}
		}
	}
	f.AddEdge("x0", "y0", kgtypes.EdgeRelatesTo)

	findings, err := CommunityAnalyzer{}.Run(newTestCtx(t), f.req(kgtypes.GraphKnowledge, "default", 0))
	if err != nil {
		t.Fatalf("CommunityAnalyzer.Run: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("two cliques should produce 2 community findings, got %d", len(findings))
	}
	for _, fnd := range findings {
		if fnd.Algorithm != "community" {
			t.Errorf("algorithm = %q, want community", fnd.Algorithm)
		}
		if got := fnd.Metrics["size"]; got != per {
			t.Errorf("community size = %g, want %d", got, per)
		}
	}
}

// TestArticulation_Bridge verifies the articulation analyzer flags the cut
// vertex in a barbell graph (two triangles joined through a single hub node).
func TestArticulation_Bridge(t *testing.T) {
	f := newGraphFixture()
	for _, id := range []string{"a", "b", "hub", "c", "d"} {
		f.AddNode(id, kgtypes.NodeFinding)
	}
	// Left triangle a-b-hub, right triangle hub-c-d; hub is the cut vertex.
	f.AddEdge("a", "b", kgtypes.EdgeRelatesTo)
	f.AddEdge("b", "hub", kgtypes.EdgeRelatesTo)
	f.AddEdge("hub", "a", kgtypes.EdgeRelatesTo)
	f.AddEdge("hub", "c", kgtypes.EdgeRelatesTo)
	f.AddEdge("c", "d", kgtypes.EdgeRelatesTo)
	f.AddEdge("d", "hub", kgtypes.EdgeRelatesTo)

	findings, err := ArticulationAnalyzer{}.Run(newTestCtx(t), f.req(kgtypes.GraphKnowledge, "default", 0))
	if err != nil {
		t.Fatalf("ArticulationAnalyzer.Run: %v", err)
	}
	found := false
	for _, fnd := range findings {
		if len(fnd.Evidence) > 0 && fnd.Evidence[0] == "hub" {
			found = true
		}
	}
	if !found {
		t.Errorf("hub should be flagged as an articulation point, findings=%v", findings)
	}
}
