// SPDX-License-Identifier: Apache-2.0

// Package config loads, resolves, and validates the TOML configuration that
// drives knowledge's per-consumer LLM wiring.
//
// The config file is a small TOML document — by default at
// ~/.knowledge/config, overridable with --config-file — that names which
// LLM provider and model each substrate consumer should use:
//
//   - summarizer (domains/store) — chunk + node summarization
//   - dream (background analysis) — out of scope for the initial wiring
//   - transformer (domains/transformer) — graph→graph DSL bodies
//
// Inheritance: a [default] section provides Provider/Model that any unset
// per-consumer field falls back to. Resolve picks the matching per-consumer
// section, then falls back per-field to [default]; both empty is an error.
//
// Secrets: API keys MAY be set in an optional [credentials] section
// (voyage_api_key, linear_api_key, anthropic_api_key, openai_api_key,
// gemini_api_key) — chmod the file 600 if you do. Each key falls back to
// its matching env var (VOYAGE_API_KEY, LINEAR_API_KEY, ANTHROPIC_API_KEY,
// OPENAI_API_KEY, GEMINI_API_KEY) when unset in [credentials]; the
// config value wins when both are present. Accessors live in keys.go
// (VoyageAPIKey, LinearAPIKey, APIKeyForProvider) and apply that
// precedence. The Fulminate auth token is NOT in [credentials] — it
// lives in the OS keychain via domains/fulminate/auth. CLI providers
// (claude-cli, codex-cli) need no key; they auth via the local CLI login.
// The validator enforces presence of the required env var / config key /
// binary at startup.
//
// Example:
//
//	[default]
//	provider = "anthropic"
//	model    = "claude-haiku-5"
//
//	[summarizer]
//	model    = "claude-haiku-5"   # provider inherited from [default]
//
//	[transformer]
//	provider = "openai"
//	model    = "gpt-5-mini"
//
// The provider strings match the constants in cmd/knowledge/internal/llm/types.go exactly:
// "anthropic", "openai", "gemini", "claude-cli", "codex-cli".
//
// Singleton: after a successful Load + Validate, the parsed config is
// installed via setActive and read with Active(). Active panics when the
// config has not yet been loaded — same fail-fast convention as
// store.Store(). Tests can substitute a config with SetForTest, which
// returns a t.Cleanup-friendly restore closure.
package config
