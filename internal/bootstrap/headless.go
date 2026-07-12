// SPDX-License-Identifier: Apache-2.0

// headless.go — the single home for the --headless umbrella flag's behavior:
// applyHeadless expands the flag into its implied gate set, and (Phase 2)
// loadHeadlessConfig loads ~/.knowledge/config independently of the worker
// runtime so [credentials] resolve config-first under an embedded daemon.
// Split out of config.go/daemon.go to keep both under the 500-line cap and to
// give the umbrella one obvious definition site.

package bootstrap

import (
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// applyHeadless expands the --headless umbrella flag into the concrete gate
// bools the wiring paths read. It is the ONE place --headless is turned into an
// implied posture: when cfg.Headless is set, the embedded/supervisor-managed
// daemon skips every background content + coordination loop by setting all seven
// gate bools — the four existing --no-* runtime gates (NoWorkerRuntime,
// NoPropagationRuntime, SkipLLMPrecheck, NoLLMPipeline) plus the three internal
// hive/transcript gates (NoHiveMonitor, NoHiveReaper, NoTranscriptUpload).
//
// What STAYS ENABLED under headless (deliberately NOT gated here): loading
// ~/.knowledge/config (loadHeadlessConfig, so [credentials] resolve config-first
// with env fallback), the loopback /mcp HTTP server, the client query
// embedder + rerank, and instruction bootstrap (.claude agents/skills seeding).
// Auth is untouched — --headless does NOT couple to --no-auth; machine-auth
// (--auth-token / KNOWLEDGE_AUTH_TOKEN) and the fail-closed posture are exactly
// as they are for a normal serve.
//
// No-op when Headless is false, and idempotent (it only ever sets bools true),
// so calling it once in runServe after flag parse is sufficient.
func applyHeadless(cfg *Config) {
	if !cfg.Headless {
		return
	}
	cfg.NoWorkerRuntime = true
	cfg.NoPropagationRuntime = true
	cfg.SkipLLMPrecheck = true
	cfg.NoLLMPipeline = true
	cfg.NoHiveMonitor = true
	cfg.NoHiveReaper = true
	cfg.NoTranscriptUpload = true
}

// loadHeadlessConfig loads ~/.knowledge/config for an embedded/supervisor-managed
// daemon, decoupling the config load from the worker runtime. A normal serve
// loads config only as a side-effect of wireWorkerRuntime → buildRuntime →
// config.LoadOrAutoDetect; --headless skips the worker runtime, so without this
// the [credentials] section would never load and keys would resolve env-only by
// accident. Called from wireRuntimesBackground's worker-gate else-branch under
// f.Headless, BEFORE BuildEmbedder, so the query embedder + rerank resolve the
// config voyage_api_key rather than falling straight to VOYAGE_API_KEY.
//
// It uses config.Load — NOT config.LoadOrAutoDetect — on purpose:
//   - config.Load parses + setActive with NO summarizer Validate, so a
//     credentials-only template (bare [credentials], no [default] provider
//     section) loads fine; LoadOrAutoDetect additionally validates the
//     ConsumerSummarizer and would REJECT such a template.
//   - config.Load NEVER writes; LoadOrAutoDetect auto-detects and writes a
//     starter file when the path is absent. The supervisor owns config
//     placement, so a headless daemon must never write one.
//
// Guarded by config.Loaded() so an already-loaded config (or a test's
// SetForTest) is left untouched — no double load, no clobber. Degrade-not-die:
// on a defaultConfigPath error OR a config.Load error (missing / unparseable
// file) it slog.Warn's and returns, leaving config unloaded so credentials fall
// back to the *_API_KEY env vars via credOrEnv — the documented degrade posture
// (missing voyage → BM25-only, missing linear → backend disabled). It does NOT
// resurrect the LLM precheck: RunPrecheck stays gated on !NoWorkerRuntime.
func loadHeadlessConfig() {
	if config.Loaded() {
		return
	}
	cfgPath, err := defaultConfigPath()
	if err != nil {
		slog.Warn("headless: could not resolve config path; credentials will resolve from env only", "error", err)
		return
	}
	if _, err := config.Load(cfgPath); err != nil {
		slog.Warn("headless: could not load ~/.knowledge/config; credentials will resolve from env only",
			"path", cfgPath, "error", err)
		return
	}
	slog.Debug("headless: loaded config for credential resolution", "path", cfgPath)
}
