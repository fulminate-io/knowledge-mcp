// SPDX-License-Identifier: Apache-2.0

package thought

// loop_topic_deps.go holds the OPTIONAL dependency seams the PropagationLoop owns —
// the member-vector scanner and the topic summarizer abstractions, plus the
// WithTopicDeps fluent setter. The summarizer is consumed only by the manual
// similarity lever (RunSimilarityPass); the scanner is consumed by BOTH the lever
// AND the hourly runClusterDetection pass (the leaf-attachment drain).

import "context"

// TopicInput is one eligible group handed to the TopicSummarizer: its stable
// cluster_id and the concatenated member symbol-names/summaries that form the
// summarization prompt content.
type TopicInput struct {
	ClusterID string
	Content   string
}

// TopicSummary is the summarizer's per-group result: the cluster_id it belongs
// to and the one-line topic summary text.
type TopicSummary struct {
	ClusterID string
	Summary   string
}

// TopicSummarizer is the narrow LLM seam the lever uses to produce topic
// summaries. Declared in the thought package so the loop depends on an
// abstraction it owns; the bootstrap adapts the concrete llmproviders.Summarizer
// to it via a bounded-parallel chunked pass (envelope-safe per-call batches, never
// per-group calls), with per-batch failure isolation. nil → no summaries (degraded).
type TopicSummarizer interface {
	SummarizeTopics(ctx context.Context, inputs []TopicInput) ([]TopicSummary, error)
}

// WithTopicDeps attaches the OPTIONAL dependencies (member-vector scanner, topic
// summarizer) to the loop and returns it for fluent construction. The production
// bootstrap calls this with the real adapters; tests omit it, leaving the loop in
// degraded mode. The scanner IS read by the hourly pass (the leaf-attachment drain
// skips loudly when it is nil); the summarizer is lever-only. Any argument may be nil.
func (p *PropagationLoop) WithTopicDeps(scanner PipelineScanner, summarizer TopicSummarizer) *PropagationLoop {
	if p == nil {
		return nil
	}
	p.scanner = scanner
	p.summarizer = summarizer
	return p
}

// WithCorpusScanner attaches the OPTIONAL CorpusDelta wire seam + initializes the
// resident thought-corpus cache, returning the loop for fluent
// construction. The production bootstrap passes the routed CorpusDelta client; a
// nil scanner leaves the loop in DEGRADED mode (corpus stays nil → the full
// drainThoughtBrowse path, behavior-equivalent to pre-cache). Nil-tolerant on p.
func (p *PropagationLoop) WithCorpusScanner(scanner CorpusDeltaScanner) *PropagationLoop {
	if p == nil {
		return nil
	}
	p.corpusScanner = scanner
	if scanner != nil {
		p.corpus = newCorpusCache()
	}
	return p
}

// WithCorpusPersistence attaches the client data root the resident corpus cache
// persists its warm-start record under, returning the loop for fluent
// construction. An EMPTY root leaves persistence DISABLED — the loop stays
// resident-only and cold on every restart, which is what every test that omits
// this setter exercises. Nil-tolerant on p.
//
// It takes the ROOT and never the record path: CorpusCachePathFor binds the graph
// type and name internally to the same (knowledge, "default") pair drainCorpusDelta
// sends on the wire, so the record and the drain cannot disagree about which graph
// they describe. Reconstructing the layout at the call site is what would let them.
func (p *PropagationLoop) WithCorpusPersistence(root string) *PropagationLoop {
	if p == nil {
		return nil
	}
	if root != "" {
		p.corpusCachePath = CorpusCachePathFor(root)
	}
	return p
}
