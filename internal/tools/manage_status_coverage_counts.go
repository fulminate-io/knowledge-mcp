// SPDX-License-Identifier: Apache-2.0

package tools

// manage_status_coverage_counts.go — the selector-addressed per-graph COUNT
// reads: the embedded (binary-vector) count on its own, and the node count
// alongside it.
//
// SPLIT FROM manage_status_coverage.go FOR THE LINE BUDGET, the same reason its
// _collect / _erasure / _evicted siblings were split. The two helpers move
// TOGETHER and stay adjacent deliberately: the second is an additive split of the
// first, and separating them is how a third helper with its own Stats call and
// its own field choice gets added without anyone noticing. GraphEmbeddedCount —
// the exported (gt, name) form both consumers call — stays in
// manage_status_coverage.go and delegates here.

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// graphEmbeddedCountFor is the SELECTOR-ADDRESSED form of GraphEmbeddedCount,
// carrying the one number both share. It exists for the caller whose graph cannot
// be named by (gt, name) alone: a code BRANCH, which the server resolves from Repo
// AND Branch together (resolveCode → Scope), so a composed "repo@branch" in the
// repo field addresses a graph that does not exist rather than the branch.
//
// THE SPLIT IS ADDITIVE, and that is what keeps the single definition intact.
// GraphEmbeddedCount still builds its own (gt, name) selector and delegates here,
// so every consumer — the coverage-ratio auto-heal, the manage(status) column,
// and the unified-search completeness gate — reads the SAME field off the SAME
// one RPC. A second helper that issued its own Stats call with its own field
// choice is exactly the fork the single-definition rule forbids.
//
// Its CONTRACT is unchanged by the both-counts split below: still (0, nil) for a
// caller that does not satisfy the stats seam, still (0, err) on a failed Stats.
func graphEmbeddedCountFor(ctx context.Context, gc GraphCaller, target *knowledgev1.GraphSelector) (int, error) {
	cov, err := graphCoverageFor(ctx, gc, target)
	return cov.Embedded, err
}

// GraphCoverage is the WHOLE on-demand LLM-coverage set for one graph, read off a
// single Stats call.
//
// IT EXISTS BECAUSE THE READ ALREADY FETCHED ALL OF IT. The Stats request carries
// IncludeCoverage, so the response has already computed the summarized count, both
// failure counts and the non-proxy node count — the previous shape read two fields
// off that response and dropped the rest, so a caller needing a third had no choice
// but to issue a second call. Widening the one definition is what keeps "how a
// graph's coverage is read" a single behaviour; a sibling helper with its own Stats
// call and its own field choice is exactly the fork this file's header forbids.
//
// Measurable is the SEPARATE flag graphCoverageFor's doc explains: it distinguishes
// "we could not measure" from "we measured zero", which several consumers must tell
// apart and a bare zero cannot express.
type GraphCoverage struct {
	Nodes           int
	Embedded        int
	Summarized      int
	EmbedFailures   int
	SummaryFailures int
	NonProxyNodes   int
	Measurable      bool

	// EmbedFailuresHoldingVector is the subset of EmbedFailures whose nodes STILL
	// HOLD a vector, and EmbedFailuresHoldingVectorMeasured says whether the server
	// sent it at all.
	//
	// IT HAS ITS OWN MEASURABILITY FLAG RATHER THAN RIDING Measurable, because the
	// two answer different questions. Measurable is about the whole response — could
	// this client read a coverage set at all. This flag is about ONE field of a
	// response that read fine: a server that predates the count, or a backend that
	// does not compute it, answers every other field exactly and simply omits this
	// one. Folding the second into the first would either declare the whole coverage
	// set unmeasurable on such a server, or — far worse — read the omitted field's
	// zero as a measured zero, which is how an approximation gets promoted to an
	// exact result by nobody's decision.
	EmbedFailuresHoldingVector         int
	EmbedFailuresHoldingVectorMeasured bool
}

// graphCoverageFor is THE DEFINITION: one Stats call with IncludeCoverage,
// returning every coverage field that response carries. graphEmbeddedCountFor
// above is a projection of it, which is what keeps "how a graph's coverage is
// read" one behaviour rather than two.
//
// WHY Measurable IS A SEPARATE FIELD. graphEmbeddedCountFor answers (0, nil) for a
// caller with no stats seam, which is indistinguishable at its signature from a
// graph that genuinely has zero vectors. A caller deciding whether a zero-hit
// search means a MISSING ranked index or an EMPTY graph must tell those apart:
// one is "we could not measure", the other "we measured zero". Collapsing them
// would make the un-measurable case render as a confident zero, which is the
// exact failure the segment-gap notice exists to remove.
func graphCoverageFor(
	ctx context.Context, gc GraphCaller, target *knowledgev1.GraphSelector,
) (GraphCoverage, error) {
	sc, isStats := gc.(statsRPC)
	if !isStats {
		return GraphCoverage{}, nil
	}
	resp, serr := sc.Stats(ctx, &knowledgev1.StatsRequest{
		Target:          target,
		IncludeCoverage: true,
	})
	if serr != nil {
		return GraphCoverage{}, serr
	}
	stats := resp.GetGraphStats()
	return GraphCoverage{
		Nodes:           int(stats.GetNodeCount()),
		Embedded:        int(stats.GetBinaryVectorCount()),
		Summarized:      int(stats.GetSummarizedCount()),
		SummaryFailures: int(stats.GetSummaryFailureCount()),
		EmbedFailures:   int(stats.GetEmbedFailureCount()),
		NonProxyNodes:   int(stats.GetNonProxyNodeCount()),
		Measurable:      true,
		// PRESENCE IS READ OFF THE POINTER, never off the value. The generated
		// getter answers 0 for an absent field exactly as it does for a sent 0, so
		// asking the getter alone would erase the distinction the optional field was
		// added to carry.
		EmbedFailuresHoldingVector:         int(stats.GetEmbedFailureHoldingVectorCount()),
		EmbedFailuresHoldingVectorMeasured: stats != nil && stats.EmbedFailureHoldingVectorCount != nil,
	}, nil
}
