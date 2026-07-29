// SPDX-License-Identifier: Apache-2.0

// Package graph / hits_iteration.go implements hitsScores — a from-scratch
// HITS power-iteration kernel that reproduces gonum's network.HITS
// step-for-step while adding the three properties the library call cannot
// offer: a degenerate-input short-circuit, a hard iteration cap, and
// cancellation inside the loop.
//
// Why in-house rather than a wrapper around network.HITS: that function takes
// only (graph, tolerance) — it exposes no iteration limit and no context, and
// its update loop is a bare `for {}`. On a graph with no edges every authority
// score is zero, so the 2-norm it divides by is zero, every score becomes NaN,
// and the `norm < tol` break condition is false forever because every NaN
// comparison is false. Bounding that from the outside would need a watchdog
// goroutine, which abandons a goroutine spinning a full core for the life of
// the process — worse than the hang it hides. Owning the loop is the only way
// the analyzer's termination contract can actually hold.
//
// This mirrors the precedent already set for PageRank in this package:
// runFullPowerIteration (pagerank_incremental.go) is likewise a from-scratch
// kernel replacing gonum's equally-unbounded PageRankSparse. Equivalence with
// network.HITS on graphs where gonum terminates is covered by test.
package graph

import (
	"context"
	"fmt"
	"math"

	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/network"
)

// hitsMaxIterations is the safety cap on mutually-reinforcing sweeps. HITS
// converges faster than PageRank in practice — the default 1e-8 tolerance
// settles in well under 50 sweeps on real graphs — so the cap is only ever
// reached by an input that does not converge at all.
const hitsMaxIterations = 200

// hitsScores computes HITS hub/authority pairs for every node of g, keyed by
// gonum int64 node ID.
//
// Termination is guaranteed three ways: a graph with no edges (post-adapter,
// so after self-loops have been dropped) returns zero scores for every node
// without entering the iteration; the iteration itself is capped at maxIter
// sweeps and reports a convergence failure rather than spinning; and ctx is
// checked at the top of every sweep so a cancelled request interrupts a long
// convergence. name identifies the calling analyzer in both error messages.
func hitsScores(
	ctx context.Context,
	g graph.Directed,
	tolerance float64,
	maxIter int,
	name string,
) (map[int64]network.HubAuthority, error) {
	nodes := graph.NodesOf(g.Nodes())
	if len(nodes) == 0 {
		return map[int64]network.HubAuthority{}, nil
	}

	indexOf := make(map[int64]int, len(nodes))
	for i, n := range nodes {
		indexOf[n.ID()] = i
	}
	linkingTo, linkedFrom, edges := hitsAdjacency(g, nodes, indexOf)
	if edges == 0 {
		// No edges means no node points at anything and no node is pointed
		// at: every hub and authority score is zero by definition. Returning
		// them directly is both the correct answer and the guard that keeps
		// the iteration away from a zero-norm division.
		return zeroHITSScores(nodes), nil
	}

	w := make([]float64, 4*len(nodes))
	auth, hub := w[:len(nodes)], w[len(nodes):2*len(nodes)]
	for i := range nodes {
		auth[i] = 1
		hub[i] = 1
	}
	deltaAuth, deltaHub := w[2*len(nodes):3*len(nodes)], w[3*len(nodes):]

	converged := false
	for range maxIter {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/%s: %w", name, err)
		}
		if !hitsSweep(auth, hub, deltaAuth, deltaHub, linkingTo, linkedFrom) {
			// The score vector collapsed to zero mid-sweep, so there is
			// nothing to normalize against. Same answer as the edgeless case.
			return zeroHITSScores(nodes), nil
		}
		if norm2(deltaAuth) < tolerance && norm2(deltaHub) < tolerance {
			converged = true
			break
		}
	}
	if !converged {
		return nil, fmt.Errorf(
			"topology/%s: HITS did not converge within %d iterations (tolerance=%g)",
			name, maxIter, tolerance)
	}

	out := make(map[int64]network.HubAuthority, len(nodes))
	for i, n := range nodes {
		out[n.ID()] = network.HubAuthority{Hub: hub[i], Authority: auth[i]}
	}
	return out, nil
}

// hitsAdjacency builds the dense-indexed in/out neighbor lists the iteration
// walks, plus the total edge count over the materialized node set. Edges to
// nodes outside the set cannot occur — the adapter only materializes edges
// whose endpoints both survived — but an unmapped endpoint is skipped rather
// than indexed at zero.
func hitsAdjacency(g graph.Directed, nodes []graph.Node, indexOf map[int64]int) (linkingTo, linkedFrom [][]int, edges int) {
	linkingTo = make([][]int, len(nodes))
	linkedFrom = make([][]int, len(nodes))
	for i, n := range nodes {
		id := n.ID()
		from := g.To(id)
		for from.Next() {
			if u, ok := indexOf[from.Node().ID()]; ok {
				linkingTo[i] = append(linkingTo[i], u)
			}
		}
		to := g.From(id)
		for to.Next() {
			if v, ok := indexOf[to.Node().ID()]; ok {
				linkedFrom[i] = append(linkedFrom[i], v)
				edges++
			}
		}
	}
	return linkingTo, linkedFrom, edges
}

// hitsSweep runs one mutually-reinforcing update: authorities absorb the hub
// scores pointing at them, hubs absorb the authority scores they point to,
// each vector normalized to unit 2-norm. deltaAuth and deltaHub come back
// holding the per-node change across the sweep. Returns false when either
// vector collapses to all-zero, leaving nothing to normalize against — the
// caller treats that as the degenerate all-zero result instead of dividing by
// zero and iterating on NaN.
func hitsSweep(auth, hub, deltaAuth, deltaHub []float64, linkingTo, linkedFrom [][]int) bool {
	var norm float64
	for v := range auth {
		var a float64
		for _, u := range linkingTo[v] {
			a += hub[u]
		}
		deltaAuth[v] = auth[v]
		auth[v] = a
		norm += a * a
	}
	if norm = math.Sqrt(norm); norm == 0 {
		return false
	}
	for i := range auth {
		auth[i] /= norm
		deltaAuth[i] -= auth[i]
	}

	norm = 0
	for u := range hub {
		var h float64
		for _, v := range linkedFrom[u] {
			h += auth[v]
		}
		deltaHub[u] = hub[u]
		hub[u] = h
		norm += h * h
	}
	if norm = math.Sqrt(norm); norm == 0 {
		return false
	}
	for i := range hub {
		hub[i] /= norm
		deltaHub[i] -= hub[i]
	}
	return true
}

// zeroHITSScores returns an all-zero score pair for every node — the answer
// for any graph whose structure carries no hub or authority signal at all.
func zeroHITSScores(nodes []graph.Node) map[int64]network.HubAuthority {
	out := make(map[int64]network.HubAuthority, len(nodes))
	for _, n := range nodes {
		out[n.ID()] = network.HubAuthority{}
	}
	return out
}

// norm2 returns the 2-norm of v.
func norm2(v []float64) float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	return math.Sqrt(sum)
}
