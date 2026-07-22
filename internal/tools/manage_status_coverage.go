// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

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
// '0 of 0 (live 0)'; the live-0 WARN signal is derived client-side (web) from
// live_resident vs seg_covered, only when has_segments is true.
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
}

// coverageTarget is one graph instance the coverage table reports on: its row
// label, type/name (for the segment-coverage seam), and the Stats selector.
type coverageTarget struct {
	label  string
	gt     kgtypes.GraphType
	name   string
	target *knowledgev1.GraphSelector
}

// coverageStatsConcurrency bounds the parallel Stats(IncludeCoverage:true)
// fan-out. Each RPC does per-graph COUNT work server-side; the bound keeps a
// many-graph install from dogpiling the server while still collapsing the
// wall-clock from O(graphs)×RTT to roughly O(graphs/8)×RTT.
const coverageStatsConcurrency = 8

// collectCoverageRows issues the per-graph Stats(IncludeCoverage:true) walk once
// and returns the shared []CoverageRow that both the markdown table and the JSON
// coverage[] block render from — so the two never drift. Returns nil when the
// stats seam is unavailable (degraded/headless), and callers omit the block.
//
// The enumeration is identical to the historical renderLLMCoverage walk: the
// default knowledge graph is emitted explicitly via the empty-name selector (its
// empty instance name is dropped by listGraphNamesOfType), then every other
// SyncEligibleGraphType is enumerated via listGraphNamesOfType +
// graphsel.GraphSelectorFor.
//
// The per-graph Stats RPCs run CONCURRENTLY (bounded): against a remote server
// each is a network round trip carrying that graph's coverage COUNTs, and a
// sequential walk cost ~8s across ~22 graphs — most of manage(status)'s
// remaining latency after the liveness probes went no-retry. Row order stays
// (knowledge first, then enumeration order) because results land by index, not
// completion order. A failed Stats drops its row, same as the sequential walk.
// segCoveredFor stays on the assembly loop: it is a local read, not an RPC.
func collectCoverageRows(ctx context.Context, deps ClientDeps) []CoverageRow {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil
	}
	sc, ok := gc.(statsRPC)
	if !ok {
		return nil
	}

	targets := coverageTargets(ctx, deps)

	stats := make([]*knowledgev1.GraphStats, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, coverageStatsConcurrency)
	for i, t := range targets {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			resp, err := sc.Stats(ctx, &knowledgev1.StatsRequest{
				Target:          t.target,
				IncludeCoverage: true,
			})
			if err != nil {
				return
			}
			stats[i] = resp.GetGraphStats()
		})
	}
	wg.Wait()

	rows := make([]CoverageRow, 0, len(targets))
	for i, t := range targets {
		if stats[i] == nil {
			continue
		}
		segCovered, liveResident, hasSeg := segCoveredFor(ctx, deps, t.gt, t.name)
		rows = append(rows, newCoverageRow(t.label, stats[i], segCovered, liveResident, hasSeg))
	}
	return rows
}

// coverageTargets enumerates every graph instance the coverage table covers, in
// the table's deterministic order: the default knowledge graph first (explicit
// empty-name selector — its empty instance name is dropped by
// listGraphNamesOfType), then every other SyncEligibleGraphType in order, each
// instance in enumeration order. The per-type name enumerations are independent
// RPCs, so they run concurrently; a failed enumeration drops that type's rows,
// same as the historical sequential walk.
func coverageTargets(ctx context.Context, deps ClientDeps) []coverageTarget {
	types := kgtypes.SyncEligibleGraphTypes()
	perType := make([][]string, len(types))
	var wg sync.WaitGroup
	for i, gt := range types {
		if gt == kgtypes.GraphKnowledge {
			// Emitted explicitly below via the empty-name selector; enumerating
			// it again would skip the empty-name default and/or double-count.
			continue
		}
		wg.Go(func() {
			names, err := listGraphNamesOfType(ctx, deps, string(gt))
			if err != nil {
				return
			}
			perType[i] = names
		})
	}
	wg.Wait()

	targets := []coverageTarget{{
		label:  "knowledge",
		gt:     kgtypes.GraphKnowledge,
		target: &knowledgev1.GraphSelector{Graph: ""},
	}}
	for i, gt := range types {
		for _, name := range perType[i] {
			targets = append(targets, coverageTarget{
				label:  fmt.Sprintf("%s/%s", gt, name),
				gt:     gt,
				name:   name,
				target: graphsel.GraphSelectorFor(gt, name, false),
			})
		}
	}
	return targets
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
		sb.WriteString(formatCoverageRow(r))
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

// newCoverageRow projects a per-graph GraphStats + segment-coverage triple into
// the shared CoverageRow. The embedded count is GraphStats.BinaryVectorCount —
// the SAME denominator the coverage-ratio auto-heal compares against (T3-2 single
// definition; do not fork it) and the segment-coverage cell's denominator.
func newCoverageRow(label string, st *knowledgev1.GraphStats, segCovered, liveResident int, hasSeg bool) CoverageRow {
	return CoverageRow{
		Graph:        label,
		Total:        int(st.GetNonProxyNodeCount()),
		Summarized:   int(st.GetSummarizedCount()),
		Embedded:     int(st.GetBinaryVectorCount()),
		SegCovered:   segCovered,
		LiveResident: liveResident,
		HasSegments:  hasSeg,
		SummaryFail:  int(st.GetSummaryFailureCount()),
		EmbedFail:    int(st.GetEmbedFailureCount()),
	}
}

// formatCoverageRow renders one Markdown table row from a CoverageRow. An empty
// (zero-denominator) graph renders "(empty graph)" so a never-populated graph is
// visibly distinct from a covered one; otherwise summarized/embedded render as
// "X of N" so "0 of N summarized" is unambiguous against "N of N summarized". The
// segment-coverage cell renders "covered of embedded (live resident)" for a
// segment-bearing graph (HasSegments) — the "(live N)" suffix is the in-memory
// engine resident count so a collapsed live pool reads "covered of embedded
// (live 0)" instead of being masked behind the intact shipped figure — and "—"
// for a graph with no segment pool.
func formatCoverageRow(r CoverageRow) string {
	if r.Total == 0 {
		return fmt.Sprintf("| %s | (empty graph) | | | | | |", r.Graph)
	}
	segCell := "—"
	if r.HasSegments {
		segCell = fmt.Sprintf("%d of %d (live %d)", r.SegCovered, r.Embedded, r.LiveResident)
	}
	return fmt.Sprintf("| %s | %d | %d of %d | %d of %d | %s | %d | %d |",
		r.Graph, r.Total,
		r.Summarized, r.Total,
		r.Embedded, r.Total,
		segCell,
		r.SummaryFail, r.EmbedFail)
}
