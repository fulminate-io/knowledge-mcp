// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// voyageRerankURL is the Voyage rerank endpoint. Hardcoded — changing the
// URL would mean a different model provider entirely.
const voyageRerankURL = "https://api.voyageai.com/v1/rerank"

// voyageRerankModel is the model used. rerank-2.5 is the current SOTA tier;
// rerank-2.5-lite would be the cheaper/faster alternative if cost matters.
const voyageRerankModel = "rerank-2.5"

// voyageRerankerInputCap is the absolute max documents per Voyage rerank
// call. The API rejects requests with more than 1000 documents. The
// constructor accepts a configurable inputDocs (BootstrapConfig.RerankInputDocs)
// but defensively caps at this constant inside Rerank — guards against an
// unwise BootstrapConfig override and preserves the "no client-side
// windowing" property at the configured cap.
const voyageRerankerInputCap = 1000

// voyageReranker calls the Voyage AI rerank API to re-score a candidate set.
// The production reranker diverges from the test-internal makeVoyageRerank
// (in domains/store/rankeval/rerankers_voyage_test.go) in two ways:
//
//  1. NO client-side pre-windowing. The test ranks `min(len(in), windowSize)`
//     docs; production sends ALL input docs (capped only by inputDocs at
//     construction). This lets Voyage decide which top-K to surface using
//     the request body's top_k field.
//  2. The request body carries a top_k field — the rerank API returns only
//     this many scored docs back, rather than re-scoring all candidates.
type voyageReranker struct {
	apiKey    string
	inputDocs int    // max candidates sent per request (defensive cap; default 1000)
	topK      int    // top_k value sent in request body (response size)
	baseURL   string // rerank endpoint URL (defaults to voyageRerankURL; overridden in tests)
	client    *http.Client
}

// newVoyageReranker constructs a voyageReranker bound to the given
// inputDocs cap and topK response size. inputDocs is defensively re-capped
// at voyageRerankerInputCap in Rerank.
func newVoyageReranker(apiKey string, inputDocs, topK int) *voyageReranker {
	return &voyageReranker{
		apiKey:    apiKey,
		inputDocs: inputDocs,
		topK:      topK,
		baseURL:   voyageRerankURL,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

// NewVoyage is the exported constructor for callers that need to inject
// a real Voyage reranker outside the BootstrapConfig path — e.g. the
// cloud serve subcommand which bypasses kgstore.Init via DBOverride and
// must wire the reranker manually so cloud search parity matches OSS.
// Production OSS code goes through bootstrap. Mirrors
// NewVoyageBinaryEmbedder.
func NewVoyage(apiKey string, inputDocs, topK int) Reranker {
	return newVoyageReranker(apiKey, inputDocs, topK)
}

// Compile-time interface check — voyageReranker must satisfy Reranker.
var _ Reranker = (*voyageReranker)(nil)

// applyDocCap clamps the input candidate slice to the configured inputDocs
// cap (or the absolute API ceiling, whichever is lower). Extracted from
// Rerank so the parent stays under the funlen cap.
func (r *voyageReranker) applyDocCap(results []engine.SearchResult) []engine.SearchResult {
	cap := r.inputDocs
	if cap <= 0 || cap > voyageRerankerInputCap {
		cap = voyageRerankerInputCap
	}
	if len(results) > cap {
		return results[:cap]
	}
	return results
}

// buildRequestBody renders each candidate via renderForRerank, marshals the
// Voyage rerank-request JSON, and returns (body, totalRenderedBytes, err).
// totalRenderedBytes is the sum of rendered document lengths — used as a
// payload-size signal in the per-call timing log.
func (r *voyageReranker) buildRequestBody(query string, send []engine.SearchResult) ([]byte, int, error) {
	docs := make([]string, len(send))
	totalDocBytes := 0
	for i := range send {
		docs[i] = renderForRerank(send[i].Node)
		totalDocBytes += len(docs[i])
	}
	body, err := json.Marshal(voyageRerankRequest{
		Query:     query,
		Documents: docs,
		Model:     voyageRerankModel,
		TopK:      r.topK,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("voyage rerank marshal: %w", err)
	}
	return body, totalDocBytes, nil
}

type voyageRerankRequest struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	Model     string   `json:"model"`
	TopK      int      `json:"top_k,omitempty"`
}

type voyageRerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type voyageRerankResponse struct {
	Data []voyageRerankResult `json:"data"`
}

// Rerank issues one Voyage rerank request for the given query+candidates
// and returns a re-ordered slice with updated Scores. On any error the
// original `results` slice is returned unchanged alongside the error.
//
// The Voyage response sorts results by relevance descending; the response's
// Data[i].Index points back into the input docs slice. The output preserves
// that descending-relevance order: out[i].Node = results[Data[i].Index].Node,
// out[i].Score = Data[i].RelevanceScore.
func (r *voyageReranker) Rerank(ctx context.Context, query string, results []engine.SearchResult) ([]engine.SearchResult, error) {
	if r.apiKey == "" || len(results) == 0 {
		return results, nil
	}

	totalStart := time.Now()
	send := r.applyDocCap(results)

	renderStart := time.Now()
	body, totalDocBytes, err := r.buildRequestBody(query, send)
	if err != nil {
		return results, err
	}
	renderDur := time.Since(renderStart)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL, bytes.NewReader(body))
	if err != nil {
		return results, fmt.Errorf("voyage rerank new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.apiKey)

	httpStart := time.Now()
	resp, err := r.client.Do(req)
	httpDur := time.Since(httpStart)
	if err != nil {
		slog.Debug("voyage rerank: HTTP error",
			"docs", len(send), "top_k", r.topK,
			"render_dur", renderDur.Round(time.Microsecond),
			"http_dur", httpDur.Round(time.Microsecond),
			"err", err.Error())
		return results, fmt.Errorf("voyage rerank HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return results, fmt.Errorf("voyage rerank status %d: %s", resp.StatusCode, string(respBody))
	}

	decodeStart := time.Now()
	var parsed voyageRerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return results, fmt.Errorf("voyage rerank decode: %w", err)
	}
	decodeDur := time.Since(decodeStart)

	// Voyage returns Data sorted by relevance descending; Index references
	// the input docs slice. Build the output preserving that order.
	out := make([]engine.SearchResult, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(send) {
			continue
		}
		out = append(out, engine.SearchResult{
			Node:  send[d.Index].Node,
			Score: d.RelevanceScore,
		})
	}

	// Per-call timing breakdown for cross-referencing the always-on search
	// latency footer against Voyage roundtrip time. Debug-level so the
	// default INFO log stays quiet — turn on with --log-level=debug when
	// chasing search-latency outliers. Network is the dominant cost; for
	// a Redis-tier in-memory database, any sub-second hybrid search is
	// bottlenecked here, not in the local hybrid pipeline.
	slog.Debug("voyage rerank: ok",
		"docs", len(send),
		"top_k", r.topK,
		"doc_bytes", totalDocBytes,
		"render_dur", renderDur.Round(time.Microsecond),
		"http_dur", httpDur.Round(time.Microsecond),
		"decode_dur", decodeDur.Round(time.Microsecond),
		"total_dur", time.Since(totalStart).Round(time.Microsecond))
	return out, nil
}
