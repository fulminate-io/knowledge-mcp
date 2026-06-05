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

// groupKey is the writeback-grouping key: the (GraphType, GraphName) graphKey
// PLUS the concrete Backend the items were scanned from. Grouping on the
// COMPOSITE (not graphKey alone) keeps each group backend-homogeneous so a
// survivor graphKey's items — which can carry TWO backends on the global
// channel during a mid-session login flip — fan out to the correct backend
// instead of collapsing onto one. Backend is a WireClient interface value bound
// to a *GraphClient pointer → comparable → a safe map key. A nil Backend (a
// collector-less test work item) groups under the zero value and resolves to
// p.client at the writeback site.
type groupKey struct {
	Key     graphKey
	Backend WireClient
}

// backendOr returns the group's bound Backend, falling back to p.client when
// the work items carried no Backend (collector-less test path). This is the
// single seam that turns "write through the scanning backend" into a concrete
// WireClient for the writeBatchUpdates call.
func backendOr(p *Pipeline, b WireClient) WireClient {
	if b != nil {
		return b
	}
	return p.client
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

// groupSummaryByGraph groups SummaryWork items by the COMPOSITE (graphKey,
// Backend) so each group is backend-homogeneous (see groupKey).
func groupSummaryByGraph(batch []SummaryWork) map[groupKey][]SummaryWork {
	groups := make(map[groupKey][]SummaryWork)
	for _, w := range batch {
		k := groupKey{
			Key:     graphKey{GraphType: w.GraphType, GraphName: w.GraphName},
			Backend: w.Backend,
		}
		groups[k] = append(groups[k], w)
	}
	return groups
}

// processSummaryGroup handles one (gt, name) group: fetches each node
// via wire, builds chunks, calls the summarizer, and issues exactly ONE
// mutate(update_batch) RPC for the per-id writeback.
func processSummaryGroup(ctx context.Context, p *Pipeline, key groupKey, items []SummaryWork) {
	gk := key.Key
	be := backendOr(p, key.Backend)
	slog.Debug("pipeline.summary: processing group", "graph_type", gk.GraphType, "graph_name", gk.GraphName, "items", len(items))
	chunks, idMap := buildSummaryChunks(ctx, p, gk, items)
	if len(chunks) == 0 {
		slog.Debug("pipeline.summary: no chunks produced — batch skipped",
			"graph_type", gk.GraphType, "graph_name", gk.GraphName, "items", len(items))
		return
	}

	// Pause if a prior transient failure opened the shared backoff window.
	p.backoff.wait(ctx)
	slog.Debug("pipeline.summary: invoking summarizer", "chunks", len(chunks), "graph_type", gk.GraphType, "graph_name", gk.GraphName)
	results, err := p.summarizer(ctx, chunks)
	if err != nil {
		slog.Warn("pipeline.summary: summarizer call failed",
			"graph_type", gk.GraphType, "graph_name", gk.GraphName, "chunks", len(chunks), "error", err)
		handleSummarizerError(ctx, p, be, gk, items, err)
		return
	}
	p.backoff.ok()
	slog.Debug("pipeline.summary: summarizer returned",
		"chunks", len(chunks), "results", len(results), "graph_type", gk.GraphType, "graph_name", gk.GraphName)
	writeSummaryResults(ctx, p, be, gk, results, idMap)
	slog.Debug("pipeline.summary: writeSummaryResults done",
		"chunks", len(chunks), "graph_type", gk.GraphType, "graph_name", gk.GraphName)
}

// buildSummaryChunks builds the BatchChunk payload from the SERVER-COMPOSED
// SummarizeText carried on each SummaryWork — it no longer re-fetches
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
func writeSummaryResults(ctx context.Context, p *Pipeline, be WireClient, key graphKey, results map[string]llmproviders.SummarizeResult, idMap map[string]string) {
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
	// Log the EXACT ids + writeback target graph name (see debugLogWriteback) so
	// a recurring summary re-computation is traceable.
	debugLogWriteback("summary", key.GraphName, items)
	if err := writeBatchUpdates(ctx, be, key.GraphType, key.GraphName, items); err != nil {
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
func handleSummarizerError(ctx context.Context, p *Pipeline, be WireClient, key graphKey, items []SummaryWork, err error) {
	if llm.IsTransient(err) {
		// Honor a provider 429/503 Retry-After when present (else exponential).
		hint := llm.RetryAfterOf(err)
		delay := p.backoff.failHint(hint)
		slog.Warn("pipeline.summary: transient error, backing off before retry",
			"items", len(items), "backoff", delay, "retry_after_hint", hint, "error", err)
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
	if werr := writeBatchUpdates(ctx, be, key.GraphType, key.GraphName, batchItems); werr != nil {
		slog.Warn("pipeline.summary: write failure markers failed",
			"items", len(batchItems), "error", werr, "graph_type", key.GraphType, "graph_name", key.GraphName)
	}
	for range items {
		p.metrics.summaryFail()
	}
}
