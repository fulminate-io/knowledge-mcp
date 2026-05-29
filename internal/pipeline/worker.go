// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// runSummaryWorker pulls batches from batchIn, calls the configured
// summarizer for each batch, and writes results back via a single
// mutate(update_batch) RPC per group. Per-batch panic recovery: deferred
// recover() inside the for-loop body guarantees a panic in the summarizer
// does not kill the worker goroutine.
func runSummaryWorker(ctx context.Context, p *Pipeline, batchIn <-chan []SummaryWork, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case batch, ok := <-batchIn:
			if !ok {
				return
			}
			runSummaryWorkerBatch(ctx, p, batch)
		}
	}
}

// runSummaryWorkerBatch processes one batch with panic recovery scoped to
// the batch (not the goroutine). Groups items by (GraphType, GraphName),
// then per-group: fetches nodes via wire, builds BatchChunks, calls the
// summarizer, writes results via mutate(update_batch).
func runSummaryWorkerBatch(ctx context.Context, p *Pipeline, batch []SummaryWork) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("pipeline.summary worker: batch panic recovered", "panic", r, "batch_size", len(batch))
		}
	}()
	for range batch {
		p.metrics.summaryRun()
	}
	// Release in-flight slot back to the originating collector regardless
	// of outcome (success / transient / terminal / panic). Without this,
	// transient-failed items remain in the collector's in-flight set and
	// never get re-queued. Non-blocking — the release channel is sized
	// to never fill in steady state.
	defer func() {
		for _, w := range batch {
			p.metrics.summaryDone()
			releaseInFlight(w.Release, w.NodeID)
		}
	}()

	groups := groupSummaryByGraph(batch)
	for key, items := range groups {
		processSummaryGroup(ctx, p, key, items)
	}
}

// releaseInFlight tries to send id on release. Non-blocking — if the
// receiver is gone (collector unregistered between dispatch and worker
// completion) the send is dropped silently. Nil release is a no-op so
// tests that construct work items without a collector keep working.
func releaseInFlight(release chan<- string, id string) {
	if release == nil {
		return
	}
	select {
	case release <- id:
	default:
	}
}

// groupSummaryByGraph groups SummaryWork items by their (GraphType, GraphName).
func groupSummaryByGraph(batch []SummaryWork) map[graphKey][]SummaryWork {
	groups := make(map[graphKey][]SummaryWork)
	for _, w := range batch {
		k := graphKey{GraphType: w.GraphType, GraphName: w.GraphName}
		groups[k] = append(groups[k], w)
	}
	return groups
}

// processSummaryGroup handles one (gt, name) group: fetches each node
// via wire, builds chunks, calls the summarizer, and issues exactly ONE
// mutate(update_batch) RPC for the per-id writeback.
func processSummaryGroup(ctx context.Context, p *Pipeline, key graphKey, items []SummaryWork) {
	slog.Debug("pipeline.summary: processing group", "graph_type", key.GraphType, "graph_name", key.GraphName, "items", len(items))
	chunks, idMap := buildSummaryChunks(ctx, p, key, items)
	if len(chunks) == 0 {
		slog.Debug("pipeline.summary: no chunks produced — batch skipped",
			"graph_type", key.GraphType, "graph_name", key.GraphName, "items", len(items))
		return
	}

	// Pause if a prior transient failure opened the shared backoff window.
	p.backoff.wait(ctx)
	slog.Debug("pipeline.summary: invoking summarizer", "chunks", len(chunks), "graph_type", key.GraphType, "graph_name", key.GraphName)
	results, err := p.summarizer(ctx, chunks)
	if err != nil {
		slog.Warn("pipeline.summary: summarizer call failed",
			"graph_type", key.GraphType, "graph_name", key.GraphName, "chunks", len(chunks), "error", err)
		handleSummarizerError(ctx, p, key, items, err)
		return
	}
	p.backoff.ok()
	slog.Debug("pipeline.summary: summarizer returned",
		"chunks", len(chunks), "results", len(results), "graph_type", key.GraphType, "graph_name", key.GraphName)
	writeSummaryResults(ctx, p, key, results, idMap)
	slog.Debug("pipeline.summary: writeSummaryResults done",
		"chunks", len(chunks), "graph_type", key.GraphType, "graph_name", key.GraphName)
}

// buildSummaryChunks builds the BatchChunk payload from the SERVER-COMPOSED
// SummarizeText carried on each SummaryWork (FUL-305) — it no longer re-fetches
// the node, no longer composes the chunkInput envelope, and no longer walks
// hierarchy children client-side (the server did all of that at the gap-scan
// site, and dropped any node whose composed text was empty). The chunk ID is the
// NodeID directly, so idMap is the trivial identity the UNCHANGED
// writeSummaryResults still consumes. An item with empty SummarizeText is
// skipped defensively (the server should never emit one on the summary axis;
// the post-write no-op merge still handles the scan-to-write race).
func buildSummaryChunks(_ context.Context, _ *Pipeline, key graphKey, items []SummaryWork) ([]llmproviders.BatchChunk, map[string]string) {
	chunks := make([]llmproviders.BatchChunk, 0, len(items))
	idMap := make(map[string]string, len(items))
	skipEmpty := 0
	for _, w := range items {
		if w.SummarizeText == "" {
			skipEmpty++
			continue
		}
		chunks = append(chunks, llmproviders.BatchChunk{ID: w.NodeID, Content: w.SummarizeText})
		idMap[w.NodeID] = w.NodeID
	}
	slog.Debug("pipeline.summary: chunks built",
		"graph_type", key.GraphType, "graph_name", key.GraphName,
		"items", len(items), "chunks", len(chunks), "skip_empty", skipEmpty)
	return chunks, idMap
}

// writeSummaryResults applies the summarizer's per-id results in ONE
// mutate(update_batch) RPC: every successful summary becomes a batch
// item carrying Summary + Keywords + the cleared MetaKeySummaryFailureReason.
// Load-bearing perf criterion (the integration test asserts the call
// counter on the fake WireClient): exactly 1 RPC per call, regardless of
// len(results).
func writeSummaryResults(ctx context.Context, p *Pipeline, key graphKey, results map[string]llmproviders.SummarizeResult, idMap map[string]string) {
	items := make([]updateBatchItem, 0, len(results))
	for chunkID, res := range results {
		nodeID, ok := idMap[chunkID]
		if !ok {
			continue
		}
		summary := res.Summary
		keywords := res.Keywords
		items = append(items, updateBatchItem{
			ID:       nodeID,
			Summary:  &summary,
			Keywords: &keywords,
			Metadata: map[string]string{
				kgtypes.MetaKeySummaryFailureReason: "",
			},
		})
	}
	if len(items) == 0 {
		return
	}
	if err := writeBatchUpdates(ctx, p.client, key.GraphType, key.GraphName, items); err != nil {
		slog.Warn("pipeline.summary: writeSummaryResults batch write failed", "error", err, "items", len(items), "graph_type", key.GraphType, "graph_name", key.GraphName)
		for range items {
			p.metrics.summaryFail()
		}
		return
	}
	for range items {
		p.metrics.summaryOK()
	}
}

// handleSummarizerError applies the transient/terminal classification per
// item. Transient: no marker, increment summaryFailed (the next tick
// re-discovers and retries). Terminal: write MetaKeySummaryFailureReason
// on each item in ONE mutate(update_batch) RPC.
//
// Both branches log at WARN so the operator sees the actual error in the
// log immediately. Transient errors WARN with retry context; terminal
// errors WARN with the reason that just got stamped on each affected
// node's metadata.
func handleSummarizerError(ctx context.Context, p *Pipeline, key graphKey, items []SummaryWork, err error) {
	if llm.IsTransient(err) {
		delay := p.backoff.fail()
		slog.Warn("pipeline.summary: transient error, backing off before retry",
			"items", len(items), "backoff", delay, "error", err)
		for range items {
			p.metrics.summaryFail()
		}
		return
	}

	reason := err.Error()
	if llmErr, ok := errors.AsType[*llm.LLMError](err); ok {
		reason = llmErr.Error()
	}

	slog.Warn("pipeline.summary: terminal error, marking nodes as failed",
		"items", len(items), "reason", reason, "error", err)

	batchItems := make([]updateBatchItem, 0, len(items))
	for _, w := range items {
		batchItems = append(batchItems, updateBatchItem{
			ID: w.NodeID,
			Metadata: map[string]string{
				kgtypes.MetaKeySummaryFailureReason: reason,
			},
		})
	}
	if werr := writeBatchUpdates(ctx, p.client, key.GraphType, key.GraphName, batchItems); werr != nil {
		slog.Warn("pipeline.summary: write failure markers failed",
			"items", len(batchItems), "error", werr, "graph_type", key.GraphType, "graph_name", key.GraphName)
	}
	for range items {
		p.metrics.summaryFail()
	}
}
