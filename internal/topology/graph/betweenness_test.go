// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// betweenness_test.go covers SampledBetweennessAnalyzer over a wire-backed
// code-graph fixture. The fixture is a small "bow-tie" — two clusters joined
// through a single bridge node — so the bridge has the highest betweenness.
// Below exactThreshold the analyzer runs the exact gonum Brandes path.

// buildBridgeFixture builds a code graph where node "bridge" sits on every
// shortest path between the left cluster {l1,l2} and the right cluster
// {r1,r2}. Edges are directed both ways so the undirected betweenness signal
// concentrates on the bridge.
func buildBridgeFixture() *graphFixture {
	f := newGraphFixture()
	for _, id := range []string{"l1", "l2", "bridge", "r1", "r2"} {
		f.AddNodeFull(id, id, kgtypes.NodeType("function"), "go", nil)
	}
	link := func(a, b string) {
		f.AddEdge(a, b, kgtypes.EdgeCalls)
		f.AddEdge(b, a, kgtypes.EdgeCalls)
	}
	link("l1", "l2")
	link("l1", "bridge")
	link("l2", "bridge")
	link("bridge", "r1")
	link("bridge", "r2")
	link("r1", "r2")
	return f
}

// TestBetweenness_BridgeRanksTop verifies the exact betweenness path flags the
// bridge node as the top broker.
func TestBetweenness_BridgeRanksTop(t *testing.T) {
	f := buildBridgeFixture()
	findings, err := SampledBetweennessAnalyzer{}.Run(newTestCtx(t), f.req(kgtypes.GraphCode, "repo", 5))
	if err != nil {
		t.Fatalf("SampledBetweennessAnalyzer.Run: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected at least one betweenness finding")
	}
	if findings[0].Evidence[0] != "bridge" {
		t.Errorf("top bridge = %s, want bridge", findings[0].Evidence[0])
	}
	for _, fnd := range findings {
		if fnd.Algorithm != "betweenness" {
			t.Errorf("algorithm = %q, want betweenness", fnd.Algorithm)
		}
		if got := fnd.Metrics["mode"]; got != modeExact {
			t.Errorf("mode = %g, want exact (%g) for small graph", got, modeExact)
		}
	}
}

// TestBetweenness_Registered guards self-registration.
func TestBetweenness_Registered(t *testing.T) {
	if _, ok := foundation.Get("betweenness"); !ok {
		t.Fatal("betweenness analyzer must be registered at package init")
	}
}

// TestBetweenness_UnsupportedGraphSkips verifies cloud graphs are skipped.
func TestBetweenness_UnsupportedGraphSkips(t *testing.T) {
	f := buildBridgeFixture()
	req := foundation.Request{Caller: f, Graph: kgtypes.GraphCloud, Name: "acct", TopK: 5}
	findings, err := SampledBetweennessAnalyzer{}.Run(newTestCtx(t), req)
	if err != nil {
		t.Fatalf("SampledBetweennessAnalyzer.Run: %v", err)
	}
	if findings != nil {
		t.Errorf("betweenness on a cloud graph should return nil, got %v", findings)
	}
}

// TestBetweenness_PerPackage verifies the per-package mode reads NodePackage
// nodes + their CONTAINS members over the wire and emits per-package findings.
func TestBetweenness_PerPackage(t *testing.T) {
	f := newGraphFixture()
	// One package containing the bridge fixture's nodes.
	f.AddNodeFull("pkg/x", "pkg/x", kgtypes.NodePackage, "go", nil)
	for _, id := range []string{"l1", "l2", "bridge", "r1", "r2"} {
		f.AddNodeFull(id, id, kgtypes.NodeType("function"), "go", nil)
		f.AddEdge("pkg/x", id, kgtypes.EdgeContains)
	}
	link := func(a, b string) {
		f.AddEdge(a, b, kgtypes.EdgeCalls)
		f.AddEdge(b, a, kgtypes.EdgeCalls)
	}
	link("l1", "bridge")
	link("l2", "bridge")
	link("bridge", "r1")
	link("bridge", "r2")

	req := foundation.Request{
		Caller: f, Graph: kgtypes.GraphCode, Name: "repo", TopK: 5,
		Extra: map[string]string{"per_package": "true"},
	}
	findings, err := SampledBetweennessAnalyzer{}.Run(newTestCtx(t), req)
	if err != nil {
		t.Fatalf("per-package betweenness: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected per-package betweenness findings")
	}
	for _, fnd := range findings {
		if got := fnd.Metrics["mode"]; got != modePerPackage {
			t.Errorf("mode = %g, want per-package (%g)", got, modePerPackage)
		}
	}
}
