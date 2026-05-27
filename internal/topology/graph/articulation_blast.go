// SPDX-License-Identifier: Apache-2.0

package graph

// articulation_blast.go computes the "blast radius" of each articulation
// point. After findArticulationPoints identifies which nodes, if removed,
// would disconnect the graph, this file scores how *bad* that
// disconnection would be using the per-AP component sizes already
// captured during the iterative DFS, then applies the LARGEST-STRANDING
// heuristic from open question f4391a3832efde6a466ebbbc32817ea7.
//
// LARGEST-STRANDING (OQ-G, option a):
//
//	downstream_affected = total_reachable - largest_split_component_size
//
// Intuition: when an articulation point is removed, the graph fractures
// into one "main" component (almost always the largest) and one or more
// "stranded" sets that are now unreachable from the main. The number of
// nodes in those stranded sets is the blast radius — those are the
// nodes the user "loses" when the articulation fails.
//
// Why not the largest component itself? Because for cloud SPOF analysis
// the largest component is the surviving AZ, and the stranded sets are
// the failed AZs. Reporting the surviving size would be backwards. For
// code dependency analysis the same logic applies: the main module
// continues, the stranded sub-modules are the impact.
//
// Pure in-memory: this file reads only the precomputed component sizes
// out of articulationState. All component sizes were captured during the
// iterative DFS in articulation_dfs.go; there is no graph read here.
//
// Complexity: O(1) per articulation point lookup. The expensive work
// happened once during the DFS in articulation_dfs.go (O(V + E) total
// for the entire graph).

// blastScore is the per-articulation-point blast radius computed by
// computeBlastScore. Returned as a struct so the caller can attach the
// individual numbers to a foundation.Finding.Metrics map.
type blastScore struct {
	// TotalReachable is the count of nodes reachable from any
	// non-articulation seed AFTER removing the articulation point. For
	// a connected graph this equals totalNodes-1; for a disconnected
	// graph it is the size of the original connected component minus 1.
	TotalReachable int
	// LargestComponent is the size of the biggest split component
	// produced by removing the articulation point. Always >= 1.
	LargestComponent int
	// StrandedNodes is the LARGEST-STRANDING blast radius:
	// TotalReachable - LargestComponent. This is the field analyzers
	// use to surface "how many nodes get cut off if X fails".
	StrandedNodes int
	// ComponentCount is the number of distinct split components after
	// removal. >= 2 for a true articulation point; 1 means the
	// articulation candidate was actually a leaf (defensive).
	ComponentCount int
}

// computeBlastScore returns the blast score for one articulation point
// by reading the precomputed split-component sizes from state. The
// state must have been produced by findArticulationPoints; calling
// this for a node that is not actually an articulation point will
// return a zero blastScore (no components recorded).
//
// For non-root articulation points: every DFS-tree child whose low
// link satisfied the articulation rule contributed its subtree size to
// state.componentSizes[apID] inside applyChildReturn. The "above"
// component (the side that remains reachable up through the AP's
// parent in the DFS tree) is the rest of the connected component
// minus the AP itself and minus the stranded subtrees.
//
// For root articulation points: each DFS-tree child of the root became
// its own stranded component on removal of the root. The largest of
// those is the surviving component and the rest are stranded.
func computeBlastScore(state *articulationState, apID int64) blastScore {
	subtreeSizes := state.componentSizes[apID]
	if len(subtreeSizes) == 0 {
		return blastScore{}
	}

	rootID := state.rootOf[apID]
	componentTotal := state.componentTotal[rootID]
	apIdx := state.idIndex[apID]
	isRoot := rootID == apID

	componentSizes := make([]int, 0, len(subtreeSizes)+1)
	if isRoot {
		// Root case: every recorded subtree IS a split component. The
		// root is only an AP if it had >= 2 DFS-tree children, and in
		// that case removing the root strands every child subtree.
		componentSizes = append(componentSizes, subtreeSizes...)
	} else {
		// Non-root case: each qualifying child subtree is a stranded
		// component. The "above" component is the rest of the
		// connected component sitting above the AP in the DFS tree
		// (componentTotal - subtreeSize[apIdx]) PLUS any non-stranded
		// descendants (children whose subtrees did not satisfy the
		// articulation rule because they had a back edge to "above").
		// Those merge into the "above" component on AP removal.
		strandedSum := 0
		for _, s := range subtreeSizes {
			componentSizes = append(componentSizes, s)
			strandedSum += s
		}
		above := componentTotal - state.subtreeSize[apIdx]
		nonStrandedDescendants := state.subtreeSize[apIdx] - 1 - strandedSum
		aboveFinal := above + nonStrandedDescendants
		if aboveFinal > 0 {
			componentSizes = append(componentSizes, aboveFinal)
		}
	}

	if len(componentSizes) == 0 {
		return blastScore{}
	}

	total := 0
	largest := 0
	for _, s := range componentSizes {
		total += s
		if s > largest {
			largest = s
		}
	}

	return blastScore{
		TotalReachable:   total,
		LargestComponent: largest,
		StrandedNodes:    total - largest,
		ComponentCount:   len(componentSizes),
	}
}
