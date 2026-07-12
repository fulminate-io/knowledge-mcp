// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sort"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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
	// Sort by scalar with a deterministic (ClusterA, ClusterB) tie-break: the pairs
	// are gathered in Go map-iteration order, so without a stable secondary key the
	// top-5 selection over tied scalars (e.g. an uncharged corpus where every pair
	// is 1.0) would be nondeterministic across runs.
	sort.Slice(pairs, func(i, j int) bool { return lessPairAsc(pairs[i], pairs[j]) })
	limit := min(len(pairs), 5)
	report.TopStubborn = append([]ClusterPairScalar(nil), pairs[:limit]...)
	sort.Slice(pairs, func(i, j int) bool { return lessPairDesc(pairs[i], pairs[j]) })
	limit = min(len(pairs), 5)
	report.TopGullible = append([]ClusterPairScalar(nil), pairs[:limit]...)
	return report
}

// lessPairAsc / lessPairDesc order cluster-pair scalars ascending / descending
// with a deterministic (ClusterA, ClusterB) tie-break so equal-scalar pairs sort
// reproducibly regardless of the map-iteration order they were gathered in.
func lessPairAsc(a, b ClusterPairScalar) bool {
	if a.Scalar != b.Scalar {
		return a.Scalar < b.Scalar
	}
	if a.ClusterA != b.ClusterA {
		return a.ClusterA < b.ClusterA
	}
	return a.ClusterB < b.ClusterB
}

func lessPairDesc(a, b ClusterPairScalar) bool {
	if a.Scalar != b.Scalar {
		return a.Scalar > b.Scalar
	}
	if a.ClusterA != b.ClusterA {
		return a.ClusterA < b.ClusterA
	}
	return a.ClusterB < b.ClusterB
}

// BlindSpotInfluenceVector computes the per-thought influence vector the blind-spot
// foundational-but-unexamined facet ranks on — ONE BuildTrustMatrix +
// ComputeInfluenceVector pass over the thought set (the same structural eigenvector
// ReflectInfluence uses, no personality re-weighting). A build error yields a nil
// vector (the influence-keyed facet then sees zero influence everywhere, leaving
// the other facets intact) rather than failing the whole surface. Called by the
// propagation loop's computeBlindSpots so the facet classifier gets the influence
// signal for free off the loop's tick.
func BlindSpotInfluenceVector(ctx context.Context, gc Caller, thoughtIDs []string) map[string]float64 {
	if len(thoughtIDs) == 0 {
		return nil
	}
	// One now for this pass: the recency-weighted SelfTrust diagonal.
	now := time.Now()
	matrix, err := BuildTrustMatrix(ctx, gc, thoughtIDs, now)
	if err != nil {
		return nil
	}
	return ComputeInfluenceVector(matrix)
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
		now := time.Now()
		var totalV, totalM float64
		for _, id := range thoughtIDs {
			props := computePropertiesFromCharges(charges[id], now)
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

// listAllThoughtIDs pulls every thought ID via browseNodeIDs, which drains the
// type=thought browse in bounded offset pages and projects the full
// *knowledgev1.Node payloads to IDs (no text-parse of a render envelope).
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

// countByType returns the count of nodes of a given type via the same paged
// browse listAllThoughtIDs uses (browseNodeIDs → drainThoughtBrowse). A nil gc /
// error yields 0 (the reflective surface is best-effort).
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

// browseNodeIDs drains the full type-browse via drainThoughtBrowse (bounded
// offset paging) and projects the decoded *knowledgev1.Node IDs. Shared by
// listAllThoughtIDs + countByType. Paging is required because a single limit:0
// browse is silently rewritten to browseDefaultLimit(10) rows by the engine —
// the drain is what makes the summary counts corpus-complete.
func browseNodeIDs(ctx context.Context, gc Caller, nodeType string) ([]string, error) {
	nodes, err := drainThoughtBrowse(ctx, gc, nodeType, browsePageSize)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Id)
	}
	return out, nil
}

// queryArgs is the typed payload for the type-browse query call. Kept as a
// struct so json.Marshal stays errchkjson-safe (map[string]any triggers
// the unsafe-type lint). Offset carries the paging cursor for drainThoughtBrowse.
type queryArgs struct {
	Type   string `json:"type"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}
