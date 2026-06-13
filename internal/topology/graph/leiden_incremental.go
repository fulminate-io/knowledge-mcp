// SPDX-License-Identifier: Apache-2.0

package graph

// leiden_incremental.go — Dynamic Frontier (Sahu 2405.11658) incremental
// updates layered on top of the static Leiden partition.
//
// The Dynamic Frontier strategy avoids re-running the full Leiden pass when
// only a small fraction of edges change between two graph snapshots. Instead,
// we BFS-walk outward from the affected vertices, run a local-move pass on
// the frontier, and then refine only the communities that received vertex
// movement. This file holds:
//
//   - NewLeidenState     — full Leiden pass + cached partition
//   - UpdateIncremental  — DF entry point
//   - dfLocalMove        — BFS frontier walker (the "DF" in DF-Leiden)
//   - dfRefine           — restricted refinement on moved communities
//   - bestMove           — CPM-best community for a single node
//   - renumber           — in-place stable relabel after the partition shifts
//
// The static-path counterparts (localMovePhase, subsetRefinePhase,
// applySubsetRefine, cpmBestMoveLocal) live in leiden_static.go and are
// reused via applySubsetRefine when refining moved communities.

// NewLeidenState runs a full Leiden pass and caches the result.
// The returned state is ready to receive UpdateIncremental calls when the
// underlying graph changes.
func NewLeidenState(nodeIDs []string, adj map[string][]string, gamma float64) *LeidenState {
	cfg := defaultLeidenConfig()
	cfg.Gamma = gamma
	s := &LeidenState{cfg: cfg}
	s.CommunityOf = runLeidenFull(nodeIDs, adj, cfg)
	s.CommSize = buildCommSize(s.CommunityOf)
	s.CommWeightIn = buildCommWeightIn(s.CommunityOf, adj)
	return s
}

// RehydrateLeidenState reconstructs a LeidenState from an already-computed
// partition WITHOUT re-running Leiden. Sibling of NewLeidenState minus the
// runLeidenFull call: it IMPORTS the supplied communityOf verbatim (e.g. the
// persisted cluster_id partition read back on cold start) and rebuilds the
// auxiliary CommSize/CommWeightIn maps PURELY from (communityOf, adj) via the
// same buildCommSize/buildCommWeightIn helpers NewLeidenState uses.
//
// Community labels are arbitrary strings — only the partition (the equivalence
// classes of communityOf) is load-bearing. Because buildCommSize and
// buildCommWeightIn are deterministic pure functions of (communityOf, adj), the
// rehydrated aux maps are identical to what an in-memory NewLeidenState held for
// the same partition, so subsequent UpdateIncremental calls behave identically.
func RehydrateLeidenState(communityOf map[string]string, adj map[string][]string, gamma float64) *LeidenState {
	cfg := defaultLeidenConfig()
	cfg.Gamma = gamma
	return &LeidenState{
		cfg:          cfg,
		CommunityOf:  communityOf,
		CommSize:     buildCommSize(communityOf),
		CommWeightIn: buildCommWeightIn(communityOf, adj),
	}
}

// UpdateIncremental re-clusters only the nodes affected by changedEdges (DF strategy).
// Returns the post-update partition. Pass an empty slice to get the current
// partition without doing any work — used by callers that want a snapshot
// after a no-op refresh.
func (ls *LeidenState) UpdateIncremental(changedEdges []EdgeChange, adj map[string][]string) map[string]string {
	if len(changedEdges) == 0 {
		return ls.CommunityOf
	}
	affected := make(map[string]bool, len(changedEdges)*2)
	for _, e := range changedEdges {
		if _, ok := ls.CommunityOf[e.From]; ok {
			affected[e.From] = true
		}
		if _, ok := ls.CommunityOf[e.To]; ok {
			affected[e.To] = true
		}
	}
	movedComms := ls.dfLocalMove(affected, adj)
	if len(movedComms) > 0 {
		ls.dfRefine(movedComms, adj)
	}
	ls.renumber()
	ls.CommSize = buildCommSize(ls.CommunityOf)
	ls.CommWeightIn = buildCommWeightIn(ls.CommunityOf, adj)
	return ls.CommunityOf
}

// dfLocalMove runs BFS local-moving from the affected seed set.
// Returns the set of communities that gained or lost members during the walk;
// the caller passes this into dfRefine to restrict refinement scope.
func (ls *LeidenState) dfLocalMove(affected map[string]bool, adj map[string][]string) map[string]bool {
	movedComms := make(map[string]bool)
	queue := make([]string, 0, len(affected))
	inQ := make(map[string]bool, len(affected))
	for n := range affected {
		queue = append(queue, n)
		inQ[n] = true
	}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		delete(inQ, node)
		oldComm := ls.CommunityOf[node]
		best := ls.bestMove(node, adj[node])
		if best == oldComm {
			continue
		}
		ls.CommSize[oldComm]--
		if ls.CommSize[oldComm] == 0 {
			delete(ls.CommSize, oldComm)
		}
		ls.CommSize[best]++
		for _, nb := range adj[node] {
			if nb != node {
				if ls.CommunityOf[nb] == oldComm {
					ls.CommWeightIn[oldComm]--
				}
				if ls.CommunityOf[nb] == best {
					ls.CommWeightIn[best]++
				}
			}
		}
		ls.CommunityOf[node] = best
		movedComms[oldComm] = true
		movedComms[best] = true
		for _, nb := range adj[node] {
			if !inQ[nb] {
				queue = append(queue, nb)
				inQ[nb] = true
			}
		}
	}
	return movedComms
}

// dfRefine runs Subset Refine on the communities that received vertex movement.
// Reuses the static-path applySubsetRefine helper so the refinement logic stays
// in one place.
func (ls *LeidenState) dfRefine(movedComms map[string]bool, adj map[string][]string) {
	groups := make(map[string][]string)
	for node, comm := range ls.CommunityOf {
		if movedComms[comm] {
			groups[comm] = append(groups[comm], node)
		}
	}
	for comm, members := range groups {
		if len(members) > 1 {
			applySubsetRefine(comm, members, adj, ls.CommunityOf, ls.CommSize, ls.cfg)
		}
	}
}

// bestMove returns the community that maximizes CPM gain for node (or current if no gain).
// Mirrors the static local-move computation but is a method on LeidenState so it
// can read the cached CommunityOf / CommSize without re-passing them.
func (ls *LeidenState) bestMove(node string, neighbors []string) string {
	cur := ls.CommunityOf[node]
	edgesTo := make(map[string]float64)
	for _, nb := range neighbors {
		if nb != node {
			edgesTo[ls.CommunityOf[nb]]++
		}
	}
	eCur := edgesTo[cur]
	nCur := float64(ls.CommSize[cur])
	best, bestGain := cur, 0.0
	for c, eC := range edgesTo {
		if c == cur {
			continue
		}
		gain := (eC - ls.cfg.Gamma*float64(ls.CommSize[c])) - (eCur - ls.cfg.Gamma*(nCur-1))
		if gain > bestGain {
			bestGain = gain
			best = c
		}
	}
	return best
}

// renumber re-labels each community with its lexicographically smallest member
// nodeID (minMemberLabel) in-place. Called after every UpdateIncremental so
// callers see deterministic community labels regardless of internal walk order.
// Shares the canonical min-member scheme with the full path (renumberIntToMap):
// because the label depends only on each community's member SET, a partition
// produced incrementally here gets the SAME labels a full pass would assign —
// no full-vs-incremental divergence, and (labels being nodeID strings, never
// re-sorted as decimal) no lexicographic >10-community relabel churn.
func (ls *LeidenState) renumber() {
	membersByComm := make(map[string][]string, len(ls.CommunityOf))
	for n, c := range ls.CommunityOf {
		membersByComm[c] = append(membersByComm[c], n)
	}
	oldToNew := make(map[string]string, len(membersByComm))
	for c, members := range membersByComm {
		oldToNew[c] = minMemberLabel(members)
	}
	for n, old := range ls.CommunityOf {
		ls.CommunityOf[n] = oldToNew[old]
	}
}
