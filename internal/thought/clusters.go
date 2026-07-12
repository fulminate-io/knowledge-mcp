// SPDX-License-Identifier: Apache-2.0

// Package thought holds the client-side reflective surface for the
// thought graph: cluster detection, personality scalars, DeGroot
// propagation, and the periodic propagation loop. Every read goes
// through the supplied *graphclient.GraphClient via the bulk MCP tools
// added for the bulk-read path (thoughts(adjacency), thoughts(charges_for),
// query(ids), mutate(bulk_update_metadata)). NO Store-shaped wrapper —
// callers pass the GraphClient directly.
package thought

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// ThoughtCluster represents an emergent subject — a group of densely
// connected thoughts (or, for DetectAllClusters, any non-Proxy node).
type ThoughtCluster struct {
	ID           string   // cluster identifier
	ThoughtIDs   []string // thoughts in this cluster
	Label        string   // auto-generated from top thoughts
	Size         int
	AvgValence   float64
	AvgMagnitude float64

	// Centroid and MedoidID on THIS struct are computed-and-held at LEVER time
	// (Phase 4) by ComputeClusterCentroids over the drained member-vector index — they
	// are NOT populated on the ThoughtCluster values the hourly cluster-detection pass
	// returns, and are never persisted on the struct itself (topic-doc persistence is
	// the lever's job). (The hourly leaf-attachment fallback also calls
	// ComputeClusterCentroids, but only over transient internal cluster shells to gate
	// attachment — it never writes these fields back onto a returned cluster.) Centroid
	// is the bit-majority 256-bit centroid (nil when no member vectors); MedoidID is
	// the member node ID whose vector is bit-closest to the centroid (empty when no
	// vectors).
	Centroid []byte // bit-majority binary centroid over member vectors
	MedoidID string // member node ID bit-closest to the centroid
}

// DetectThoughtClusters detects communities over the thought subgraph
// using CPM-inspired Leiden. One gc.Call to thoughts(adjacency,
// scope:"all") drives the input materialization; Leiden runs locally.
// Persistence happens via a single mutate(bulk_update_metadata) emitted
// by buildClusterObjects.
//
// This standalone helper is PURE Leiden membership — it does NOT apply the
// post-Leiden leaf-attachment fallback. That fallback lives on the hourly
// runClusterDetection path (loop_detection.go), which has a wired member-vector
// scanner; this helper has no scanner and returns Leiden's partition as-is.
//
// gamma controls boundary sharpness; higher = smaller tighter clusters.
func DetectThoughtClusters(ctx context.Context, gc Caller, gamma float64) ([]ThoughtCluster, error) {
	if gamma <= 0 {
		gamma = 0.5
	}
	nodeIDs, adj, err := fetchAdjacency(ctx, gc, "all", nil)
	if err != nil {
		return nil, fmt.Errorf("thought: detect clusters: %w", err)
	}
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	clusterOf := runLeidenLocal(nodeIDs, adj, gamma)
	return groupAndBuildClusters(ctx, gc, nodeIDs, clusterOf), nil
}

// DetectAllClusters detects communities over EVERY node type (except
// NodeProxy) — same single-adjacency-RPC + single-bulk-metadata-write
// shape as DetectThoughtClusters. No query+topology fallback (T2-2
// lock).
func DetectAllClusters(ctx context.Context, gc Caller, gamma float64) ([]ThoughtCluster, error) {
	if gamma <= 0 {
		gamma = 0.5
	}
	nodeIDs, adj, err := fetchAdjacency(ctx, gc, "all_types", nil)
	if err != nil {
		return nil, fmt.Errorf("thought: detect all clusters: %w", err)
	}
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	clusterOf := runLeidenLocal(nodeIDs, adj, gamma)
	return groupAndBuildClusters(ctx, gc, nodeIDs, clusterOf), nil
}

// ErrClustersNotComputed is the cold-case sentinel DetectPersistedClusters
// returns when the corpus is NON-EMPTY but no node carries cluster_id yet — i.e.
// the hourly propagation loop has not completed a pass, so persisted cluster
// state does not exist. The live handlers branch on this (errors.Is) to render an
// explicit "reflection has not completed a pass yet" message instead of a silent
// empty report (which would look like a healthy empty graph). A TRULY empty graph
// (zero nodes drained) returns (nil, nil) — the ordinary empty case — so the two
// are distinguishable with no extra RPC.
var ErrClustersNotComputed = errors.New("thought: persisted cluster state not computed yet (no node carries cluster_id)")

// DetectPersistedClusters reconstructs the cluster set from the cluster_id
// metadata the hourly propagation loop persists (buildClusterObjects →
// persistClusterAssignments) — the READ-ONLY live counterpart to
// DetectThoughtClusters. It does NO adjacency fetch, NO Leiden, and NO persist
// (re-persisting on a read path would be a write on a read): the loop owns
// writes, this reader only reflects what the loop last wrote. The live
// personality/clusters surfaces use this so they return within the tool ceiling
// instead of recomputing the full adjacency + Leiden pass live.
//
// RPCs: the paged thought drain (~13 bounded pages) + one bulk fetchChargesFor +
// the node payloads already in hand from the drain — all bounded, well within
// the 180s ceiling. Cold case (N>0 nodes drained, none carry cluster_id) →
// (nil, ErrClustersNotComputed) so the caller renders it loudly; truly empty
// graph (zero nodes) → (nil, nil).
func DetectPersistedClusters(ctx context.Context, gc Caller) ([]ThoughtCluster, error) {
	nodes, err := fetchAllThoughtNodes(ctx, gc)
	if err != nil {
		return nil, fmt.Errorf("thought: detect persisted clusters: %w", err)
	}
	groups := make(map[string][]string)
	nodeByID := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		nodeByID[n.Id] = n
		cid := kgtypes.Value(n, "cluster_id")
		if cid == "" {
			continue // unassigned — not yet reflected by the loop.
		}
		groups[cid] = append(groups[cid], n.Id)
	}
	if len(groups) == 0 {
		if len(nodes) == 0 {
			return nil, nil // truly empty graph — ordinary empty case.
		}
		return nil, ErrClustersNotComputed // cold case: loop hasn't run yet.
	}

	var allMembers []string
	for _, members := range groups {
		allMembers = append(allMembers, members...)
	}
	chargesByID := fetchChargesFor(ctx, gc, allMembers)

	now := time.Now()
	clusters := make([]ThoughtCluster, 0, len(groups))
	for cid, members := range groups {
		cluster := ThoughtCluster{ID: cid, ThoughtIDs: members, Size: len(members)}
		cluster.AvgValence, cluster.AvgMagnitude, cluster.Label = computeClusterAggregatesFromMaps(members, nodeByID, chargesByID, now)
		if strings.TrimSpace(cluster.Label) == "" {
			cluster.Label = cluster.ID
		}
		clusters = append(clusters, cluster)
	}
	sort.Slice(clusters, func(i, j int) bool {
		// Size desc with an ID tie-break so equal-size clusters order deterministically
		// (the groups map is iterated in random order upstream).
		if clusters[i].Size != clusters[j].Size {
			return clusters[i].Size > clusters[j].Size
		}
		return clusters[i].ID < clusters[j].ID
	})
	return clusters, nil
}

// partitionFromPersisted reads the persisted cluster_id metadata back into a lean
// node→cluster partition map (tid → cluster_id) for every node that carries a
// non-empty cluster_id. It is the charge-free, partition-only core of the
// cold-start Leiden rehydration path (graph.RehydrateLeidenState): it drains the
// thought corpus once via fetchAllThoughtNodes and issues NO fetchChargesFor and
// NO Leiden — rehydration needs only the equivalence classes, not labeled
// ThoughtCluster aggregates.
//
// Returns an empty (non-nil) map when no node carries cluster_id, so the caller
// distinguishes a true first run (nothing to rehydrate → full pass) from a
// rehydratable corpus. A standalone reader rather than a refactor of
// DetectPersistedClusters: that function groups into the INVERSE shape
// (cluster_id → members) alongside a nodeByID map for its aggregates, so the
// shared reuse is the fetchAllThoughtNodes drain + the kgtypes.Value accessor,
// not the grouping loop.
func partitionFromPersisted(ctx context.Context, gc Caller) (map[string]string, error) {
	nodes, err := fetchAllThoughtNodes(ctx, gc)
	if err != nil {
		return nil, fmt.Errorf("thought: partition from persisted: %w", err)
	}
	communityOf := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if cid := kgtypes.Value(n, "cluster_id"); cid != "" {
			communityOf[n.Id] = cid
		}
	}
	return communityOf, nil
}

// groupAndBuildClusters groups Leiden output into ThoughtCluster values
// then sorts by size descending. Aggregates and persistence run inside
// buildClusterObjects (which issues the locked single bulk_update_metadata
// write).
func groupAndBuildClusters(ctx context.Context, gc Caller, nodeIDs []string, clusterOf map[string]string) []ThoughtCluster {
	groups := make(map[string][]string)
	for _, id := range nodeIDs {
		groups[clusterOf[id]] = append(groups[clusterOf[id]], id)
	}
	clusters := buildClusterObjects(ctx, gc, groups)
	sort.Slice(clusters, func(i, j int) bool {
		// Size desc with an ID tie-break so equal-size clusters order deterministically
		// (the groups map is iterated in random order upstream).
		if clusters[i].Size != clusters[j].Size {
			return clusters[i].Size > clusters[j].Size
		}
		return clusters[i].ID < clusters[j].ID
	})
	return clusters
}

// buildClusterObjects constructs ThoughtCluster values from grouped
// node IDs. Calls fetchNodesByIDs (one bulk node hydrate over the Execute
// seam) for label resolution + fetchChargesFor (one bulk charge fetch) for
// charge aggregation, then constructs all clusters from the two prebuilt
// maps. Persistence is a single bulk_update_metadata mutate.
func buildClusterObjects(ctx context.Context, gc Caller, groups map[string][]string) []ThoughtCluster {
	// Flatten all members for the bulk fetches.
	var allMembers []string
	for _, members := range groups {
		allMembers = append(allMembers, members...)
	}
	nodeByID := fetchNodesByIDs(ctx, gc, allMembers)
	chargesByID := fetchChargesFor(ctx, gc, allMembers)

	now := time.Now()
	clusters := make([]ThoughtCluster, 0, len(groups))
	assignments := make(map[string]string, len(allMembers))
	// groupKey IS the canonical community label (the min-member-node-ID assigned by
	// renumberIntToMap/renumber via communityOf). Use it directly as cluster.ID so
	// an UNCHANGED community keeps the SAME cluster_id every tick — independent of
	// Go map-iteration order. The old positional fmt.Sprintf("cluster-%d") relabeled
	// every community per tick (randomized map order), defeating the diff writeback
	// and Case A byte-identity. This mirrors DetectPersistedClusters, which already
	// uses the persisted cluster_id as cluster.ID, so live and persisted paths agree.
	for groupKey, members := range groups {
		cluster := ThoughtCluster{
			ID:         groupKey,
			ThoughtIDs: members,
			Size:       len(members),
		}
		cluster.AvgValence, cluster.AvgMagnitude, cluster.Label = computeClusterAggregatesFromMaps(members, nodeByID, chargesByID, now)
		if strings.TrimSpace(cluster.Label) == "" {
			cluster.Label = cluster.ID
		}
		for _, mid := range members {
			assignments[mid] = cluster.ID
		}
		clusters = append(clusters, cluster)
	}
	// Diff-gate the cluster_id writeback against the members' persisted cluster_id
	// (already in hand via nodeByID) so ONLY members whose canonical label changed
	// are written — O(|changed|). Untouched members keep their persisted cluster_id.
	persistClusterAssignments(ctx, gc, assignments, clusterIDAccessor(nodeByID))
	return clusters
}

// computeClusterAggregatesFromMaps computes per-cluster averages + a
// label from prebuilt node+charge maps. Pure read; never issues a wire
// call.
func computeClusterAggregatesFromMaps(members []string, nodeByID map[string]*knowledgev1.Node, chargesByID map[string][]*knowledgev1.Node, now time.Time) (avgValence, avgMagnitude float64, label string) {
	var totalValence, totalMagnitude float64
	var bestMag float64
	for _, mid := range members {
		props := computePropertiesFromCharges(chargesByID[mid], now)
		totalValence += props.Valence
		totalMagnitude += props.Magnitude
		if props.Magnitude > bestMag {
			bestMag = props.Magnitude
			if n, ok := nodeByID[mid]; ok {
				label = n.SymbolName
			}
		}
	}
	if len(members) > 0 {
		avgValence = totalValence / float64(len(members))
		avgMagnitude = totalMagnitude / float64(len(members))
	}
	return avgValence, avgMagnitude, label
}

// clusterIDAccessor builds the diffMetadataUpdates current-value accessor over the
// already-fetched member node map: it reads each member's persisted cluster_id via
// kgtypes.Value — no extra wire read. Returns nil when nodeByID is nil so the diff
// treats it as the cold case (keep every row), preserving first-pass behavior.
func clusterIDAccessor(nodeByID map[string]*knowledgev1.Node) func(id, key string) string {
	if nodeByID == nil {
		return nil
	}
	return func(id, key string) string {
		if n, ok := nodeByID[id]; ok {
			return kgtypes.Value(n, key)
		}
		return ""
	}
}

// persistClusterAssignments issues one bulk_update_metadata write through the
// Execute carrier seam (executeViaEngine → MUTATION_KIND_UPDATE_ITEMS) to write
// cluster_id metadata into member nodes. The desired rows pass through
// diffMetadataUpdates with the supplied current accessor, so ONLY members whose
// cluster_id CHANGED are written (O(|changed|)); a nil accessor (cold case) writes
// every row. Failures are logged-and-dropped — the reflective surface is
// best-effort and never blocks the caller.
func persistClusterAssignments(ctx context.Context, gc Caller, assignments map[string]string, current func(id, key string) string) {
	if len(assignments) == 0 || gc == nil {
		return
	}
	desired := make([]map[string]any, 0, len(assignments))
	for mid, cid := range assignments {
		desired = append(desired, map[string]any{
			"id":       mid,
			"metadata": map[string]string{"cluster_id": cid},
		})
	}
	updates := diffMetadataUpdates(desired, current)
	if len(updates) == 0 {
		return // nothing changed — no writeback (O(|changed|)=0).
	}
	args, err := json.Marshal(map[string]any{
		"operation": "bulk_update_metadata",
		"updates":   updates,
	})
	if err != nil {
		slog.Warn("thought: persistClusterAssignments: marshal failed", "err", err)
		return
	}
	// bulk_update_metadata lowers to MUTATION_KIND_UPDATE_ITEMS via the engine
	// (compileMutateBulkMetadata) and rides the Execute carrier seam. Use the
	// reflect-inert variant: this cluster_id writeback is the reflection pass's
	// OWN write and must NOT advance the reflect dirty-gen (T1-1 self-trigger fix).
	if err := executeReflectInertMutate(ctx, gc, args); err != nil {
		slog.Warn("thought: persistClusterAssignments: execute failed", "err", err)
	}
}

// ComputeClusterBoundaryFromMembers computes the internal-edge fraction
// for each member of a cluster. Pure local computation over a prebuilt
// adjacency map; caller passes the full adjacency (e.g. from a prior
// thoughts(adjacency) call). Members not in adj are treated as isolated.
func ComputeClusterBoundaryFromMembers(members []string, adj map[string][]string) map[string]float64 {
	if len(members) == 0 {
		return nil
	}
	memberSet := make(map[string]bool, len(members))
	for _, id := range members {
		memberSet[id] = true
	}
	result := make(map[string]float64, len(members))
	for _, id := range members {
		neighbors := adj[id]
		if len(neighbors) == 0 {
			result[id] = 1.0 // isolated within cluster
			continue
		}
		internal := 0
		for _, nid := range neighbors {
			if memberSet[nid] {
				internal++
			}
		}
		result[id] = float64(internal) / float64(len(neighbors))
	}
	return result
}
