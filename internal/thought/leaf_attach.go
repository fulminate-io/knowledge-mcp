// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"maps"
	"sort"
)

// leaf_attach.go holds the post-Leiden leaf-attachment fallback: a deterministic,
// non-recursive pass that lets a Leiden SINGLETON thought with >=1 adjacency edge
// to a clustered thought join the best edge-reachable cluster when its vector is
// similar enough to that cluster's bit-majority centroid. It is a
// clustering-correctness fallback (CPM at gamma 0.5 gives a single-edge leaf zero
// join-gain, so one edge never buys membership), NOT an operator lever. The pass
// runs inside runClusterDetection (loop_detection.go) on every detection path.

// leafAttachGateLinked is the centroid-similarity gate for a candidate leaf
// reachable to a target cluster via a REAL deliberate edge (a typed reasoning
// edge, or a machine-provenance tree-link / densify / topic-similarity edge). The
// explicit edge is strong deliberate signal, so the gate only vetoes actively
// dissimilar mislinks — hence a LOW bar.
//
// leafAttachGateSession is the higher gate for a candidate reachable to a target
// cluster ONLY via session-sibling expansion (incidental co-occurrence in a
// session, no backing edge). Incidental co-occurrence needs stronger vector
// agreement to justify membership, so the bar is higher.
//
// When both provenances reach the SAME target cluster the lower gate wins (a real
// edge to a cluster is never weakened by also being a session sibling of it).
// Both are shake-out tunables like every threshold (mirror topic_create.go:18).
const (
	leafAttachGateLinked  = 0.60
	leafAttachGateSession = 0.80
)

// leafAttachStats tallies one attachLeaves pass for the loud detection log line.
// byProvenance keys on the WINNING provenance of each attach
// (real-link/tree-link/densify/topic-similarity/session-sibling) so the
// per-provenance effect (esp. the session-edged blast radius) is observable.
type leafAttachStats struct {
	candidates        int
	attached          int
	gateVetoed        int
	vectorlessSkipped int
	byProvenance      map[string]int
}

// provenanceSessionSibling is the elimination tag for a neighbor present in adj
// but with no backing edge in the bulk edge read (deriveSessionSiblings appends
// session adjacency with no stored edge). It is the ONLY provenance that gates at
// the higher session tier; every real category (real-link/tree-link/densify/
// topic-similarity) gates at the linked tier.
const provenanceSessionSibling = "session-sibling"

// attachLeaves runs ONE non-recursive post-Leiden attachment pass, mutating
// communityOf and commSize IN PLACE: each candidate singleton leaf that passes its
// per-target effective gate joins the best centroid-similar reachable cluster.
//
// communityOf is node→community; commSize is community→member-count (both the
// Leiden partition maps, mutated so the downstream groups build + persistence and
// the next tick's incremental baseline stay consistent). adj is the flat
// all-provenance bidirectional adjacency Leiden ingested. vectorIndex is the
// drained nodeID→256-bit vector index. leafProvenance is leaf→{neighbor→provenance}
// recovered by buildLeafProvenance (a neighbor absent from this map but present in
// adj is a session-sibling by elimination).
//
// SINGLE-PASS / NON-RECURSIVE INVARIANT: candidates and the target centroids are
// snapshotted BEFORE any mutation, so a leaf attached this pass can neither become
// a candidate nor a target this pass (a chain of leaves attaches one layer per
// pass). The next detection tick picks up the next layer.
func attachLeaves(
	communityOf map[string]string,
	commSize map[string]int,
	adj map[string][]string,
	vectorIndex map[string][]byte,
	leafProvenance map[string]map[string]string,
) leafAttachStats {
	stats := leafAttachStats{byProvenance: map[string]int{}}

	// Snapshot the candidate leaves BEFORE any mutation (single-non-recursive-pass
	// invariant): a node is a candidate leaf iff its Leiden community is a singleton
	// (communityOf[id]==id AND commSize[id]==1; mirrors seedNewNodes' singleton
	// shape). Sorted for deterministic iteration and tie-breaks.
	var candidates []string
	for id, comm := range communityOf {
		if comm == id && commSize[id] == 1 {
			candidates = append(candidates, id)
		}
	}
	sort.Strings(candidates)
	stats.candidates = len(candidates)
	if len(candidates) == 0 {
		return stats
	}

	// Per-cluster bit-majority centroids, snapshotted BEFORE attachment: a leaf
	// attaching to cluster X this pass does NOT re-center X this pass — deliberate,
	// matching the non-recursive invariant (prevents a future "fix" into recursive
	// re-centering). Built over the non-singleton communities only (singletons are
	// candidates/leaves, never targets).
	centroidByCluster := buildTargetCentroids(communityOf, commSize, vectorIndex)

	// FROZEN PRE-PASS PARTITION SNAPSHOT: neighbor→target resolution reads these,
	// NEVER the live communityOf/commSize that the loop mutates. Without this, sorted
	// iteration would let an earlier-attached leaf become a non-singleton target for a
	// later candidate this same pass (a chain B→C would collapse both layers at once),
	// violating the single-layer-per-pass invariant. The live maps are mutated only
	// for the downstream groups build + persistence.
	snapCommunityOf := make(map[string]string, len(communityOf))
	maps.Copy(snapCommunityOf, communityOf)
	snapCommSize := make(map[string]int, len(commSize))
	maps.Copy(snapCommSize, commSize)

	for _, leaf := range candidates {
		attachOneLeaf(leaf, communityOf, commSize, snapCommunityOf, snapCommSize, adj, vectorIndex, leafProvenance, centroidByCluster, &stats)
	}
	return stats
}

// buildTargetCentroids constructs minimal ThoughtCluster shells for every
// NON-singleton community and reuses ComputeClusterCentroids (centroid.go:69) to
// set each shell's bit-majority centroid over its member vectors — no
// re-implementation of BitMajorityCentroid. Returns community→centroid for the
// populated shells; a cluster with no member vectors has a nil centroid (and so
// self-vetoes, since BitSimilarity against nil returns 0 < either gate).
func buildTargetCentroids(communityOf map[string]string, commSize map[string]int, vectorIndex map[string][]byte) map[string][]byte {
	membersByCluster := make(map[string][]string)
	for id, comm := range communityOf {
		if commSize[comm] > 1 {
			membersByCluster[comm] = append(membersByCluster[comm], id)
		}
	}
	shells := make([]ThoughtCluster, 0, len(membersByCluster))
	for comm, members := range membersByCluster {
		shells = append(shells, ThoughtCluster{ID: comm, ThoughtIDs: members})
	}
	ComputeClusterCentroids(shells, vectorIndex)
	centroidByCluster := make(map[string][]byte, len(shells))
	for i := range shells {
		if shells[i].Centroid != nil {
			centroidByCluster[shells[i].ID] = shells[i].Centroid
		}
	}
	return centroidByCluster
}

// attachOneLeaf evaluates a single candidate leaf against its edge-reachable
// non-singleton target clusters and, when the best target passes its per-target
// effective gate, attaches the leaf (mutating the LIVE communityOf + commSize in
// place) and records the winning provenance. Neighbor→target resolution reads the
// FROZEN snapCommunityOf/snapCommSize so a leaf attached earlier this pass cannot
// become a target for a later one (single-layer-per-pass). A vector-less leaf is
// SKIPPED (never attached ungated). A leaf that fails every gate stays a singleton
// (gateVetoed++).
func attachOneLeaf(
	leaf string,
	communityOf map[string]string,
	commSize map[string]int,
	snapCommunityOf map[string]string,
	snapCommSize map[string]int,
	adj map[string][]string,
	vectorIndex map[string][]byte,
	leafProvenance map[string]map[string]string,
	centroidByCluster map[string][]byte,
	stats *leafAttachStats,
) {
	leafVec, ok := vectorIndex[leaf]
	if !ok || len(leafVec) != vectorBytes {
		stats.vectorlessSkipped++ // cannot gate → never attach (settled design point 6).
		return
	}

	// Per reachable non-singleton target cluster, track the BEST (lowest-gate-wins)
	// provenance: real provenance preferred over session-sibling. provByCluster maps
	// target→winning provenance string; a real provenance there means the linked
	// (0.60) tier applies. Target membership is read from the FROZEN snapshot.
	provByCluster := map[string]string{}
	for _, nb := range adj[leaf] {
		comm := snapCommunityOf[nb]
		if snapCommSize[comm] <= 1 {
			continue // neighbor was a singleton at snapshot time — not a target cluster.
		}
		prov := provenanceSessionSibling
		if p, ok := leafProvenance[leaf][nb]; ok {
			prov = p // a backing edge → real provenance; absence → session-sibling by elimination.
		}
		if existing, seen := provByCluster[comm]; !seen || provenanceIsReal(prov) && !provenanceIsReal(existing) {
			// First sighting, or upgrade a session-only target to real (lower gate wins).
			provByCluster[comm] = prov
		}
	}
	if len(provByCluster) == 0 {
		stats.gateVetoed++ // reachable to no non-singleton cluster → nothing to join.
		return
	}

	// Sort reachable targets ascending so the max-sim selection's tie-break lands on
	// the lowest cluster id deterministically.
	targets := make([]string, 0, len(provByCluster))
	for comm := range provByCluster {
		targets = append(targets, comm)
	}
	sort.Strings(targets)

	bestTarget := ""
	bestSim := -1.0
	bestProv := ""
	for _, target := range targets {
		centroid, ok := centroidByCluster[target]
		if !ok {
			continue // nil-centroid cluster (no member vectors) — cannot gate.
		}
		sim := BitSimilarity(leafVec, centroid)
		prov := provByCluster[target]
		gate := effectiveGate(prov)
		if sim < gate {
			continue // below this target's effective gate.
		}
		if sim > bestSim { // strict > keeps the first (lowest-id) target on a tie.
			bestSim = sim
			bestTarget = target
			bestProv = prov
		}
	}

	if bestTarget == "" {
		stats.gateVetoed++ // no target passed its gate.
		return
	}

	// Attach: reassign the leaf's community and update commSize so the old singleton
	// community empties and the target grows. Because targets were snapshotted by the
	// commSize>1 check above, the leaf's NEW community was already non-singleton, so
	// no recursion is introduced this pass.
	communityOf[leaf] = bestTarget
	commSize[leaf] = 0
	commSize[bestTarget]++
	stats.attached++
	stats.byProvenance[bestProv]++
}

// provenanceIsReal reports whether a provenance tag is a real deliberate edge (any
// category other than the session-sibling elimination tag). Real provenance gates
// at the linked (0.60) tier; session-sibling at the session (0.80) tier.
func provenanceIsReal(prov string) bool {
	return prov != provenanceSessionSibling
}

// effectiveGate returns the per-target centroid-similarity gate for a provenance:
// the linked (0.60) tier for any real deliberate edge, the session (0.80) tier for
// session-sibling-only reachability.
func effectiveGate(prov string) float64 {
	if provenanceIsReal(prov) {
		return leafAttachGateLinked
	}
	return leafAttachGateSession
}

// buildLeafProvenance recovers per-edge provenance for every edge incident to a
// candidate leaf in ONE bulk fetchEdgesForNodeSet read (no per-leaf round-trips),
// returning leaf→{neighbor→provenance}. Provenance is classified from the edge
// Method: tree-link / densify / topic-similarity machine edges by their stamped
// method, every other typed reasoning edge (relates-to / next / branches-from /
// informed-by / produced / because, including empty-Method bare edges) as a
// real-link. The session-sibling case is NOT represented here — a neighbor present
// in adj but ABSENT from this map is a session-sibling by elimination
// (deriveSessionSiblings appends session adjacency with no backing edge); that
// elimination is applied by attachOneLeaf against the live adjacency, so this helper
// needs no adjacency input — only the leaf id set the bulk edge read pivots on.
func buildLeafProvenance(ctx context.Context, gc Caller, leafIDs []string) map[string]map[string]string {
	out := make(map[string]map[string]string)
	if gc == nil || len(leafIDs) == 0 {
		return out
	}
	leafSet := make(map[string]bool, len(leafIDs))
	for _, id := range leafIDs {
		leafSet[id] = true
	}
	edges, err := fetchEdgesForNodeSet(ctx, gc, leafIDs, nil)
	if err != nil {
		// Degraded: no provenance recovered → every neighbor falls to the
		// session-sibling tier by elimination (the stricter gate). Detection
		// continues; the empty map is correct, not a failure.
		return out
	}
	for i := range edges {
		e := &edges[i]
		prov := classifyEdgeProvenance(e.Method)
		// Record provenance for whichever endpoint(s) are candidate leaves, keyed by
		// the OTHER endpoint (the neighbor). The flat adj is bidirectional, so an edge
		// in either direction is reachability.
		if leafSet[e.FromId] {
			recordProvenance(out, e.FromId, e.ToId, prov)
		}
		if leafSet[e.ToId] {
			recordProvenance(out, e.ToId, e.FromId, prov)
		}
	}
	return out
}

// classifyEdgeProvenance maps an edge Method to a real-link provenance category.
// Machine-stamped methods classify by identifier (never a hardcoded literal); any
// other method (including empty) is a bare typed reasoning edge → real-link.
func classifyEdgeProvenance(method string) string {
	switch method {
	case treeLinkMethod:
		return "tree-link"
	case densifyMethod:
		return "densify"
	case topicSimilarityMethod:
		return "topic-similarity"
	default:
		return "real-link"
	}
}

// recordProvenance stores leaf→neighbor→provenance. First-write wins per
// (leaf,neighbor): every classified category is the real tier, so the gate only
// distinguishes real-vs-session and the specific real category does not change it.
func recordProvenance(out map[string]map[string]string, leaf, neighbor, prov string) {
	if out[leaf] == nil {
		out[leaf] = map[string]string{}
	}
	if _, exists := out[leaf][neighbor]; !exists {
		out[leaf][neighbor] = prov
	}
}
