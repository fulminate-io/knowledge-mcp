// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// worker_summary_split.go — what the summary worker does when the provider
// refuses a call for SIZE rather than failing it.
//
// THE CONDITION IS TYPED, NEVER SNIFFED. The trigger is llm.InputTooLargeOf over
// the transport's stamped Reason. Nothing here reads a provider's message text:
// message text is not a contract, and a match on it stops working silently when
// a provider rewords its refusal.
//
// ITS FAILURE POLICY, because a later reader will be tempted to simplify it:
//
//   - A DOCUMENT WHOSE OWN TEXT EXCEEDS THE REPORTED LIMIT IS NEVER SENT AGAIN.
//     Its size already answers the question, so re-sending it would bill a round
//     trip to be told the same thing. It is marked TERMINALLY — both marker keys,
//     naming the size and the limit — and the rest of its batch proceeds.
//   - A SUB-BATCH THAT IS STILL TOO LARGE IS SPLIT AGAIN, never marked. The
//     owner's ruling is "we may need multiple splits"; a one-level split that
//     gave up on its own sub-batch would fail documents that fit perfectly well
//     two levels down.
//   - CONTENT IS NEVER TRUNCATED to make a sub-batch fit (AGENTS.md). The split
//     changes how many documents ride one call, never what any of them says.
//   - A RESOLVED OVERSIZE IS NOT AN ERRORED CALL. It reaches neither the
//     breaker's recordErr, nor the backoff's failHint, nor the summaryFail
//     counter — not once per attempt and not once per logical batch. Two
//     recorded ClassInvalidRequest failures with no success between them latch
//     the whole summary axis into a human-only pause
//     (DefaultDeterministicFastTripThreshold is 2), and a lease whose every
//     document is oversize produces no success to reset the streak, so
//     "record once per batch" would pause the axis on the second stride. Only a
//     sub-batch that fails for some OTHER reason records, once, through the
//     unchanged handleSummarizerError.
//   - CTX CANCELLATION IS THE ONLY EARLY EXIT, and it marks NOTHING. That is the
//     stride loop's policy one level down (worker_summary_lease.go): an
//     abandoned document stays summary-eligible, and a durable marker written on
//     a shutdown would strand it.
//
// SEQUENTIAL, for the reason the stride loop is (worker_summary_lease.go): total
// LLM concurrency stays at one in-flight call per worker, which is what keeps
// the breaker window and the backoff gate meaning what they meant. Fanning
// sub-batches out would multiply in-flight provider calls per worker.

// splitOversizeSummaryGroup re-issues a size-refused call as smaller ones and
// returns the merged results and idMap for its caller's SINGLE writeback. limit
// is the provider's reported character limit, or zero when it reported none.
//
// It recurses through summaryGroupOnce rather than calling the summarizer
// itself, so each sub-batch crosses the same breaker and backoff gates, records
// its own success, routes a non-size failure through the same unchanged error
// path, and can split AGAIN — the multiple-splits case is the recursion, not a
// second mechanism.
//
// TERMINATION. Every recursive step strictly reduces the batch: a batch of one
// is marked and returns; a batch with a known limit drops every
// individually-oversize document before packing; and a pack that fails to
// reduce (a sub-batch refused despite fitting the reported limit, because the
// provider counts scaffolding we do not model) falls back to halving, which
// always yields two smaller parts.
func splitOversizeSummaryGroup(
	ctx context.Context, p *Pipeline, key groupKey, items []SummaryWork, limit int,
) (map[string]llmproviders.SummarizeResult, map[string]string) {
	gk := key.Key
	if len(items) == 1 {
		// A single document refused on its own cannot be split further, whatever
		// the reported limit says — including a document UNDER a reported limit
		// that the provider still refuses. The owner's ruling: "any single node
		// too large gets marked as a llm failure that is terminal".
		markOversizeSummaryItems(ctx, p, key, items, limit)
		return nil, nil
	}

	parts := packSummaryItems(items, limit)
	if len(parts) == 1 && len(parts[0]) == len(items) {
		// No progress: the pack put everything back in one batch, so halve
		// instead. Without this the recursion would re-issue the identical call
		// forever.
		parts = halveSummaryItems(items)
	}
	if oversize := knownOversizeSummaryItems(items, limit); len(oversize) > 0 {
		// ONE marker write for every individually-oversize document of this
		// batch, not one per split leaf.
		markOversizeSummaryItems(ctx, p, key, oversize, limit)
	}

	mergedResults := make(map[string]llmproviders.SummarizeResult, len(items))
	mergedIDMap := make(map[string]string, len(items))
	for i, sub := range parts {
		if ctx.Err() != nil {
			// AT WARN, NOT DEBUG, and NAMING WHAT WAS DROPPED. The documents in
			// the remaining parts are abandoned unsummarized: they stay
			// summary-eligible and the next scan re-discovers them, so nothing is
			// lost — but the work was dropped, and a dropped-work line that is
			// invisible at default verbosity is a lane that continues with no
			// report. The stride loop one level up logs the same event at Debug;
			// this one does not copy that.
			abandoned := 0
			for _, rest := range parts[i:] {
				abandoned += len(rest)
			}
			slog.Warn("pipeline.summary: oversize split abandoned on context cancellation — the remaining documents stay summary-eligible for the next scan",
				"graph_type", gk.GraphType, "graph_name", gk.GraphName,
				"merged_so_far", len(mergedResults), "documents_abandoned", abandoned)
			break
		}
		results, idMap := summaryGroupOnce(ctx, p, key, sub)
		maps.Copy(mergedResults, results)
		maps.Copy(mergedIDMap, idMap)
	}
	return mergedResults, mergedIDMap
}

// knownOversizeSummaryItems returns the items whose OWN composed text exceeds
// limit. Empty when limit is zero (unknown): a document cannot be known oversize
// against a limit nobody reported, and guessing one would fail documents that
// the provider would have accepted.
func knownOversizeSummaryItems(items []SummaryWork, limit int) []SummaryWork {
	if limit <= 0 {
		return nil
	}
	var out []SummaryWork
	for _, w := range items {
		if len(w.SummarizeText) > limit {
			out = append(out, w)
		}
	}
	return out
}

// packSummaryItems groups items into sub-batches whose summed composed size stays
// within limit, dropping the individually-oversize ones (which the caller marks
// instead of sending). With limit zero — no limit reported — it halves, because
// a pack needs a number and only a transport that read one can supply it.
//
// PACKING RATHER THAN ALWAYS HALVING is what makes the retry cheap on the one
// transport that reports a limit: the production shape of this defect was twenty
// documents totalling 1.6M characters against a 1,048,576 limit, which packs
// into the refusal plus two calls plus the oversize document's marker, where
// blind halving of the same batch is eleven calls, and 2n-1 in the worst case.
// It degrades to halving with no special case at the call site.
//
// The order of items is PRESERVED. Sorting by size would pack marginally
// tighter, and it would also make the call composition depend on a comparison
// this function has no reason to own; keeping the caller's order makes the
// sequence of calls a function of the batch alone.
func packSummaryItems(items []SummaryWork, limit int) [][]SummaryWork {
	if limit <= 0 {
		return halveSummaryItems(items)
	}
	var (
		parts   [][]SummaryWork
		current []SummaryWork
		running int
	)
	for _, w := range items {
		size := len(w.SummarizeText)
		if size > limit {
			continue // marked terminally by the caller; never sent again.
		}
		if len(current) > 0 && running+size > limit {
			parts = append(parts, current)
			current, running = nil, 0
		}
		current = append(current, w)
		running += size
	}
	if len(current) > 0 {
		parts = append(parts, current)
	}
	return parts
}

// halveSummaryItems splits items into two halves, the blind strategy for a
// refusal that reported no limit. A batch of one or fewer is returned whole —
// the caller's single-item base case owns that shape.
func halveSummaryItems(items []SummaryWork) [][]SummaryWork {
	if len(items) < 2 {
		return [][]SummaryWork{items}
	}
	mid := len(items) / 2
	return [][]SummaryWork{items[:mid], items[mid:]}
}

// markOversizeSummaryItems stamps the TERMINAL summary marker on every item, in
// ONE update_batch, with per-item text naming that item's own composed size and
// the provider limit. It goes through the shared marker writer, so there is
// still exactly one marker-writing path on this axis.
func markOversizeSummaryItems(ctx context.Context, p *Pipeline, key groupKey, items []SummaryWork, limit int) {
	gk := key.Key
	marks := make([]summaryMarkerItem, 0, len(items))
	for _, w := range items {
		size := len(w.SummarizeText)
		slog.Error("pipeline.summary: document is too large for the provider — marking it a TERMINAL llm failure",
			"graph_type", gk.GraphType, "graph_name", gk.GraphName,
			"node_id", w.NodeID, "composed_chars", size, "provider_limit", limitLabel(limit))
		marks = append(marks, summaryMarkerItem{
			ID: w.NodeID,
			Meta: map[string]string{
				kgtypes.MetaKeySummaryFailureReason:   oversizeFailureReason(size, limit),
				kgtypes.MetaKeySummaryFailureTerminal: oversizeTerminalDetail(size, limit),
			},
		})
	}
	writeSummaryMarkers(ctx, p, backendOr(p, key.Backend), gk, marks)
}

// logOversizeRejection records the measurement requirement 5 asks for: the
// per-document composed size of the refused batch and the limit, so an operator
// can see WHICH document is too large and by how much without re-deriving it.
//
// It logs at WARN beside the existing summarizer-failure sites, and it names the
// limit as UNKNOWN when the transport reported none — a zero rendered as a limit
// reads as "the provider allows nothing", which is the opposite of what happened.
func logOversizeRejection(gk graphKey, items []SummaryWork, reportedSize, limit int) {
	ordered := make([]SummaryWork, len(items))
	copy(ordered, items)
	// Descending by composed size, so the document an operator is looking for is
	// the first entry rather than somewhere in a batch-ordered list.
	slices.SortStableFunc(ordered, func(a, b SummaryWork) int {
		return len(b.SummarizeText) - len(a.SummarizeText)
	})
	sizes := make([]string, 0, len(ordered))
	total := 0
	largestID, largest := "", 0
	for _, w := range ordered {
		size := len(w.SummarizeText)
		total += size
		sizes = append(sizes, w.NodeID+"="+strconv.Itoa(size))
	}
	if len(ordered) > 0 {
		largestID, largest = ordered[0].NodeID, len(ordered[0].SummarizeText)
	}
	slog.Warn("pipeline.summary: provider refused the batch as too large — splitting",
		"graph_type", gk.GraphType, "graph_name", gk.GraphName,
		"documents", len(items),
		"composed_chars_total", total,
		"provider_counted_chars", reportedSize,
		"provider_limit", limitLabel(limit),
		"largest_document", largestID,
		"largest_document_chars", largest,
		"document_chars", sizes)
}

// limitLabel renders a provider limit for an operator: the number, or the word
// "unknown" when the transport reported none. It exists so no log line and no
// marker can show a limit of 0, which would read as a real bound.
func limitLabel(limit int) string {
	if limit <= 0 {
		return "unknown"
	}
	return strconv.Itoa(limit)
}

// oversizeFailureReason is the value written to the ORDINARY summary marker key
// for a terminally-oversize document: the operator-facing prose every existing
// reader of that key already renders.
func oversizeFailureReason(size, limit int) string {
	return fmt.Sprintf("%s: composed summarize text is %d characters, provider limit %s",
		llm.ReasonInputTooLarge, size, limitLabel(limit))
}

// oversizeTerminalDetail is the value written to the TERMINAL marker key: the
// same three facts in a compact, stable shape, because this is the value an
// operator reads back with a by-id metadata projection and the one a later
// reader is most likely to want to parse.
func oversizeTerminalDetail(size, limit int) string {
	return fmt.Sprintf("reason=%s size=%d limit=%s", llm.ReasonInputTooLarge, size, limitLabel(limit))
}
