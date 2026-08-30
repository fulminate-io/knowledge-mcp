// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/rerank"
)

// buildReranker resolves the [reranker] section and constructs the arm
// through the rerank registry, for the two production search paths that
// need one: the search intercept and the thoughts-recall intercept.
//
// It lives in its own file, and both call sites go through it, so the two
// cannot drift — the same reason the LLM wiring keeps one shared
// single-entry construction rather than two copies of the client-build
// sequence. (It is NOT in search.go because that file is close to the
// repo's 500-line commit cap, and a helper plus its doc comment would
// plausibly push it over.)
//
// THE NIL-RERANKER DEGRADE IS PRE-EXISTING AND ITS SCOPE DOES NOT WIDEN.
// A missing credential still yields a nil reranker, which the callers
// degrade to RRF ordering on — the documented contract, unchanged. What is
// new is that a MALFORMED [reranker] section or an unregistered provider
// now produces a logged error and a nil reranker, rather than being
// unrepresentable. There is no retry, no fallback to a different provider,
// and no silent substitution of the embed section's provider.
//
// inputDocs and topK stay CALLER-supplied: they are search-tuning
// parameters (the operating pool and the response size the caller is
// searching with), not provider configuration, so they are arguments here
// rather than [reranker] keys.
func buildReranker(ctx context.Context, inputDocs, topK int) rerank.Reranker {
	sec, err := resolveRerankSection()
	if err != nil {
		slog.Error("rerank: [reranker] section is unusable; reranking disabled, results keep RRF ordering", "error", err)
		return nil
	}
	key := sec.ResolveRerankKey()
	if sec.Provider.IsAPI() && key == "" && sec.BaseURL == "" {
		// The documented BM25/RRF degrade: no credential, no reranker.
		return nil
	}
	rr, err := rerank.NewReranker(ctx, &rerank.Config{
		Provider:  sec.Provider,
		Model:     sec.Model,
		APIKey:    key,
		BaseURL:   sec.BaseURL,
		InputDocs: inputDocs,
		TopK:      topK,
	})
	if err != nil {
		slog.Error("rerank: reranker construction failed; reranking disabled, results keep RRF ordering",
			"provider", sec.Provider, "error", err)
		return nil
	}
	return rr
}

// resolveRerankSection resolves the [reranker] section, tolerating an
// UNLOADED config.
//
// The nil-config path is behavior preservation, not a convenience: before
// this axis was configurable the two call sites read the credential
// through an accessor that falls back to the env var whether or not a
// config file was ever loaded, so a process with a key in the environment
// and no config still reranked. config.Active() panics when unloaded, so
// the resolver is called on a nil *Config — which resolves to the same
// default section an absent [reranker] table does.
func resolveRerankSection() (config.RerankSection, error) {
	var cfg *config.Config
	if config.Loaded() {
		cfg = config.Active()
	}
	return cfg.ResolveReranker()
}

// rerankCredentialPresent reports whether the resolved rerank axis has
// something to authenticate with — the predicate the search intercept's
// rerank decision reads, replacing a direct read of the Voyage key. A
// malformed section reports false: an axis that cannot be built cannot
// rerank.
func rerankCredentialPresent() bool {
	sec, err := resolveRerankSection()
	if err != nil {
		return false
	}
	if !sec.Provider.IsAPI() {
		return true
	}
	return sec.ResolveRerankKey() != "" || sec.BaseURL != ""
}
