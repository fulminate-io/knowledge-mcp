// SPDX-License-Identifier: Apache-2.0

// Package graph / pagerank_incremental.go implements runFullPowerIteration —
// a from-scratch power-iteration kernel that reproduces gonum's
// network.PageRankSparse step-for-step. It returns scores keyed by gonum
// int64 ID and does NOT import gonum's network package — it reproduces the
// algorithm directly so the analyzer is independent of PageRankSparse.
//
// The server-side variant of this file additionally carried the Sahu 2024
// (arXiv 2401.03256) Dynamic Frontier incremental kernel, which warm-started
// from a cached previous score vector plus a dirty set extracted from the
// in-process version overlay. That incremental path is dropped in the
// client-side relocation: there is no wire read-helper that returns "nodes
// mutated since a watermark", so the analyzer recomputes the full power
// iteration on every run. The output is unchanged — the DF kernel converges
// to the same fixed point runFullPowerIteration computes — so only the
// warm-start performance optimization is lost.
package graph

import (
	"math"

	"gonum.org/v1/gonum/graph"
)

// dfprMaxIterations is the safety cap on power-iteration sweeps. Default
// tolerance (1e-6) converges in 30-60 sweeps on real graphs.
const dfprMaxIterations = 200

// dfprSparseEntry is a single (column, value) pair in a compressed row.
// Repeated entries on the same column are tolerated by dot product.
type dfprSparseEntry struct {
	col int
	val float64
}

// nodeIDsOf snapshots every gonum node ID in g; the iteration order is
// the canonical dense indexing for this run.
func nodeIDsOf(g graph.Directed) []int64 {
	it := g.Nodes()
	out := make([]int64, 0, it.Len())
	for it.Next() {
		out = append(out, it.Node().ID())
	}
	return out
}

// buildCompressedRows assembles the transition matrix: rows[i] holds
// {col: j, val: damping/outDeg(j)} for every edge j→i. dangling holds
// the rank-sink contributions (zero out-degree vertices spread mass).
func buildCompressedRows(
	g graph.Directed,
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
		if len(toIDs) == 0 {
			dangling = append(dangling, dfprSparseEntry{col: j, val: df})
			continue
		}
		f := damping / float64(len(toIDs))
		for _, v := range toIDs {
			i, ok := indexOf[v]
			if !ok {
				continue
			}
			rows[i] = append(rows[i], dfprSparseEntry{col: j, val: f})
		}
	}
	return rows, dangling
}

// mulCompressedRows computes dst[i] = sum over entries in rows[i] of
// e.val * src[e.col]. dst and src must have the same length as rows.
func mulCompressedRows(rows [][]dfprSparseEntry, src, dst []float64) {
	for i, r := range rows {
		var sum float64
		for _, e := range r {
			sum += e.val * src[e.col]
		}
		dst[i] = sum
	}
}

// dotSparse returns sum over entries of e.val * v[e.col]. Used for the
// dangling-vertex rebalance term in the full iteration.
func dotSparse(entries []dfprSparseEntry, v []float64) float64 {
	var sum float64
	for _, e := range entries {
		sum += e.val * v[e.col]
	}
	return sum
}

// sumFloats returns the sum of every element in v.
func sumFloats(v []float64) float64 {
	var sum float64
	for _, x := range v {
		sum += x
	}
	return sum
}

// normDiffFloats returns the 2-norm of (x - y). Both slices must be the
// same length. Mirrors gonum's normDiff helper.
func normDiffFloats(x, y []float64) float64 {
	var sum float64
	for i, v := range x {
		d := v - y[i]
		sum += d * d
	}
	return math.Sqrt(sum)
}

// runFullPowerIteration computes PageRank scores via the same
// compressed-row-matrix kernel gonum's PageRankSparse uses (see
// gonum/graph/network/page.go lines 282-349). The only intentional
// deviation: gonum uses random.NormFloat64 init, we use a uniform 1/n
// vector. Both converge to the same fixed point — uniform is
// deterministic, which is what tests need. Caller validates damping ∈
// (0,1) and tolerance > 0. Empty graph returns an empty (non-nil) map.
func runFullPowerIteration(g graph.Directed, damping, tolerance float64) map[int64]float64 {
	nodes := nodeIDsOf(g)
	n := len(nodes)
	if n == 0 {
		return map[int64]float64{}
	}

	indexOf := make(map[int64]int, n)
	for i, id := range nodes {
		indexOf[id] = i
	}
	rows, dangling := buildCompressedRows(g, nodes, indexOf, damping)

	last := make([]float64, n)
	cur := make([]float64, n)
	for i := range cur {
		cur[i] = 1.0 / float64(n)
	}

	teleport := (1 - damping) / float64(n)
	for range dfprMaxIterations {
		last, cur = cur, last
		mulCompressedRows(rows, last, cur)
		danglingMass := dotSparse(dangling, last)
		teleportMass := teleport * sumFloats(last)
		add := danglingMass + teleportMass
		for i := range cur {
			cur[i] += add
		}
		if normDiffFloats(cur, last) < tolerance {
			break
		}
	}

	out := make(map[int64]float64, n)
	for i, id := range nodes {
		out[id] = cur[i]
	}
	return out
}
