// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// runEmbedWorker pulls embed batches and processes them with panic recovery.
func runEmbedWorker(ctx context.Context, p *Pipeline, batchIn <-chan []EmbedWork, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case batch, ok := <-batchIn:
			if !ok {
				return
			}
			runEmbedWorkerBatch(ctx, p, batch)
		}
	}
}

// runEmbedWorkerBatch is the embed-side mirror of runSummaryWorkerBatch.
func runEmbedWorkerBatch(ctx context.Context, p *Pipeline, batch []EmbedWork) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("pipeline.embed worker: batch panic recovered", "panic", r, "batch_size", len(batch))
		}
	}()
	for range batch {
		p.metrics.embedRun()
	}
	defer func() {
		for _, w := range batch {
			p.metrics.embedDone()
			releaseInFlight(w.Release, w.NodeID)
		}
	}()

	groups := groupEmbedByGraph(batch)
	for key, items := range groups {
		processEmbedGroup(ctx, p, key, items)
	}
}

// groupEmbedByGraph groups EmbedWork items by (GraphType, GraphName).
func groupEmbedByGraph(batch []EmbedWork) map[graphKey][]EmbedWork {
	groups := make(map[graphKey][]EmbedWork)
	for _, w := range batch {
		k := graphKey{GraphType: w.GraphType, GraphName: w.GraphName}
		groups[k] = append(groups[k], w)
	}
	return groups
}

// processEmbedGroup handles one (gt, name) embed group: fetches nodes
// via wire, composes EmbedText, calls the embedder, writes binary vectors
// via ONE mutate(update_batch) RPC.
func processEmbedGroup(ctx context.Context, p *Pipeline, key graphKey, items []EmbedWork) {
	slog.Debug("pipeline.embed: processing group", "graph_type", key.GraphType, "graph_name", key.GraphName, "items", len(items))
	embedItems, idsForMarker := composeEmbedItems(ctx, p, key, items)
	if len(embedItems) == 0 {
		slog.Debug("pipeline.embed: no items produced — batch skipped",
			"graph_type", key.GraphType, "graph_name", key.GraphName, "items", len(items))
		return
	}

	// Pause if a prior transient failure opened the shared backoff window.
	p.backoff.wait(ctx)
	slog.Debug("pipeline.embed: invoking embedder", "items", len(embedItems), "graph_type", key.GraphType, "graph_name", key.GraphName)
	vectors, err := p.embedder(ctx, embedItems)
	if err != nil {
		slog.Warn("pipeline.embed: embedder call failed",
			"graph_type", key.GraphType, "graph_name", key.GraphName, "items", len(embedItems), "error", err)
		handleEmbedderError(ctx, p, key, idsForMarker, err)
		return
	}
	p.backoff.ok()
	slog.Debug("pipeline.embed: embedder returned",
		"items", len(embedItems), "vectors", len(vectors), "graph_type", key.GraphType, "graph_name", key.GraphName)
	writeEmbedResults(ctx, p, key, vectors, idsForMarker)
	slog.Debug("pipeline.embed: writeEmbedResults done",
		"items", len(embedItems), "graph_type", key.GraphType, "graph_name", key.GraphName)
}

// composeEmbedItems reads the SERVER-COMPOSED EmbedText from each EmbedWork
// (FUL-305) straight into an EmbedItem — it no longer re-fetches the node or
// composes EmbedText client-side (the server already did so at the gap-scan
// site, zero extra read). An item whose server-composed EmbedText is
// whitespace-only is routed to markStuckEmbedItems instead of the embedder: the
// server deliberately EMITS empty-embed items (it does not drop them) so the
// client stamps the durable MetaKeyEmbedFailureReason marker here — the
// eligibility-loop circuit breaker. Without the marker, an eligibility/EmbedText
// mismatch (ShouldEmbed=true but EmbedText returns "" or "\n") would loop the
// pipeline forever.
func composeEmbedItems(ctx context.Context, p *Pipeline, key graphKey, items []EmbedWork) ([]EmbedItem, []string) {
	out := make([]EmbedItem, 0, len(items))
	ids := make([]string, 0, len(items))
	var stuck []string
	for _, w := range items {
		if strings.TrimSpace(w.EmbedText) == "" {
			stuck = append(stuck, w.NodeID)
			continue
		}
		out = append(out, EmbedItem{ID: w.NodeID, Text: w.EmbedText})
		ids = append(ids, w.NodeID)
	}
	if len(stuck) > 0 {
		markStuckEmbedItems(ctx, p, key, stuck)
	}
	return out, ids
}

// markStuckEmbedItems stamps MetaKeyEmbedFailureReason on every node whose
// SERVER-COMPOSED EmbedText was whitespace-only — eligibility-loop circuit
// breaker. The empty-text detection now reads the server-supplied EmbedText
// (EmbedWork.EmbedText) rather than composing locally; the durable marker +
// embedFail metric behavior is unchanged. Issued as ONE mutate(update_batch)
// RPC scoped to (graphType, graphName).
func markStuckEmbedItems(ctx context.Context, p *Pipeline, key graphKey, ids []string) {
	const reason = "embed-text-empty: ShouldEmbed=true but EmbedText returned whitespace-only"
	slog.Warn("pipeline.embed: stuck nodes detected, stamping failure marker",
		"count", len(ids), "reason", reason, "graph_type", key.GraphType, "graph_name", key.GraphName)
	items := make([]updateBatchItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, updateBatchItem{
			ID: id,
			Metadata: map[string]string{
				kgtypes.MetaKeyEmbedFailureReason: reason,
			},
		})
	}
	if err := writeBatchUpdates(ctx, p.client, key.GraphType, key.GraphName, items); err != nil {
		slog.Warn("pipeline.embed: stamp stuck markers failed", "items", len(items), "error", err, "graph_type", key.GraphType, "graph_name", key.GraphName)
	}
	for range ids {
		p.metrics.embedFail()
	}
}

// writeEmbedResults writes each id's binary vector + clears the embed
// failure marker via ONE mutate(update_batch) RPC. ids carries the
// per-position node-ID order matching the embedder's output map keys.
func writeEmbedResults(ctx context.Context, p *Pipeline, key graphKey, vectors map[string][]byte, ids []string) {
	items := make([]updateBatchItem, 0, len(vectors))
	for _, id := range ids {
		v, ok := vectors[id]
		if !ok || len(v) == 0 {
			continue
		}
		items = append(items, updateBatchItem{
			ID:           id,
			BinaryVector: v,
			Metadata: map[string]string{
				kgtypes.MetaKeyEmbedFailureReason: "",
			},
		})
	}
	if len(items) == 0 {
		return
	}
	if err := writeBatchUpdates(ctx, p.client, key.GraphType, key.GraphName, items); err != nil {
		slog.Warn("pipeline.embed: writeEmbedResults batch write failed", "error", err, "items", len(items), "graph_type", key.GraphType, "graph_name", key.GraphName)
		for range items {
			p.metrics.embedFail()
		}
		return
	}
	for range items {
		p.metrics.embedOK()
	}
}

// handleEmbedderError applies the transient/terminal classification per id.
// Both branches log at WARN so the operator sees the actual error in the
// log immediately. Terminal errors stamp the failure marker on every id
// via ONE mutate(update_batch) RPC.
func handleEmbedderError(ctx context.Context, p *Pipeline, key graphKey, ids []string, err error) {
	if llm.IsTransient(err) {
		delay := p.backoff.fail()
		slog.Warn("pipeline.embed: transient error, backing off before retry",
			"items", len(ids), "backoff", delay, "error", err)
		for range ids {
			p.metrics.embedFail()
		}
		return
	}

	reason := err.Error()
	var llmErr *llm.LLMError
	if errors.As(err, &llmErr) {
		reason = llmErr.Error()
	}

	slog.Warn("pipeline.embed: terminal error, marking nodes as failed",
		"items", len(ids), "reason", reason, "error", err)

	items := make([]updateBatchItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, updateBatchItem{
			ID: id,
			Metadata: map[string]string{
				kgtypes.MetaKeyEmbedFailureReason: reason,
			},
		})
	}
	if werr := writeBatchUpdates(ctx, p.client, key.GraphType, key.GraphName, items); werr != nil {
		slog.Warn("pipeline.embed: write failure markers failed",
			"items", len(items), "error", werr, "graph_type", key.GraphType, "graph_name", key.GraphName)
	}
	for range ids {
		p.metrics.embedFail()
	}
}
