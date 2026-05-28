// SPDX-License-Identifier: Apache-2.0

// Package thought holds the client-side reflective surface for the
// thought graph: cluster detection, personality scalars, DeGroot
// propagation, and the periodic propagation loop. Every read goes
// through the supplied *graphclient.GraphClient via the bulk MCP tools
// added in BCN4 v2 Phase 1 (thoughts(adjacency), thoughts(charges_for),
// query(ids), mutate(bulk_update_metadata)). NO Store-shaped wrapper —
// callers pass the GraphClient directly.
package thought

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
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
}

// DetectThoughtClusters detects communities over the thought subgraph
// using CPM-inspired Leiden. One gc.Call to thoughts(adjacency,
// scope:"all") drives the input materialization; Leiden runs locally.
// Persistence happens via a single mutate(bulk_update_metadata) emitted
// by buildClusterObjects.
//
// gamma controls boundary sharpness; higher = smaller tighter clusters.
func DetectThoughtClusters(ctx context.Context, gc *graphclient.GraphClient, gamma float64) ([]ThoughtCluster, error) {
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
func DetectAllClusters(ctx context.Context, gc *graphclient.GraphClient, gamma float64) ([]ThoughtCluster, error) {
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

// groupAndBuildClusters groups Leiden output into ThoughtCluster values
// then sorts by size descending. Aggregates and persistence run inside
// buildClusterObjects (which issues the locked single bulk_update_metadata
// write).
func groupAndBuildClusters(ctx context.Context, gc *graphclient.GraphClient, nodeIDs []string, clusterOf map[string]string) []ThoughtCluster {
	groups := make(map[string][]string)
	for _, id := range nodeIDs {
		groups[clusterOf[id]] = append(groups[clusterOf[id]], id)
	}
	clusters := buildClusterObjects(ctx, gc, groups)
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].Size > clusters[j].Size })
	return clusters
}

// buildClusterObjects constructs ThoughtCluster values from grouped
// node IDs. Issues EXACTLY ONE gc.Call("query", {ids: allMembers}) for
// label resolution + ONE gc.Call("thoughts", {operation: "charges_for",
// thought_ids: allMembers}) for charge aggregation, then constructs all
// clusters from the two prebuilt maps. Persistence is a single
// gc.Call("mutate", {operation: "bulk_update_metadata"}).
func buildClusterObjects(ctx context.Context, gc *graphclient.GraphClient, groups map[string][]string) []ThoughtCluster {
	// Flatten all members for the bulk fetches.
	var allMembers []string
	for _, members := range groups {
		allMembers = append(allMembers, members...)
	}
	nodeByID := fetchNodesByIDs(ctx, gc, allMembers)
	chargesByID := fetchChargesFor(ctx, gc, allMembers)

	clusters := make([]ThoughtCluster, 0, len(groups))
	assignments := make(map[string]string, len(allMembers))
	clusterIdx := 0
	for _, members := range groups {
		clusterIdx++
		cluster := ThoughtCluster{
			ID:         fmt.Sprintf("cluster-%d", clusterIdx),
			ThoughtIDs: members,
			Size:       len(members),
		}
		cluster.AvgValence, cluster.AvgMagnitude, cluster.Label = computeClusterAggregatesFromMaps(members, nodeByID, chargesByID)
		if strings.TrimSpace(cluster.Label) == "" {
			cluster.Label = cluster.ID
		}
		for _, mid := range members {
			assignments[mid] = cluster.ID
		}
		clusters = append(clusters, cluster)
	}
	persistClusterAssignments(ctx, gc, assignments)
	return clusters
}

// computeClusterAggregatesFromMaps computes per-cluster averages + a
// label from prebuilt node+charge maps. Pure read; never issues a wire
// call.
func computeClusterAggregatesFromMaps(members []string, nodeByID map[string]*knowledgev1.Node, chargesByID map[string][]*knowledgev1.Node) (avgValence, avgMagnitude float64, label string) {
	var totalValence, totalMagnitude float64
	var bestMag float64
	for _, mid := range members {
		props := computePropertiesFromCharges(chargesByID[mid])
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

// persistClusterAssignments issues one bulk_update_metadata write through the
// Execute carrier seam (executeViaEngine → MUTATION_KIND_UPDATE_ITEMS) to write
// cluster_id metadata into every member node. Failures are logged-and-dropped —
// the reflective surface is best-effort and never blocks the caller.
func persistClusterAssignments(ctx context.Context, gc *graphclient.GraphClient, assignments map[string]string) {
	if len(assignments) == 0 || gc == nil {
		return
	}
	updates := make([]map[string]any, 0, len(assignments))
	for mid, cid := range assignments {
		updates = append(updates, map[string]any{
			"id":       mid,
			"metadata": map[string]string{"cluster_id": cid},
		})
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
	// (compileMutateBulkMetadata) and rides the Execute carrier seam.
	if _, err := executeViaEngine(ctx, gc, "mutate", args); err != nil {
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
