// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// WHY THIS LIVES HERE AND NOT IN THE PRECHECK PACKAGE, MEASURED.
//
// Its sibling CheckEmbedProvider sits in cmd/knowledge/internal/llm/precheck
// beside RunAll's fan-out, and this one belongs there too. It does not,
// because of what importing this package DRAGS IN. Every edge below is a
// non-test import:
//
//	llm/precheck -> rerank            (this check)
//	rerank       -> engine            (reranker.go:18, for SearchResult)
//	engine       -> graphclient       (dispatch.go)
//
// llm/precheck is a leaf the provider packages depend on; naming rerank from
// it would pull engine and the whole graph client in behind it, and importing
// engine directly is the same dependency by another name because the Reranker
// interface names engine.SearchResult. So the check is authored in the package
// that owns the arm.
//
// IT IS WIRED INTO RunAll's FAN-OUT THROUGH A REQUIRED PARAMETER.
// precheck.RunAll takes a RerankCheck it cannot construct itself, and the
// composition root — package bootstrap, which already depends on engine and
// graphclient — passes CheckProvider. RunAll REFUSES a nil check rather than
// skipping the axis, so the indirection cannot decay into a check that
// silently stops running.
//
// COST: net-new billed spend. No rerank call happened at startup before
// this, and this is one single-document rerank per startup against the
// configured rerank model. It inherits the same opt-out both other axes
// have — an axis with no resolved credential and no base_url returns nil
// WITHOUT CALLING ANYTHING.

// checkPingTimeout caps the ping's round-trip, matching the embed axis
// check's bound. A provider typically responds in 200-500ms; 10s absorbs a
// slow-network startup without holding up boot.
const checkPingTimeout = 10 * time.Second

// CheckProvider verifies the resolved [reranker] axis with a single
// single-document rerank. Returns nil when the axis has no credential and
// no base_url — RRF ordering is the documented opt-out, and the search
// paths already degrade to it. Returns nil on success, or an error naming
// the failure class.
//
// Failure classes the check catches:
//   - a key set to an invalid, revoked or out-of-credits value (401/403).
//   - the configured endpoint unreachable (firewall, captive portal, a
//     local compatible server that is not running).
//   - a provider outage (5xx).
func CheckProvider(ctx context.Context, sec config.RerankSection) error {
	key := sec.ResolveRerankKey()
	if sec.Provider.IsAPI() && key == "" && sec.BaseURL == "" {
		slog.Info("precheck: no rerank credential — cross-encoder rerank disabled (RRF ordering only)", "provider", sec.Provider)
		return nil
	}

	pingCtx, cancel := context.WithTimeout(ctx, checkPingTimeout)
	defer cancel()

	reranker, err := NewReranker(pingCtx, &Config{
		Provider:  sec.Provider,
		Model:     sec.Model,
		APIKey:    key,
		BaseURL:   sec.BaseURL,
		InputDocs: 1,
		TopK:      1,
	})
	if err != nil {
		return fmt.Errorf("rerank precheck: %w", err)
	}

	slog.Info("precheck: pinging the rerank provider", "provider", sec.Provider, "base_url", sec.BaseURL)
	start := time.Now()
	_, err = reranker.Rerank(pingCtx, "ping", []engine.SearchResult{
		{Node: &knowledgev1.Node{Id: "precheck-ping", Type: "document", Summary: "ping"}},
	})
	elapsed := time.Since(start)
	if err != nil {
		return rerankPingError(sec.Provider, elapsed, err)
	}
	slog.Info("precheck: rerank provider ok", "provider", sec.Provider, "elapsed", elapsed.Round(time.Millisecond))
	return nil
}

// rerankPingError re-expresses a failed ping in the status classes an
// operator can act on, preserving the three the previous hardcoded check
// reported: 401/403 names an invalid, revoked or out-of-credits key; 429
// names rate limiting; anything else names the status.
//
// The Voyage rerank arm returns plain wrapped errors rather than
// *llm.LLMError, so the classification reads an *llm.LLMError when one is
// present (any arm that returns the shared taxonomy) and falls back to
// naming the raw failure otherwise.
func rerankPingError(provider config.EmbedProvider, elapsed time.Duration, err error) error {
	if llmErr, ok := errors.AsType[*llm.LLMError](err); ok {
		switch llmErr.Reason {
		case "http_401", "http_403":
			return fmt.Errorf("rerank precheck: %s rejected the API key (%s): the key may be invalid, revoked, or out of credits — %w", provider, llmErr.Reason, err)
		case "http_429":
			return fmt.Errorf("rerank precheck: %s rate-limited (%s): retry shortly or check your usage — %w", provider, llmErr.Reason, err)
		}
		return fmt.Errorf("rerank precheck: %s returned %s (elapsed=%s): %w", provider, llmErr.Reason, elapsed.Round(time.Millisecond), err)
	}
	return fmt.Errorf("rerank precheck: %s ping failed (elapsed=%s): %w", provider, elapsed.Round(time.Millisecond), err)
}
