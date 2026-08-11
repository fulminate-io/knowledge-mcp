// SPDX-License-Identifier: Apache-2.0

// Package embed provides the concrete Voyage AI binary-embedding client.
//
// It lives under cmd/knowledge/internal/ deliberately: the OSS knowledge-server
// binary (cmd/knowledge-server) must carry ZERO LLM capability (by design —
// the server is a generic graph toolbox). Go's internal/
// visibility makes this package STRUCTURALLY unreachable from any binary
// outside the cmd/knowledge subtree — the server cannot import it even by
// accident, so the Voyage HTTP embedding code can never reach a server binary.
// That compiler-enforced boundary is stronger than the prior pkg/ link
// discipline (which relied on the server merely declining to import it).
//
// The sole consumer is the LLM-key-holding stdio client, cmd/knowledge: it
// embeds content during the client-side index pipeline and embeds the query
// text at search time, then ships the vectors over the wire. No server binary
// embeds — they store and search the client-supplied vectors only.
//
// The BinaryEmbedder interface contract co-locates with this concrete
// implementation (see embedder.go) — idiomatic for a single-impl interface.
// Mirrors the sibling cmd/knowledge/internal/rerank layout for the
// client-side reranker.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// voyageEmbedder calls the Voyage AI API for binary code embeddings.
type voyageEmbedder struct {
	APIKey  string
	BaseURL string
	Model   string
	client  *http.Client
}

// newVoyageEmbedder creates an embedder using Voyage code-3 with ubinary output.
func newVoyageEmbedder(apiKey string) *voyageEmbedder {
	return &voyageEmbedder{
		APIKey:  apiKey,
		BaseURL: "https://api.voyageai.com",
		Model:   "voyage-code-3",
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// NewVoyageBinaryEmbedder is the exported constructor for the stdio client's
// llmproviders.BuildEmbedder and harnesses that need a real Voyage binary
// embedder outside the BootstrapConfig path — e.g. the rankeval regen tool
// that pre-computes query embeddings to commit alongside labels. Returns
// BinaryEmbedder so callers keep type-qualifying against the interface
// (declared in embedder.go).
func NewVoyageBinaryEmbedder(apiKey string) BinaryEmbedder {
	return newVoyageEmbedder(apiKey)
}

// Compile-time assertion: *voyageEmbedder satisfies BinaryEmbedder.
var _ BinaryEmbedder = (*voyageEmbedder)(nil)

// Voyage enforces two independent caps per embeddings call: a max item count,
// and a max TOTAL token count across the batch, counted by its own tokenizer
// after per-item truncation. Exceeding the total fails the whole call with
// TOO_MANY_TOKENS_IN_BATCH, so packing by item count alone lets a batch of
// large texts fail every text in it. Packs are therefore also bounded by an
// estimated token budget. The estimate is deliberately conservative (few
// characters per token): estimator error can only shrink a pack, never
// overfill one.
const (
	// embedBatchSize is the max texts per Voyage API call.
	embedBatchSize = 128
	// batchTokenBudget bounds the estimated token total per call; the real
	// cap is 120k, and the gap is headroom for estimator error.
	batchTokenBudget = 100_000
	// charsPerToken is the conservative divisor for the token estimate.
	charsPerToken = 3
)

// estimateTokens conservatively estimates the Voyage token count of one text.
func estimateTokens(text string) int {
	return len(text)/charsPerToken + 1
}

type voyageEmbedRequest struct {
	Input      []string `json:"input"`
	Model      string   `json:"model"`
	InputType  string   `json:"input_type"`
	OutputDim  int      `json:"output_dimension"`
	OutputType string   `json:"output_dtype"`
}

type voyageEmbedResponse struct {
	Data []struct {
		Embedding []int `json:"embedding"` // ubinary: array of uint8 values
	} `json:"data"`
}

// EmbedBinary generates a 256-bit binary embedding via Voyage code-3.
func (e *voyageEmbedder) EmbedBinary(ctx context.Context, text string) ([]byte, error) {
	results, err := e.EmbedBinaryBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}
	return results[0], nil
}

// EmbedBinaryBatch generates binary embeddings for multiple texts, splitting
// them into API calls that respect both of Voyage's per-call caps: item count
// and total batch tokens. A text whose own estimate exceeds the budget is sent
// alone — Voyage truncates each item to its context limit before counting, so
// a single text cannot overflow the batch cap. Results preserve input order.
func (e *voyageEmbedder) EmbedBinaryBatch(ctx context.Context, texts []string) ([][]byte, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	var allResults [][]byte
	batchNo := 0
	start := 0
	for start < len(texts) {
		end := start + 1
		budget := estimateTokens(texts[start])
		for end < len(texts) && end-start < embedBatchSize {
			t := estimateTokens(texts[end])
			if budget+t > batchTokenBudget {
				break
			}
			budget += t
			end++
		}

		results, err := e.embedPackBisecting(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("batch %d: %w", batchNo, err)
		}
		allResults = append(allResults, results...)
		start = end
		batchNo++
	}
	return allResults, nil
}

// embedPackBisecting posts one pack and, when Voyage rejects it for total
// batch tokens anyway (the estimator undercounted), splits the pack in half
// and retries each side rather than failing every text in it. The recursion
// terminates because a single text cannot overflow the batch cap (per-item
// truncation happens before the count); any other error propagates unchanged.
func (e *voyageEmbedder) embedPackBisecting(ctx context.Context, texts []string) ([][]byte, error) {
	results, err := e.callVoyageBatch(ctx, texts)
	if err == nil || len(texts) < 2 || !isBatchTokenOverflow(err) {
		return results, err
	}

	mid := len(texts) / 2
	left, err := e.embedPackBisecting(ctx, texts[:mid])
	if err != nil {
		return nil, err
	}
	right, err := e.embedPackBisecting(ctx, texts[mid:])
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

// isBatchTokenOverflow recognizes Voyage's whole-batch token-cap rejection,
// the one 400 whose correct handling is splitting the batch, not failing it.
func isBatchTokenOverflow(err error) bool {
	var llmErr *llm.LLMError
	return errors.As(err, &llmErr) && llmErr.Cause != nil &&
		strings.Contains(llmErr.Cause.Error(), "TOO_MANY_TOKENS_IN_BATCH")
}

// callVoyageBatch posts one batch of texts to the Voyage embeddings API.
// Errors return as *llm.LLMError so the pipeline embed worker can distinguish
// transient (HTTP 429 / 5xx — retry next tick) from terminal (4xx-other /
// JSON parse — write embed_failure_reason marker). Batch wrapper above already
// passes the error through `fmt.Errorf("batch %d: %w", ...)` so errors.As
// traverses the wrap.
func (e *voyageEmbedder) callVoyageBatch(ctx context.Context, texts []string) ([][]byte, error) {
	body, err := json.Marshal(voyageEmbedRequest{
		Input:      texts,
		Model:      e.Model,
		InputType:  "document",
		OutputDim:  256,
		OutputType: "ubinary",
	})
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "marshal_request", Cause: err}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "create_request", Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.APIKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, &llm.LLMError{Transient: true, Reason: "network", Cause: fmt.Errorf("voyage embed request: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &llm.LLMError{
			Transient:  llm.HTTPStatusToTransient(resp.StatusCode),
			Reason:     fmt.Sprintf("http_%d", resp.StatusCode),
			RetryAfter: llm.ParseRetryAfter(resp.Header),
			Cause:      fmt.Errorf("voyage embed: status %d: %s", resp.StatusCode, string(respBody)),
		}
	}

	var result voyageEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "decode_response", Cause: err}
	}

	vectors := make([][]byte, len(result.Data))
	for i, d := range result.Data {
		vec := make([]byte, len(d.Embedding))
		for j, v := range d.Embedding {
			vec[j] = byte(v)
		}
		vectors[i] = vec
	}
	return vectors, nil
}

// Available checks if the API key is set.
func (e *voyageEmbedder) Available() bool {
	return e.APIKey != ""
}
