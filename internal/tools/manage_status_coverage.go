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
	addRow := func(label string, st *knowledgev1.GraphStats) {
		rows = append(rows, formatCoverageRow(label, st))
	}

	// Knowledge row — emitted explicitly with the empty-name selector because
	// the default knowledge graph's empty instance name is dropped by
	// listGraphNamesOfType. This is the graph users check most.
	if resp, err := sc.Stats(ctx, &knowledgev1.StatsRequest{
		Target:          &knowledgev1.GraphSelector{Graph: ""},
		IncludeCoverage: true,
	}); err == nil {
		addRow("knowledge", resp.GetGraphStats())
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
			addRow(fmt.Sprintf("%s/%s", gt, name), resp.GetGraphStats())
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
	sb.WriteString("| graph | total | summarized | embedded | summary-fail | embed-fail |\n")
	sb.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, r := range rows {
		sb.WriteString(r)
		sb.WriteString("\n")
	}
	return sb.String()
}

// formatCoverageRow renders one Markdown table row. An empty (zero-denominator)
// graph renders "(empty graph)" so a never-populated graph is visibly distinct
// from a covered one; otherwise summarized/embedded render as "X of N" so
// "0 of N summarized" is unambiguous against "N of N summarized".
func formatCoverageRow(label string, st *knowledgev1.GraphStats) string {
	total := int(st.GetNonProxyNodeCount())
	if total == 0 {
		return fmt.Sprintf("| %s | (empty graph) | | | | |", label)
	}
	summarized := int(st.GetSummarizedCount())
	embedded := int(st.GetBinaryVectorCount())
	return fmt.Sprintf("| %s | %d | %d of %d | %d of %d | %d | %d |",
		label, total,
		summarized, total,
		embedded, total,
		int(st.GetSummaryFailureCount()),
		int(st.GetEmbedFailureCount()))
}
