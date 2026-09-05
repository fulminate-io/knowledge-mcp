// SPDX-License-Identifier: Apache-2.0

package precheck

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// The embed and rerank startup checks. Each builds its axis's arm through
// that axis's registry and issues one minimal live call, so the ARM owns
// the endpoint, the auth header, the model and the error taxonomy and this
// package stops duplicating them.
//
// That delegation is what makes the check HONEST ABOUT A KEYLESS BASE_URL.
// This file used to hardcode a single vendor endpoint, with a comment
// conceding the hardcoding and pointing at a future need — "If a future
// need surfaces (proxy, air-gap), parameterize via a precheck option."
// That need is now: an [embedder] section can name a keyless base_url
// pointing at a local compatible server, and a check that pinged the
// vendor would be reporting on a service the client never calls.
//
// === WHAT BOOT COSTS, BOTH AXES, STATED PLAINLY ===
//
//   - EMBED ping: a single 1-token embed against the CONFIGURED model,
//     which for the default is the arm's own default model rather than a
//     cheaper ping-only one. That is deliberate — a ping against a model
//     you do not use proves less — and it is a small per-startup cost
//     increase over the previous fixed cheap model.
//   - RERANK ping: NET-NEW BILLED SPEND. No rerank call happened at
//     startup before this; it is one single-document rerank per startup
//     against the configured rerank model.
//
// Both pings inherit the existing opt-out: an axis with no resolved
// credential and no base_url returns nil WITHOUT CALLING ANYTHING, so an
// operator who wants zero startup spend already has the lever.

// voyagePingTimeout caps each axis's round-trip. A provider typically
// responds in 200-500ms; 10s absorbs a slow-network startup without
// holding up server boot.
const voyagePingTimeout = 10 * time.Second

// CheckEmbedProvider verifies the resolved [embedder] axis with a single
// live embed. Returns nil when the axis has no credential and no base_url
// — BM25-only mode is the documented opt-out, and the embedder pipeline
// already degrades to it. Returns nil on success, or an error naming the
// failure class.
//
// Failure classes the check catches:
//   - a key set to an invalid, revoked or out-of-credits value (401/403).
//   - the configured endpoint unreachable (firewall, captive portal, a
//     local compatible server that is not running).
//   - a provider outage (5xx).
func CheckEmbedProvider(ctx context.Context, sec config.EmbedSection) error {
	key := sec.ResolveEmbedKey()
	if unconfigured(sec.Provider, key, sec.BaseURL) {
		slog.Info("precheck: no embed credential — vector search disabled (BM25 only)", "provider", sec.Provider)
		return nil
	}

	pingCtx, cancel := context.WithTimeout(ctx, voyagePingTimeout)
	defer cancel()

	embedder, err := embed.NewEmbedder(pingCtx, &embed.Config{
		Provider:  sec.Provider,
		Model:     sec.Model,
		APIKey:    key,
		BaseURL:   sec.BaseURL,
		Dimension: sec.Dimension,
		Dtype:     sec.Dtype,
		InputRole: embed.InputRoleDocument,
	})
	if err != nil {
		return fmt.Errorf("embed precheck: %w", err)
	}

	slog.Info("precheck: pinging the embed provider", "provider", sec.Provider, "base_url", sec.BaseURL)
	start := time.Now()
	_, err = embedder.EmbedBinary(pingCtx, "ping")
	elapsed := time.Since(start)
	if err != nil {
		return axisPingError("embed", sec.Provider, elapsed, err)
	}
	slog.Info("precheck: embed provider ok", "provider", sec.Provider, "elapsed", elapsed.Round(time.Millisecond))
	return nil
}

// THE RERANK AXIS CHECK IS NOT IN THIS PACKAGE, AND DOES NOT BELONG HERE.
//
// MEASURED, every edge a non-test import: rerank imports engine
// (rerank/reranker.go:18) and engine imports graphclient
// (engine/dispatch.go). This package is a leaf the provider packages depend
// on — llmproviders imports it at llmproviders/precheck.go:12 — so naming
// rerank here, or naming engine directly (whose SearchResult the Reranker
// interface names), would pull the whole graph client in behind a startup
// precheck.
//
// The rerank axis check therefore lives in the rerank package itself, as
// rerank.CheckProvider, where it can construct the arm through the rerank
// registry exactly as CheckEmbedProvider does here. RunAll reaches it
// through the REQUIRED RerankCheck parameter, supplied by the composition
// root (package bootstrap, which already depends on engine and graphclient).
// It runs in the same parallel fan-out as this check — see runall.go.

// unconfigured reports the documented opt-out condition: an API provider
// with neither a resolved credential nor an explicit endpoint. A non-API
// provider (the deterministic fake) is always configured.
func unconfigured(provider config.EmbedProvider, key, baseURL string) bool {
	return provider.IsAPI() && key == "" && baseURL == ""
}

// axisPingError re-expresses a failed ping in the status classes an
// operator can act on. The arms return *llm.LLMError whose Reason carries
// http_<code>, so the classification reads that rather than re-deriving it
// from a raw HTTP response.
func axisPingError(axis string, provider config.EmbedProvider, elapsed time.Duration, err error) error {
	if llmErr, ok := errors.AsType[*llm.LLMError](err); ok {
		switch llmErr.Reason {
		case "http_401", "http_403":
			return fmt.Errorf("%s precheck: %s rejected the API key (%s): the key may be invalid, revoked, or out of credits — %w", axis, provider, llmErr.Reason, err)
		case "http_429":
			return fmt.Errorf("%s precheck: %s rate-limited (%s): retry shortly or check your usage — %w", axis, provider, llmErr.Reason, err)
		}
		return fmt.Errorf("%s precheck: %s returned %s (elapsed=%s): %w", axis, provider, llmErr.Reason, elapsed.Round(time.Millisecond), err)
	}
	return fmt.Errorf("%s precheck: %s ping failed (elapsed=%s): %w", axis, provider, elapsed.Round(time.Millisecond), err)
}
