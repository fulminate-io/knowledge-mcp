// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// PersonalityProfile holds per-cluster-pair scalars derived from external
// charge accuracy.
type PersonalityProfile struct {
	// Scalars[clusterA][clusterB] = how much cluster A trusts influence
	// from cluster B. > 1.0 = gullible, < 1.0 = stubborn.
	Scalars       map[string]map[string]float64
	ClusterLabels map[string]string
}

// ScalarSnapshot captures the personality scalar at a point in time.
type ScalarSnapshot struct {
	Timestamp       time.Time
	Scalar          float64
	ExternalCharges int
	Accuracy        float64
}

// chargeInfo holds precomputed data for a single charge node, avoiding
// repeated DB lookups inside the cluster-pair O(C²) loop.
type chargeInfo struct {
	node            *knowledgev1.Node
	polarity        string
	weight          float64
	evidenceCluster string // cluster ID of the evidence source, or "" if none
}

// thoughtChargeCache holds precomputed charges and evidence for all
// thoughts.
type thoughtChargeCache struct {
	charges map[string][]chargeInfo
}

// buildChargeCache fetches all charges and their evidence mappings
// once upfront. Issues ONE gc.Call("thoughts", {operation:"charges_for"})
// for the bulk thought→charges map (chargeMapForThoughts), then one
// gc.Call("thoughts", {operation:"adjacency", thought_ids: chargeIDs})
// is NOT issued — instead, evidence resolution uses the prebuilt
// thoughtToCluster map (so a charge's EvidencedBy edge is mapped via
// the cluster the evidence target lives in). The full evidence-edge
// walk happens on the client by reusing the adj from a prior fetch;
// if the caller didn't supply it, we skip the evidence resolution and
// leave every charge's evidenceCluster as "" (degraded — affects
// personality scalars only).
func buildChargeCache(ctx context.Context, gc *graphclient.GraphClient, clusters []ThoughtCluster, thoughtToCluster map[string]string, evidenceAdj map[string][]string) thoughtChargeCache {
	var allThoughtIDs []string
	for _, c := range clusters {
		allThoughtIDs = append(allThoughtIDs, c.ThoughtIDs...)
	}
	chargeNodeMap := chargeMapForThoughts(ctx, gc, allThoughtIDs)
	evidenceCluster := buildChargeEvidenceMap(chargeNodeMap, thoughtToCluster, evidenceAdj)

	cache := thoughtChargeCache{charges: make(map[string][]chargeInfo, len(chargeNodeMap))}
	for tid, charges := range chargeNodeMap {
		infos := make([]chargeInfo, 0, len(charges))
		for _, ch := range charges {
			infos = append(infos, chargeInfo{
				node:            ch,
				polarity:        kgtypes.Value(ch, "polarity"),
				weight:          parseFloat(kgtypes.Value(ch, "weight")),
				evidenceCluster: evidenceCluster[ch.Id],
			})
		}
		cache.charges[tid] = infos
	}
	return cache
}

// buildScopedChargeCache is the cluster-scoped sibling of
// buildChargeCache. Bulk-fetches charges only for the supplied
// thoughtIDs (typically a single cluster's thoughts during
// ComputeScalarEvolution), then resolves their evidence-cluster links
// against the full thoughtToCluster map.
func buildScopedChargeCache(ctx context.Context, gc *graphclient.GraphClient, thoughtIDs []string, thoughtToCluster map[string]string, evidenceAdj map[string][]string) thoughtChargeCache {
	chargeNodeMap := chargeMapForThoughts(ctx, gc, thoughtIDs)
	evidenceCluster := buildChargeEvidenceMap(chargeNodeMap, thoughtToCluster, evidenceAdj)
	cache := thoughtChargeCache{charges: make(map[string][]chargeInfo, len(chargeNodeMap))}
	for tid, charges := range chargeNodeMap {
		infos := make([]chargeInfo, 0, len(charges))
		for _, ch := range charges {
			infos = append(infos, chargeInfo{
				node:            ch,
				polarity:        kgtypes.Value(ch, "polarity"),
				weight:          parseFloat(kgtypes.Value(ch, "weight")),
				evidenceCluster: evidenceCluster[ch.Id],
			})
		}
		cache.charges[tid] = infos
	}
	return cache
}

// buildChargeEvidenceMap walks each charge's outgoing edges (via the
// prebuilt evidenceAdj map, which a caller may have populated from an
// adjacency call) to resolve evidence → cluster. Returns
// map[chargeID]evidenceClusterID; charges with no in-cluster evidence
// target are absent from the map.
//
// When evidenceAdj is nil/empty, evidence resolution is skipped (every
// charge gets ""). This is the degraded mode for callers that don't
// pre-fetch a charge-rooted adjacency — personality scalars still
// compute, just without cross-cluster signal.
func buildChargeEvidenceMap(chargeMap map[string][]*knowledgev1.Node, thoughtToCluster map[string]string, evidenceAdj map[string][]string) map[string]string {
	out := make(map[string]string)
	if len(evidenceAdj) == 0 {
		return out
	}
	for _, charges := range chargeMap {
		for _, ch := range charges {
			for _, target := range evidenceAdj[ch.Id] {
				if cluster, ok := thoughtToCluster[target]; ok {
					out[ch.Id] = cluster
					break
				}
			}
		}
	}
	return out
}

// ComputePersonalityScalars derives per-cluster-pair trust scalars
// from the track record of cross-cluster charge accuracy.
// evidenceAdj is the optional charge→evidence-target adjacency map
// (callers may have it from a prior adjacency fetch); pass nil for
// the degraded mode (zero evidence resolution).
func ComputePersonalityScalars(ctx context.Context, gc *graphclient.GraphClient, clusters []ThoughtCluster, evidenceAdj map[string][]string) (PersonalityProfile, error) {
	profile := PersonalityProfile{
		Scalars:       make(map[string]map[string]float64),
		ClusterLabels: make(map[string]string),
	}

	thoughtToCluster := make(map[string]string)
	for _, c := range clusters {
		profile.ClusterLabels[c.ID] = c.Label
		for _, tid := range c.ThoughtIDs {
			thoughtToCluster[tid] = c.ID
		}
	}

	cache := buildChargeCache(ctx, gc, clusters, thoughtToCluster, evidenceAdj)

	for _, clusterA := range clusters {
		profile.Scalars[clusterA.ID] = make(map[string]float64)
		for _, clusterB := range clusters {
			if clusterA.ID == clusterB.ID {
				continue
			}
			scalar := computeClusterPairScalar(clusterA, clusterB.ID, cache, time.Time{})
			profile.Scalars[clusterA.ID][clusterB.ID] = scalar
		}
	}

	return profile, nil
}

// computeClusterPairScalar computes the trust scalar for clusterA listening to clusterB.
func computeClusterPairScalar(clusterA ThoughtCluster, clusterBID string, cache thoughtChargeCache, cutoff time.Time) float64 {
	var confirmedWeight, contradictedWeight float64
	for _, thoughtID := range clusterA.ThoughtIDs {
		c, ct := accumulateChargeAccuracy(cache.charges[thoughtID], clusterBID, cutoff)
		confirmedWeight += c
		contradictedWeight += ct
	}
	total := confirmedWeight + contradictedWeight
	if total == 0 {
		return 1.0
	}
	accuracy := confirmedWeight / total
	return 0.2 + 1.6*accuracy
}

// accumulateChargeAccuracy accumulates confirmed/contradicted weights
// for charges evidenced by clusterBID.
func accumulateChargeAccuracy(charges []chargeInfo, clusterBID string, cutoff time.Time) (confirmedWeight, contradictedWeight float64) {
	for _, ci := range charges {
		if ci.evidenceCluster != clusterBID {
			continue
		}
		if !cutoff.IsZero() && nanosToTime(ci.node.CreatedAt).After(cutoff) {
			continue
		}
		confirmed, contradicted := weighSubsequentChargesFromCache(charges, ci.node.Id, nanosToTime(ci.node.CreatedAt), ci.polarity, cutoff)
		confirmedWeight += confirmed
		contradictedWeight += contradicted
		if confirmed == 0 && contradicted == 0 {
			confirmedWeight += ci.weight * 0.5
		}
	}
	return confirmedWeight, contradictedWeight
}

// weighSubsequentChargesFromCache computes confirmed/contradicted
// weights from precomputed charge info that came after chargeTime.
func weighSubsequentChargesFromCache(charges []chargeInfo, chargeID string, chargeTime time.Time, chargePolarity string, cutoff time.Time) (confirmed, contradicted float64) {
	for _, sc := range charges {
		if sc.node.Id == chargeID || nanosToTime(sc.node.CreatedAt).Before(chargeTime) || nanosToTime(sc.node.CreatedAt).Equal(chargeTime) {
			continue
		}
		if !cutoff.IsZero() && nanosToTime(sc.node.CreatedAt).After(cutoff) {
			continue
		}
		if sc.polarity == chargePolarity {
			confirmed += sc.weight
		} else {
			contradicted += sc.weight
		}
	}
	return confirmed, contradicted
}

const defaultEvolutionSamples = 30

// ComputeScalarEvolution returns a temporal series of personality
// scalars for clusterA's trust toward clusterB.
func ComputeScalarEvolution(ctx context.Context, gc *graphclient.GraphClient, clusters []ThoughtCluster, clusterA, clusterB string, n int, evidenceAdj map[string][]string) ([]ScalarSnapshot, error) {
	if n <= 0 {
		n = defaultEvolutionSamples
	}
	if len(clusters) == 0 {
		var err error
		clusters, err = DetectThoughtClusters(ctx, gc, 0.5)
		if err != nil {
			return nil, fmt.Errorf("detect clusters: %w", err)
		}
	}

	clusterANode := findCluster(clusters, clusterA)
	if clusterANode == nil {
		return nil, nil
	}

	thoughtToCluster := make(map[string]string)
	for _, c := range clusters {
		for _, tid := range c.ThoughtIDs {
			thoughtToCluster[tid] = c.ID
		}
	}
	cache := buildScopedChargeCache(ctx, gc, clusterANode.ThoughtIDs, thoughtToCluster, evidenceAdj)

	timestamps := chargeTimestampsForCluster(*clusterANode, cache)
	if len(timestamps) == 0 {
		return []ScalarSnapshot{{Timestamp: time.Now(), Scalar: 1.0}}, nil
	}
	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i].Before(timestamps[j]) })

	samples := sampleTimestamps(timestamps, n)
	snapshots := make([]ScalarSnapshot, 0, len(samples))
	for _, ts := range samples {
		scalar := computeClusterPairScalar(*clusterANode, clusterB, cache, ts)
		external, accuracy := externalChargeStats(*clusterANode, clusterB, cache, ts)
		snapshots = append(snapshots, ScalarSnapshot{
			Timestamp:       ts,
			Scalar:          scalar,
			ExternalCharges: external,
			Accuracy:        accuracy,
		})
	}
	return snapshots, nil
}

func findCluster(clusters []ThoughtCluster, id string) *ThoughtCluster {
	for i := range clusters {
		if clusters[i].ID == id {
			return &clusters[i]
		}
	}
	return nil
}

func chargeTimestampsForCluster(clusterA ThoughtCluster, cache thoughtChargeCache) []time.Time {
	var ts []time.Time
	for _, tid := range clusterA.ThoughtIDs {
		for _, ci := range cache.charges[tid] {
			if !nanosToTime(ci.node.CreatedAt).IsZero() {
				ts = append(ts, nanosToTime(ci.node.CreatedAt))
			}
		}
	}
	return ts
}

func sampleTimestamps(sorted []time.Time, n int) []time.Time {
	if len(sorted) <= n {
		return sorted
	}
	if n == 1 {
		return []time.Time{sorted[len(sorted)-1]}
	}
	earliest := sorted[0]
	latest := sorted[len(sorted)-1]
	span := latest.Sub(earliest)
	out := make([]time.Time, n)
	for i := range n {
		t := time.Duration(int64(span) * int64(i) / int64(n-1))
		out[i] = earliest.Add(t)
	}
	return out
}

func externalChargeStats(clusterA ThoughtCluster, clusterB string, cache thoughtChargeCache, cutoff time.Time) (count int, accuracy float64) {
	var confirmed, contradicted float64
	for _, tid := range clusterA.ThoughtIDs {
		for _, ci := range cache.charges[tid] {
			if ci.evidenceCluster != clusterB {
				continue
			}
			if !cutoff.IsZero() && nanosToTime(ci.node.CreatedAt).After(cutoff) {
				continue
			}
			count++
			c, ct := weighSubsequentChargesFromCache(cache.charges[tid], ci.node.Id, nanosToTime(ci.node.CreatedAt), ci.polarity, cutoff)
			confirmed += c
			contradicted += ct
			if c == 0 && ct == 0 {
				confirmed += ci.weight * 0.5
			}
		}
	}
	if total := confirmed + contradicted; total > 0 {
		accuracy = confirmed / total
	}
	return count, accuracy
}

// BuildTrustMatrixWithPersonality constructs a trust matrix with
// personality scalars applied. The cluster-membership map is read
// from the prebuilt nodeByID map (one gc.Call("query", {ids:})
// upstream).
func BuildTrustMatrixWithPersonality(ctx context.Context, gc *graphclient.GraphClient, thoughtIDs []string, profile PersonalityProfile, nodeByID map[string]*knowledgev1.Node) (TrustMatrix, error) {
	matrix, err := BuildTrustMatrix(ctx, gc, thoughtIDs)
	if err != nil {
		return matrix, err
	}
	thoughtToCluster := make(map[string]string, len(thoughtIDs))
	for _, id := range thoughtIDs {
		if n, ok := nodeByID[id]; ok {
			if cid := kgtypes.Value(n, "cluster_id"); cid != "" {
				thoughtToCluster[id] = cid
			}
		}
	}
	n := len(matrix.IDs)
	for i := range n {
		applyPersonalityScalarsToRow(matrix, i, thoughtToCluster, profile)
		renormalizeSparseRow(matrix.Rows[i])
	}
	return matrix, nil
}

func applyPersonalityScalarsToRow(matrix TrustMatrix, i int, thoughtToCluster map[string]string, profile PersonalityProfile) {
	idI := matrix.IDs[i]
	clusterI := thoughtToCluster[idI]
	if clusterI == "" {
		return
	}
	scalars, ok := profile.Scalars[clusterI]
	if !ok {
		return
	}
	row := matrix.Rows[i]
	for k := range row {
		j := row[k].Col
		if j == i {
			continue
		}
		clusterJ := thoughtToCluster[matrix.IDs[j]]
		if clusterJ == "" || clusterJ == clusterI {
			continue
		}
		if s2, ok := scalars[clusterJ]; ok {
			row[k].Val *= s2
		}
	}
}

func renormalizeSparseRow(row []SparseEntry) {
	rowSum := 0.0
	for _, e := range row {
		rowSum += e.Val
	}
	if rowSum > 0 {
		for j := range row {
			row[j].Val /= rowSum
		}
	}
}
