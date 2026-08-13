// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sort"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// InfluenceReport shows a thought's influence on the global consensus.
type InfluenceReport struct {
	ThoughtID      string
	Node           *knowledgev1.Node
	InfluenceScore float64
	Properties     ThoughtProperties
}

// InfluenceRanking is the two-section result of ReflectInfluence. Evidenced
// holds the charged thoughts that survived the top-N cut; BackfillCandidates
// holds the zero-charge structural hubs, kept in a separate labeled section so a
// consumer never mistakes near-uniform eigenvector mass for evidence.
type InfluenceRanking struct {
	Evidenced          []InfluenceReport
	BackfillCandidates []InfluenceReport
}

// ReflectInfluence returns the evidence-aware two-section influence ranking.
// SELECTION is charge-aware BEFORE the influence cut: thoughts are partitioned
// into charged vs zero-charge, then each section is ranked and truncated to
// limit independently. This is deliberately NOT a top-N-by-influence headroom
// window — on the live corpus the influence head is near-uniform zero-charge
// structural hubs (~0.0005 each), so a charge-blind top-N would drop every
// charged peripheral thought and leave the evidenced section empty.
//
// Evidenced = the CHARGED thoughts ranked by influence×(1+chargeWeight) desc,
// truncated to limit. chargeWeight is total charge magnitude (PositiveWeight +
// NegativeWeight); a heavily-contested/refuted thought ranks as
// influential-with-evidence BY DESIGN, consistent with Magnitude's total-weight
// semantics — contestation is evidence, not its absence.
// BackfillCandidates = the ZERO-CHARGE structural hubs ranked by raw influence
// desc, truncated to limit, surfaced in their own labeled section so a consumer
// never mistakes near-uniform eigenvector mass for evidence.
//
// Wire cost (non-profile path): ONE thought-ID list, ONE adjacency + ONE
// full-corpus charge read FUSED inside BuildTrustMatrixWithCharges (the charge
// map is reused for the partition — no second full-corpus charge fetch), and ONE
// node hydrate BOUNDED to the selected charged set ∪ the top-limit backfill hubs
// (never the full corpus). The profile path additionally pays the pre-existing
// full-corpus node hydrate for cluster_id metadata and one explicit charge fetch.
//
// sortMode selects the DISPLAY ordering of the Evidenced section ONLY. "" /
// "influence" leaves the influence×(1+chargeWeight) selection order intact.
// "composite" reorders the ALREADY-SELECTED evidenced set by
// InfluenceScore×Magnitude descending — a within-set display reorder ONLY; it
// never changes which thoughts are selected and does not touch BackfillCandidates.
func ReflectInfluence(ctx context.Context, gc Caller, limit int, profile *PersonalityProfile, sortMode string) (InfluenceRanking, error) {
	if limit <= 0 {
		limit = 10
	}
	thoughtIDs, err := listAllThoughtIDs(ctx, gc)
	if err != nil {
		return InfluenceRanking{}, err
	}
	if len(thoughtIDs) == 0 {
		return InfluenceRanking{}, nil
	}

	// One now for the whole pass: the recency-weighted SelfTrust diagonal and the
	// partition's per-thought props must be computed at a single consistent instant.
	now := time.Now()

	// Need cluster_id metadata for personality-adjusted matrix — pull
	// the node bulk once.
	var nodeByID map[string]*knowledgev1.Node
	if profile != nil {
		nodeByID = fetchNodesByIDs(ctx, gc, thoughtIDs)
	}

	var matrix TrustMatrix
	var chargeMap map[string][]*knowledgev1.Node
	if profile != nil {
		matrix, err = BuildTrustMatrixWithPersonality(ctx, gc, thoughtIDs, *profile, nodeByID, now, nil)
		if err != nil {
			return InfluenceRanking{}, err
		}
		// The personality builder re-weights self-trust but does not surface the
		// charge map; the per-thought charge data the partition needs is the same
		// full-corpus read. Threading the map through the personality builder would
		// widen a second signature with more callers, out of proportion to the gain,
		// so this rare profile path pays ONE explicit full-corpus charge fetch.
		chargeMap = fetchChargesFor(ctx, gc, thoughtIDs, nil)
	} else {
		// Non-profile (default) path: the charge map is FUSED with the matrix build
		// — reused for the partition, no second full-corpus charge read.
		matrix, chargeMap, err = BuildTrustMatrixWithCharges(ctx, gc, thoughtIDs, now, nil)
		if err != nil {
			return InfluenceRanking{}, err
		}
	}

	influence := ComputeInfluenceVector(matrix)
	ranking := partitionInfluenceRanking(ctx, gc, thoughtIDs, influence, chargeMap, limit, now)

	// composite: a within-evidenced-set display reorder by InfluenceScore×Magnitude.
	// The selection is unchanged — the evidence-weighted rank already selected the
	// evidenced set above; this only reshuffles the already-chosen reports.
	// sort.SliceStable keeps the selection order as the tie-break so equal-product
	// rows stay deterministic. BackfillCandidates is never reordered.
	if sortMode == "composite" {
		sort.SliceStable(ranking.Evidenced, func(i, j int) bool {
			pi := ranking.Evidenced[i].InfluenceScore * ranking.Evidenced[i].Properties.Magnitude
			pj := ranking.Evidenced[j].InfluenceScore * ranking.Evidenced[j].Properties.Magnitude
			return pi > pj
		})
	}
	return ranking, nil
}

// partitionInfluenceRanking splits thoughtIDs into the charged (Evidenced) and
// zero-charge (BackfillCandidates) sections off the already-fetched charge map,
// ranks each section, truncates both to limit, and hydrates ONLY the selected
// node set. props is computed once per id from chargeMap (no extra wire) and
// reused when building the reports. The node hydrate is the perf lock: ONE bounded
// fetchNodesByIDs over the selected charged set ∪ the top-limit backfill hubs only
// — never the full corpus.
func partitionInfluenceRanking(ctx context.Context, gc Caller, thoughtIDs []string, influence map[string]float64, chargeMap map[string][]*knowledgev1.Node, limit int, now time.Time) InfluenceRanking {
	type scoredID struct {
		id        string
		influence float64
		weighted  float64 // influence × (1 + chargeWeight)
		props     ThoughtProperties
	}
	var charged, backfill []scoredID
	for _, id := range thoughtIDs {
		props := computePropertiesFromCharges(chargeMap[id], now)
		inf := influence[id]
		if props.ChargeCount > 0 {
			chargeWeight := props.PositiveWeight + props.NegativeWeight
			charged = append(charged, scoredID{id: id, influence: inf, weighted: inf * (1 + chargeWeight), props: props})
		} else {
			backfill = append(backfill, scoredID{id: id, influence: inf, props: props})
		}
	}
	sort.SliceStable(charged, func(i, j int) bool { return charged[i].weighted > charged[j].weighted })
	sort.SliceStable(backfill, func(i, j int) bool { return backfill[i].influence > backfill[j].influence })
	if len(charged) > limit {
		charged = charged[:limit]
	}
	if len(backfill) > limit {
		backfill = backfill[:limit]
	}

	selectedIDs := make([]string, 0, len(charged)+len(backfill))
	for _, r := range charged {
		selectedIDs = append(selectedIDs, r.id)
	}
	for _, r := range backfill {
		selectedIDs = append(selectedIDs, r.id)
	}
	selNodes := fetchNodesByIDs(ctx, gc, selectedIDs)

	toReports := func(scored []scoredID) []InfluenceReport {
		reports := make([]InfluenceReport, 0, len(scored))
		for _, r := range scored {
			n, ok := selNodes[r.id]
			if !ok {
				continue
			}
			reports = append(reports, InfluenceReport{
				ThoughtID:      r.id,
				Node:           n,
				InfluenceScore: r.influence,
				Properties:     r.props,
			})
		}
		return reports
	}
	return InfluenceRanking{
		Evidenced:          toReports(charged),
		BackfillCandidates: toReports(backfill),
	}
}
