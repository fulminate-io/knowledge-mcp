// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"math"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// blindSpotReportCap bounds the number of ranked blind-spot clusters
// ReflectBlindSpots returns, keeping the surface in-context: only the top-N
// highest-impact under-evidenced clusters are shown.
const blindSpotReportCap = 20

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
	// Influence is the summed per-thought influence over the cluster's members —
	// the structural weight of the under-evidenced region.
	Influence float64
	// Impact is the rank score: Size × Influence × chargeThinness. Higher = a
	// larger, more central, charge-thinner cluster — a higher-priority blind spot.
	Impact float64
}

// BlindSpotResult carries the ranked + capped blind-spot spots plus the totals an
// LLM consumer needs to judge the surface in-context: how many human-genre
// clusters were under-evidenced before the cap, and how many machine-genre
// clusters were excluded from the denominator entirely.
type BlindSpotResult struct {
	Spots                []BlindSpotReport
	TotalUnderEvidenced  int // human-genre clusters that qualified, before the cap.
	ExcludedMachineGenre int // machine-genre clusters dropped from the denominator.
	TotalClusters        int // all clusters considered (human + machine genre).
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

// BlindSpotInfluenceVector computes the per-thought influence vector ReflectBlindSpots
// ranks on — ONE BuildTrustMatrix + ComputeInfluenceVector pass over the thought
// set (the same structural eigenvector ReflectInfluence uses, no personality
// re-weighting). A build error yields a nil vector (ranking then degrades to
// Impact=0 for every cluster, leaving the under-evidenced gate intact) rather than
// failing the whole surface. Shared by both ReflectBlindSpots callers (the tools
// handler and the loop) so the influence pass is built identically.
func BlindSpotInfluenceVector(ctx context.Context, gc Caller, thoughtIDs []string) map[string]float64 {
	if len(thoughtIDs) == 0 {
		return nil
	}
	matrix, err := BuildTrustMatrix(ctx, gc, thoughtIDs)
	if err != nil {
		return nil
	}
	return ComputeInfluenceVector(matrix)
}

// ReflectBlindSpots finds the highest-impact under-evidenced clusters, EXCLUDING
// machine-genre clusters from the denominator, then ranks + caps the survivors.
// Issues ONE gc.Call("thoughts", {charges_for}) + ONE fetchNodesByIDs for ALL
// cluster members before the per-cluster loop (T2-3 perf lock); the genre
// classification reuses that same nodeByID (no extra wire).
//
// influence is the per-thought influence vector (built ONCE by the caller via
// BuildTrustMatrix+ComputeInfluenceVector) — clusterInfluence is the sum over a
// cluster's members. sessionByThought maps a thought to its enclosing session
// label, feeding the genre classifier's session-marker facet.
//
// Genre exclusion: a cluster whose MAJORITY of members classify as machine-genre
// (dream/worker-generated) is DROPPED from the spots slice entirely and counted
// into ExcludedMachineGenre — a machine cluster is charge-thin by construction, so
// counting it as a blind spot is noise. Surviving human-genre clusters keep the
// under-evidenced gate (charges/Size < 1.0 OR bridges present), then RANK by
// Impact = Size × clusterInfluence × chargeThinness descending and CAP to
// blindSpotReportCap.
func ReflectBlindSpots(ctx context.Context, gc Caller, clusters []ThoughtCluster, adj map[string][]string, influence map[string]float64, sessionByThought map[string]string) BlindSpotResult {
	// Flatten members for one bulk fetch.
	var allMembers []string
	for _, c := range clusters {
		allMembers = append(allMembers, c.ThoughtIDs...)
	}
	charges := fetchChargesFor(ctx, gc, allMembers)

	// One bulk hydrate for bridge labels AND the genre classifier's source/origin
	// facets (reused — no extra wire).
	nodeByID := fetchNodesByIDs(ctx, gc, allMembers)

	result := BlindSpotResult{TotalClusters: len(clusters)}
	var spots []BlindSpotReport
	for _, c := range clusters {
		// Genre exclusion: a majority-machine cluster never enters the denominator.
		if clusterIsMachineGenre(c.ThoughtIDs, nodeByID, sessionByThought) {
			result.ExcludedMachineGenre++
			continue
		}

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
			result.TotalUnderEvidenced++

			// clusterInfluence = sum of member influence; chargeThinness in [0,1]
			// (1 = no charges, 0 = ≥1 charge/member). Impact rewards a large,
			// central, charge-thin region.
			var clusterInfluence float64
			for _, tid := range c.ThoughtIDs {
				clusterInfluence += influence[tid]
			}
			chargeThinness := 1.0 - math.Min(1.0, float64(totalCharges)/float64(c.Size))
			impact := float64(c.Size) * clusterInfluence * chargeThinness

			spots = append(spots, BlindSpotReport{
				Cluster:        c,
				ChargeCount:    totalCharges,
				AvgMagnitude:   c.AvgMagnitude,
				BridgeThoughts: bridges,
				Influence:      clusterInfluence,
				Impact:         impact,
			})
		}
	}

	// Rank by Impact descending, with a deterministic cluster-ID tie-break so
	// equal-impact rows order reproducibly, then cap.
	sort.Slice(spots, func(i, j int) bool {
		if spots[i].Impact != spots[j].Impact {
			return spots[i].Impact > spots[j].Impact
		}
		return spots[i].Cluster.ID < spots[j].Cluster.ID
	})
	if len(spots) > blindSpotReportCap {
		spots = spots[:blindSpotReportCap]
	}
	result.Spots = spots
	return result
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
