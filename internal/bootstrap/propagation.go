// SPDX-License-Identifier: Apache-2.0

// Client-side PropagationLoop construction.
//
// The reflective scheduler runs in the serve daemon's client — the hourly
// cluster detection + valence/magnitude propagation loop. wirePropagationRuntime
// mirrors wireWorkerRuntime: a single helper that constructs the
// long-lived runtime object, attaches it to *client, and starts it.
// buildClient's cleanup closure (daemon.go) wires the deferred Stop with the
// same nil-safety convention dream.Runner.Stop uses.
//
// PropagationLoop holds the Execute-only thought.Caller (passed c.router) —
// no client-side store-shaped wrapper. Every read and write the loop performs
// is a wire call through the routed caller, so propagation routes
// cloud-when-logged-in (no local-server probe in a cloud-only daemon).

package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// wirePropagationRuntime constructs the client-side PropagationLoop and
// wires it into the *client. Passes the login-aware c.router (so the loop's
// reads/writes route cloud-when-logged-in) plus the full-pass backstop cadence
// from Config, and assigns the returned loop to c.propLoop. Calls Start so the
// loop begins ticking immediately.
//
// Construction cannot fail today, but the caller logs any future error
// modes the same way wireWorkerRuntime does — see buildClient in daemon.go.
func wirePropagationRuntime(c *client, f Config) {
	// Pass the routedWireClient adapter (NOT c.router directly): it satisfies the
	// Execute-only thought.Caller (so every existing loop read/write keeps routing
	// cloud-when-logged-in via router.Backend) AND exposes PipelineScan, which the
	// loop type-asserts to its package-local reflectProbe for the quiet-tick reflect
	// probe. This reuses the SAME adapter the LLM-pipeline collector uses — no new
	// Router.PipelineScan method, no Router widening. The backstop cadence comes from
	// Config.ReflectBackstopInterval (--reflect-backstop-interval, default 24h).
	scanner := routedWireClient{router: c.router}
	loop := clientthought.NewPropagationLoop(scanner, f.ReflectBackstopInterval)
	// Attach the OPTIONAL topic deps: the member-vector scanner (same routedWireClient
	// adapter) and the LLM topic summarizer adapter. The scanner is consumed by BOTH
	// the hourly runClusterDetection pass (the leaf-attachment drain) AND the
	// manual lever (RunSimilarityPass); the summarizer is lever-only. Both nil-tolerant —
	// a degraded client (no summarizer) still runs centroids + cascade + links at lever
	// time. Drift detection anchors to the stored topic_centroid, so no embedder is wired here.
	loop.WithTopicDeps(scanner, newTopicSummarizerAdapter(context.Background()))
	c.propLoop = loop
	loop.Start()
}

// topicSummarizerAdapter implements clientthought.TopicSummarizer over the
// existing client LLM seam (llmproviders.Summarizer.SummarizeBatch). It is a thin
// shape-translator: each TopicInput becomes one BatchChunk keyed by cluster_id, and
// each result's one-line Summary maps back to a TopicSummary per cluster_id.
//
// The eligible inputs are split into envelope-safe chunks of topicSummarizeBatchSize
// and summarized through a bounded-parallel pass (one SummarizeBatch call per chunk,
// at most topicSummarizeMaxWorkers in flight). A single giant strict-JSON call over
// all groups is beyond the model's reliable structured-output envelope and hard-fails;
// chunking keeps each call inside it. Per-batch failure isolation: a failed batch
// yields empty summaries for ITS topics only (degrade-not-die) and is aggregated into
// an errors.Join error, while every other batch's survivors are still returned.
type topicSummarizerAdapter struct {
	sum llmproviders.Summarizer
}

// topicSummarizeBatchSize is the per-call chunk size for topic summarization. It
// mirrors the pipeline's default batch size — the envelope-safe size proven by the
// high-volume pipeline path — so each strict-JSON SummarizeBatch call stays well
// inside the model's reliable structured-output envelope.
const topicSummarizeBatchSize = 20

// topicSummarizeMaxWorkers caps the bounded-parallel fan-out over topic batches.
// Deliberately small: the lever runs interactively against an opus-class model, so
// the cap is sized for latency/concurrency balance, NOT the pipeline's high throughput.
const topicSummarizeMaxWorkers = 6

// SummarizeTopics splits N TopicInputs into topicSummarizeBatchSize chunks, runs one
// SummarizeBatch call per chunk across a bounded worker pool, and returns one
// TopicSummary per input cluster_id in INPUT ORDER. A failed batch contributes an
// empty Summary for its topics only and is aggregated into the returned error; the
// other batches' summaries are still returned. An empty input list issues ZERO LLM calls.
func (a topicSummarizerAdapter) SummarizeTopics(ctx context.Context, inputs []clientthought.TopicInput) ([]clientthought.TopicSummary, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	batches := chunkTopicInputs(inputs, topicSummarizeBatchSize)

	// Per-batch outcome slots, written by index so each goroutine touches only its
	// own slot — no shared-map mutation under concurrency. Reassembly reads these
	// slots in batch order, preserving input order across the whole result.
	batchResults := make([]map[string]llmproviders.SummarizeResult, len(batches))
	batchErrs := make([]error, len(batches))

	workers := min(runtime.NumCPU(), len(batches), topicSummarizeMaxWorkers)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for bi, batch := range batches {
		wg.Add(1)
		sem <- struct{}{}
		go func(bi int, batch []clientthought.TopicInput) {
			defer wg.Done()
			defer func() { <-sem }()
			chunks := make([]llmproviders.BatchChunk, len(batch))
			for i, in := range batch {
				chunks[i] = llmproviders.BatchChunk{ID: in.ClusterID, Content: in.Content}
			}
			results, err := a.sum.SummarizeBatch(ctx, chunks)
			if err != nil {
				batchErrs[bi] = err
				return
			}
			batchResults[bi] = results
		}(bi, batch)
	}
	wg.Wait()

	// Reassemble in input order: iterate batches in order, and inputs within each
	// batch in order. A failed/absent batch leaves the cluster's Summary empty.
	out := make([]clientthought.TopicSummary, 0, len(inputs))
	for bi, batch := range batches {
		results := batchResults[bi]
		for _, in := range batch {
			out = append(out, clientthought.TopicSummary{
				ClusterID: in.ClusterID,
				Summary:   results[in.ClusterID].Summary,
			})
		}
	}
	return out, errors.Join(batchErrs...)
}

// chunkTopicInputs splits inputs into consecutive batches of at most size,
// preserving order. The final batch carries the remainder.
func chunkTopicInputs(inputs []clientthought.TopicInput, size int) [][]clientthought.TopicInput {
	if size <= 0 {
		return [][]clientthought.TopicInput{inputs}
	}
	batches := make([][]clientthought.TopicInput, 0, (len(inputs)+size-1)/size)
	for i := 0; i < len(inputs); i += size {
		end := min(i+size, len(inputs))
		batches = append(batches, inputs[i:end])
	}
	return batches
}

// newTopicSummarizerAdapter builds the lever-time topic summarizer from the
// [topics] config consumer (absent section inherits [default]) — a SEPARATE
// consumer from the high-volume pipeline [summarizer], so topic summaries can
// run an opus-class model without dragging pipeline summarization costs up.
// Returns a nil clientthought.TopicSummarizer (NOT a typed-nil wrapper) when no
// summarizer is configured or its build fails, so the loop's summarizer field
// stays nil and the lever runs the summary-less degraded path. Mirrors
// wirePipelineRuntime's degrade-not-die summarizer build.
func newTopicSummarizerAdapter(ctx context.Context) clientthought.TopicSummarizer {
	sum, err := llmproviders.BuildSummarizerFor(ctx, config.ConsumerTopics)
	if err != nil {
		slog.Warn("topic summarizer build failed; lever runs summary-less", "error", err)
		return nil
	}
	if sum == nil {
		return nil
	}
	return topicSummarizerAdapter{sum: sum}
}

// ReflectionForcer returns the live PropagationLoop as the on-demand full-corpus
// reflection backstop lever (thoughts(propagate, force_full:true) drives it via
// ForceFullPass). Returns a nil interface (not a typed-nil pointer) when the loop
// was not wired (--no-propagation-runtime, or a cloud-only daemon with the runtime
// disabled) so handlePropagateClient's nil-check fires — returning the typed-nil
// *PropagationLoop directly would yield a non-nil interface wrapping a nil pointer,
// defeating the nil-check (ForceFullPass is itself nil-safe, but the tool must
// report "loop not running" loudly, not invoke a no-op). Satisfies tools.ClientDeps.
func (c *client) ReflectionForcer() tools.ReflectionForcer {
	if c.propLoop == nil {
		return nil
	}
	return c.propLoop
}

// SimilarityForcer returns the live PropagationLoop as the on-demand topic-
// similarity lever (thoughts(propagate, similarity:true) drives it via
// RunSimilarityPass). Returns a nil interface (not a typed-nil pointer) when the
// loop was not wired so handlePropagateClient's nil-check fires with a loud error
// rather than invoking a no-op. Satisfies tools.ClientDeps. Mirrors ReflectionForcer.
func (c *client) SimilarityForcer() tools.SimilarityForcer {
	if c.propLoop == nil {
		return nil
	}
	return c.propLoop
}
