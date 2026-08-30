// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"
	"maps"

	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// worker_summary_lease.go is the summary-axis twin of worker_embed_lease.go:
// one lease becomes N summarizer calls and ONE writeback.
//
// The strides are SEQUENTIAL for the same reason as on the embed axis — total
// LLM concurrency stays at SummaryWorkersOrDefault() workers with one in-flight
// call each, which is what keeps the breaker's window and the backoff gate
// meaning what they meant before the lease existed.

// processSummaryLeaseGroup spends one summary lease: it walks items in strides
// of the provider-call cap, calls summaryGroupOnce per stride, merges the
// results and the idMap, and issues exactly ONE writeback for the whole lease.
//
// STRIDE FAILURE PARTITIONING — the same policy as the embed axis, stated here
// too because it is the policy a reader is most likely to "simplify". A failed
// stride contributes NOTHING to the merge and the loop CONTINUES; the only early
// exit is ctx cancellation. The writeback carries every succeeded stride;
// handleSummarizerError has already applied its unchanged transient/terminal
// rules to that stride's items; and the failed stride's ids are absent from the
// writeback, so they stay summary-eligible for the next scan. Do NOT abort the
// lease on a stride failure.
func processSummaryLeaseGroup(ctx context.Context, p *Pipeline, key groupKey, items []SummaryWork) {
	gk := key.Key
	be := backendOr(p, key.Backend)
	stride := max(p.cfg.SummaryBatchSizeOrDefault(), 1)
	mergedResults := make(map[string]llmproviders.SummarizeResult, len(items))
	mergedIDMap := make(map[string]string, len(items))
	strides := 0
	for start := 0; start < len(items); start += stride {
		if ctx.Err() != nil {
			slog.Debug("pipeline.summary: lease abandoned mid-stride on context cancellation",
				"graph_type", gk.GraphType, "graph_name", gk.GraphName, "merged_so_far", len(mergedResults))
			break
		}
		end := min(start+stride, len(items))
		results, idMap := summaryGroupOnce(ctx, p, key, items[start:end])
		strides++
		maps.Copy(mergedResults, results)
		maps.Copy(mergedIDMap, idMap)
	}
	if len(mergedResults) == 0 {
		return
	}
	slog.Debug("pipeline.summary: lease complete — one writeback",
		"graph_type", gk.GraphType, "graph_name", gk.GraphName,
		"lease_items", len(items), "strides", strides, "merged_results", len(mergedResults))
	writeSummaryResults(ctx, p, be, gk, mergedResults, mergedIDMap)
}
