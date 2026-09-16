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
// daemon skips every background content + coordination loop EXCEPT the minimum
// the deterministic BM25 text-index arm needs to drain and to be woken, by
// setting all four gate bools — the three existing --no-* runtime gates
// (NoPropagationRuntime, SkipLLMPrecheck, NoLLMPipeline) plus the internal
// transcript gate (NoTranscriptUpload).
//
// THE EXCEPTION IS NAMED RATHER THAN IMPLIED, because this block used to say
// "skips every background content + coordination loop" and a reader was entitled
// to believe it. NoLLMPipeline no longer means "no pipeline at all": it means the
// two LLM axes are off and the client wires the BM25 arm alone
// (wireBM25OnlyRuntime, pipeline_bm25_only.go). Under --headless exactly two
// things run that did not before — the per-graph BM25 collector loop and the
// central gen-poll loop that feeds it the server's corpus stamp. The heal
// factory, the balance verdict, the segment nudger, the boot segment passes and
// the segment reconcile loop all STAY SKIPPED, and that wiring function is where
// each one's reason is written down.
//
// WHY THE EXCEPTION EXISTS. A headless, keyless, logged-out daemon — the Desktop's
// own runtime posture — returned ZERO results for text search on nodes it held,
// for the life of the process, because the arm's closures were built inside the
// pipeline this flag skipped. Text search costs no LLM call and no credential;
// gating it on an LLM axis was a defect, not a posture.
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
