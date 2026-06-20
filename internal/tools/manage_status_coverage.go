// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"strings"

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
// Returns "" when the stats seam is unavailable (the caller appends nothing).
func renderLLMCoverage(ctx context.Context, deps ClientDeps) string {
	gc := deps.GraphCaller()
	if gc == nil {
		return ""
	}
	sc, ok := gc.(statsRPC)
	if !ok {
		return ""
	}

	var rows []string
	addRow := func(label string, gt kgtypes.GraphType, name string, st *knowledgev1.GraphStats) {
		segCovered, liveResident, hasSeg := segCoveredFor(ctx, deps, gt, name)
		rows = append(rows, formatCoverageRow(label, st, segCovered, liveResident, hasSeg))
	}

	// Knowledge row — emitted explicitly with the empty-name selector because
	// the default knowledge graph's empty instance name is dropped by
	// listGraphNamesOfType. This is the graph users check most.
	if resp, err := sc.Stats(ctx, &knowledgev1.StatsRequest{
		Target:          &knowledgev1.GraphSelector{Graph: ""},
		IncludeCoverage: true,
	}); err == nil {
		addRow("knowledge", kgtypes.GraphKnowledge, "", resp.GetGraphStats())
	}

	for _, gt := range kgtypes.SyncEligibleGraphTypes() {
		if gt == kgtypes.GraphKnowledge {
			// Already emitted above via the empty-name selector; enumerating it
			// again would skip the empty-name default and/or double-count.
			continue
		}
		names, err := listGraphNamesOfType(ctx, deps, string(gt))
		if err != nil {
			continue
		}
		for _, name := range names {
			resp, err := sc.Stats(ctx, &knowledgev1.StatsRequest{
				Target:          graphsel.GraphSelectorFor(gt, name, false),
				IncludeCoverage: true,
			})
			if err != nil {
				continue
			}
			addRow(fmt.Sprintf("%s/%s", gt, name), gt, name, resp.GetGraphStats())
		}
	}

	if len(rows) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## LLM coverage (durable, per-graph)\n")
	// summarized counts any non-empty Summary, which INCLUDES deterministic
	// auto-summaries — so it is most meaningful as a coverage signal for code
	// graphs, where Summary is populated only by the summarizer.
	sb.WriteString("_summarized = node has a non-empty Summary, which INCLUDES deterministic auto-summaries for structured nodes (decisions, findings, thoughts, etc.) — most meaningful as an LLM-coverage signal for code graphs, where Summary is populated only by the summarizer._\n\n")
	// segment coverage = HNSW-segment-covered docs of embedded (the same
	// BinaryVectorCount denominator the coverage-ratio auto-heal compares against —
	// T3-2 single definition); a degenerate pool shows few-of-many here, which is
	// the same signal lever 2 heals on. The "(live N)" suffix is the LIVE in-memory
	// engine resident doc count — when it reads 0 (or far below covered) the live
	// searchable pool has collapsed even though the server-shipped corpus is intact,
	// the post-restart incident the startup/periodic reconcile heals. Non-segment
	// graphs render "—".
	sb.WriteString("| graph | total | summarized | embedded | segment coverage | summary-fail | embed-fail |\n")
	sb.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range rows {
		sb.WriteString(r)
		sb.WriteString("\n")
	}
	return sb.String()
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
func GraphEmbeddedCount(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, name string) (int, error) {
	sc, ok := gc.(statsRPC)
	if !ok {
		return 0, nil
	}
	target := graphsel.GraphSelectorFor(gt, name, false)
	if gt == kgtypes.GraphKnowledge && name == "" {
		target = &knowledgev1.GraphSelector{Graph: ""}
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
// a single atomic snapshot (no RPC); it is surfaced so a live-pool collapse (live 0
// while covered is N) is detectable instead of masked behind the shipped figure.
func segCoveredFor(ctx context.Context, deps ClientDeps, gt kgtypes.GraphType, name string) (covered, liveResident int, hasSeg bool) {
	if !kgtypes.HasRebuildableSegments(gt) {
		return 0, 0, false
	}
	sr := deps.SegmentCoverage()
	if sr == nil {
		return 0, 0, false
	}
	c, _, err := sr.ShippedSegmentDocCount(ctx, gt, name)
	if err != nil {
		return 0, 0, false
	}
	return c, sr.ResidentDocCount(gt, name), true
}

// formatCoverageRow renders one Markdown table row. An empty (zero-denominator)
// graph renders "(empty graph)" so a never-populated graph is visibly distinct
// from a covered one; otherwise summarized/embedded render as "X of N" so
// "0 of N summarized" is unambiguous against "N of N summarized". The segment-
// coverage cell renders "covered of embedded (live resident)" for a segment-bearing
// graph (hasSeg) — the embedded denominator is the SAME GraphStats.BinaryVectorCount
// the coverage-ratio auto-heal compares against (T3-2 single definition; do not fork
// it), and the "(live N)" suffix is the in-memory engine resident count so a
// collapsed live pool reads "covered of embedded (live 0)" instead of being masked
// behind the intact shipped figure — and "—" for a graph with no segment pool.
func formatCoverageRow(label string, st *knowledgev1.GraphStats, segCovered, liveResident int, hasSeg bool) string {
	total := int(st.GetNonProxyNodeCount())
	if total == 0 {
		return fmt.Sprintf("| %s | (empty graph) | | | | | |", label)
	}
	summarized := int(st.GetSummarizedCount())
	embedded := int(st.GetBinaryVectorCount())
	segCell := "—"
	if hasSeg {
		segCell = fmt.Sprintf("%d of %d (live %d)", segCovered, embedded, liveResident)
	}
	return fmt.Sprintf("| %s | %d | %d of %d | %d of %d | %s | %d | %d |",
		label, total,
		summarized, total,
		embedded, total,
		segCell,
		int(st.GetSummaryFailureCount()),
		int(st.GetEmbedFailureCount()))
}
