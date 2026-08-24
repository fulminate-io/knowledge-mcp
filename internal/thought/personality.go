// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"sort"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

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
// once upfront. Calls chargeMapForThoughts (a bulk thought→charges
// read over the Execute seam) for the bulk thought→charges map; it does
// NOT fetch a separate adjacency — instead, evidence resolution uses the
// prebuilt thoughtToCluster map (so a charge's EvidencedBy edge is mapped
// via the cluster the evidence target lives in). The full evidence-edge
// walk happens on the client by reusing the adj from a prior fetch;
// if the caller didn't supply it, we skip the evidence resolution and
// leave every charge's evidenceCluster as "" (degraded — affects
// personality scalars only).
func buildChargeCache(ctx context.Context, gc Caller, clusters []ThoughtCluster, thoughtToCluster map[string]string, evidenceAdj map[string][]string, src CorpusSource) thoughtChargeCache {
	var allThoughtIDs []string
	for _, c := range clusters {
		allThoughtIDs = append(allThoughtIDs, c.ThoughtIDs...)
	}
	chargeNodeMap := chargeMapForThoughts(ctx, gc, allThoughtIDs, src)
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
func buildScopedChargeCache(ctx context.Context, gc Caller, thoughtIDs []string, thoughtToCluster map[string]string, evidenceAdj map[string][]string, src CorpusSource) thoughtChargeCache {
	chargeNodeMap := chargeMapForThoughts(ctx, gc, thoughtIDs, src)
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

// evidenceAdjEdgeTypes is BuildEvidenceAdj's charge-pivot read filter, hoisted from an
// inline literal so the per-pass read inventory (loop_pass.go) can cite it by name. It
// is declared ABOVE the doc block below rather than between that block and its
// function, which would detach the doc comment from the exported symbol it documents.
var evidenceAdjEdgeTypes = []kgtypes.EdgeType{kgtypes.EdgeEvidencedBy}

// BuildEvidenceAdj builds the charge→evidence-target adjacency map that
// ComputePersonalityScalars / ComputeScalarEvolution consume as evidenceAdj:
// map[chargeID][]evidenceTargetID. It is the cross-cluster ATTRIBUTION leg —
// only charges whose evidence resolves to a clustered thought sharpen a specific
// A→B pair (buildChargeEvidenceMap checks thoughtToCluster[target]). The base
// charged-by leg needs no evidenceAdj, so trust differentiation holds even when
// the corpus has near-zero thought-targeted evidence edges.
//
// Two bounded bulk legs, no per-node traversal (fits the live 180s ceiling):
// (1) chargeMapForThoughts over every clustered thought ID (the same bulk
// charges_for the scalar compute runs), then (2) fetchAllEdgesBanded over the
// collected charge IDs filtered to EdgeEvidencedBy — a BANDED match-all read, so
// the charge ids derive its band boundaries rather than being sent as pivots, and
// it costs one Execute per band. EdgeEvidencedBy is charge→evidence, so FromId is
// the charge and ToId the evidence target.
//
// THE RESULT IS A SUPERSET and that is safe HERE for a reason worth stating: the
// map is consumed only by buildChargeEvidenceMap, which walks the pivot-derived
// chargeMap and looks up evidenceAdj[ch.Id] by KEY. Extra keys are never reached.
//
// src is the per-pass read memo: the charge map leg is shared with every other
// stage of the pass rather than composed again here. A nil/non-memo src composes it
// on the spot, exactly as before.
func BuildEvidenceAdj(ctx context.Context, gc Caller, clusters []ThoughtCluster, src CorpusSource) map[string][]string {
	out := map[string][]string{}
	if gc == nil || len(clusters) == 0 {
		return out
	}
	var allThoughtIDs []string
	for _, c := range clusters {
		allThoughtIDs = append(allThoughtIDs, c.ThoughtIDs...)
	}
	chargeMap := chargeMapForThoughts(ctx, gc, allThoughtIDs, src)
	var chargeIDs []string
	for _, charges := range chargeMap {
		for _, ch := range charges {
			chargeIDs = append(chargeIDs, ch.Id)
		}
	}
	if len(chargeIDs) == 0 {
		return out
	}
	edges, err := fetchAllEdgesBanded(ctx, gc, chargeIDs, evidenceAdjEdgeTypes)
	if err != nil {
		return out
	}
	for i := range edges {
		e := &edges[i]
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeEvidencedBy {
			continue
		}
		out[e.FromId] = append(out[e.FromId], e.ToId)
	}
	return out
}

// ComputePersonalityScalars derives per-cluster-pair trust scalars
// from the track record of cross-cluster charge accuracy.
//
// The returned profile is SPARSE: each cluster contributes one RowDefault entry
// carrying the value shared by that row's columns, plus a Deviations entry only
// when some column genuinely differs. Rows with no differing column — the great
// majority — add nothing to Deviations at all. Read a pair back through
// PersonalityProfile.Scalar rather than indexing the maps directly; it is what
// resolves a default against a deviation and reports the absent cases.
//
// evidenceAdj is the optional charge→evidence-target adjacency map
// (callers may have it from a prior adjacency fetch); pass nil for
// the degraded mode (zero evidence resolution). src is the per-pass read memo, so
// the charge map this drives is the pass's single composition; an on-demand handler
// with no loop in hand passes nil and takes the uncached read.
func ComputePersonalityScalars(ctx context.Context, gc Caller, clusters []ThoughtCluster, evidenceAdj map[string][]string, src CorpusSource) (PersonalityProfile, error) {
	profile := PersonalityProfile{
		RowDefault:    make(map[string]float64, len(clusters)),
		Deviations:    make(map[string]map[string]float64),
		ClusterLabels: make(map[string]string, len(clusters)),
	}

	thoughtToCluster := make(map[string]string)
	for _, c := range clusters {
		profile.ClusterLabels[c.ID] = c.Label
		for _, tid := range c.ThoughtIDs {
			thoughtToCluster[tid] = c.ID
		}
	}

	cache := buildChargeCache(ctx, gc, clusters, thoughtToCluster, evidenceAdj, src)

	for _, clusterA := range clusters {
		rowDefault, deviations := computeSparseRow(clusterA, cache)
		profile.RowDefault[clusterA.ID] = rowDefault
		if len(deviations) > 0 {
			profile.Deviations[clusterA.ID] = deviations
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

// evidenceReinforcementBoost is how much MORE a graph-evidenced charge weighs
// than an otherwise-identical non-evidenced one. It is deliberately MODEST and
// must never dominate the caller-asserted charge weight (1-10): a charge without
// a graph evidenced-by edge is NOT a weaker confirmation — its evidence is
// usually real but extra-graph (an API behaving exactly as the thought
// predicted, worked-first-time, an unrelated source confirming a theory, the
// user reinforcing the conclusion). The caller weight already encodes
// confirmation strength. What a graph evidenced-by edge uniquely adds is in-graph
// TRACEABILITY + cross-cluster ATTRIBUTION, not greater validity — hence a small
// re-enforcement, not a gate.
const evidenceReinforcementBoost = 1.2

// accumulateChargeAccuracy accumulates confirmed/contradicted weights toward
// clusterBID. RE-ENFORCER semantics (not a gate): EVERY charge on the thought
// contributes its same-thought track record to the pair via the universal
// charged-by anchor at full caller-asserted weight (factor 1.0, identical across
// every B for a given thought — this is what lifts a thought's scalars off the
// 1.000 default once it has any charge history). When a charge's evidence target
// lives in clusterBID, its contribution to THAT specific pair is scaled by the
// modest evidenceReinforcementBoost — sharpening A→B (the attributed pair) over
// A→other without dominating the weight. The evidenced/non-evidenced delta is
// (boost-1) of one charge's track record: bounded, sub-weight, never
// order-of-magnitude.
func accumulateChargeAccuracy(charges []chargeInfo, clusterBID string, cutoff time.Time) (confirmedWeight, contradictedWeight float64) {
	for _, ci := range charges {
		if !cutoff.IsZero() && nanosToTime(ci.node.CreatedAt).After(cutoff) {
			continue
		}
		confirmed, contradicted := weighSubsequentChargesFromCache(charges, ci.node.Id, nanosToTime(ci.node.CreatedAt), ci.polarity, cutoff)
		if confirmed == 0 && contradicted == 0 {
			confirmed += ci.weight * 0.5
		}
		factor := 1.0
		if ci.evidenceCluster == clusterBID {
			factor = evidenceReinforcementBoost
		}
		confirmedWeight += confirmed * factor
		contradictedWeight += contradicted * factor
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
// scalars for clusterA's trust toward clusterB. src is the per-pass read memo; an
// on-demand handler with no loop in hand passes nil and takes the uncached read.
func ComputeScalarEvolution(ctx context.Context, gc Caller, clusters []ThoughtCluster, clusterA, clusterB string, n int, evidenceAdj map[string][]string, src CorpusSource) ([]ScalarSnapshot, error) {
	if n <= 0 {
		n = defaultEvolutionSamples
	}
	if len(clusters) == 0 {
		var err error
		clusters, err = DetectThoughtClusters(ctx, gc, 0.5, src)
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
	cache := buildScopedChargeCache(ctx, gc, clusterANode.ThoughtIDs, thoughtToCluster, evidenceAdj, src)

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

// externalChargeStats mirrors accumulateChargeAccuracy's RE-ENFORCER semantics
// for the evolution surface: EVERY charge on clusterA's thoughts participates
// (count and accuracy) via its charged-by track record at full weight; a charge
// whose evidence targets clusterB is scaled by the modest evidenceReinforcementBoost
// in the accuracy numerator/denominator. Count reflects real participation (every
// contributing charge), not just evidence-target charges.
func externalChargeStats(clusterA ThoughtCluster, clusterB string, cache thoughtChargeCache, cutoff time.Time) (count int, accuracy float64) {
	var confirmed, contradicted float64
	for _, tid := range clusterA.ThoughtIDs {
		for _, ci := range cache.charges[tid] {
			if !cutoff.IsZero() && nanosToTime(ci.node.CreatedAt).After(cutoff) {
				continue
			}
			count++
			c, ct := weighSubsequentChargesFromCache(cache.charges[tid], ci.node.Id, nanosToTime(ci.node.CreatedAt), ci.polarity, cutoff)
			if c == 0 && ct == 0 {
				c += ci.weight * 0.5
			}
			factor := 1.0
			if ci.evidenceCluster == clusterB {
				factor = evidenceReinforcementBoost
			}
			confirmed += c * factor
			contradicted += ct * factor
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
// upstream). src is forwarded to the underlying matrix build as the resident
// corpus seam (nil = drain).
func BuildTrustMatrixWithPersonality(ctx context.Context, gc Caller, thoughtIDs []string, profile PersonalityProfile, nodeByID map[string]*knowledgev1.Node, now time.Time, src CorpusSource) (TrustMatrix, error) {
	matrix, err := BuildTrustMatrix(ctx, gc, thoughtIDs, now, src)
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
	if _, ok := profile.RowDefault[clusterI]; !ok {
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
		if s2, ok := profile.Scalar(clusterI, clusterJ); ok {
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
