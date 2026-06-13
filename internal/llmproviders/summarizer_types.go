// SPDX-License-Identifier: Apache-2.0

package llmproviders

import "context"

// BatchChunk is an item to be summarized, identified by ID with its content.
type BatchChunk struct {
	ID      string
	Content string
}

// SummarizeResult holds the output of summarizing a single chunk.
type SummarizeResult struct {
	Summary  string
	Keywords string // space-joined keywords ready to store on Node
}

// summarizer generates summaries for content chunks. Implementations own their
// LLM configuration (prompt, temperature, output format, model) but have no
// control over batching or parallelism — those are pipeline concerns.
//
// The pipeline calls SummarizeBatch with pre-batched chunks sized according
// to SummarizeConfig. Implementations should process whatever they receive
// and return results.
type summarizer interface {
	// SummarizeBatch summarizes a batch of chunks and returns a map of ID → SummarizeResult.
	SummarizeBatch(ctx context.Context, chunks []BatchChunk) (map[string]SummarizeResult, error)
}

// Summarizer is the exported alias of the unexported summarizer interface,
// kept so the pipeline wiring and integration-test harnesses can name the
// interface type for fakes.
type Summarizer = summarizer

// defaultCodeSummarizePrompt is the system prompt used by the summarizer
// implementation for code graph reindexing.
const defaultCodeSummarizePrompt = "For each code chunk, produce a one-sentence summary (under 30 words, specific to domain purpose) " +
	"AND a list of 5-10 search keywords (identifiers, domain terms, operations — no stop words). " +
	"Each chunk is a JSON object with language, type, file, name, and content."

// defaultTopicSummarizePrompt is the system prompt used by the summarizer
// implementation for thought-cluster topic summaries. Each input is a digest
// of RELATED THOUGHTS drawn from a persistent reasoning graph — hypotheses,
// observations, and debugging notes that clustered together — NOT a code
// chunk. The summary must name the shared theme or concern of the cluster in
// plain, searchable terms (it feeds BM25 and name visibility), and the
// keywords must be domain terms rather than code identifiers.
const defaultTopicSummarizePrompt = "For each item, produce a one-line topic summary (under 200 characters) that names the shared theme or concern " +
	"of a cluster of related thoughts in plain, searchable terms — NOT code-chunk wording. " +
	"AND a list of 3-15 keywords that are domain terms describing the topic (no code identifiers, no stop words). " +
	"Each item is a digest of related thoughts (hypotheses, observations, debugging notes) from a persistent reasoning graph; " +
	"produce exactly one summary per item, in the same order as the input."
