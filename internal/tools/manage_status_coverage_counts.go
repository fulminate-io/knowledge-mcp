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
// its own field choice gets added without anyone noticing. GraphEmbeddedCount and
// GraphCoverageCounts — the exported (gt, name) forms every consumer calls — MOVED
// HERE from manage_status_coverage.go when that file reached the cap again, along
// with statusGraphTarget, the selector rule they share. That paragraph used to say
// they stayed there and delegated here; they now sit beside the halves they
// delegate to, which is where a reader looking for either one goes.

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
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

// GraphEmbeddedCount is the SINGLE definition of a graph's "embedded count" — the
// denominator BOTH the coverage-ratio auto-heal (lever 2, via bootstrap) and the
// manage(status) segment-coverage column (lever 3) compare segment-covered docs
// against, so the definition cannot drift between them. It issues ONE Stats RPC
// with IncludeCoverage:true (the same seam renderLLMCoverage uses) and returns
// GraphStats.BinaryVectorCount — the count of nodes with a stored binary vector.
//
// gc is deps.GraphCaller(); when it does not satisfy the Stats seam (a router-less
// fixture / degraded headless mode) the helper returns (0, nil) — a zero embedded
// count, which the heal probe reads as "no coverage signal" and the status column
// renders as a placeholder. The DEFAULT knowledge graph (empty instance name) uses
// the empty-name GraphSelector{Graph:""}, mirroring renderLLMCoverage's
// knowledge-row handling.
//
// The selector-addressed graphEmbeddedCountFor it delegates to, and the
// both-counts helper THAT now projects from, live in
// manage_status_coverage_counts.go.
func GraphEmbeddedCount(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, name string) (int, error) {
	return graphEmbeddedCountFor(ctx, gc, statusGraphTarget(gt, name))
}

// statusGraphTarget builds the Stats target for ONE NAMED graph in the status
// coverage table.
//
// ONE FAMILY NEEDS MORE THAN THE DERIVATION, and it is named here rather than
// duplicated at the two call sites below: the DEFAULT knowledge graph (empty
// instance name) addresses as an empty selector, mirroring renderLLMCoverage's
// knowledge-row handling.
//
// PRACTICE USED TO NEED ONE TOO, through the legacy read selector, and no longer
// does. The family became a singleton, so graphsel puts no instance field on its
// selector — right for a write and for an unselected read, and wrong while this
// table walked a catalog of eight practice graphs: a derived target would have
// asked about the combined graph once per name and printed the same numbers down
// every row, and a repeated number reads as a working table. The catalog holds
// one practice graph now, so the derivation is right and the one row it produces
// carries that graph's own counts.
func statusGraphTarget(gt kgtypes.GraphType, name string) *knowledgev1.GraphSelector {
	if gt == kgtypes.GraphKnowledge && name == "" {
		return &knowledgev1.GraphSelector{Graph: ""}
	}
	return graphsel.GraphSelectorFor(gt, name, false)
}

// GraphCoverageCounts returns the FULL on-demand LLM-coverage set for one graph,
// off the SAME single Stats RPC GraphEmbeddedCount uses.
//
// IT IS THE WIDER PROJECTION OF ONE READ, NOT A SECOND READ. GraphEmbeddedCount
// takes one field off a response that already carries six; a caller needing the
// failure counts alongside the embedded count therefore had to issue a second
// Stats call, and two calls mean two snapshots that can disagree about the same
// graph. This returns all of them from one, so a consumer comparing them is
// comparing numbers taken at the same instant.
//
// It carries the same (gt, name) special case as GraphEmbeddedCount above: the
// unnamed knowledge graph addresses as an empty selector rather than by name.
func GraphCoverageCounts(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, name string) (GraphCoverage, error) {
	return graphCoverageFor(ctx, gc, statusGraphTarget(gt, name))
}
