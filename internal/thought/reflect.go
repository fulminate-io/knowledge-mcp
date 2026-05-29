// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"encoding/json"
	"math"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// ClusterPairScalar holds a scalar between two clusters with labels.
type ClusterPairScalar struct {
	ClusterA string
	ClusterB string
	LabelA   string
	LabelB   string
	Scalar   float64
}

// PersonalityReport summarizes the agent's personality profile.
type PersonalityReport struct {
	Profile      PersonalityProfile
	TopStubborn  []ClusterPairScalar
	TopGullible  []ClusterPairScalar
	ClusterCount int
}

// InfluenceReport shows a thought's influence on the global consensus.
type InfluenceReport struct {
	ThoughtID      string
	Node           *knowledgev1.Node
	InfluenceScore float64
	Properties     ThoughtProperties
}

// TensionReport identifies two connected thoughts with opposing valence.
type TensionReport struct {
	ThoughtA     *knowledgev1.Node
	ThoughtB     *knowledgev1.Node
	PropertiesA  ThoughtProperties
	PropertiesB  ThoughtProperties
	ValenceDelta float64
}

// BridgeThought is a thought at a cluster boundary with its internal
// edge fraction.
type BridgeThought struct {
	ThoughtID        string
	Name             string
	InternalFraction float64
	HasCharges       bool
}

// BlindSpotReport identifies clusters with little evidence.
type BlindSpotReport struct {
	Cluster        ThoughtCluster
	ChargeCount    int
	AvgMagnitude   float64
	BridgeThoughts []BridgeThought
}

// ThoughtGraphSummary provides a high-level overview.
type ThoughtGraphSummary struct {
	TotalThoughts  int
	TotalCharges   int
	TotalSessions  int
	ClusterCount   int
	AvgValence     float64
	AvgMagnitude   float64
	TopClusters    []ThoughtCluster
	RecentThoughts []*knowledgev1.Node
}

// ReflectPersonality returns the agent's personality profile with top
// stubborn/gullible pairs. PURE function — no DB, no gc.Call.
func ReflectPersonality(clusters []ThoughtCluster, profile *PersonalityProfile, clusterFilter string) PersonalityReport {
	report := PersonalityReport{ClusterCount: len(clusters)}
	if profile == nil {
		return report
	}
	report.Profile = *profile

	var pairs []ClusterPairScalar
	for cA, m := range profile.Scalars {
		if clusterFilter != "" && cA != clusterFilter {
			continue
		}
		for cB, scalar := range m {
			pairs = append(pairs, ClusterPairScalar{
				ClusterA: cA, ClusterB: cB,
				LabelA: profile.ClusterLabels[cA],
				LabelB: profile.ClusterLabels[cB],
				Scalar: scalar,
			})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Scalar < pairs[j].Scalar })
	limit := min(len(pairs), 5)
	report.TopStubborn = append([]ClusterPairScalar(nil), pairs[:limit]...)
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Scalar > pairs[j].Scalar })
	limit = min(len(pairs), 5)
	report.TopGullible = append([]ClusterPairScalar(nil), pairs[:limit]...)
	return report
}

// ReflectInfluence returns the top-N most influential thoughts. Issues
// ONE gc.Call to list thought IDs, ONE gc.Call("thoughts", {adjacency})
// inside BuildTrustMatrix, and ONE bulk hydrate for the top-N nodes.
func ReflectInfluence(ctx context.Context, gc Caller, limit int, profile *PersonalityProfile) ([]InfluenceReport, error) {
	if limit <= 0 {
		limit = 10
	}
	thoughtIDs, err := listAllThoughtIDs(ctx, gc)
	if err != nil {
		return nil, err
	}
	if len(thoughtIDs) == 0 {
		return nil, nil
	}

	// Need cluster_id metadata for personality-adjusted matrix — pull
	// the node bulk once.
	var nodeByID map[string]*knowledgev1.Node
	if profile != nil {
		nodeByID = fetchNodesByIDs(ctx, gc, thoughtIDs)
	}

	var matrix TrustMatrix
	if profile != nil {
		matrix, err = BuildTrustMatrixWithPersonality(ctx, gc, thoughtIDs, *profile, nodeByID)
	} else {
		matrix, err = BuildTrustMatrix(ctx, gc, thoughtIDs)
	}
	if err != nil {
		return nil, err
	}

	influence := ComputeInfluenceVector(matrix)
	type scoredID struct {
		id    string
		score float64
	}
	ranked := make([]scoredID, 0, len(thoughtIDs))
	for _, id := range thoughtIDs {
		ranked = append(ranked, scoredID{id: id, score: influence[id]})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	// Hydrate only the top-N — ONE bulk query call + ONE charges_for
	// for properties.
	topIDs := make([]string, len(ranked))
	for i, r := range ranked {
		topIDs[i] = r.id
	}
	topNodes := fetchNodesByIDs(ctx, gc, topIDs)
	topCharges := fetchChargesFor(ctx, gc, topIDs)

	reports := make([]InfluenceReport, 0, len(ranked))
	for _, r := range ranked {
		n, ok := topNodes[r.id]
		if !ok {
			continue
		}
		props := computePropertiesFromCharges(topCharges[r.id])
		reports = append(reports, InfluenceReport{
			ThoughtID:      r.id,
			Node:           n,
			InfluenceScore: r.score,
			Properties:     props,
		})
	}
	return reports, nil
}

// ReflectTensions finds pairs of connected thoughts with opposing
// valence. Issues ONE gc.Call("thoughts", {adjacency}) for the
// thought subgraph + ONE gc.Call("thoughts", {charges_for}) for
// property derivation (T2-3 perf lock). NO per-thought wire calls
// inside the pair loop.
func ReflectTensions(ctx context.Context, gc Caller) ([]TensionReport, error) {
	nodeIDs, adj, err := fetchAdjacency(ctx, gc, "all", nil)
	if err != nil {
		return nil, err
	}
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	nodeByID := fetchNodesByIDs(ctx, gc, nodeIDs)
	charges := fetchChargesFor(ctx, gc, nodeIDs)

	propsCache := make(map[string]ThoughtProperties, len(nodeIDs))
	for _, id := range nodeIDs {
		propsCache[id] = computePropertiesFromCharges(charges[id])
	}

	seen := make(map[string]bool)
	var tensions []TensionReport
	for _, id := range nodeIDs {
		pA := propsCache[id]
		if pA.Magnitude < 0.5 {
			continue
		}
		for _, nid := range adj[id] {
			pairKey := id + ":" + nid
			if seen[pairKey] || seen[nid+":"+id] {
				continue
			}
			seen[pairKey] = true
			pB, ok := propsCache[nid]
			if !ok || pB.Magnitude < 0.5 {
				continue
			}
			delta := math.Abs(pA.Valence - pB.Valence)
			if delta < 0.5 {
				continue
			}
			nA, okA := nodeByID[id]
			nB, okB := nodeByID[nid]
			if !okA || !okB {
				continue
			}
			tensions = append(tensions, TensionReport{
				ThoughtA:     nA,
				ThoughtB:     nB,
				PropertiesA:  pA,
				PropertiesB:  pB,
				ValenceDelta: delta,
			})
		}
	}
	sort.Slice(tensions, func(i, j int) bool { return tensions[i].ValenceDelta > tensions[j].ValenceDelta })
	return tensions, nil
}

// ReflectBlindSpots finds clusters with few charges relative to
// thought count. Issues ONE gc.Call("thoughts", {charges_for}) for
// ALL cluster members before the per-cluster loop (T2-3 perf lock).
// Bridge thoughts use the prebuilt adjacency map; per-bridge
// hydration uses fetchNodesByIDs.
func ReflectBlindSpots(ctx context.Context, gc Caller, clusters []ThoughtCluster, adj map[string][]string) []BlindSpotReport {
	// Flatten members for one bulk fetch.
	var allMembers []string
	for _, c := range clusters {
		allMembers = append(allMembers, c.ThoughtIDs...)
	}
	charges := fetchChargesFor(ctx, gc, allMembers)

	// One bulk hydrate for any bridge labels.
	nodeByID := fetchNodesByIDs(ctx, gc, allMembers)

	var spots []BlindSpotReport
	for _, c := range clusters {
		totalCharges := 0
		chargesByThought := make(map[string]int, len(c.ThoughtIDs))
		for _, tid := range c.ThoughtIDs {
			cnt := len(charges[tid])
			totalCharges += cnt
			chargesByThought[tid] = cnt
		}

		var bridges []BridgeThought
		boundary := ComputeClusterBoundaryFromMembers(c.ThoughtIDs, adj)
		for tid, fraction := range boundary {
			if fraction < 0.7 {
				name := tid
				if n, ok := nodeByID[tid]; ok {
					name = n.SymbolName
				}
				bridges = append(bridges, BridgeThought{
					ThoughtID:        tid,
					Name:             name,
					InternalFraction: fraction,
					HasCharges:       chargesByThought[tid] > 0,
				})
			}
		}
		sort.Slice(bridges, func(i, j int) bool {
			return bridges[i].InternalFraction < bridges[j].InternalFraction
		})

		if c.Size > 0 && (float64(totalCharges)/float64(c.Size) < 1.0 || len(bridges) > 0) {
			spots = append(spots, BlindSpotReport{
				Cluster:        c,
				ChargeCount:    totalCharges,
				AvgMagnitude:   c.AvgMagnitude,
				BridgeThoughts: bridges,
			})
		}
	}

	sort.Slice(spots, func(i, j int) bool { return spots[i].ChargeCount < spots[j].ChargeCount })
	return spots
}

// ReflectSummary returns a high-level overview of the thought store.
// Issues a small number of bulk wire calls — list-by-type for the
// three node-type counters, ONE bulk fetchChargesFor, ONE bulk
// fetchNodesByIDs for recent thought hydration.
func ReflectSummary(ctx context.Context, gc Caller, clusters []ThoughtCluster) ThoughtGraphSummary {
	var summary ThoughtGraphSummary
	summary.ClusterCount = len(clusters)

	// Count thoughts/charges/sessions via list-by-type via query tool.
	thoughtIDs, _ := listAllThoughtIDs(ctx, gc)
	summary.TotalThoughts = len(thoughtIDs)
	summary.TotalCharges = countByType(ctx, gc, "charge")
	summary.TotalSessions = countByType(ctx, gc, "thought_session")

	if summary.TotalThoughts > 0 {
		charges := fetchChargesFor(ctx, gc, thoughtIDs)
		var totalV, totalM float64
		for _, id := range thoughtIDs {
			props := computePropertiesFromCharges(charges[id])
			totalV += props.Valence
			totalM += props.Magnitude
		}
		summary.AvgValence = totalV / float64(summary.TotalThoughts)
		summary.AvgMagnitude = totalM / float64(summary.TotalThoughts)

		// Recent thoughts — pull all and sort by CreatedAt; ONE bulk
		// hydrate for the full set so we can sort and slice client-side.
		nodes := fetchNodesByIDs(ctx, gc, thoughtIDs)
		all := make([]*knowledgev1.Node, 0, len(nodes))
		for _, n := range nodes {
			all = append(all, n)
		}
		sort.Slice(all, func(i, j int) bool {
			return all[i].CreatedAt > all[j].CreatedAt
		})
		if len(all) > 10 {
			all = all[:10]
		}
		summary.RecentThoughts = all
	}

	if len(clusters) > 5 {
		summary.TopClusters = clusters[:5]
	} else {
		summary.TopClusters = clusters
	}
	return summary
}

// listAllThoughtIDs pulls every thought ID via the query tool's type-browse
// mode through the Execute carrier seam: a type=thought browse whose nodes
// carrier carries the full *knowledgev1.Node payloads, projected to IDs via
// engine.DecodeNodes (no text-parse of a render envelope).
func listAllThoughtIDs(ctx context.Context, gc Caller) ([]string, error) {
	if gc == nil {
		return nil, nil
	}
	nodes, err := browseNodeIDs(ctx, gc, "thought")
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

// countByType returns the count of nodes of a given type via the same Execute
// carrier browse listAllThoughtIDs uses (engine.DecodeNodes). A nil gc / error
// yields 0 (the reflective surface is best-effort).
func countByType(ctx context.Context, gc Caller, nodeType string) int {
	if gc == nil {
		return 0
	}
	ids, err := browseNodeIDs(ctx, gc, nodeType)
	if err != nil {
		return 0
	}
	return len(ids)
}

// browseNodeIDs runs a type-browse through the Execute carrier seam and projects
// the decoded *knowledgev1.Node IDs. Shared by listAllThoughtIDs + countByType.
// limit:0 means "no cap" (the engine's unbounded browse), preserving the prior
// large-page-size intent without a magic 100000.
func browseNodeIDs(ctx context.Context, gc Caller, nodeType string) ([]string, error) {
	raw, err := json.Marshal(queryArgs{Type: nodeType, Limit: 0})
	if err != nil {
		return nil, err
	}
	resp, err := executeViaEngine(ctx, gc, "query", raw)
	if err != nil {
		return nil, err
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return nil, derr
	}
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Id)
	}
	return out, nil
}

// queryArgs is the typed payload for the type-browse query call. Kept as a
// struct so json.Marshal stays errchkjson-safe (map[string]any triggers
// the unsafe-type lint).
type queryArgs struct {
	Type  string `json:"type"`
	Limit int    `json:"limit"`
}
