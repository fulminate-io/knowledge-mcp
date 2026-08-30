// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// TestRerankCheckProvider_PingsResolvedBaseURL mirrors the embed axis
// check's contract for the rerank axis: the ping goes to the RESOLVED
// base_url, an unconfigured axis calls nothing, and a rejection is an
// error naming the class.
//
// The unconfigured case is the known-positive control for the hit
// counter — it must leave the counter at ZERO.
func TestRerankCheckProvider_PingsResolvedBaseURL(t *testing.T) {
	ctx := context.Background()
	t.Setenv("VOYAGE_API_KEY", "")
	t.Cleanup(config.SetForTest(nil))

	server := func(hits *atomic.Int32, status int) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			if err := json.NewEncoder(w).Encode(voyageRerankResponse{
				Data: []voyageRerankResult{{Index: 0, RelevanceScore: 1}},
			}); err != nil {
				t.Errorf("encode rerank response: %v", err)
			}
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	t.Run("unconfigured axis calls nothing", func(t *testing.T) {
		var hits atomic.Int32
		_ = server(&hits, http.StatusOK)
		sec := config.RerankSection{Provider: config.EmbedProviderVoyage}
		if err := CheckProvider(ctx, sec); err != nil {
			t.Fatalf("an axis with no credential and no base_url must opt out, not error: %v", err)
		}
		if got := hits.Load(); got != 0 {
			t.Errorf("the opt-out path issued %d requests; want 0 (zero startup spend is the lever)", got)
		}
	})

	t.Run("resolved base_url is pinged", func(t *testing.T) {
		t.Setenv("VOYAGE_API_KEY", "k")
		var hits atomic.Int32
		srv := server(&hits, http.StatusOK)
		sec := config.RerankSection{Provider: config.EmbedProviderVoyage, BaseURL: srv.URL}
		if err := CheckProvider(ctx, sec); err != nil {
			t.Fatalf("CheckProvider against a live local endpoint: %v", err)
		}
		if got := hits.Load(); got != 1 {
			t.Errorf("the resolved base_url received %d requests; want exactly 1", got)
		}
	})

	t.Run("a rejecting endpoint errors", func(t *testing.T) {
		t.Setenv("VOYAGE_API_KEY", "k")
		var hits atomic.Int32
		srv := server(&hits, http.StatusUnauthorized)
		sec := config.RerankSection{Provider: config.EmbedProviderVoyage, BaseURL: srv.URL}
		err := CheckProvider(ctx, sec)
		if err == nil {
			t.Fatal("a 401 from the configured endpoint must be an error")
		}
		if !strings.Contains(err.Error(), "rerank precheck") {
			t.Errorf("error %q does not name the axis", err)
		}
		if got := hits.Load(); got != 1 {
			t.Errorf("hits = %d; want 1", got)
		}
	})

	t.Run("a KEYLESS custom base_url issues the request", func(t *testing.T) {
		// The config layer ACCEPTS a keyless base_url on this axis — the
		// shared "key OR base_url, never neither" rule, which exists for
		// exactly the local-compatible-endpoint case, where a server
		// handles auth out-of-band. The arm must honor it.
		//
		// It previously did not: Rerank returned early on an empty apiKey
		// without consulting baseURL, so a reranker pointed at a keyless
		// local endpoint validated, constructed, reported itself ready and
		// then never issued a request — the caller saw unreranked results
		// and a nil error, indistinguishable from a reranker that ran and
		// preferred the input order. That silent degrade is the defect
		// this subtest now pins closed.
		t.Setenv("VOYAGE_API_KEY", "")
		var hits atomic.Int32
		srv := server(&hits, http.StatusOK)
		sec := config.RerankSection{Provider: config.EmbedProviderVoyage, BaseURL: srv.URL}
		if err := CheckProvider(ctx, sec); err != nil {
			t.Fatalf("CheckProvider with a keyless base_url: %v", err)
		}
		if got := hits.Load(); got != 1 {
			t.Fatalf("a keyless custom base_url issued %d requests; want exactly 1 — the local-endpoint case the key-or-base_url rule exists for", got)
		}
	})

	t.Run("keyless with the DEFAULT endpoint stays inert", func(t *testing.T) {
		// The other half of the repair, and the known-negative that keeps
		// the assertion above honest. With NO key and NO custom base_url
		// there is nothing to authenticate against the vendor endpoint, so
		// the arm must still short-circuit and return the input unchanged
		// with a nil error — today's documented no-reranker behavior,
		// unchanged. Asserted directly on the arm because CheckProvider
		// opts out one layer earlier for this same shape.
		arm := &voyageReranker{baseURL: voyageRerankURL, model: voyageRerankModel, inputDocs: 10, topK: 5}
		in := []engine.SearchResult{{Node: &knowledgev1.Node{Id: "n1", Type: "function"}, Score: 0.1}}
		out, err := arm.Rerank(ctx, "q", in)
		if err != nil {
			t.Fatalf("keyless at the default endpoint must not error: %v", err)
		}
		if len(out) != 1 || out[0].Score != 0.1 {
			t.Errorf("keyless at the default endpoint must return the input unchanged; got %+v", out)
		}
	})

	t.Run("a provider with no rerank arm errors before any call", func(t *testing.T) {
		t.Setenv("GEMINI_API_KEY", "gk")
		var hits atomic.Int32
		_ = server(&hits, http.StatusOK)
		err := CheckProvider(ctx, config.RerankSection{Provider: config.EmbedProviderGemini})
		if err == nil {
			t.Fatal("a provider with no registered rerank arm must error")
		}
		if got := hits.Load(); got != 0 {
			t.Errorf("a refused config issued %d requests; want 0", got)
		}
	})
}
