// SPDX-License-Identifier: Apache-2.0

// Package embed provides the concrete Voyage AI binary-embedding client.
//
// It lives under cmd/knowledge/internal/ deliberately: the OSS knowledge-server
// binary (cmd/knowledge-server) must carry ZERO LLM capability (governing
// contract 147fda42 — the server is a generic graph toolbox). Go's internal/
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
	"fmt"
	"io"
	"net/http"
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

// embedBatchSize is the max texts per Voyage API call.
const embedBatchSize = 128

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

// EmbedBinaryBatch generates binary embeddings for multiple texts in one API call.
// Handles batching internally if len(texts) > embedBatchSize.
func (e *voyageEmbedder) EmbedBinaryBatch(ctx context.Context, texts []string) ([][]byte, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	var allResults [][]byte
	for i := 0; i < len(texts); i += embedBatchSize {
		end := min(i+embedBatchSize, len(texts))
		batch := texts[i:end]

		results, err := e.callVoyageBatch(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("batch %d: %w", i/embedBatchSize, err)
		}
		allResults = append(allResults, results...)
	}
	return allResults, nil
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
			Transient: llm.HTTPStatusToTransient(resp.StatusCode),
			Reason:    fmt.Sprintf("http_%d", resp.StatusCode),
			Cause:     fmt.Errorf("voyage embed: status %d: %s", resp.StatusCode, string(respBody)),
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
