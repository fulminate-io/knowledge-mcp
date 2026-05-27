// SPDX-License-Identifier: Apache-2.0

package graph

// leiden_static.go — String-keyed Leiden refinement helpers retained
// for the Dynamic Frontier incremental path (leiden_incremental.go),
// which operates on LeidenState's map[string]string partition and
// still calls applySubsetRefine + cpmBestMoveLocal directly.
//
// The static full-pass is served by runLeidenFullInt (see
// leiden_static_int.go) which avoids these string-keyed helpers
// entirely.

// applySubsetRefine tries to split a community via an intra-community local-move pass.
// If at least one sub-community emerges that is smaller than the original, the
// largest sub-community keeps the original label and the rest are promoted to
// fresh community IDs (one per sub-community member).
func applySubsetRefine(
	comm string, members []string, adj map[string][]string,
	communityOf map[string]string, commSize map[string]int, cfg leidenConfig,
) {
	memberSet := make(map[string]bool, len(members))
	for _, m := range members {
		memberSet[m] = true
	}
	subComm := make(map[string]string, len(members))
	for _, m := range members {
		subComm[m] = m
	}
	changed := true
	for range cfg.MaxIterations {
		if !changed {
			break
		}
		changed = false
		for _, node := range members {
			best := cpmBestMoveLocal(node, adj[node], subComm, memberSet, cfg.Gamma)
			if best != subComm[node] {
				subComm[node] = best
				changed = true
			}
		}
	}
	// Find how many distinct sub-communities emerged.
	subSizes := make(map[string]int)
	for _, sc := range subComm {
		subSizes[sc]++
	}
	if len(subSizes) <= 1 {
		return
	}
	// Largest sub-community inherits the original label.
	var largest string
	var maxSz int
	for sc, sz := range subSizes {
		if sz > maxSz {
			maxSz = sz
			largest = sc
		}
	}
	commSize[comm] = 0
	for _, m := range members {
		sc := subComm[m]
		newComm := m // split-off nodes get their own ID
		if sc == largest {
			newComm = comm
		}
		communityOf[m] = newComm
		commSize[newComm]++
	}
	if commSize[comm] == 0 {
		delete(commSize, comm)
	}
}

// cpmBestMoveLocal picks the best sub-community for node within a restricted member set.
// Mirrors the static local-move best-community computation but operates on the
// sub-partition produced by applySubsetRefine rather than the global partition.
func cpmBestMoveLocal(
	node string, neighbors []string,
	subComm map[string]string, memberSet map[string]bool, gamma float64,
) string {
	cur := subComm[node]
	edgesTo := make(map[string]int)
	for _, nb := range neighbors {
		if nb != node && memberSet[nb] {
			edgesTo[subComm[nb]]++
		}
	}
	eCur := float64(edgesTo[cur])
	nCur := float64(subCommSize(subComm, cur))
	best, bestGain := cur, 0.0
	for sc, e := range edgesTo {
		if sc == cur {
			continue
		}
		gain := (float64(e) - gamma*float64(subCommSize(subComm, sc))) - (eCur - gamma*(nCur-1))
		if gain > bestGain {
			bestGain = gain
			best = sc
		}
	}
	return best
}
