// SPDX-License-Identifier: Apache-2.0

// headless.go — the single home for the --headless umbrella flag's behavior:
// applyHeadless expands the flag into its implied gate set. Split out of
// config.go/daemon.go to keep both under the 500-line cap and to give the
// umbrella one obvious definition site. The boot-time config load is NOT
// --headless behavior — it runs on every serve — and lives in boot_config.go.

package bootstrap

// applyHeadless expands the --headless umbrella flag into the concrete gate
// bools the wiring paths read. It is the ONE place --headless is turned into an
// implied posture: when cfg.Headless is set, the embedded/supervisor-managed
// daemon skips every background content + coordination loop by setting all four
// gate bools — the three existing --no-* runtime gates (NoPropagationRuntime,
// SkipLLMPrecheck, NoLLMPipeline) plus the internal transcript gate
// (NoTranscriptUpload).
//
// What STAYS ENABLED under headless (deliberately NOT gated here): loading
// ~/.knowledge/config (loadBootConfig's never-write arm, so [credentials]
// resolve config-first with env fallback), the loopback /mcp HTTP server, and the
// client query embedder + rerank.
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
	cfg.NoPropagationRuntime = true
	cfg.SkipLLMPrecheck = true
	cfg.NoLLMPipeline = true
	cfg.NoTranscriptUpload = true
}
