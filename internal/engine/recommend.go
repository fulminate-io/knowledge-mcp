// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"slices"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// recommend.go owns the CLIENT-side recommendation/render cluster the
// query(mode: metadata_stats) handler renders into the "Recommended Action"
// column. Relocated from
// cmd/knowledge-server/internal/store/node_value_stats_recommend.go +
// node_value_stats.go (T5.5): the descriptive RecommendAction + evaluate now
// run CLIENT-side over the proto knowledgev1.KeyStats/OverrideConfig carriers,
// while the server store package KEEPS its own Apply/ForceDecision/Decision
// (cmd/knowledge-server/internal/store/node_value_stats_recommend.go, consumed by
// node_value_apply.go and cloud/store/promotion_decision.go) over its
// store.KeyStats/OverrideConfig.
//
// evaluate() is intentionally duplicated across the boundary: both derive from
// the SAME hysteresis thresholds (1000/3000 distinct, 5/3 median) — the client
// reads them off the proto accessors, the server off the store fields. The
// parity is pinned by recommend_test.go (client) + the store-side Apply test.

// Representation describes how a metadata key is physically stored. Mirrors the
// server store's Representation string consts
// (cmd/knowledge-server/internal/store/node_value_registry.go) — the proto
// KeyStats.current_representation carries the same string values ("", "scalar",
// "edge"), so the client compares against these consts directly.
type Representation = string

const (
	// RepresentationAuto is the zero value: no recorded decision for the key.
	RepresentationAuto Representation = ""
	// RepresentationScalar means the key is stored inline in Node.Metadata.
	RepresentationScalar Representation = "scalar"
	// RepresentationEdge means the key is stored as an edge to a shared value-node.
	RepresentationEdge Representation = "edge"
)

// Recommendation is the descriptive action RecommendAction returns. Stringly
// typed so the renderer surfaces it directly in the markdown table without an
// enum→string conversion. The constants are the canonical strings — callers
// must NOT compare against literals.
type Recommendation string

const (
	// RecommendForceEdge means an OverrideConfig.ForceEdge entry pins the key to
	// edge representation regardless of cardinality.
	RecommendForceEdge Recommendation = "FORCE_EDGE"

	// RecommendForceScalar means an OverrideConfig.ForceScalar entry pins the key
	// to scalar representation regardless of cardinality.
	RecommendForceScalar Recommendation = "FORCE_SCALAR"

	// RecommendPromote means the key is currently scalar and the stats show it
	// would benefit from edge representation (distinct < 1000 AND median ≥ 5).
	RecommendPromote Recommendation = "PROMOTE"

	// RecommendDemote means the key is currently edge and the stats show it has
	// outgrown the edge representation (distinct > 3000 OR median < 3).
	RecommendDemote Recommendation = "DEMOTE"

	// RecommendKeepScalar means the key is currently scalar and the stats do not
	// justify a flip.
	RecommendKeepScalar Recommendation = "KEEP (scalar)"

	// RecommendKeepEdge means the key is currently edge and the stats do not
	// justify a flip.
	RecommendKeepEdge Recommendation = "KEEP (edge)"
)

// Hysteresis thresholds, pinned by the Phase 4 design decisions:
//
//   - promoteDistinctMax: scalar→edge promotion needs distinct values below this
//     cap (1000 is the value-node sweet spot).
//   - promoteMedianMin: scalar→edge promotion needs at least this many nodes
//     sharing the median value (5 is the dedupe break-even).
//   - demoteDistinctMin: edge→scalar demotion fires when distinct values cross
//     this cap (3000 leaves a 2x band above promoteDistinctMax so a key
//     oscillating around 1000-2000 doesn't flap).
//   - demoteMedianMax: edge→scalar demotion fires when the median drops below
//     this many nodes/value (3 leaves a 2x band below promoteMedianMin).
const (
	promoteDistinctMax int64 = 1000
	promoteMedianMin   int64 = 5
	demoteDistinctMin  int64 = 3000
	demoteMedianMax    int64 = 3
)

// RecommendAction returns the descriptive recommendation string for ks under
// ovr. Order of operations matches the locked design:
//
//  1. ForceEdge override → FORCE_EDGE (highest precedence; never flipped).
//  2. ForceScalar override → FORCE_SCALAR (same precedence as ForceEdge;
//     ForceEdge wins on conflict).
//  3. Currently scalar + distinct < 1000 AND median ≥ 5 → PROMOTE.
//  4. Currently edge + (distinct > 3000 OR median < 3) → DEMOTE.
//  5. Else: KEEP (scalar) or KEEP (edge) per CurrentRepresentation.
//
// nil ks / nil ovr are defended (the proto getters are nil-safe). Operates over
// the proto carriers threaded straight from resp.GetMetadataStats().GetKeys()
// + resp.GetOverrideConfig() — no decode step.
func RecommendAction(ks *knowledgev1.KeyStats, ovr *knowledgev1.OverrideConfig, key string) Recommendation {
	return evaluate(ks, ovr, key)
}

// evaluate is the single source of truth for the client-side recommendation
// thresholds. It reads the proto accessors (nil-safe) rather than store fields:
// ovr.GetForceScalar()/GetForceEdge() for the override precedence, and
// ks.GetDistinctValues()/GetMedianNodesPerValue()/GetCurrentRepresentation()
// for the hysteresis bands. Mirrors the server store's evaluate() over store
// types.
func evaluate(ks *knowledgev1.KeyStats, ovr *knowledgev1.OverrideConfig, key string) Recommendation {
	if isInList(ovr.GetForceEdge(), key) {
		return RecommendForceEdge
	}
	if isInList(ovr.GetForceScalar(), key) {
		return RecommendForceScalar
	}
	if ks == nil {
		return RecommendKeepScalar
	}
	switch ks.GetCurrentRepresentation() {
	case RepresentationEdge:
		if ks.GetDistinctValues() > demoteDistinctMin || ks.GetMedianNodesPerValue() < demoteMedianMax {
			return RecommendDemote
		}
		return RecommendKeepEdge
	default: // RepresentationAuto + RepresentationScalar
		if ks.GetDistinctValues() < promoteDistinctMax && ks.GetMedianNodesPerValue() >= promoteMedianMin {
			return RecommendPromote
		}
		return RecommendKeepScalar
	}
}

// isInList returns true when needle is in haystack. OverrideConfig slices are
// short (0-10 entries per graph) so a linear scan dominates a map allocation.
func isInList(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}

// liveDistinctValues returns the current distinct-value count, preferring the
// live ValueDistribution map (authoritative for in-memory observations) and
// falling back to the persisted DistinctValues field when the distribution is
// empty. Free-func port of store.KeyStats.LiveDistinctValues over the proto
// KeyStats (the proto getters are nil-safe).
func liveDistinctValues(ks *knowledgev1.KeyStats) int64 {
	if ks == nil {
		return 0
	}
	if n := int64(len(ks.GetValueDistribution())); n > 0 {
		return n
	}
	return ks.GetDistinctValues()
}

// liveMedianNodesPerValue mirrors liveDistinctValues for the median — computes
// from ValueDistribution when populated, falls back to the persisted
// MedianNodesPerValue otherwise. Free-func port of
// store.KeyStats.LiveMedianNodesPerValue over the proto KeyStats.
func liveMedianNodesPerValue(ks *knowledgev1.KeyStats) int64 {
	if ks == nil {
		return 0
	}
	if len(ks.GetValueDistribution()) > 0 {
		return medianCount(ks.GetValueDistribution())
	}
	return ks.GetMedianNodesPerValue()
}

// medianCount returns the median of the value-distribution counts. Byte-faithful
// port of store.medianCount (node_value_stats_topn.go:33): a counts-only O(n)
// sort, returning the upper-middle element counts[n/2] for BOTH odd and even
// lengths (NOT the two-element average) so the client answer is identical to the
// store. Empty → 0. Examples: {a:2,b:4}→sorted[2,4]→counts[1]=4 (upper middle).
func medianCount(dist map[string]int64) int64 {
	n := len(dist)
	if n == 0 {
		return 0
	}
	counts := make([]int64, 0, n)
	for _, c := range dist {
		counts = append(counts, c)
	}
	slices.Sort(counts)
	return counts[n/2]
}
