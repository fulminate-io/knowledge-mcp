// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// TestBuildReranker_NilOnMissingCredential pins the shared helper's two
// dispositions: the PRE-EXISTING degrade (missing credential yields a nil
// reranker, which the callers turn into RRF ordering) and the NEW loud
// path (a malformed [reranker] section is an error, logged, and also
// yields nil rather than being unrepresentable).
//
// The credential-present case is the known-positive control: it returns a
// real reranker, so the nils below are the specific conditions firing and
// not a helper that always returns nil.
func TestBuildReranker_NilOnMissingCredential(t *testing.T) {
	ctx := context.Background()
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("COHERE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	t.Run("unloaded config with no key yields nil", func(t *testing.T) {
		t.Cleanup(config.SetForTest(nil))
		if rr := buildReranker(ctx, 50, 10); rr != nil {
			t.Errorf("buildReranker with no config and no key = %T; want nil", rr)
		}
		if rerankCredentialPresent() {
			t.Error("rerankCredentialPresent with no config and no key must be false")
		}
	})

	t.Run("unloaded config still reads the env key", func(t *testing.T) {
		// BEHAVIOR PRESERVATION. Before this axis was configurable both
		// call sites read the credential through an accessor that falls
		// back to the env var whether or not a config file was ever
		// loaded, so a process with a key in the environment and no config
		// still reranked. Losing that would silently disable reranking for
		// every config-less install.
		t.Setenv("VOYAGE_API_KEY", "env-only")
		t.Cleanup(config.SetForTest(nil))
		if !rerankCredentialPresent() {
			t.Error("an env-only key with no loaded config must still enable reranking")
		}
		if rr := buildReranker(ctx, 50, 10); rr == nil {
			t.Error("an env-only key with no loaded config must still build a reranker")
		}
	})

	t.Run("missing credential yields nil", func(t *testing.T) {
		t.Cleanup(config.SetForTest(&config.Config{}))
		if rr := buildReranker(ctx, 50, 10); rr != nil {
			t.Errorf("buildReranker with no credential = %T; want nil (the RRF degrade)", rr)
		}
		if rerankCredentialPresent() {
			t.Error("rerankCredentialPresent with no credential must be false")
		}
	})

	t.Run("credential present yields a reranker", func(t *testing.T) {
		t.Setenv("VOYAGE_API_KEY", "k")
		t.Cleanup(config.SetForTest(&config.Config{}))
		rr := buildReranker(ctx, 50, 10)
		if rr == nil {
			t.Fatal("buildReranker with a resolved credential must return a reranker")
		}
		if !rerankCredentialPresent() {
			t.Error("rerankCredentialPresent with a resolved credential must be true")
		}
	})

	t.Run("per-axis credential does not read the other axis", func(t *testing.T) {
		// The trap this ticket dissolves: a Cohere reranker must NOT be
		// enabled by a Voyage key.
		t.Setenv("VOYAGE_API_KEY", "k")
		t.Cleanup(config.SetForTest(&config.Config{
			Reranker: &config.RerankSection{Provider: config.EmbedProviderCohere},
		}))
		if rerankCredentialPresent() {
			t.Error("a cohere reranker must not be enabled by a VOYAGE key")
		}
		if rr := buildReranker(ctx, 50, 10); rr != nil {
			t.Errorf("buildReranker for cohere with only a voyage key = %T; want nil", rr)
		}
		// And WITH its own key it resolves — the control proving the above
		// is the per-axis routing and not an unconditional refusal.
		t.Setenv("COHERE_API_KEY", "ck")
		if !rerankCredentialPresent() {
			t.Error("a cohere reranker with COHERE_API_KEY set must be enabled")
		}
	})

	t.Run("unregistered provider errors to nil", func(t *testing.T) {
		// Gemini publishes no rerank API, so no arm is registered for it.
		// A section naming it is a misconfiguration: logged, and nil.
		t.Setenv("GEMINI_API_KEY", "gk")
		t.Cleanup(config.SetForTest(&config.Config{
			Reranker: &config.RerankSection{Provider: config.EmbedProviderGemini},
		}))
		if rr := buildReranker(ctx, 50, 10); rr != nil {
			t.Errorf("buildReranker for a provider with no rerank arm = %T; want nil", rr)
		}
	})
}
