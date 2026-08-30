// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// TestRerankRegistry_ValidatesAndConfigDrives pins two things at once: the
// registry's validate-then-lookup ORDER, and that the posted rerank
// request is driven by config rather than by the constants it defaults to.
func TestRerankRegistry_ValidatesAndConfigDrives(t *testing.T) {
	ctx := context.Background()

	t.Run("validates before lookup", func(t *testing.T) {
		t.Cleanup(SnapshotRegistryForTest())
		resetRegistryForTest()

		// An API provider with neither a key nor a base_url is a BAD
		// CONFIG, reported as such even though no factory is registered.
		_, err := NewReranker(ctx, &Config{Provider: ProviderVoyage})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("NewReranker(no credential) = %v; want ErrInvalidConfig", err)
		}
		if errors.Is(err, ErrProviderNotRegistered) {
			t.Errorf("validation must run BEFORE lookup; got a not-registered error: %v", err)
		}

		// A VALID config for a provider with no rerank arm reports the
		// miss and names the provider — this is how "gemini publishes no
		// rerank API" surfaces.
		_, err = NewReranker(ctx, &Config{Provider: ProviderGemini, APIKey: "k"})
		if !errors.Is(err, ErrProviderNotRegistered) {
			t.Fatalf("NewReranker(unregistered) = %v; want ErrProviderNotRegistered", err)
		}
		if !strings.Contains(err.Error(), string(ProviderGemini)) {
			t.Errorf("miss error %q does not name the provider", err)
		}

		// Known-positive: with a factory registered, the same call path
		// dispatches. Without this the two errors above could be produced
		// by a NewReranker that always fails.
		RegisterProvider(ProviderGemini, func(context.Context, *Config) (Reranker, error) {
			return &voyageReranker{}, nil
		})
		if _, err := NewReranker(ctx, &Config{Provider: ProviderGemini, APIKey: "k"}); err != nil {
			t.Fatalf("NewReranker(registered) = %v", err)
		}
		RegisterProvider(ProviderCohere, nil)
		if HasProvider(ProviderCohere) {
			t.Error("a nil factory must not register")
		}
	})

	t.Run("config drives the posted request", func(t *testing.T) {
		var got voyageRerankRequest
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode rerank request: %v", err)
			}
			if h := r.Header.Get("Authorization"); h != "Bearer cfg-key" {
				t.Errorf("Authorization = %q; want the configured key", h)
			}
			if err := json.NewEncoder(w).Encode(voyageRerankResponse{
				Data: []voyageRerankResult{{Index: 0, RelevanceScore: 0.9}},
			}); err != nil {
				t.Errorf("encode rerank response: %v", err)
			}
		}))
		defer server.Close()

		rr, err := NewReranker(ctx, &Config{
			Provider: ProviderVoyage, APIKey: "cfg-key", BaseURL: server.URL,
			Model: "rerank-2.5-lite", InputDocs: 50, TopK: 7,
		})
		if err != nil {
			t.Fatalf("NewReranker: %v", err)
		}
		in := []engine.SearchResult{{Node: &knowledgev1.Node{Id: "n1", Type: "function"}, Score: 0.1}}
		out, err := rr.Rerank(ctx, "q", in)
		if err != nil {
			t.Fatalf("Rerank: %v", err)
		}
		if got.Model != "rerank-2.5-lite" {
			t.Errorf("posted model = %q; want the CONFIGURED model, not the default", got.Model)
		}
		if got.TopK != 7 {
			t.Errorf("posted top_k = %d; want the caller-supplied 7", got.TopK)
		}
		if got.Query != "q" || len(got.Documents) != 1 {
			t.Errorf("posted query/documents = %q / %d", got.Query, len(got.Documents))
		}
		if len(out) != 1 || out[0].Score != 0.9 {
			t.Errorf("Rerank returned %+v; want the re-scored result", out)
		}
	})

	t.Run("empty model and base_url fall back", func(t *testing.T) {
		rr, err := NewReranker(ctx, &Config{Provider: ProviderVoyage, APIKey: "k", InputDocs: 10, TopK: 5})
		if err != nil {
			t.Fatalf("NewReranker: %v", err)
		}
		arm, ok := rr.(*voyageReranker)
		if !ok {
			t.Fatalf("registered voyage factory returned %T", rr)
		}
		if arm.baseURL != voyageRerankURL {
			t.Errorf("baseURL = %q; want the arm's default", arm.baseURL)
		}
		if arm.model != voyageRerankModel {
			t.Errorf("model = %q; want the arm's default", arm.model)
		}
		if !HasProvider(ProviderVoyage) {
			t.Error("the voyage rerank arm must self-register from init()")
		}
	})
}

// TestVoyageReranker_InputCapIsAbsolute proves the absolute API document
// ceiling still governs even when a caller passes a larger operating pool:
// the constant is a hard limit, not an operator choice.
func TestVoyageReranker_InputCapIsAbsolute(t *testing.T) {
	arm := &voyageReranker{inputDocs: voyageRerankerInputCap + 500}
	results := make([]engine.SearchResult, voyageRerankerInputCap+200)
	if got := len(arm.applyDocCap(results)); got != voyageRerankerInputCap {
		t.Errorf("applyDocCap with an over-large caller value kept %d docs; want the absolute cap %d", got, voyageRerankerInputCap)
	}
	// A zero/negative caller value also falls back to the absolute cap.
	zero := &voyageReranker{inputDocs: 0}
	if got := len(zero.applyDocCap(results)); got != voyageRerankerInputCap {
		t.Errorf("applyDocCap with inputDocs=0 kept %d docs; want %d", got, voyageRerankerInputCap)
	}
	// Known-positive: a SMALLER caller value is honored, so the two
	// assertions above are the re-cap firing rather than a clamp that
	// always returns the constant.
	small := &voyageReranker{inputDocs: 12}
	if got := len(small.applyDocCap(results)); got != 12 {
		t.Errorf("applyDocCap with inputDocs=12 kept %d docs; want 12", got)
	}
}
