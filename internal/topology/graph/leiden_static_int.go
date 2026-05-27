// SPDX-License-Identifier: Apache-2.0

package graph

// leiden_static_int.go — Int-indexed fast path for runLeidenFull. The
// string-keyed helpers in leiden_static.go remain in place for the
// incremental Dynamic Frontier path (leiden_incremental.go) which still
// works off LeidenState's map[string]string partition.
//
// Why int-indexed: profiling showed localMovePhase spent ~60% of its
// runtime in string-keyed map operations — `edgesTo[communityOf[nb]]++`
// is two map lookups + a hash per neighbor, and countEdgesToComms
// allocated a fresh map every single call (15K nodes × ~10 iterations =
// 150K small-map allocations per Leiden run).
//
// This file reworks the static path so:
//   - nodeIDs are replaced with a contiguous int32 space [0, n)
//   - adj is materialized into [][]int32 once up front
//   - commOf / commSize are plain []int32 slices indexed by node
//   - the edgesTo scratch accumulator is a sparse [keys, vals, seen]
//     triple rather than a map: seen is length-n indexed by community
//     ID, keys holds the communities touched by the current node, vals
//     holds parallel counts. Reset is O(K) where K is unique
//     neighbor communities (typically 1-5 per node), and iteration is
//     a dense `for i := 0; i < len(keys); i++` — no hashing, no
//     map-iteration overhead.

import "sort"

// edgesToAccum is a sparse accumulator that replaces map[int32]int32
// in the Leiden hot path. The seen slice is sized to the number of
// communities (same as number of nodes) and records the position of
// community id in keys, or -1 if community id has not been touched
// during the current accumulation. keys/vals are parallel slices that
// are reset to length 0 at the start of each node's accumulation.
//
// Allocation lifecycle: one accumulator per Leiden run, reused across
// every node and every outer-loop iteration. No allocations in the
// hot path.
type edgesToAccum struct {
	keys []int32
	vals []int32
	seen []int32 // -1 = not seen, else index into keys
}

// newEdgesToAccum returns a scratch accumulator sized for n communities.
// seen is filled with -1 so the first add() of every key is a miss.
func newEdgesToAccum(n int) *edgesToAccum {
	acc := &edgesToAccum{
		keys: make([]int32, 0, 16),
		vals: make([]int32, 0, 16),
		seen: make([]int32, n),
	}
	for i := range acc.seen {
		acc.seen[i] = -1
	}
	return acc
}

// reset clears the accumulator for the next node. Only the touched
// community IDs in keys need seen[k] reset — the rest of seen is
// already -1 from the previous reset (or the initial fill).
func (a *edgesToAccum) reset() {
	for _, k := range a.keys {
		a.seen[k] = -1
	}
	a.keys = a.keys[:0]
	a.vals = a.vals[:0]
}

// add increments the count for community c. First touch appends to
// keys/vals; subsequent touches just bump vals[i]. Amortized O(1)
// both paths.
func (a *edgesToAccum) add(c int32) {
	idx := a.seen[c]
	if idx < 0 {
		a.seen[c] = int32(len(a.keys))
		a.keys = append(a.keys, c)
		a.vals = append(a.vals, 1)
		return
	}
	a.vals[idx]++
}

// get returns the count for community c, or 0 if not present.
func (a *edgesToAccum) get(c int32) int32 {
	idx := a.seen[c]
	if idx < 0 {
		return 0
	}
	return a.vals[idx]
}

// runLeidenFullInt is the int-indexed implementation of runLeidenFull.
// It takes the original (nodeIDs, adj) inputs, builds an int-indexed
// view, runs local-move + subset-refine to fixed point, and returns the
// node→community partition in the same map[string]string shape the
// string path produced (with stable commID relabeling).
func runLeidenFullInt(nodeIDs []string, adj map[string][]string, cfg leidenConfig) map[string]string {
	n := len(nodeIDs)
	if n == 0 {
		return make(map[string]string)
	}
	if n == 1 {
		return map[string]string{nodeIDs[0]: nodeIDs[0]}
	}

	adjInt, _ := buildIntAdjacency(nodeIDs, adj)

	commOf := make([]int32, n)
	commSize := make([]int32, n)
	for i := range commOf {
		commOf[i] = int32(i)
		commSize[i] = 1
	}

	// Scratch buffers reused across every localMove/subsetRefine call.
	acc := newEdgesToAccum(n)
	movedComms := make([]bool, n)

	for range cfg.MaxIterations {
		anyMoved := localMovePhaseInt(adjInt, commOf, commSize, cfg.Gamma, acc, movedComms)
		if !anyMoved {
			break
		}
		subsetRefinePhaseInt(adjInt, commOf, commSize, movedComms, cfg, acc)
		// Clear movedComms for the next iteration.
		for i := range movedComms {
			movedComms[i] = false
		}
	}

	return renumberIntToMap(nodeIDs, commOf)
}

// buildIntAdjacency materializes an int-indexed adjacency list from the
// (nodeIDs, adj) inputs. Neighbors that don't appear in nodeIDs are
// silently dropped (matches the string-keyed helpers' "filter against
// surviving set" semantics).
func buildIntAdjacency(nodeIDs []string, adj map[string][]string) ([][]int32, map[string]int32) {
	n := len(nodeIDs)
	nodeToIdx := make(map[string]int32, n)
	for i, id := range nodeIDs {
		nodeToIdx[id] = int32(i)
	}
	adjInt := make([][]int32, n)
	for i, id := range nodeIDs {
		nbs := adj[id]
		if len(nbs) == 0 {
			continue
		}
		out := make([]int32, 0, len(nbs))
		for _, nb := range nbs {
			if idx, ok := nodeToIdx[nb]; ok && idx != int32(i) {
				out = append(out, idx)
			}
		}
		adjInt[i] = out
	}
	return adjInt, nodeToIdx
}

// localMovePhaseInt is the int-indexed version of localMovePhase. It
// greedily reassigns each node to its best-gain community using the
// CPM quality function. Returns whether any movement occurred; the
// caller's movedComms slice is updated in place.
func localMovePhaseInt(
	adj [][]int32, commOf []int32, commSize []int32, gamma float64,
	acc *edgesToAccum, movedComms []bool,
) bool {
	anyMoved := false
	for node := int32(0); node < int32(len(adj)); node++ {
		cur := commOf[node]
		acc.reset()
		for _, nb := range adj[node] {
			if nb != node {
				acc.add(commOf[nb])
			}
		}
		eCur := float64(acc.get(cur))
		nCur := float64(commSize[cur])
		best, bestGain := cur, 0.0
		// Dense iteration over touched communities — no map hashing.
		for i, c := range acc.keys {
			if c == cur {
				continue
			}
			e := acc.vals[i]
			gain := (float64(e) - gamma*float64(commSize[c])) - (eCur - gamma*(nCur-1))
			if gain > bestGain {
				bestGain = gain
				best = c
			}
		}
		if best != cur {
			commSize[cur]--
			commSize[best]++
			commOf[node] = best
			movedComms[cur] = true
			movedComms[best] = true
			anyMoved = true
		}
	}
	return anyMoved
}

// subsetRefinePhaseInt runs applySubsetRefineInt on communities that had
// vertex movement during the most recent local-move pass. Matches the
// semantics of the string-keyed subsetRefinePhase exactly.
func subsetRefinePhaseInt(
	adj [][]int32, commOf []int32, commSize []int32,
	movedComms []bool, cfg leidenConfig, acc *edgesToAccum,
) {
	groups := make(map[int32][]int32)
	for i, c := range commOf {
		if movedComms[c] {
			groups[c] = append(groups[c], int32(i))
		}
	}
	for comm, members := range groups {
		if len(members) > 1 {
			applySubsetRefineInt(comm, members, adj, commOf, commSize, cfg, acc)
		}
	}
}

// applySubsetRefineInt tries to split a community via an intra-community
// local-move pass. Mirrors applySubsetRefine but uses int indices
// throughout. Uses the shared edgesToAccum for the inner best-move
// scratch buffer; the memberSet / subComm state remains map-backed
// because each refine call operates on a small subset of nodes where
// allocating a fresh map is cheap compared to carrying another sparse
// scratch buffer.
func applySubsetRefineInt(
	comm int32, members []int32, adj [][]int32,
	commOf []int32, commSize []int32, cfg leidenConfig,
	acc *edgesToAccum,
) {
	memberSet := make(map[int32]bool, len(members))
	subComm := make(map[int32]int32, len(members))
	for _, m := range members {
		memberSet[m] = true
		subComm[m] = m
	}
	changed := true
	for range cfg.MaxIterations {
		if !changed {
			break
		}
		changed = false
		for _, node := range members {
			best := cpmBestMoveLocalInt(node, adj[node], subComm, memberSet, cfg.Gamma, acc)
			if best != subComm[node] {
				subComm[node] = best
				changed = true
			}
		}
	}
	subSizes := make(map[int32]int, len(members))
	for _, sc := range subComm {
		subSizes[sc]++
	}
	if len(subSizes) <= 1 {
		return
	}
	var largest int32
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
		newComm := m
		if sc == largest {
			newComm = comm
		}
		commOf[m] = newComm
		commSize[newComm]++
	}
}

// cpmBestMoveLocalInt picks the best sub-community for node within a
// restricted member set, using int indices throughout. Mirrors
// cpmBestMoveLocal but reuses the caller-supplied sparse accumulator.
func cpmBestMoveLocalInt(
	node int32, neighbors []int32,
	subComm map[int32]int32, memberSet map[int32]bool, gamma float64,
	acc *edgesToAccum,
) int32 {
	acc.reset()
	for _, nb := range neighbors {
		if nb != node && memberSet[nb] {
			acc.add(subComm[nb])
		}
	}
	cur := subComm[node]
	eCur := float64(acc.get(cur))
	nCur := float64(subCommSizeInt(subComm, cur))
	best, bestGain := cur, 0.0
	for i, sc := range acc.keys {
		if sc == cur {
			continue
		}
		e := acc.vals[i]
		gain := (float64(e) - gamma*float64(subCommSizeInt(subComm, sc))) - (eCur - gamma*(nCur-1))
		if gain > bestGain {
			bestGain = gain
			best = sc
		}
	}
	return best
}

// subCommSizeInt is the int-indexed counterpart to subCommSize. O(n)
// over the sub-partition — acceptable because refinement only runs on
// small communities.
func subCommSizeInt(subComm map[int32]int32, sc int32) int {
	count := 0
	for _, s := range subComm {
		if s == sc {
			count++
		}
	}
	return count
}

// renumberIntToMap converts the int-indexed partition back to the
// map[string]string shape callers expect, applying the same stable
// sort-then-relabel semantics as the string-keyed renumberMap: unique
// community reps are mapped back to their original nodeIDs string,
// the resulting strings are sorted, and stableCommID(i) is assigned
// in sorted order.
func renumberIntToMap(nodeIDs []string, commOf []int32) map[string]string {
	seenOrder := make(map[int32]struct{}, 16)
	var origIDs []string
	for _, c := range commOf {
		if _, ok := seenOrder[c]; !ok {
			seenOrder[c] = struct{}{}
			origIDs = append(origIDs, nodeIDs[c])
		}
	}
	sort.Strings(origIDs)
	origToStable := make(map[string]string, len(origIDs))
	for i, old := range origIDs {
		origToStable[old] = stableCommID(i)
	}
	result := make(map[string]string, len(commOf))
	for i, c := range commOf {
		result[nodeIDs[i]] = origToStable[nodeIDs[c]]
	}
	return result
}
