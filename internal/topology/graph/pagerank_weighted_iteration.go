// SPDX-License-Identifier: Apache-2.0

// Package graph / pagerank_weighted_iteration.go implements
// runWeightedPowerIteration — the edge-weight-honoring PageRank kernel, and
// the weighted row builder that is the only thing separating it from the
// unweighted runFullPowerIteration (pagerank_incremental.go).
//
// Why in-house rather than gonum's network.PageRankSparse: that function takes
// only (graph, damping, tolerance) — no iteration limit, no context. For a
// simple.WeightedDirectedGraph it dispatches to edgeWeightedPageRankSparse,
// whose update loop is a bare `for {}` whose sole exit is convergence. An
// input that fails to converge therefore spins a goroutine for the life of the
// process, uninterruptibly — the same structural shape that hung HITS. Bounding
// it from outside would need a watchdog that abandons a spinning goroutine
// anyway, so owning the loop is the only way the analyzer's termination
// contract can actually hold.
//
// The math is unchanged from the library: a source spreads its damped mass over
// its out-edges in proportion to their weight, and a source with no outgoing
// weight is a rank sink that spreads damping/n uniformly. On a graph whose
// edges all carry weight 1 that reduces exactly to damping/outDeg, which is
// what the unweighted builder computes — the weighted builder is a strict
// generalization, and both are pinned to gonum by test.
package graph

import (
	"context"

	"gonum.org/v1/gonum/graph"
)

// buildCompressedRowsWeighted assembles the weighted transition matrix:
// rows[i] holds {col: j, val: w(j→i)*damping/z(j)} for every edge j→i, where
// z(j) is the sum of j's outgoing edge weights. A source whose outgoing weight
// sums to zero — no out-edges, or out-edges that all carry zero weight — is
// dangling and contributes damping/n uniformly instead, exactly as the
// unweighted builder treats a zero-out-degree vertex.
func buildCompressedRowsWeighted(
	g graph.WeightedDirected,
	nodes []int64,
	indexOf map[int64]int,
	damping float64,
) (rows [][]dfprSparseEntry, dangling []dfprSparseEntry) {
	n := len(nodes)
	rows = make([][]dfprSparseEntry, n)
	df := damping / float64(n)
	for j, u := range nodes {
		toIt := g.From(u)
		toIDs := make([]int64, 0, toIt.Len())
		for toIt.Next() {
			toIDs = append(toIDs, toIt.Node().ID())
		}

		var z float64
		for _, v := range toIDs {
			if w, ok := g.Weight(u, v); ok {
				z += w
			}
		}
		if z == 0 {
			dangling = append(dangling, dfprSparseEntry{col: j, val: df})
			continue
		}

		for _, v := range toIDs {
			i, ok := indexOf[v]
			if !ok {
				continue
			}
			w, ok := g.Weight(u, v)
			if !ok {
				continue
			}
			rows[i] = append(rows[i], dfprSparseEntry{col: j, val: (w * damping) / z})
		}
	}
	return rows, dangling
}

// runWeightedPowerIteration computes edge-weighted PageRank scores keyed by
// gonum int64 node ID. It serves the `pagerank_weighted` analyzer only, so
// that name is what its errors carry. It shares the bounded sweep, and
// therefore the termination contract, with the unweighted kernel — see
// runPowerIteration.
func runWeightedPowerIteration(
	ctx context.Context,
	g graph.WeightedDirected,
	damping, tolerance float64,
	maxIter int,
) (map[int64]float64, error) {
	build := func(nodes []int64, indexOf map[int64]int, damping float64) ([][]dfprSparseEntry, []dfprSparseEntry) {
		return buildCompressedRowsWeighted(g, nodes, indexOf, damping)
	}
	return runPowerIteration(ctx, nodeIDsOf(g), build, damping, tolerance, maxIter, "pagerank_weighted")
}
