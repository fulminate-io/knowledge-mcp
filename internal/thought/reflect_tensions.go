// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"math"
	"sort"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// reflect_tensions.go holds the tensions reflective surface — the machine-edge
// excluded, cluster-pair-collapsed, ranked-and-capped tension computation
// (ReflectTensions) and its helpers. Split out of reflect.go (pure relocation) to
// keep both files under the 500-line cap.

// tensionReportCap bounds the number of tension representatives ReflectTensions
// returns after the cluster-pair collapse + rank, keeping the surface in-context
// for an LLM consumer (one representative per cluster-pair, capped).
const tensionReportCap = 25

// TensionReport identifies two connected nodes (thought/finding/research) with
// opposing valence. The provenance + collapse fields (Method/EdgeType/ClusterA/
// ClusterB/PairCount/DistinctEvidence) make a row judgeable in-context without a
// follow-up query: they name the linking edge's provenance, the cluster pair the
// representative stands for, how many candidate pairs collapsed into it, and the
// distinct charge evidence backing it.
type TensionReport struct {
	NodeA        *knowledgev1.Node
	NodeB        *knowledgev1.Node
	PropertiesA  ThoughtProperties
	PropertiesB  ThoughtProperties
	ValenceDelta float64
	// Method is the linking edge's provenance tag (empty for a human-authored
	// mutate(link); machine tags never reach here — they are pre-filtered out).
	Method string
	// EdgeType is the linking edge's type (e.g. relates-to, contradicts).
	EdgeType string
	// ClusterA / ClusterB are the cluster_id metadata of the two thoughts (empty
	// when a thought carries no persisted cluster_id yet).
	ClusterA string
	ClusterB string
	// PairCount is how many candidate tension pairs this representative collapses
	// (1 when the cluster-pair had a single qualifying pair).
	PairCount int
	// DistinctEvidence is the count of distinct charge nodes across the pair —
	// the evidence depth backing the representative, used in the rank.
	DistinctEvidence int
}

// ReflectTensions finds pairs of thoughts with opposing valence that are joined
// by an EXPLICIT, HUMAN thought↔thought reasoning edge, then COLLAPSES the
// candidate pairs to one representative per cluster-pair, ranks them, and caps the
// result at tensionReportCap. It pairs over fetchTensionEdges, NOT
// fetchAdjacency("all"): bare co-session membership no longer makes two thoughts a
// tension, and machine relates-to edges (densify/similarity/tree/artifact links)
// are pre-filtered out — only a real human reasoning edge between them does.
//
// The collapse removes the clique-amplification artifact: one hub thought
// human-edged to many siblings would otherwise emit one row per sibling. Grouping
// by the normalized cluster-pair key (min(cidA,cidB):max(cidA,cidB), read from
// the persisted cluster_id metadata) keeps a single max-delta representative per
// cluster-pair carrying PairCount (how many candidate pairs collapsed) and
// DistinctEvidence (distinct charge nodes across the representative pair).
// Thoughts with no persisted cluster_id form their own singleton group keyed by
// thought id so they are never collapsed together. Representatives rank by
// evidence-weighted delta (delta × (1+DistinctEvidence)) descending.
//
// Issues the tension-local reads (ONE bulk node browse + ONE bulk edge read over
// tensionEdgeTypes via fetchTensionEdges) + ONE bulk fetchNodesByIDs + ONE bulk
// fetchChargesFor (T2-3 perf lock). NO per-thought wire calls inside the loop; the
// group-by + rank are pure in-memory over the already-fetched nodeByID + charges.
func ReflectTensions(ctx context.Context, gc Caller) ([]TensionReport, error) {
	nodeIDs, humanEdges, idSet, err := fetchTensionEdges(ctx, gc)
	if err != nil {
		return nil, err
	}
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	nodeByID := fetchNodesByIDs(ctx, gc, nodeIDs)
	charges := fetchChargesFor(ctx, gc, nodeIDs)

	now := time.Now()
	propsCache := make(map[string]ThoughtProperties, len(nodeIDs))
	for _, id := range nodeIDs {
		propsCache[id] = computePropertiesFromCharges(charges[id], now)
	}

	candidates := buildTensionCandidates(humanEdges, idSet, propsCache, nodeByID, charges)
	tensions := collapseTensionsByCluster(candidates, nodeByID)
	return rankAndCapTensions(tensions), nil
}

// tensionCandidate is one qualifying tension pair before the cluster-pair collapse,
// carrying the linking edge's provenance so the representative can surface it.
type tensionCandidate struct {
	idA, idB string
	report   TensionReport
}

// buildTensionCandidates filters the human edge slice to qualifying tension pairs:
// both endpoints in idSet, both magnitude≥0.5, |Δvalence|≥0.5, deduped undirected.
// Each candidate carries the linking edge's Method + Type + the pair's cluster ids
// + distinct charge evidence count.
func buildTensionCandidates(humanEdges []*knowledgev1.Edge, idSet map[string]bool, propsCache map[string]ThoughtProperties, nodeByID map[string]*knowledgev1.Node, charges map[string][]*knowledgev1.Node) []tensionCandidate {
	seen := make(map[string]bool)
	var candidates []tensionCandidate
	for _, e := range humanEdges {
		a, b := e.FromId, e.ToId
		if !idSet[a] || !idSet[b] || a == b || seen[a+":"+b] || seen[b+":"+a] {
			continue
		}
		seen[a+":"+b] = true
		pA, okA := propsCache[a]
		pB, okB := propsCache[b]
		if !okA || !okB || pA.Magnitude < 0.5 || pB.Magnitude < 0.5 {
			continue
		}
		delta := math.Abs(pA.Valence - pB.Valence)
		if delta < 0.5 {
			continue
		}
		nA, hasA := nodeByID[a]
		nB, hasB := nodeByID[b]
		if !hasA || !hasB {
			continue
		}
		candidates = append(candidates, tensionCandidate{
			idA: a, idB: b,
			report: TensionReport{
				NodeA:            nA,
				NodeB:            nB,
				PropertiesA:      pA,
				PropertiesB:      pB,
				ValenceDelta:     delta,
				Method:           e.Method,
				EdgeType:         e.Type,
				ClusterA:         clusterIDOf(nodeByID, a),
				ClusterB:         clusterIDOf(nodeByID, b),
				DistinctEvidence: distinctChargeEvidence(charges, a, b),
			},
		})
	}
	return candidates
}

// clusterIDOf reads a thought's persisted cluster_id metadata (empty when absent).
func clusterIDOf(nodeByID map[string]*knowledgev1.Node, id string) string {
	if n, ok := nodeByID[id]; ok {
		return kgtypes.Value(n, "cluster_id")
	}
	return ""
}

// distinctChargeEvidence counts the distinct charge node ids across two thoughts.
func distinctChargeEvidence(charges map[string][]*knowledgev1.Node, a, b string) int {
	ids := make(map[string]bool, len(charges[a])+len(charges[b]))
	for _, c := range charges[a] {
		ids[c.Id] = true
	}
	for _, c := range charges[b] {
		ids[c.Id] = true
	}
	return len(ids)
}

// tensionGroupKey collapses an (a,b) pair to its normalized cluster-pair key. A
// thought with no persisted cluster_id forms its own singleton group keyed by
// thought id so unassigned thoughts are never collapsed together.
func tensionGroupKey(nodeByID map[string]*knowledgev1.Node, a, b string) string {
	cA, cB := clusterIDOf(nodeByID, a), clusterIDOf(nodeByID, b)
	if cA == "" {
		cA = "thought:" + a
	}
	if cB == "" {
		cB = "thought:" + b
	}
	if cA > cB {
		cA, cB = cB, cA
	}
	return cA + "|" + cB
}

// collapseTensionsByCluster groups candidate pairs by normalized cluster-pair key,
// keeping the max-|delta| representative per group and setting PairCount to the
// group size.
func collapseTensionsByCluster(candidates []tensionCandidate, nodeByID map[string]*knowledgev1.Node) []TensionReport {
	type groupAgg struct {
		rep   TensionReport
		count int
	}
	groups := make(map[string]*groupAgg)
	for _, c := range candidates {
		key := tensionGroupKey(nodeByID, c.idA, c.idB)
		g, ok := groups[key]
		if !ok {
			rep := c.report
			rep.PairCount = 1
			groups[key] = &groupAgg{rep: rep, count: 1}
			continue
		}
		g.count++
		if c.report.ValenceDelta > g.rep.ValenceDelta {
			g.rep = c.report
		}
	}
	tensions := make([]TensionReport, 0, len(groups))
	for _, g := range groups {
		g.rep.PairCount = g.count
		tensions = append(tensions, g.rep)
	}
	return tensions
}

// rankAndCapTensions sorts tensions by evidence-weighted delta descending (with a
// deterministic thought-id tie-break) and caps the slice to tensionReportCap.
func rankAndCapTensions(tensions []TensionReport) []TensionReport {
	weighted := func(r TensionReport) float64 { return r.ValenceDelta * float64(1+r.DistinctEvidence) }
	sort.Slice(tensions, func(i, j int) bool {
		wi, wj := weighted(tensions[i]), weighted(tensions[j])
		if wi != wj {
			return wi > wj
		}
		return tensions[i].NodeA.Id < tensions[j].NodeA.Id
	})
	if len(tensions) > tensionReportCap {
		tensions = tensions[:tensionReportCap]
	}
	return tensions
}
