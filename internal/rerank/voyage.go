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

// voyageRerankURL is the Voyage rerank endpoint this arm posts to when the
// resolved config supplies no base_url. This file is the ONE home of the
// rerank endpoint literal.
const voyageRerankURL = "https://api.voyageai.com/v1/rerank"

// voyageRerankModel is the model this arm uses when the resolved config
// supplies an empty model. The current SOTA tier; the lite variant is the
// cheaper and faster alternative an operator can now name in [reranker]
// rather than editing this constant.
const voyageRerankModel = "rerank-2.5"

// voyageRerankerInputCap is the absolute max documents per Voyage rerank
// call. The API rejects requests with more than 1000 documents. The
// constructor accepts an inputDocs argument — both production callers pass
// the operating pool widePoolSize (cmd/knowledge/internal/tools/search.go
// and intercept_thoughts_recall.go) — and Rerank defensively re-caps at
// this constant, guarding against an over-large caller value and preserving
// the "no client-side windowing" property at the configured cap.
const voyageRerankerInputCap = 1000

// voyageReranker calls the Voyage AI rerank API to re-score a candidate set.
// Two properties of how it does so are worth stating outright, because both
// were once recorded only as contrasts against a since-removed rank-evaluation
// harness whose own windowing behaviour is no longer in the tree to compare
// against:
//
//  1. NO client-side pre-windowing. All input docs are sent, capped only by the
//     inputDocs value supplied at construction (applyDocCap re-caps at
//     voyageRerankerInputCap). Which top-K to surface is Voyage's decision,
//     driven by the request body's top_k field rather than by trimming the
//     candidate slice before the call.
//  2. The request body carries that top_k field — the rerank API returns only
//     this many scored docs back, rather than re-scoring all candidates.
type voyageReranker struct {
	apiKey    string
	model     string // rerank model (defaults to voyageRerankModel)
	inputDocs int    // max candidates per request; callers pass the operating pool, Rerank re-caps at voyageRerankerInputCap
	topK      int    // top_k value sent in request body (response size)
	baseURL   string // rerank endpoint URL (defaults to voyageRerankURL; overridden in tests)
	client    *http.Client
}

func init() { RegisterProvider(ProviderVoyage, newVoyageFromConfig) }

// newVoyageFromConfig is the registered factory. Empty cfg.BaseURL and
// empty cfg.Model fall back to this arm's own defaults, so an operator
// with no [reranker] section gets exactly today's behavior.
func newVoyageFromConfig(_ context.Context, cfg *Config) (Reranker, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", ErrInvalidConfig)
	}
	r := &voyageReranker{
		apiKey:    cfg.APIKey,
		model:     cfg.Model,
		inputDocs: cfg.InputDocs,
		topK:      cfg.TopK,
		baseURL:   cfg.BaseURL,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
	if r.baseURL == "" {
		r.baseURL = voyageRerankURL
	}
	if r.model == "" {
		r.model = voyageRerankModel
	}
	return r, nil
}

// newVoyageReranker constructs a voyageReranker bound to the given
// inputDocs cap and topK response size. inputDocs is defensively re-capped
// at voyageRerankerInputCap in Rerank.
func newVoyageReranker(apiKey string, inputDocs, topK int) *voyageReranker {
	r, err := newVoyageFromConfig(context.Background(), &Config{
		Provider:  ProviderVoyage,
		APIKey:    apiKey,
		InputDocs: inputDocs,
		TopK:      topK,
	})
	if err != nil {
		// newVoyageFromConfig only errors on a nil config, which cannot
		// happen here; the branch exists so the compiler is satisfied.
		return nil
	}
	arm, ok := r.(*voyageReranker)
	if !ok {
		// Equally unreachable: the factory just above returns exactly this
		// concrete type. Checked rather than asserted so a future factory
		// change surfaces as a nil here instead of a panic in a caller.
		return nil
	}
	return arm
}

// NewVoyage is the exported constructor for a Voyage reranker at this
// arm's defaults, built without going through the registry or the config
// resolution. It is a thin wrapper over the config path, kept so callers
// that hold a bare key and a pool size keep compiling; production goes
// through rerank.NewReranker. Mirrors NewVoyageBinaryEmbedder.
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
		Model:     r.model,
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
// The no-reranker short-circuit gates on having NEITHER a key NOR a custom
// endpoint. It deliberately does NOT gate on the key alone: a KEYLESS
// CUSTOM base_url is a valid configuration — the "key OR base_url, never
// neither" rule the config layer enforces exists for exactly that case, a
// local or compatible server that handles auth out-of-band. Gating on the
// key alone made that configuration validate, construct, report itself
// ready and then never issue a request, returning the input unreranked
// with a nil error: a silent degrade indistinguishable from a reranker
// that ran and preferred the input order. With no key and no custom
// endpoint there is nothing to authenticate against the vendor endpoint,
// so that case still short-circuits — today's documented behavior.
func (r *voyageReranker) Rerank(ctx context.Context, query string, results []engine.SearchResult) ([]engine.SearchResult, error) {
	hasCustomEndpoint := r.baseURL != "" && r.baseURL != voyageRerankURL
	if (r.apiKey == "" && !hasCustomEndpoint) || len(results) == 0 {
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
	// the input docs slice. Build the output preserving that order. Carry each
	// input doc's source-graph identity (Graph/GraphInstance) forward —
	// the rerank only RE-ORDERS and RE-SCORES; it must not strip the per-result
	// graph stamp the graph-UI traverses each result in (this rebuild is a
	// round-trip strip point alongside HydrateFromJSON).
	out := make([]engine.SearchResult, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(send) {
			continue
		}
		out = append(out, engine.SearchResult{
			Node:          send[d.Index].Node,
			Score:         d.RelevanceScore,
			Graph:         send[d.Index].Graph,
			GraphInstance: send[d.Index].GraphInstance,
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
