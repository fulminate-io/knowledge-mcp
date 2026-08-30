// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"errors"
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
// intermittent reconnect failures. The precheck's
// VALUE is "surface bad-config early via a clear slog line"; that
// value is preserved by logging from the spawned goroutine. Its
// previous "abort startup on failure" behavior was rare-fire safety
// theater — a misconfigured provider trips at first tool use anyway
// with a clearer per-call error.
//
// checkRerank is REQUIRED and is FORWARDED, not constructed here. This
// package does not import rerank — rerank imports engine and engine imports
// graphclient, so naming rerank.CheckProvider here would pull the whole graph
// client into a provider package. The single caller of RunPrecheck is package
// bootstrap, which already depends on both, so the real implementation is
// supplied from there and passed straight through to precheck.RunAll.
//
// Behavior:
//   - skip=true     → silently no-op (matches --skip-llm-precheck).
//   - config not loaded (config.Loaded() false) → WARN log + no-op. Same
//     degrade-not-die contract as the prior server path; bare config = no LLM
//     features. The guard is Loaded(), not a nil check on the returned
//     pointer: config.Active() PANICS when nothing has been loaded and never
//     returns nil, so a nil check here would be unreachable and the process
//     would die on an unreadable or unparseable ~/.knowledge/config.
//   - checkRerank == nil → ERROR, returned synchronously. It is a
//     programming error, never a skip: the whole point of threading the
//     check is that the rerank axis runs, and a nil that quietly disabled
//     it would defeat the indirection.
//   - an axis with no credential and no base_url passes without calling
//     anything (documented BM25-only / RRF-only mode).
//
// Returns nil immediately on the happy path. The background goroutine
// emits one of:
//   - slog.Info  "llmproviders: precheck passed"  on success
//   - slog.Error "llmproviders: precheck failed"  on failure (includes
//     elapsed + the wrapped error so misconfig surfaces in the host's
//     stderr log).
func RunPrecheck(ctx context.Context, skip bool, checkRerank precheck.RerankCheck) error {
	if skip {
		slog.Info("llmproviders: precheck skipped via flag")
		return nil
	}
	if checkRerank == nil {
		return errors.New("llmproviders.RunPrecheck: nil rerank check — the caller must pass rerank.CheckProvider; a nil check is never treated as skip")
	}
	if !config.Loaded() {
		slog.Warn("llmproviders: precheck skipped — no config loaded")
		return nil
	}
	active := config.Active()
	// Goroutine inherits the caller's ctx as parent so any cancel signal
	// propagates (gosec G118: don't drop request-scope ctx). The 90s
	// WithTimeout caps the precheck's own runtime independently.
	go func(parentCtx context.Context) {
		t0 := time.Now()
		slog.Debug("llmproviders: precheck started (async)")
		runCtx, cancel := context.WithTimeout(parentCtx, 90*time.Second)
		defer cancel()
		consumers := []config.Consumer{config.ConsumerSummarizer, config.ConsumerSupervisor}
		if err := precheck.RunAll(runCtx, active, consumers, checkRerank); err != nil {
			slog.Error("llmproviders: precheck failed",
				"error", fmt.Errorf("LLM precheck failed: %w", err),
				"elapsed", time.Since(t0))
			return
		}
		slog.Info("llmproviders: precheck passed", "elapsed", time.Since(t0))
	}(ctx)
	return nil
}
