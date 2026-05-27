// SPDX-License-Identifier: Apache-2.0

// Package precheck makes a tiny live request through every configured
// LLM provider before knowledge-server accepts traffic. The check
// catches three failure classes the static config validator can't see:
//
//   - CLI-provider auth: claude / codex CLIs that exist on disk but
//     aren't logged in (claude session expired, codex API key wrong).
//   - API-provider auth: ANTHROPIC_API_KEY / OPENAI_API_KEY /
//     GEMINI_API_KEY that's set but invalid or revoked.
//   - Network reachability: provider endpoints unreachable from the
//     server's network namespace (firewall, captive portal, etc.).
//
// Failing fast at startup beats failing on the user's first tool call
// — the operator gets one clear message naming the consumer + provider
// + reason, instead of a confusing "summarizer failed" report mid-
// session.
//
// Cost: a single ~10-token round-trip per unique (provider, model,
// cli_bin) tuple. Distinct consumers that share a provider/model
// resolve to the same key and ping once. With a haiku-class model
// the per-startup cost is ~$0.0001 — effectively free.
//
// CLI providers (claude-cli, codex-cli) inherit the user's CLI auth
// (e.g. ~/.claude/credentials, OpenAI's codex key store) — the ping
// confirms that auth is live. The CLI binaries themselves were
// already validated by config.Validate before this runs.

package precheck

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// pingTimeout caps the per-provider check budget. 60s covers cold-
// start jitter for CLI providers. Claude-cli on a warm cache replies
// in ~10s; first invocation after a system reboot takes longer
// (Node subprocess startup + ~/.claude credential load + network
// handshake). API providers are considerably faster (~1-3s) but we
// use the same budget for simplicity. Total startup latency is
// bounded by the caller's outer ctx in cmd/knowledge-server/server.go
// so a wedged provider can't stall server boot indefinitely.
const pingTimeout = 60 * time.Second

// pingMaxTokens caps the model's output. We don't care what comes
// back — only that it comes back without an error. 8 tokens is plenty
// for any sane response to "ping" and keeps the minimum-cost contract
// honest.
const pingMaxTokens = 8

// pingPrompt is the single-token user message every provider sees.
// Short and provider-agnostic — every model ever shipped knows how
// to respond to "ping" without reasoning steps.
const pingPrompt = "ping"

// checkOne pings a single tuple and returns the wrapped error or nil.
// Used by both CheckLLMs (sequential) and RunAll (parallel via
// errgroup goroutines). Logs the start + result so operators see
// per-tuple progress regardless of which orchestrator dispatched it.
func checkOne(ctx context.Context, t pingTuple) error {
	slog.Info("precheck: pinging LLM provider",
		"consumer", t.consumer, "provider", t.section.Provider, "model", t.section.Model)
	if err := ping(ctx, t.section); err != nil {
		return fmt.Errorf("precheck %s (%s/%s): %w", t.consumer, t.section.Provider, t.section.Model, err)
	}
	slog.Info("precheck: ok",
		"consumer", t.consumer, "provider", t.section.Provider, "model", t.section.Model)
	return nil
}

// pingTuple carries the consumer label alongside the resolved section
// so error messages can name which configured consumer's provider
// failed (more useful than just "anthropic failed" when both
// summarizer and dream might be on anthropic with different models).
type pingTuple struct {
	consumer config.Consumer
	section  config.Section
}

// uniqueTuples resolves every consumer's section and returns one
// representative pingTuple per (provider, model, cli_bin) key. The
// first consumer to resolve a given tuple "wins" the label for log
// output — subsequent matching consumers are skipped. Errors during
// Resolve are returned as a joined error.
func uniqueTuples(cfg *config.Config, consumers []config.Consumer) ([]pingTuple, error) {
	seen := make(map[string]bool)
	out := make([]pingTuple, 0, len(consumers))
	var errs []error
	for _, c := range consumers {
		sec, err := cfg.Resolve(c)
		if err != nil {
			errs = append(errs, fmt.Errorf("precheck: resolve %q: %w", c, err))
			continue
		}
		key := string(sec.Provider) + "|" + sec.Model + "|" + sec.CLIBin
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, pingTuple{consumer: c, section: sec})
	}
	return out, errors.Join(errs...)
}

// ping builds an llm.Client from sec and issues a single short request
// with a bounded timeout. Returns the underlying error verbatim so
// callers can wrap it with consumer/provider context for the final
// startup log line. The per-call elapsed time is logged at INFO so
// operators can see "claude-cli ping took 11s" when triaging slow
// boots vs "API ping took 600ms" — useful when diagnosing cold-cache
// vs warm-cache vs network issues.
func ping(ctx context.Context, sec config.Section) error {
	client, err := llm.NewClient(ctx, &llm.Config{
		Provider: sec.Provider,
		Model:    llm.Model(sec.Model),
		APIKey:   config.APIKeyForProvider(sec.Provider),
		CLIBin:   sec.CLIBin,
	})
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	start := time.Now()
	_, err = client.Generate(pingCtx,
		[]*schema.Message{{Role: schema.User, Content: pingPrompt}},
		llm.WithMaxTokens(pingMaxTokens),
	)
	elapsed := time.Since(start)
	if err != nil {
		slog.Warn("precheck: ping failed",
			"provider", sec.Provider, "model", sec.Model, "elapsed", elapsed.Round(time.Millisecond))
		return fmt.Errorf("generate (elapsed=%s): %w", elapsed.Round(time.Millisecond), err)
	}
	slog.Info("precheck: ping ok",
		"provider", sec.Provider, "model", sec.Model, "elapsed", elapsed.Round(time.Millisecond))
	return nil
}
