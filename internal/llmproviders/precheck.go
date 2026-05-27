// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm/precheck"
)

// RunPrecheck fires a tiny live request through every configured LLM
// provider plus a Voyage embed check, asynchronously. Mirrors the
// pre-split server-side bootstrap.runLLMPrecheck (in checks-performed
// terms) but does NOT block the caller.
//
// Async-by-design (2026-05-20): the prior synchronous variant burned
// ~5.4s on startup for a typical multi-provider config (verified via
// the `client.startup` stage timings instrumentation), pushing the MCP
// host's first-connect handshake past its tolerance window and causing
// intermittent reconnect failures (ticket fb39323b...). The precheck's
// VALUE is "surface bad-config early via a clear slog line"; that
// value is preserved by logging from the spawned goroutine. Its
// previous "abort startup on failure" behavior was rare-fire safety
// theater — a misconfigured provider trips at first tool use anyway
// with a clearer per-call error.
//
// Behavior:
//   - skip=true     → silently no-op (matches --skip-llm-precheck).
//   - config.Active() == nil → WARN log + no-op. Same degrade-not-die
//     contract as the prior server path; bare config = no LLM features.
//   - voyageKey == "" → Voyage check passes (documented BM25-only mode).
//
// Returns nil immediately. The background goroutine emits one of:
//   - slog.Info  "llmproviders: precheck passed"  on success
//   - slog.Error "llmproviders: precheck failed"  on failure (includes
//     elapsed + the wrapped error so misconfig surfaces in the host's
//     stderr log).
func RunPrecheck(ctx context.Context, skip bool) error {
	if skip {
		slog.Info("llmproviders: precheck skipped via flag")
		return nil
	}
	active := config.Active()
	if active == nil {
		slog.Warn("llmproviders: precheck skipped — config.Active() returned nil")
		return nil
	}
	// Goroutine inherits the caller's ctx as parent so any cancel signal
	// propagates (gosec G118: don't drop request-scope ctx). The 90s
	// WithTimeout caps the precheck's own runtime independently.
	go func(parentCtx context.Context) {
		t0 := time.Now()
		slog.Debug("llmproviders: precheck started (async)")
		runCtx, cancel := context.WithTimeout(parentCtx, 90*time.Second)
		defer cancel()
		consumers := []config.Consumer{config.ConsumerSummarizer, config.ConsumerDream}
		if err := precheck.RunAll(runCtx, active, consumers, config.VoyageAPIKey()); err != nil {
			slog.Error("llmproviders: precheck failed",
				"error", fmt.Errorf("LLM precheck failed: %w", err),
				"elapsed", time.Since(t0))
			return
		}
		slog.Info("llmproviders: precheck passed", "elapsed", time.Since(t0))
	}(ctx)
	return nil
}
