// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// document_order_perf_test.go measures the COST SHAPE of the reading-order
// index, which no ordering assertion can see: an index rebuilt inside the
// comparator returns identical, correct answers and turns one O(V+E) build into
// one per comparison.
//
// IT COUNTS ALLOCATIONS, NOT WALL TIME, and the choice was forced by
// measurement. The wall-clock form of this same ratio was executed five times on
// one unmodified binary and returned 2.96x, 3.04x, 3.84x, 5.57x and 7.35x
// against an 8.0 threshold — one sample within 0.65 of false-redding correct
// work, because at these sizes the run finishes fast enough for scheduler noise
// on a shared machine to dominate its ns/op. Allocation count is a property of
// the code path rather than of the machine.

// wideDoc builds a document root with n positioned sections under it.
func wideDoc(n int) ([]*knowledgev1.Node, []*knowledgev1.Edge) {
	nodes := make([]*knowledgev1.Node, 0, n+1)
	edges := make([]*knowledgev1.Edge, 0, n)
	nodes = append(nodes, docNode("doc", "document"))
	for i := range n {
		id := "s" + strconv.Itoa(i)
		nodes = append(nodes, docNode(id, "section"))
		edges = append(edges, ordEdge("doc", id, strconv.Itoa(i)))
	}
	return nodes, edges
}

// selectAllocsPerOp measures the allocations one whole select over an n-section
// document costs, INDEX BUILD INCLUDED: each iteration indexes a fresh view, so
// a per-run build is paid once per iteration and a per-comparison build is paid
// n log n times.
func selectAllocsPerOp(n int) int64 {
	nodes, edges := wideDoc(n)
	res := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			sv := renderView(nodes, edges)
			env := newEnv()
			if err := evalSelect(context.Background(), env, RuleSelect{NodeType: "section"}, sv); err != nil {
				b.Fatal(err)
			}
		}
	})
	return res.AllocsPerOp()
}

// TestDocumentOrder_ScalesSubQuadratically rejects the per-row rebuild:
// quadrupling the document must allocate strictly less than 8x. Compliant work
// measures about 4x; rebuilding the index per comparison measures about 16x.
func TestDocumentOrder_ScalesSubQuadratically(t *testing.T) {
	small := selectAllocsPerOp(500)
	large := selectAllocsPerOp(2000)
	require.Positive(t, small, "the benchmark must have run")

	ratio := float64(large) / float64(small)
	t.Logf("select over 500 sections: %d allocs/op; over 2000: %d allocs/op; ratio %.2fx (quadratic would be ~16x)",
		small, large, ratio)
	assert.Less(t, ratio, 8.0,
		"a 4x larger document allocated %.2fx — the reading-order index is being rebuilt per row, not per run", ratio)
}

// TestDocumentOrder_IndexIsBuiltOncePerRun catches the defect the ratio cannot
// see: the memo dropped while the index is still hoisted out of the comparator.
// One select pays one build either way, so the ratio stays green, but a recipe
// that orders three times pays three builds over a graph the run already holds
// whole.
func TestDocumentOrder_IndexIsBuiltOncePerRun(t *testing.T) {
	sv := renderView(wideDoc(8))
	first, err := sv.documentOrderIndex()
	require.NoError(t, err)
	second, err := sv.documentOrderIndex()
	require.NoError(t, err)
	assert.Same(t, first, second, "the second call rebuilt the index instead of returning the memo")
}
