// SPDX-License-Identifier: Apache-2.0

// Package graph holds the gonum-backed topology analyzer family that runs on
// the stdio client: PageRank / HITS / SCC / cycles / articulation / degree /
// community + the Leiden community-detection algorithm, plus the
// betweenness and god_object analyzers and the dsm layering analyzer. Every
// analyzer self-registers via init() into the foundation registry and reads
// its nodes and edges over the wire through foundation's GraphCaller seam and
// the shared wire read-helpers — none of them touch an in-process store.
//
// The Leiden family (this file + leiden_static.go + leiden_static_int.go +
// leiden_incremental.go) is pure: it operates on (nodeIDs []string,
// adj map[string][]string) and depends only on the standard library. The
// thought-package cluster detector imports RunLeiden / NewLeidenState /
// LeidenState / EdgeChange / ComputeEdgeChanges from here.
package graph

// leiden.go — Static Leiden (Traag et al. 2019) + Dynamic Frontier incremental
// updates (Sahu 2405.11658). The algorithm files import only the standard
// library so the partition math is identical regardless of how the adjacency
// snapshot was produced.
//
// This file holds:
//   - leidenConfig: shared configuration value
//   - EdgeChange:   incremental-update edge delta
//   - LeidenState:  cached partition for Dynamic Frontier
//   - RunLeiden:    exported static entry point
//   - runLeidenFull: outer loop driving local-move + subset-refine to fixed point
//   - shared helpers reused by both the static and incremental paths
//
// The static local-move/subset-refine helpers live in leiden_static.go and the
// LeidenState methods live in leiden_incremental.go. All three files share the
// same package and the same unexported type/function names.
//
// CPM quality function: Q = Σ_c [ e_c - γ · n_c·(n_c-1)/2 ]
// Single-level only (no aggregation). Subset Refine on moved communities only.

// leidenConfig bundles the algorithm tuning knobs. Kept unexported because
// every public entry point takes the gamma resolution as a positional
// argument — exposing the struct would force callers to learn an API they
// would only use to set one field.
type leidenConfig struct {
	Gamma         float64 // resolution parameter (default 0.5)
	MaxIterations int     // outer-loop cap (default 100)
}

// defaultLeidenConfig returns the standard configuration: gamma=0.5,
// MaxIterations=100. Callers override Gamma via the public RunLeiden /
// NewLeidenState parameter; MaxIterations is not currently configurable.
func defaultLeidenConfig() leidenConfig { return leidenConfig{0.5, 100} }

// EdgeChange records an edge addition or removal for incremental updates.
// The From/To pair is treated as undirected by ComputeEdgeChanges and the DF
// local-move walker — direction is preserved only so callers can render
// edges in their original orientation if they care.
type EdgeChange struct {
	From, To string
	Removed  bool
}

// LeidenState caches the partition for Dynamic Frontier incremental updates.
// Construct via NewLeidenState and call UpdateIncremental to amortize
// re-clustering cost when only a small fraction of edges change between runs.
type LeidenState struct {
	CommunityOf  map[string]string  // node → community ID
	CommSize     map[string]int     // community → member count
	CommWeightIn map[string]float64 // community → internal edge count
	cfg          leidenConfig
}

// RunLeiden is the static entry point. Drop-in replacement for any
// label-propagation-style community detector that takes (nodeIDs, adj, gamma)
// and returns a node→community partition.
func RunLeiden(nodeIDs []string, adj map[string][]string, gamma float64) map[string]string {
	cfg := defaultLeidenConfig()
	cfg.Gamma = gamma
	return runLeidenFull(nodeIDs, adj, cfg)
}

// runLeidenFull: local-move → Subset Refine → repeat until stable.
//
// Dispatches to the int-indexed fast path (leiden_static_int.go) which
// eliminates string-keyed map operations in the hot loop. The string-
// keyed helpers in leiden_static.go remain in place because the
// Dynamic Frontier incremental path (leiden_incremental.go) still
// drives applySubsetRefine through LeidenState's map-backed partition.
func runLeidenFull(nodeIDs []string, adj map[string][]string, cfg leidenConfig) map[string]string {
	return runLeidenFullInt(nodeIDs, adj, cfg)
}

// buildCommSize tallies member count per community from a node→community map.
func buildCommSize(communityOf map[string]string) map[string]int {
	sizes := make(map[string]int, len(communityOf))
	for _, c := range communityOf {
		sizes[c]++
	}
	return sizes
}

// buildCommWeightIn tallies internal edge count per community. Each undirected
// edge is counted once per endpoint and divided by two at the end.
func buildCommWeightIn(communityOf map[string]string, adj map[string][]string) map[string]float64 {
	w := make(map[string]float64)
	for node, neighbors := range adj {
		c := communityOf[node]
		for _, nb := range neighbors {
			if communityOf[nb] == c {
				w[c]++
			}
		}
	}
	for c := range w {
		w[c] /= 2 // each edge counted once per endpoint
	}
	return w
}

// stableCommID converts an integer to a short decimal string community label.
// Used to give communities deterministic, compact IDs after the partition
// stabilizes; both the static and incremental paths re-label via this helper.
func stableCommID(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return string(digits[i])
	}
	buf := make([]byte, 0, 8)
	for n := i; n > 0; n /= 10 {
		buf = append([]byte{digits[n%10]}, buf...)
	}
	return string(buf)
}

// subCommSize is O(n) — acceptable since refinement only runs on small communities.
func subCommSize(subComm map[string]string, sc string) int {
	count := 0
	for _, s := range subComm {
		if s == sc {
			count++
		}
	}
	return count
}
