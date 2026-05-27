// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVoyageReranker_RequestShape verifies the production reranker emits the
// correct JSON request shape (Query, Documents, Model, TopK) and applies the
// returned relevance_scores to the response slice in Voyage's output order.
//
// Mirrors the httptest pattern in embed_openai_test.go.
func TestVoyageReranker_RequestShape(t *testing.T) {
	var captured voyageRerankRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request body: %v", err)
			return
		}

		// Return scores in reverse order so the application step is observable:
		// input doc[2] gets the highest score, then doc[1], then doc[0].
		resp := voyageRerankResponse{
			Data: []voyageRerankResult{
				{Index: 2, RelevanceScore: 0.95},
				{Index: 1, RelevanceScore: 0.55},
				{Index: 0, RelevanceScore: 0.15},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	r := newVoyageReranker("test-key", 1000, 500)
	r.baseURL = server.URL

	in := []engine.SearchResult{
		{Node: &knowledgev1.Node{Id: "a", Type: "function", SymbolName: "Alpha"}, Score: 0.1},
		{Node: &knowledgev1.Node{Id: "b", Type: "function", SymbolName: "Beta"}, Score: 0.2},
		{Node: &knowledgev1.Node{Id: "c", Type: "function", SymbolName: "Gamma"}, Score: 0.3},
	}
	out, err := r.Rerank(context.Background(), "my query", in)
	require.NoError(t, err)

	// Request body assertions.
	assert.Equal(t, "my query", captured.Query)
	assert.Equal(t, voyageRerankModel, captured.Model)
	assert.Equal(t, 500, captured.TopK)
	require.Len(t, captured.Documents, 3)

	// Output ordering and score application.
	require.Len(t, out, 3)
	assert.Equal(t, "c", out[0].Node.Id)
	assert.InDelta(t, 0.95, out[0].Score, 0.001)
	assert.Equal(t, "b", out[1].Node.Id)
	assert.InDelta(t, 0.55, out[1].Score, 0.001)
	assert.Equal(t, "a", out[2].Node.Id)
	assert.InDelta(t, 0.15, out[2].Score, 0.001)
}

// TestVoyageReranker_ScoreApplication verifies that Voyage's relevance_scores
// replace the input fan-in scores rather than being combined with them.
// Critical contract: the reranker is authoritative — its scores stand alone.
func TestVoyageReranker_ScoreApplication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := voyageRerankResponse{
			Data: []voyageRerankResult{
				{Index: 0, RelevanceScore: 0.99},
				{Index: 1, RelevanceScore: 0.88},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	r := newVoyageReranker("test-key", 1000, 500)
	r.baseURL = server.URL

	// Input scores deliberately set to something that would NOT round-trip
	// if the reranker were combining instead of replacing.
	in := []engine.SearchResult{
		{Node: &knowledgev1.Node{Id: "first"}, Score: 100.0},
		{Node: &knowledgev1.Node{Id: "second"}, Score: 200.0},
	}
	out, err := r.Rerank(context.Background(), "anything", in)
	require.NoError(t, err)

	require.Len(t, out, 2)
	// Scores from Voyage replace the input scores entirely.
	assert.InDelta(t, 0.99, out[0].Score, 0.001)
	assert.InDelta(t, 0.88, out[1].Score, 0.001)
}

// TestVoyageReranker_HTTP500FallsBack verifies the contract: on any HTTP
// error the reranker returns (input slice unchanged, non-nil error). The
// caller in executeSearchHybrid logs the error and falls back to fan-in
// scoring (verified by TestSearch_RerankFailure_FallsBackSilently).
func TestVoyageReranker_HTTP500FallsBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"voyage 500"}`))
	}))
	defer server.Close()

	r := newVoyageReranker("test-key", 1000, 500)
	r.baseURL = server.URL

	in := []engine.SearchResult{
		{Node: &knowledgev1.Node{Id: "a"}, Score: 0.1},
		{Node: &knowledgev1.Node{Id: "b"}, Score: 0.2},
	}
	out, err := r.Rerank(context.Background(), "q", in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	// Contract: returned slice has the same len as input.
	require.Len(t, out, len(in))
}

// TestVoyageReranker_NoClientWindowing verifies the defensive cap inside
// Rerank: passing 1500 input docs (more than inputDocs=1000) sends exactly
// 1000 docs, NOT 500. Voyage's API rejects >1000; this guards against
// accidentally re-introducing the test-code's pre-windowing to 500.
func TestVoyageReranker_NoClientWindowing(t *testing.T) {
	var captured voyageRerankRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request body: %v", err)
			return
		}
		// Echo back enough scores to keep the decode happy.
		data := make([]voyageRerankResult, 0, len(captured.Documents))
		for i := range captured.Documents {
			data = append(data, voyageRerankResult{Index: i, RelevanceScore: 0.5})
		}
		json.NewEncoder(w).Encode(voyageRerankResponse{Data: data})
	}))
	defer server.Close()

	r := newVoyageReranker("test-key", 1000, 500)
	r.baseURL = server.URL

	// 1500 input docs — more than inputDocs=1000, more than topK=500.
	in := make([]engine.SearchResult, 1500)
	for i := range in {
		in[i] = engine.SearchResult{Node: &knowledgev1.Node{Id: "n", SymbolName: "Sym"}, Score: float64(i)}
	}
	_, err := r.Rerank(context.Background(), "q", in)
	require.NoError(t, err)
	// Cap is inputDocs (1000), not topK (500).
	assert.Len(t, captured.Documents, 1000)
}
