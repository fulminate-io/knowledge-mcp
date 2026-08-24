// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// renderLLMCoverage renders the per-graph durable LLM-coverage table surfaced by
// manage(status). For every sync-eligible graph instance it issues a Stats RPC
// WITH IncludeCoverage:true (the only caller that does — every other Stats path
// stays O(1)) and tabulates total / summarized / embedded / failure counts.
//
// It enumerates kgtypes.SyncEligibleGraphTypes() (knowledge, code, cloud, cicd,
// practice, linkage, transformers — the raw logs/web/pdf graphs that skip LLM
// processing are already filtered out). The DEFAULT knowledge graph reports an
// empty instance name, which listGraphNamesOfType drops, so the knowledge row is
// emitted EXPLICITLY via an empty-name GraphSelector{Graph:""} and the knowledge
// type is then skipped in the enumeration loop; all other types are enumerated
// via listGraphNamesOfType + graphsel.GraphSelectorFor.
//
// CoverageRow is the per-graph durable LLM-coverage fact, produced ONCE by
// collectCoverageRows and consumed by BOTH the markdown renderer
// (formatCoverageRow) and the manage(status) format:json emitter. The json tags
// are the wire contract the Daemon Status web Coverage card types against — they
// are PINNED snake_case, and the graph-label field's tag is `graph` (NOT `label`)
// even though the assembling variable is named label. has_segments is REQUIRED so
// a non-segment graph renders '—' on the web rather than a misleading
// 'shipped 0 · live 0'; the live-0 WARN signal is derived client-side (web) from
// live_resident vs seg_covered, only when has_segments is true.
//
// live_resident IS THE DISTINCT LIVE-SEARCHABLE COUNT, not the summed per-segment
// count it used to be. The old number was inflated 2-3x on measured graphs by
// documents resident in more than one segment, so any threshold tuned against it
// will read differently now. Three consequences for a consumer deriving a signal
// from it:
//
//   - The reporter is DELIBERATELY NO-LOAD: it must not put a cold-cache
//     List+Fetch on the serial assembly loop. A graph whose engine has not loaded
//     yet therefore reads live 0 legitimately. This is PRESERVED behavior, not
//     introduced — the previous reading was equally no-load, so boot-time live-0
//     already happens today.
//   - live can legitimately sit BELOW seg_covered without anything being wrong,
//     because seg_covered counts the shipped manifest while live counts distinct
//     searchable membership.
//   - So a live-0 WARN must be QUALIFIED — by daemon uptime, or by has_segments
//     together with a nonzero seg_covered — rather than fired on the bare zero.
//     Without that qualification the card alarms on every cold start.
type CoverageRow struct {
	Graph        string `json:"graph"`
	Total        int    `json:"total"`
	Summarized   int    `json:"summarized"`
	Embedded     int    `json:"embedded"`
	SegCovered   int    `json:"seg_covered"`
	LiveResident int    `json:"live_resident"`
	HasSegments  bool   `json:"has_segments"`
	SummaryFail  int    `json:"summary_fail"`
	EmbedFail    int    `json:"embed_fail"`
	// SegDisposition is the coverage band this row sits in — see
	// segCoverageDisposition. Additive: no pre-existing tag is renamed.
	SegDisposition string `json:"seg_disposition"`
	// RepairVerified reports whether the coverage BACKSTOP has verified this row's
	// band within its interval. It carries `json:"-"` deliberately: the ten-key wire
	// shape above is pinned, and this is an input to SegDisposition rather than a
	// value of its own.
	RepairVerified bool `json:"-"`
	// RetainedErasures / NewestErasureAgeNanos are the SERVER's view of the
	// deletion backlog: how many journal rows still await a consumer, and how old
	// the newest is. Zero-and-zero means no backlog; UNKNOWN has its own signal —
	// a negative age (see erasureAgeUnknown), and then the count is not meaningful.
	// They carry json:"-" for the reason RepairVerified does: the ten-key wire
	// shape above is PINNED by its own tests, and these are rendered cells rather
	// than values of that contract. Widening a pinned shape is a separate decision
	// from adding a column to the table.
	RetainedErasures      int   `json:"-"`
	NewestErasureAgeNanos int64 `json:"-"`
	// RebuildPosAgeNanos / MergePosAgeNanos are THIS CLIENT's view: how long since
	// each of its two erase-feed consumers last advanced. They answer the question
	// the server cannot — whether a consumer has stopped arriving at all, which is
	// precisely what an arrival-driven stall alarm can never report.
	//
	// ZERO MEANS NEVER STARTED, not "just advanced". A consumer with no recorded
	// position has not run, and rendering an age measured from the epoch would say
	// the opposite of the truth.
	RebuildPosAgeNanos int64 `json:"-"`
	MergePosAgeNanos   int64 `json:"-"`
	// StalledSinceNanos is 0 when this graph's coverage can still recover on its own,
	// and otherwise the wall-clock nanos at which it stopped being able to: the
	// earlier of the heal breaker's latch and the publish coverage gate's suppression.
	// It is what the stuck band renders an age from.
	//
	// InWorkingSet reports whether this client maintains the graph at all — a graph
	// no direct interaction has admitted is serviced by no background arm, which is
	// the intended steady state rather than a fault.
	//
	// Evicted reports that this client's residency budget has dropped the graph's
	// segment pool from memory — see DispositionEvicted (manage_status_coverage_evicted.go).
	//
	// ALL THREE carry `json:"-"` for the RepairVerified reason above: they are inputs
	// to SegDisposition, and the ten-key wire shape is pinned.
	StalledSinceNanos int64 `json:"-"`
	InWorkingSet      bool  `json:"-"`
	Evicted           bool  `json:"-"`
}

// repairVerifiedFrom is the three-clause formula behind CoverageRow.RepairVerified.
//
// ALL THREE CLAUSES ARE LOAD-BEARING and the middle one is the subtle one. ok is
// false when this process never loaded that graph's record, which reads as
// unverified — the honest answer rather than a degradation. Converged says the
// backstop has nothing to do for the graph. st.Scanned says something actually
// EXAMINED it: the backstop writes a converged record on two paths, and the one that
// merely DECLINED a graph leaves Scanned false. Without that clause a graph seeded
// while converged, then grown into the gap-repairing band by new embedded nodes,
// would print "gap-repairing" for up to a whole backstop interval about a row nothing
// had looked at — during exactly the window the backstop is skipping it. The last
// clause retires a verification once it is older than the interval it was good for.
func repairVerifiedFrom(st RepairVerification, ok bool, nowNanos int64) bool {
	return ok && st.Converged && st.Scanned &&
		nowNanos-st.VerifiedAtNanos < int64(SegmentRepairBackstopInterval)
}

// Coverage dispositions — the locked vocabulary segCoverageDisposition returns.
// They exist so an operator can tell a TRANSIENT drain lag from a PERMANENT hole:
// the two are visually identical without them, because a row covering far less
// than it has embedded renders exactly like a healthy one.
const (
	DispositionNoSegments   = "—"
	DispositionResidue      = "residue"
	DispositionConverged    = "converged"
	DispositionBelowFloor   = "below-floor"
	DispositionSelfHealing  = "self-healing"
	DispositionGapRepairing = "gap-repairing"
	// DispositionCacheAged is the honest answer for a row whose band the coverage
	// BACKSTOP has not verified. Under the merge architecture the backstop runs on a
	// slow interval rather than every boot, so "gap-repairing" would assert an
	// examination that may not have happened this process at all.
	DispositionCacheAged = "cache-aged"
	// DispositionStuck is the band for a graph this client maintains whose coverage
	// can no longer recover on its own: the heal breaker has latched it disarmed
	// (only a manual rebuild_segments or a restart re-arms) or the publish coverage
	// gate has suspended its republish. Both states can persist indefinitely, so the
	// row renders an age rather than a promise.
	DispositionStuck = "stuck"
	// DispositionUnmanaged is the band for a graph OUTSIDE the working set: no
	// background arm services it, so no arm will move it. It is not a fault and not a
	// stall — it is the intended state for a graph this client has not been asked to
	// work on — which is why it is a band of its own rather than either neighbor.
	DispositionUnmanaged = "unmanaged"
)

// segCoverageDisposition classifies one coverage row into the band it sits in.
//
// IT CLASSIFIES ON LiveResident, NOT SegCovered, and that is load-bearing rather
// than cosmetic. SegCovered is the SHIPPED count (the server manifest on cloud,
// the summed resident count on OSS); the repair arm triggers on the LIVE
// searchable count. Classifying on the shipped number would let this column read
// "converged" for a graph the arm is actively repairing, and "gap-repairing" for
// one it skips — the column would narrate a different graph than the system is
// acting on.
//
// BRANCH ORDER IS THE DESIGN, and three orderings are load-bearing rather than
// incidental:
//
//   - The residue arm must precede the ratio arms: when live exceeds embedded the
//     ratio test is ALSO true, so a classifier checking the band first would label
//     the hard-delete residue class as this gate's under-coverage hole.
//   - The unmanaged arm must FOLLOW the no-segments arm. A graph with no segment
//     pool has no coverage to manage, and its disposition IS the bare dash
//     (formatCoverageRow), so labeling it unmanaged would replace a correct answer
//     with a redundant one for every non-segment graph in the account.
//   - The stuck arm must PRECEDE the ratio arm. A latched or suppressed graph sits
//     in exactly the resident-below-ratio shape, so the two arms collide by
//     construction; whichever runs first wins. Placing stuck second would leave the
//     ratio arm calling the row self-healing — the claim this band exists to stop,
//     because nothing is healing it.
//
// The band arms delegate to SegmentCoverageFloor and CoverageRatioThreshold so the
// column and the auto-heal cannot disagree about which graphs self-heal.
//
// The honesty of the residue, converged, below-floor and ratio arms is exactly the
// honesty of LiveResident, which is the DISTINCT live-searchable count rather than
// the summed residency figure.
func segCoverageDisposition(r CoverageRow) string {
	switch {
	case !r.HasSegments:
		return DispositionNoSegments
	case !r.InWorkingSet:
		// No arm services a graph outside the working set, so no band describing what
		// an arm is doing about it can be true.
		return DispositionUnmanaged
	case r.Evicted:
		return DispositionEvicted
	case r.LiveResident > r.Embedded:
		// Live exceeds embedded: the hard-delete residue class, a different gate's
		// territory than this column's under-coverage band.
		return DispositionResidue
	case r.LiveResident == r.Embedded:
		return DispositionConverged
	case r.Embedded < SegmentCoverageFloor:
		// Below the floor the ratio arm is disarmed entirely; only the
		// zero-presence probe heals there.
		return DispositionBelowFloor
	case r.StalledSinceNanos > 0:
		// The graph is in the working set and below coverage, but the arm that would
		// heal it has given up — a latched breaker or a suspended republish. Reporting
		// the ratio band here would promise a recovery no arm is attempting.
		return DispositionStuck
	case float64(r.LiveResident) < CoverageRatioThreshold*float64(r.Embedded):
		return DispositionSelfHealing
	default:
		// ONLY THIS ARM CONSULTS THE BACKSTOP, and the restriction is the honesty
		// argument rather than caution. Every arm above is computed from LiveResident
		// and Embedded, which are LIVE readings taken this call — nothing about them is
		// cache-aged. What is unverified is specifically the claim THIS arm makes: that
		// the row's shortfall is a gap the repair arm is servicing. When the backstop
		// has not looked, that claim is unsupported, and this is the one cell that
		// should say so.
		if !r.RepairVerified {
			return DispositionCacheAged
		}
		return DispositionGapRepairing
	}
}

// renderLLMCoverage renders the per-graph durable LLM-coverage MARKDOWN table
// surfaced by the manage(status) TEXT body, delegating the per-graph facts to
// collectCoverageRows. Returns "" when there are no rows (stats seam unavailable
// or no eligible graphs) so the caller appends nothing.
func renderLLMCoverage(ctx context.Context, deps ClientDeps) string {
	rows := collectCoverageRows(ctx, deps)
	if len(rows) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## LLM coverage (durable, per-graph)\n")
	// summarized counts any non-empty Summary, which INCLUDES deterministic
	// auto-summaries — so it is most meaningful as a coverage signal for code
	// graphs, where Summary is populated only by the summarizer.
	sb.WriteString("_summarized = node has a non-empty Summary, which INCLUDES deterministic auto-summaries for structured nodes (decisions, findings, thoughts, etc.) — most meaningful as an LLM-coverage signal for code graphs, where Summary is populated only by the summarizer._\n\n")
	sb.WriteString("_the segment-coverage cell names INDEPENDENT counts and none of them bounds another, so they are labeled rather than joined with \"of\": `shipped N` sums the shipped manifest's doc counts (superseded generations included), `live M` is the distinct live-searchable count, and the embedded count has its own column. The bracketed term is that graph's coverage BAND, derived from the LIVE count, NOT the shipped one, and it names which arm owns the row: `self-healing` resolves within one reconcile interval, `gap-repairing` is the band the repair arm services, `cache-aged` is that same band on a graph the coverage backstop has not verified within its interval, `stuck` is a graph this client maintains whose heal breaker has latched or whose publish coverage gate has suspended its republish — no arm is servicing it, and the age is how long that has been true IN THIS PROCESS (a restart clears it), `unmanaged` is a graph this client has never searched, collected into or written to, so no background arm maintains it at all — the intended state for a graph you are not working on rather than a fault, and `evicted` is a graph whose segment pool this client's residency budget dropped from RAM to stay inside its byte ceiling — the segments are intact on local disk and the next search reloads them, so it is a memory-management state that needs NO operator action._\n\n")
	// The segment cell reads "shipped N · live M [band]". Shipped sums doc_counts
	// across the shipped manifest, in which superseded and hard-deleted generations
	// survive; live is the DISTINCT LIVE-SEARCHABLE doc count. Neither bounds the
	// other, so the cell labels the two rather than joining them with "of" — the
	// residue band is defined as live exceeding embedded, which makes the unordered
	// case a routine state rather than an anomaly. When live reads 0 the live
	// searchable pool has collapsed even though the server-shipped corpus is intact,
	// the post-restart incident the startup/periodic reconcile heals. Non-segment
	// graphs render "—".
	//
	// The embedded count is the band arms' denominator — the same BinaryVectorCount
	// the coverage-ratio auto-heal compares against (T3-2 single definition) — and it
	// keeps its own column rather than being repeated in this cell. A degenerate pool
	// therefore surfaces as the BAND, not as a ratio read off these two numbers.
	//
	// DO NOT read a live count sitting under the shipped one as a collapse. Shipped
	// counts the manifest while live counts distinct searchable membership, so live
	// sits under shipped routinely with nothing wrong. The live-0 signal is the
	// unambiguous one and is the one to keep — qualified by the fact that this read
	// does NOT load, so a graph whose engine has not warmed yet reads 0 legitimately.
	//
	// The bracketed term is the row's coverage BAND and it derives from the LIVE
	// count, not the shipped one. It also names which arm OWNS the row, and that is
	// what the vocabulary has to keep honest — a band naming an arm that is not
	// running is worse than no band at all:
	//
	//   - "self-healing" resolves itself within one reconcile interval, and
	//     "gap-repairing" is the band the repair arm services. These two are the
	//     easily-confused pair.
	//   - "stuck" is the same shortfall on a graph whose heal arm has GIVEN UP: the
	//     heal breaker latched (only a manual rebuild_segments or a restart re-arms)
	//     or the publish coverage gate suspended its republish. It renders an age,
	//     and that age is PROCESS-SCOPED — both stamps are per-process and a restart
	//     clears them, so it measures how long the graph has been stuck in THIS
	//     daemon, not since the condition first arose.
	//   - "unmanaged" is a graph outside the working set. Nothing services it because
	//     nothing is supposed to, so it gets its own band rather than borrowing one
	//     that would report a fault.
	sb.WriteString("| graph | total | summarized | embedded | segment coverage | summary-fail | embed-fail | erasure backlog |\n")
	sb.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range rows {
		sb.WriteString(formatCoverageRow(r))
		sb.WriteString("\n")
	}
	return sb.String()
}

// Coverage-heal band constants — the SINGLE definition of the band, shared by the
// auto-heal arm (via bootstrap) and the manage(status) coverage disposition. The
// auto-heal arm does not trigger only on a ZERO-segment pool; it also heals a
// DEGENERATE-but-nonzero pool (segments present, but covering far fewer docs than
// the graph has embedded). Two thresholds keep it from flapping:
//
//   - SegmentCoverageFloor: the absolute embedded-count MAGNITUDE below which the
//     ratio probe is NEVER consulted — a small graph (e.g. a handful of embedded
//     nodes) can legitimately sit in one small segment, so the ratio is noisy
//     there. Below the floor only the zero-segments probe heals (never the ratio),
//     so a tiny healthy graph never churns.
//   - CoverageRatioThreshold: covered/embedded BELOW this fraction marks the pool
//     degenerate (the live incident was ~6-of-60 shards covering a fraction of the
//     embedded corpus). At/above it the pool is healthy and the arm disarms.
//
// They live here, beside GraphEmbeddedCount, because they are the same "one
// definition, two consumers" fact: the manage(status) coverage disposition
// classifies against these same two numbers, so the column and the heal cannot
// disagree about which graphs self-heal.
const (
	SegmentCoverageFloor   = 64
	CoverageRatioThreshold = 0.5
	// SegmentRepairBackstopInterval is how long a converged graph's record is trusted
	// before the backstop re-verifies it. It is DURABLE (RepairState.VerifiedAtNanos)
	// rather than per-process precisely so a restart does not re-earn a scan the
	// previous process already paid for — a per-process clock would put the whole
	// corpus back on the boot path, which is the defect this gate exists to remove.
	// It lives here for the same "one definition, two consumers" reason
	// SegmentCoverageFloor and CoverageRatioThreshold do (see the block comment
	// above): the backstop gate and the manage(status) coverage column must not
	// disagree about how long a verification is good for.
	SegmentRepairBackstopInterval = 24 * time.Hour
)

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
func GraphEmbeddedCount(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, name string) (int, error) {
	target := graphsel.GraphSelectorFor(gt, name, false)
	if gt == kgtypes.GraphKnowledge && name == "" {
		target = &knowledgev1.GraphSelector{Graph: ""}
	}
	return graphEmbeddedCountFor(ctx, gc, target)
}

// graphEmbeddedCountFor is the SELECTOR-ADDRESSED form of GraphEmbeddedCount,
// carrying the one number both share. It exists for the caller whose graph cannot
// be named by (gt, name) alone: a code BRANCH, which the server resolves from Repo
// AND Branch together (resolveCode → Scope), so a composed "repo@branch" in the
// repo field addresses a graph that does not exist rather than the branch.
//
// THE SPLIT IS ADDITIVE, and that is what keeps the single definition intact.
// GraphEmbeddedCount above still builds its own (gt, name) selector and delegates
// here, so every consumer — the coverage-ratio auto-heal, the manage(status)
// column, and the unified-search completeness gate — reads the SAME field off the
// SAME one RPC. A second helper that issued its own Stats call with its own field
// choice is exactly the fork the single-definition rule forbids.
func graphEmbeddedCountFor(ctx context.Context, gc GraphCaller, target *knowledgev1.GraphSelector) (int, error) {
	sc, ok := gc.(statsRPC)
	if !ok {
		return 0, nil
	}
	resp, err := sc.Stats(ctx, &knowledgev1.StatsRequest{
		Target:          target,
		IncludeCoverage: true,
	})
	if err != nil {
		return 0, err
	}
	return int(resp.GetGraphStats().GetBinaryVectorCount()), nil
}

// segCoveredFor reads the SERVER-shipped HNSW-segment-covered doc count AND the
// LIVE in-memory engine resident doc count for a row's graph via the nil-safe
// SegmentCoverage seam. Segments exist for every graph kgtypes.HasRebuildableSegments
// admits — the embeddable builtins (knowledge, code, cloud, cicd, practice) — the
// SAME gate buildHealFactory and the manual rebuild_segments op use, so the status
// column reports coverage for exactly the graph set the auto-heal arm services. A
// graph with no rebuildable segments (linkage, transformers, and the raw graphs)
// returns (0, 0, false) and the column renders "—". When the seam is unwired
// (degraded headless mode) or the shipped probe errs, it also returns (0, 0, false)
// — a placeholder, not a hard failure of the status table. The live resident read is
// a single snapshot walk (no RPC and no load); it is surfaced so a live-pool
// collapse (live 0 while covered is N) is detectable instead of masked behind the
// shipped figure.
func segCoveredFor(ctx context.Context, deps ClientDeps, gt kgtypes.GraphType, name string) (covered, liveResident int, hasSeg bool) {
	if !kgtypes.HasRebuildableSegments(gt) {
		return 0, 0, false
	}
	sr := deps.SegmentCoverage()
	if sr == nil {
		return 0, 0, false
	}
	if poolEvictedFor(deps, gt, name) {
		return segCoveredForEvicted()
	}
	c, _, err := sr.ShippedSegmentDocCount(ctx, gt, name)
	if err != nil {
		return 0, 0, false
	}
	return c, sr.LiveResidentDocCount(gt, name), true
}

// newCoverageRow projects a per-graph GraphStats + segment-coverage triple into
// the shared CoverageRow. The embedded count is GraphStats.BinaryVectorCount —
// the SAME denominator the coverage-ratio auto-heal compares against (T3-2 single
// definition; do not fork it) and the segment-coverage cell's denominator.
func newCoverageRow(
	label string, st *knowledgev1.GraphStats, segCovered, liveResident int,
	hasSeg, repairVerified, inWorkingSet, evicted bool, stalledSinceNanos int64,
	rebuildPosAgeNanos, mergePosAgeNanos int64,
) CoverageRow {
	row := CoverageRow{
		Graph:                 label,
		RetainedErasures:      int(st.GetRetainedErasureCount()),
		NewestErasureAgeNanos: st.GetNewestErasureAgeNanos(),
		RebuildPosAgeNanos:    rebuildPosAgeNanos,
		MergePosAgeNanos:      mergePosAgeNanos,
		Total:                 int(st.GetNonProxyNodeCount()),
		Summarized:            int(st.GetSummarizedCount()),
		Embedded:              int(st.GetBinaryVectorCount()),
		SegCovered:            segCovered,
		LiveResident:          liveResident,
		HasSegments:           hasSeg,
		SummaryFail:           int(st.GetSummaryFailureCount()),
		EmbedFail:             int(st.GetEmbedFailureCount()),
		RepairVerified:        repairVerified,
		InWorkingSet:          inWorkingSet,
		Evicted:               evicted,
		StalledSinceNanos:     stalledSinceNanos,
	}
	// Assemble first, then classify, so the disposition reads exactly the values
	// that render.
	row.SegDisposition = segCoverageDisposition(row)
	return row
}

// formatCoverageRow renders one Markdown table row from a CoverageRow. An empty
// (zero-denominator) graph renders "(empty graph)" so a never-populated graph is
// visibly distinct from a covered one; otherwise summarized/embedded render as
// "X of N" so "0 of N summarized" is unambiguous against "N of N summarized".
//
// THE SEGMENT CELL CARRIES INDEPENDENT MEASUREMENTS AND NONE OF THEM BOUNDS
// ANOTHER, which is why it renders them as separately labeled terms —
// "shipped N · live M [band]" — rather than joining any two with "of":
//
//   - shipped (SegCovered) sums HNSW doc_counts across the SHIPPED manifest, in
//     which superseded and hard-deleted generations survive. On a churned graph it
//     routinely exceeds the embedded count by multiples.
//   - live (LiveResident) is the distinct live-searchable doc count. A collapsed
//     live pool reads "live 0" against an intact shipped figure instead of being
//     masked behind it.
//   - embedded (Embedded) counts live graph nodes holding a binary vector. It is
//     deliberately NOT repeated here — the table gives it its own column — and it
//     is the denominator the band arms classify against, never a bound on either
//     figure above.
//
// The bracketed term is the band, and it names which arm OWNS the row. That is
// also the proof the counts are unordered: the residue band is DEFINED as
// live > embedded, so the very containment an "of" would assert is a state the
// bands treat as ordinary. An earlier revision rendered shipped-of-embedded, which
// printed readings like "4695 of 781" for a perfectly converged graph.
//
// A graph with no segment pool renders the bare "—": its disposition IS the dash,
// so appending would print "— [—]".
//
// The stuck band is the ONE band that renders more than its name: an age, because a
// state nothing is working to clear is only actionable once a reader can see how long
// it has persisted. Every other band names an arm that is running, so its name is the
// whole story.
func formatCoverageRow(r CoverageRow) string {
	if r.Total == 0 {
		return fmt.Sprintf("| %s | (empty graph) | | | | | | |", r.Graph)
	}
	segCell := "—"
	if r.HasSegments {
		segCell = fmt.Sprintf("shipped %d · live %d [%s]",
			r.SegCovered, r.LiveResident, coverageBandTerm(r))
	}
	return fmt.Sprintf("| %s | %d | %d of %d | %d of %d | %s | %d | %d | %s |",
		r.Graph, r.Total,
		r.Summarized, r.Total,
		r.Embedded, r.Total,
		segCell,
		r.SummaryFail, r.EmbedFail,
		erasureBacklogCell(r))
}

// coverageBandTerm lives in manage_status_coverage_evicted.go, beside the band
// vocabulary's newest member — this file is against its 500-line cap.
