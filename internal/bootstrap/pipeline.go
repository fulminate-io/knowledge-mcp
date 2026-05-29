// SPDX-License-Identifier: Apache-2.0

// Client-side LLM pipeline construction.
//
// wirePipelineRuntime mirrors wireWorkerRuntime: build runtime AFTER
// the client is constructed and BEFORE the MCP stdio loop starts, then
// spawn a graph-list refresh goroutine. The refresh polls the loaded-graph
// catalog every Tick (per-type RETURN_MODE_GRAPH_NAMES reads), diffs against the
// local collectorCancels map, and calls Register/Unregister for the delta
// — worst-case lag for new-graph pickup is one collector tick.

package bootstrap

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
)

// wirePipelineRuntime constructs the client-side LLM pipeline (summarize
// + embed worker pools + per-graph collectors) and attaches it to *client.
// Returns nil on success; nil + log-and-skip when --no-llm-pipeline is
// set OR neither summarizer nor embedder is configured (graceful degrade —
// the rest of the MCP loop continues to work without LLM features).
//
// The pipeline's refresh goroutine runs under ctx (canceled by the
// caller's defer p.Stop chain) so it exits cleanly on shutdown.
func wirePipelineRuntime(c *client, f Config) error {
	if f.NoLLMPipeline {
		slog.Info("client pipeline: skipped (--no-llm-pipeline)")
		return nil
	}
	ctx := context.Background()

	sum, err := llmproviders.BuildSummarizer(ctx)
	if err != nil {
		// Don't bubble — degrade-not-die. The client keeps serving
		// non-LLM tools so a misconfigured summarizer doesn't take down
		// the entire MCP loop.
		slog.Warn("client pipeline: summarizer build failed; skipping pipeline wire", "error", err)
		return nil
	}
	emb := llmproviders.BuildEmbedder()
	if sum == nil && emb == nil {
		slog.Info("client pipeline: no summarizer or embedder configured; skipping pipeline wire")
		return nil
	}

	pcfg := pipeline.Config{
		SummaryChannelSize: f.SummaryChannelSize,
		SummaryBatchSize:   f.SummaryBatchSize,
		SummaryWorkers:     f.SummaryWorkers,
		EmbedChannelSize:   f.EmbedChannelSize,
		EmbedBatchSize:     f.EmbedBatchSize,
		EmbedWorkers:       f.EmbedWorkers,
		Tick:               f.PipelineTick,
	}

	p := pipeline.New(pcfg, c.local, adaptSummarizer(sum), adaptEmbedder(emb))
	c.pipeline = p
	if err := p.Start(ctx); err != nil {
		return err
	}

	// Initial registration: poll once + register each (gt, name).
	// Refresh goroutine takes over from here, picking up the delta on
	// subsequent ticks (worst-case lag: one tick).
	p.RefreshOnceForBoot(ctx) //nolint:errcheck // best-effort initial seed

	// Continuous refresh in background.
	go p.RefreshLoadedGraphs(ctx)

	return nil
}

// adaptSummarizer converts an llmproviders.Summarizer to the pipeline
// package's SummarizerFunc shape. nil → nil so the pipeline's worker
// treats it as a no-op (the dispatcher still routes batches but the
// worker bails on first call when summarizer is nil).
func adaptSummarizer(s llmproviders.Summarizer) pipeline.SummarizerFunc {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, chunks []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		return s.SummarizeBatch(ctx, chunks)
	}
}

// adaptEmbedder converts an embed.BinaryEmbedder to the pipeline package's
// EmbedderFunc shape. Returns per-id binary vectors keyed by ID so the
// writeback batch can land each vector alongside its node in one RPC.
// nil → nil (pipeline embed path bails when embedder is nil).
func adaptEmbedder(e embed.BinaryEmbedder) pipeline.EmbedderFunc {
	if e == nil {
		return nil
	}
	return func(ctx context.Context, items []pipeline.EmbedItem) (map[string][]byte, error) {
		texts := make([]string, len(items))
		for i, it := range items {
			texts[i] = it.Text
		}
		vecs, err := e.EmbedBinaryBatch(ctx, texts)
		if err != nil {
			return nil, err
		}
		out := make(map[string][]byte, len(items))
		for i, it := range items {
			if i >= len(vecs) {
				break
			}
			out[it.ID] = vecs[i]
		}
		return out, nil
	}
}
