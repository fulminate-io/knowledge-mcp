// SPDX-License-Identifier: Apache-2.0

package foundation

import (
	"context"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func TestNewGonumGraphMaterializes(t *testing.T) {
	// Seed three nodes and two directed edges (a→b weight 2, b→c unweighted).
	f := &fakeCaller{
		nodes: []*knowledgev1.Node{node("a", "function"), node("b", "function"), node("c", "type")},
		edges: []*knowledgev1.Edge{edge("a", "b", 2), edge("b", "c", 0)},
	}

	g, err := NewGonumGraph(context.Background(), f, kgtypes.GraphCode, "repo", nil)
	if err != nil {
		t.Fatalf("NewGonumGraph: %v", err)
	}

	// N+1 avoidance: exactly one node-browse Execute + one edge Execute.
	if f.calls != 2 {
		t.Fatalf("want exactly 2 Execute (one node-browse + one bulk-edge), got %d", f.calls)
	}

	// Node count: three nodes materialized.
	if got := g.Nodes().Len(); got != 3 {
		t.Fatalf("want 3 gonum nodes, got %d", got)
	}

	// NodeID / StringID mappings round-trip and follow wire order (a=0,b=1,c=2).
	for want, id := range []string{"a", "b", "c"} {
		gotInt, ok := g.NodeID(id)
		if !ok {
			t.Fatalf("NodeID(%q): not found", id)
		}
		if gotInt != int64(want) {
			t.Fatalf("NodeID(%q): want %d, got %d", id, want, gotInt)
		}
		gotStr, ok := g.StringID(int64(want))
		if !ok || gotStr != id {
			t.Fatalf("StringID(%d): want %q, got %q (ok=%v)", want, id, gotStr, ok)
		}
	}

	// Edge a→b carries weight 2; b→c falls back to the unweighted baseline 1.
	aInt, _ := g.NodeID("a")
	bInt, _ := g.NodeID("b")
	cInt, _ := g.NodeID("c")
	if w := g.WeightedEdge(aInt, bInt).Weight(); w != 2 {
		t.Fatalf("edge a→b: want weight 2, got %v", w)
	}
	if w := g.WeightedEdge(bInt, cInt).Weight(); w != 1 {
		t.Fatalf("edge b→c: want weight 1 (zero→baseline), got %v", w)
	}

	// IterateNodesByType honors the wire NodeType.
	var funcs int
	g.IterateNodesByType(kgtypes.NodeType("function"), func(_ int64, _ string) bool {
		funcs++
		return true
	})
	if funcs != 2 {
		t.Fatalf("want 2 function nodes, got %d", funcs)
	}
}

func TestNewGonumGraphUnweightedForcesWeightOne(t *testing.T) {
	f := &fakeCaller{
		nodes: []*knowledgev1.Node{node("a", "x"), node("b", "y")},
		edges: []*knowledgev1.Edge{edge("a", "b", 5)},
	}
	g, err := NewGonumGraphUnweighted(context.Background(), f, kgtypes.GraphKnowledge, "", nil)
	if err != nil {
		t.Fatalf("NewGonumGraphUnweighted: %v", err)
	}
	aInt, _ := g.NodeID("a")
	bInt, _ := g.NodeID("b")
	if w := g.WeightedEdge(aInt, bInt).Weight(); w != 1 {
		t.Fatalf("unweighted edge a→b: want weight 1, got %v", w)
	}
}

func TestNewGonumGraphSubsetFiltersNodesAndEdges(t *testing.T) {
	// Subset keeps only "function" nodes; the a→b edge survives, b→c drops
	// because c (a "type" node) is filtered out.
	f := &fakeCaller{
		nodes: []*knowledgev1.Node{node("a", "function"), node("b", "function"), node("c", "type")},
		edges: []*knowledgev1.Edge{edge("a", "b", 1), edge("b", "c", 1)},
	}
	subset := func(n *knowledgev1.Node) bool { return n.Type == "function" }
	g, err := NewGonumGraph(context.Background(), f, kgtypes.GraphCode, "repo", subset)
	if err != nil {
		t.Fatalf("NewGonumGraph: %v", err)
	}
	if got := g.Nodes().Len(); got != 2 {
		t.Fatalf("want 2 nodes after subset, got %d", got)
	}
	if _, ok := g.NodeID("c"); ok {
		t.Fatalf("node c should have been filtered out")
	}
	aInt, _ := g.NodeID("a")
	bInt, _ := g.NodeID("b")
	if e := g.WeightedEdge(aInt, bInt); e == nil {
		t.Fatalf("edge a→b should survive the subset")
	}
}

func TestNewGonumGraphNilCaller(t *testing.T) {
	if _, err := NewGonumGraph(context.Background(), nil, kgtypes.GraphCode, "repo", nil); err == nil {
		t.Fatalf("want error for nil caller")
	}
}
