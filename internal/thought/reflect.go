// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"log/slog"
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
// signal for free off the loop's tick — the loop passes itself as src, so the
// matrix build reads the resident corpus instead of re-draining.
func BlindSpotInfluenceVector(ctx context.Context, gc Caller, thoughtIDs []string, src CorpusSource) map[string]float64 {
	if len(thoughtIDs) == 0 {
		return nil
	}
	// One now for this pass: the recency-weighted SelfTrust diagonal.
	now := time.Now()
	matrix, err := BuildTrustMatrix(ctx, gc, thoughtIDs, now, src)
	if err != nil {
		return nil
	}
	return ComputeInfluenceVector(matrix)
}

// ReflectSummary returns a high-level overview of the thought store.
// The thought set is served O(1) from the resident corpus cache when src is warm
// and otherwise drained (fetchAllThoughtNodes); the full Node payloads are
// in hand from that single read, so the recent-thoughts render slices them directly
// (no second fetchNodesByIDs hydrate). Charge/session counters keep their own
// type-browse (countByType) — those classes are counted, not reflected over, and the
// resident cache is the thought set. ONE bulk fetchChargesFor for the aggregates.
func ReflectSummary(ctx context.Context, gc Caller, clusters []ThoughtCluster, src CorpusSource) ThoughtGraphSummary {
	var summary ThoughtGraphSummary
	summary.ClusterCount = len(clusters)

	thoughts, err := fetchAllThoughtNodes(ctx, gc, src)
	if err != nil {
		slog.Warn("thought: ReflectSummary: thought read failed", "err", err)
	}
	thoughtIDs := make([]string, 0, len(thoughts))
	for _, n := range thoughts {
		thoughtIDs = append(thoughtIDs, n.Id)
	}
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

		// Recent thoughts — sort the in-hand full payloads by CreatedAt and slice.
		all := append([]*knowledgev1.Node(nil), thoughts...)
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
// the unsafe-type lint). AfterID carries the paging cursor for drainThoughtBrowse;
// Offset is left unset by that drain (the two are mutually exclusive server-side).
// This struct is marshaled to JSON and re-parsed by engine.Compile, so every tag
// here must match the engine-side queryArgs exactly.
type queryArgs struct {
	Type   string `json:"type"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
	// SkipTotal drops the per-page paginating COUNT: the drain discards Total, so
	// the single-layer executor returns Total==offset+pageRows instead of running
	// a COUNT for every page. Set by drainThoughtBrowse.
	SkipTotal bool `json:"skip_total"`
	// AfterID selects the id-keyset browse and carries its cursor. A POINTER so
	// PRESENCE survives this JSON hop: page 1 of the drain sets it to the EMPTY
	// string, and a plain string would marshal that identically to "no keyset
	// browse", which puts page 1 back on the backend's default order and makes the
	// cursor derived from it skip every lower id.
	AfterID *string `json:"after_id"`
}
