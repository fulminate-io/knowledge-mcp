// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chunker_edges_weight_test.go covers Phase 1 of the Weighted PageRank
// ticket: extractCallEdges aggregates call sites per (caller, callee)
// pair and emits the count as Edge.Weight.

// TestExtractCallEdges_WeightCounts builds a synthetic Go file where bar
// calls foo three times and baz once, then verifies the chunker produces
// one CALLS edge per unique callee with Weight equal to the number of
// call sites in the caller's body.
func TestExtractCallEdges_WeightCounts(t *testing.T) {
	src := []byte(`package counts

func foo() int { return 1 }
func baz() int { return 2 }

func bar() int {
	a := foo()
	b := foo()
	c := foo()
	d := baz()
	return a + b + c + d
}
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "counts.go", src)
	require.NoError(t, err)

	calls := filterEdges(result.Edges, EdgeCalls)

	// We expect bar→foo (Weight=3) and bar→baz (Weight=1). The chunker
	// emits the caller name in package-qualified form (counts.bar /
	// counts.foo / counts.baz).
	want := map[string]float64{
		"foo": 3,
		"baz": 1,
	}
	got := make(map[string]float64)
	for _, e := range calls {
		if e.FromID != "counts.bar" {
			continue
		}
		got[e.ToID] = e.Weight
	}

	for callee, weight := range want {
		assert.InDeltaf(t, weight, got[callee],
			1e-9, "bar → %s expected weight %v, got %v", callee, weight, got[callee])
	}
}

// TestExtractCallEdges_SingleCallStillWeightOne verifies that a unique
// callee called exactly once still receives Weight=1 (not 0). This is
// the boundary condition that distinguishes "one call site" from "no
// call site at all" — the latter never produces an edge.
func TestExtractCallEdges_SingleCallStillWeightOne(t *testing.T) {
	src := []byte(`package single

func helper() int { return 1 }

func caller() int {
	return helper()
}
`)
	chunker := NewChunker()
	defer chunker.Close()

	result, err := chunker.ChunkFile(context.Background(), "single.go", src)
	require.NoError(t, err)

	calls := filterEdges(result.Edges, EdgeCalls)

	var helperWeight float64
	var found bool
	for _, e := range calls {
		if e.FromID == "single.caller" && e.ToID == "helper" {
			helperWeight = e.Weight
			found = true
		}
	}
	require.True(t, found, "expected caller→helper CALLS edge")
	assert.InDelta(t, 1.0, helperWeight, 1e-9)
}
