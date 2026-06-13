// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// countingCaller wraps a graphFixture and counts the bulk-edge (Execute with
// RETURN_MODE_EDGES) calls so the god_object test can assert the metric pass
// uses bulk fetches, not a per-candidate fan-out.
type countingCaller struct {
	inner    *graphFixture
	edgeCall int
}

func (c *countingCaller) Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if req.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		c.edgeCall++
	}
	return c.inner.Execute(ctx, req)
}

var _ foundation.GraphCaller = (*countingCaller)(nil)

// buildGodObjectFixture builds a code graph with one god type (TypeA) that
// contains 3 methods, each calling out, plus two lean types. TypeA should rank
// #1 by combined percentile.
func buildGodObjectFixture() *graphFixture {
	f := newGraphFixture()
	// God type with 3 methods.
	f.AddNodeFull("TypeA", "TypeA", kgtypes.NodeType("type_declaration"), "go", nil)
	for _, m := range []string{"A.m1", "A.m2", "A.m3"} {
		f.AddNodeFull(m, m, kgtypes.NodeType("method"), "go", nil)
		f.AddEdge("TypeA", m, kgtypes.EdgeContains)
	}
	// Method callees (one hop) — distinct callee targets feed RFC.
	f.AddNodeFull("callee1", "callee1", kgtypes.NodeType("function"), "go", nil)
	f.AddNodeFull("callee2", "callee2", kgtypes.NodeType("function"), "go", nil)
	f.AddEdge("A.m1", "callee1", kgtypes.EdgeCalls)
	f.AddEdge("A.m2", "callee2", kgtypes.EdgeCalls)
	// TypeA couples out to two other types (CBO) and is depended upon (FanIn).
	f.AddNodeFull("TypeB", "TypeB", kgtypes.NodeType("type_declaration"), "go", nil)
	f.AddNodeFull("TypeC", "TypeC", kgtypes.NodeType("type_declaration"), "go", nil)
	f.AddEdge("TypeA", "TypeB", kgtypes.EdgeUsesType)
	f.AddEdge("TypeA", "TypeC", kgtypes.EdgeUsesType)
	f.AddEdge("TypeB", "TypeA", kgtypes.EdgeCalls)    // fan-in to TypeA
	f.AddEdge("TypeC", "TypeA", kgtypes.EdgeUsesType) // fan-in to TypeA
	return f
}

// TestGodObject_RanksGodType verifies TypeA ranks #1 and that the analyzer
// issues a bounded number of bulk-edge fetches (NOT one per candidate).
func TestGodObject_RanksGodType(t *testing.T) {
	cc := &countingCaller{inner: buildGodObjectFixture()}
	req := foundation.Request{Caller: cc, Graph: kgtypes.GraphCode, Name: "repo", TopK: 10}

	findings, err := GodObjectAnalyzer{}.Run(newTestCtx(t), req)
	if err != nil {
		t.Fatalf("GodObjectAnalyzer.Run: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("expected at least one god_object finding")
	}
	if findings[0].Evidence[0] != "TypeA" {
		t.Errorf("top god object = %s, want TypeA", findings[0].Evidence[0])
	}
	for _, fnd := range findings {
		for _, k := range []string{"cbo", "rfc", "wmc", "fan_in", "combined_percentile"} {
			if _, ok := fnd.Metrics[k]; !ok {
				t.Errorf("finding %s missing metric %q", fnd.Title, k)
			}
		}
	}

	// Bulk-edge discipline: the metric COMPUTE pass must use bulk fetches, not
	// a per-candidate fan-out. Total bulk-edge fetches = NewGonumGraph(1) +
	// computeGodObjectRows candidate-edges(1) + method-callee-edges(1) +
	// one sampleNeighbors fetch per SURFACED finding (the evidence sampler,
	// per-finding by design — the prior implementation sampled per finding
	// too). With 5 type-ish candidates, a per-candidate metric loop
	// (computeCBO/RFC/WMC/FanIn each doing its own IterEdges) would be ~15-20
	// fetches; the bulk pass is 3 + len(findings). Assert we are firmly in the
	// bulk regime.
	if cc.edgeCall > 3+len(findings) {
		t.Errorf("god_object issued %d bulk-edge fetches; bulk pass is 3 graph/metric fetches + %d per-finding sampleNeighbors = %d. A per-candidate metric loop would be far higher.",
			cc.edgeCall, len(findings), 3+len(findings))
	}
}

// TestGodObject_Metrics verifies the exact CK metric values for TypeA on the
// fixture, locking the bulk-edge group-by to the prior IterEdges semantics.
func TestGodObject_Metrics(t *testing.T) {
	cc := &countingCaller{inner: buildGodObjectFixture()}
	req := foundation.Request{Caller: cc, Graph: kgtypes.GraphCode, Name: "repo", TopK: 10}

	findings, err := GodObjectAnalyzer{}.Run(newTestCtx(t), req)
	if err != nil {
		t.Fatalf("GodObjectAnalyzer.Run: %v", err)
	}
	var typeA *foundation.Finding
	for i := range findings {
		if findings[i].Evidence[0] == "TypeA" {
			typeA = &findings[i]
		}
	}
	if typeA == nil {
		t.Fatal("TypeA finding not found")
	}
	// CBO: distinct other targets via UsesType/Calls from TypeA = {TypeB, TypeC} = 2.
	if got := typeA.Metrics["cbo"]; got != 2 {
		t.Errorf("CBO = %g, want 2", got)
	}
	// WMC: 3 contained methods.
	if got := typeA.Metrics["wmc"]; got != 3 {
		t.Errorf("WMC = %g, want 3", got)
	}
	// RFC: 3 methods + 2 distinct callees = 5.
	if got := typeA.Metrics["rfc"]; got != 5 {
		t.Errorf("RFC = %g, want 5", got)
	}
	// FanIn: incoming Calls/UsesType/Contains to TypeA = TypeB(calls) + TypeC(usestype) = 2.
	if got := typeA.Metrics["fan_in"]; got != 2 {
		t.Errorf("FanIn = %g, want 2", got)
	}
}

// TestGodObject_NonCodeGraphSkips verifies the analyzer returns nil on a
// non-code graph.
func TestGodObject_NonCodeGraphSkips(t *testing.T) {
	f := buildStarFixture()
	findings, err := GodObjectAnalyzer{}.Run(newTestCtx(t), f.req(kgtypes.GraphKnowledge, "default", 10))
	if err != nil {
		t.Fatalf("GodObjectAnalyzer.Run: %v", err)
	}
	if findings != nil {
		t.Errorf("god_object on a knowledge graph should return nil, got %v", findings)
	}
}

// TestLanguageMatchesScope locks the topology per-language scope filter's
// family alias: scope "typescript" includes tsx (so a typescript-scoped run
// does not drop .tsx/JSX nodes), but scope "tsx" is exact-only.
func TestLanguageMatchesScope(t *testing.T) {
	cases := []struct {
		nodeLang string
		scope    string
		want     bool
	}{
		{"tsx", "typescript", true},        // family alias: tsx folds into typescript scope
		{"typescript", "typescript", true}, // exact match
		{"tsx", "tsx", true},               // exact match
		{"typescript", "tsx", false},       // alias is one-directional
		{"go", "typescript", false},        // unrelated language excluded
	}
	for _, tc := range cases {
		if got := languageMatchesScope(tc.nodeLang, tc.scope); got != tc.want {
			t.Errorf("languageMatchesScope(%q, %q) = %t, want %t",
				tc.nodeLang, tc.scope, got, tc.want)
		}
	}
}
