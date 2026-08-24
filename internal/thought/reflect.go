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

// PersonalityReport carries the RENDERED rows only — the top-personalityTopK
// stubborn and the top-personalityTopK gullible cluster pairs, plus the cluster
// population those rows were selected from. It deliberately does NOT carry the
// underlying profile.
//
// That omission is the contract rather than an oversight, and re-adding the
// profile as an obvious convenience is the specific mistake to avoid: this
// struct is serialized wholesale by the personality query's JSON arm, so
// carrying the profile put a per-cluster row set on the wire in order to render
// ten lines. ClusterCount is what discloses the population the rows came from.
type PersonalityReport struct {
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
	pairs := personalityPairCandidates(profile, clusterFilter)
	// Sort by scalar with a deterministic (ClusterA, ClusterB) tie-break. Ties are
	// the common case, not the corner one — every column carrying a row's default
	// shares that row's scalar, and an uncharged corpus is 1.0 everywhere — and
	// sort.Slice is not stable, so without the secondary key the
	// top-personalityTopK selection over tied scalars would not be reproducible.
	// The tie-break is also what makes the bounded candidate set EXACT: it turns
	// the comparator into a total order over (Scalar, ClusterA, ClusterB), so
	// selecting over a subset that contains the dense selection returns the
	// identical rows.
	sort.Slice(pairs, func(i, j int) bool { return lessPairAsc(pairs[i], pairs[j]) })
	limit := min(len(pairs), personalityTopK)
	report.TopStubborn = append([]ClusterPairScalar(nil), pairs[:limit]...)
	sort.Slice(pairs, func(i, j int) bool { return lessPairDesc(pairs[i], pairs[j]) })
	limit = min(len(pairs), personalityTopK)
	report.TopGullible = append([]ClusterPairScalar(nil), pairs[:limit]...)
	return report
}

// personalityTopK is how many rows each end of the personality report carries.
const personalityTopK = 5

// personalityPairCandidates gathers the cluster pairs the report may select
// from. It is a BOUNDED subset of the full pair space — every deviating column
// of each row, plus that row's personalityTopK lexicographically smallest
// default columns — and selecting over it returns EXACTLY what a full dense scan
// would have selected. That is a proof, not an approximation, and a reader who
// cannot reconstruct it will "improve" the selection into a wrong one:
//
// Both comparators order by scalar first, then by (ClusterA, ClusterB)
// ascending. Within one row every column carrying the row default has the
// identical scalar, so those entries order purely by column ID. At most
// personalityTopK entries of any row can reach either end of a
// top-personalityTopK selection, so any of them drawn from the default group
// must be among that row's personalityTopK lexicographically smallest default
// columns. Emitting those plus every deviating column therefore yields a
// candidate set that CONTAINS the dense selection; since the comparator is a
// total order and the candidates are a subset of the dense entries, selecting
// over the candidates gives the identical result.
//
// Three details that look optional and are not. The column set is derived from
// profile.RowDefault rather than from any caller-supplied cluster slice, because
// a caller may pass one that disagrees with the profile. The deviating columns
// are sorted before emission so the candidate order is deterministic even before
// the sort. And the sampled counter advances only when Scalar returned ok, so a
// self column or an unknown column never consumes a sampling slot.
func personalityPairCandidates(profile *PersonalityProfile, clusterFilter string) []ClusterPairScalar {
	columns := make([]string, 0, len(profile.RowDefault))
	for id := range profile.RowDefault {
		columns = append(columns, id)
	}
	sort.Strings(columns)

	pairs := make([]ClusterPairScalar, 0, len(columns)*personalityTopK)
	for _, clusterA := range columns {
		if clusterFilter != "" && clusterA != clusterFilter {
			continue
		}
		deviations := profile.Deviations[clusterA]
		deviatingColumns := make([]string, 0, len(deviations))
		for clusterB := range deviations {
			deviatingColumns = append(deviatingColumns, clusterB)
		}
		sort.Strings(deviatingColumns)
		for _, clusterB := range deviatingColumns {
			if scalar, ok := profile.Scalar(clusterA, clusterB); ok {
				pairs = append(pairs, personalityPair(profile, clusterA, clusterB, scalar))
			}
		}
		sampled := 0
		for _, clusterB := range columns {
			if sampled == personalityTopK {
				break
			}
			if _, isDeviation := deviations[clusterB]; isDeviation {
				continue
			}
			scalar, ok := profile.Scalar(clusterA, clusterB)
			if !ok {
				continue
			}
			pairs = append(pairs, personalityPair(profile, clusterA, clusterB, scalar))
			sampled++
		}
	}
	return pairs
}

// personalityPair builds one report row, resolving both cluster labels off the
// profile.
func personalityPair(profile *PersonalityProfile, clusterA, clusterB string, scalar float64) ClusterPairScalar {
	return ClusterPairScalar{
		ClusterA: clusterA,
		ClusterB: clusterB,
		LabelA:   profile.ClusterLabels[clusterA],
		LabelB:   profile.ClusterLabels[clusterB],
		Scalar:   scalar,
	}
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
// signal for free off the loop's tick — the loop passes its per-pass read memo as
// src, so the matrix build reuses the pass's adjacency and charge map rather than
// re-reading them (and, with no memo, still reads the resident corpus not the wire).
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
		charges := fetchChargesFor(ctx, gc, thoughtIDs, src)
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
