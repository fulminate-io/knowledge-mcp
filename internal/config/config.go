// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"sync"
)

// Provider names an LLM backend by stable string identifier.
//
// The string values are intentionally the same as the constants in
// domains/llm/types.go (anthropic, openai, gemini, claude-cli, codex-cli)
// so a config Provider is interchangeable with llm.Provider at the
// boundary between domains/config and downstream wiring code. Keeping
// the type local — rather than importing domains/llm — preserves
// domains/config as a true leaf with no upstream deps.
type Provider string

// Provider constants matching domains/llm/types.go exactly.
const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderGemini    Provider = "gemini"
	ProviderClaudeCLI Provider = "claude-cli"
	ProviderCodexCLI  Provider = "codex-cli"
)

// IsAPI reports whether p is one of the direct API providers
// (anthropic / openai / gemini). API providers require an env-var key.
func (p Provider) IsAPI() bool {
	switch p {
	case ProviderAnthropic, ProviderOpenAI, ProviderGemini:
		return true
	}
	return false
}

// IsCLI reports whether p is one of the CLI-shellout providers
// (claude-cli / codex-cli). CLI providers require the binary on PATH.
func (p Provider) IsCLI() bool {
	switch p {
	case ProviderClaudeCLI, ProviderCodexCLI:
		return true
	}
	return false
}

// IsValid reports whether p is one of the recognized providers.
func (p Provider) IsValid() bool {
	return p.IsAPI() || p.IsCLI()
}

// String returns p as a plain string.
func (p Provider) String() string { return string(p) }

// Consumer names a substrate component that needs an LLM.
//
// Each consumer reads its provider+model independently from the config,
// with [default] as the per-field fallback. The Validate method takes a
// list of consumers so callers (and follow-up tickets such as dream
// wiring) decide which consumers are required.
type Consumer string

// Consumer constants. Strings match the corresponding TOML section names.
//
// The transformer runtime is intentionally absent — the recipe transformer
// is a rule-based DSL interpreter and does not use LLM clients. Only
// substrate components that actually run an LLM appear here.
const (
	ConsumerSummarizer Consumer = "summarizer"
	ConsumerDream      Consumer = "dream"
	// ConsumerHiveSupervisor is the Tier-2 strong-model LLM that judges a
	// hive worker's transcript on monitor ambiguity. Resolved like dream:
	// an absent [supervisor] section inherits fully from [default].
	ConsumerHiveSupervisor Consumer = "supervisor"
	// ConsumerTopics is the topic-summary LLM the similarity lever uses to
	// produce one-line topic summaries over thought clusters. Separate from
	// ConsumerSummarizer so the low-volume, quality-sensitive topic pass can
	// run an opus-class model while the high-volume pipeline summarizer stays
	// on a cheap one. An absent [topics] section inherits fully from [default].
	ConsumerTopics Consumer = "topics"
)

// String returns c as a plain string.
func (c Consumer) String() string { return string(c) }

// Section is the (provider, model, cli_bin) triple that drives one LLM
// client. It is the unit of resolution: per-consumer sections override
// [default] on a per-field basis (every field is inherited
// independently).
//
// CLIBin is REQUIRED for CLI providers (claude-cli, codex-cli) and
// IGNORED for API providers. The validator rejects a CLI-provider
// section with an empty CLIBin — there is no PATH fallback. This keeps
// the config self-contained: launchd/systemd-managed servers (which
// run with a sanitized PATH) work the same as interactive shells.
// First-run auto-detect populates CLIBin via exec.LookPath so the
// starter file is immediately usable; the user edits the absolute path
// if they ever move the CLI binary.
//
// BaseURL is OPTIONAL for all providers. When set on an API provider it
// overrides that provider's default endpoint (e.g. a keyless local
// OpenAI-/anthropic-/gemini-compatible server); empty means "use the
// provider's built-in default URL". No required-field gate.
type Section struct {
	Provider Provider
	Model    string
	CLIBin   string
	BaseURL  string
}

// CurrentSchemaVersion is the format version this binary writes when
// rendering a starter config. Bump when a breaking change to the
// TOML shape lands (e.g., a new required field, a renamed key, a
// removed section). The validator rejects configs whose declared
// schema_version is HIGHER than this constant — that's the "config
// from a newer build, please upgrade" path.
const CurrentSchemaVersion = 1

// Config is the parsed shape of ~/.knowledge/config.
//
// Default applies to any consumer whose per-section field is unset.
// Per-consumer pointer fields are nil when the corresponding TOML section
// is absent; Resolve treats that the same as a section with both fields
// empty (full inheritance from Default).
//
// SchemaVersion is the on-disk format identifier. Absent (zero) is
// treated as 1 — pre-versioning configs predate the field and are
// fully compatible with v1 semantics. Loading rejects anything
// HIGHER than CurrentSchemaVersion with an upgrade hint; lower
// values are accepted and may emit a migration warning in the
// future.
type Config struct {
	SchemaVersion int
	Default       Section
	Summarizer    *Section
	Dream         *Section
	Supervisor    *Section
	Topics        *Section
	// Credentials holds the optional [credentials] section: backend/LLM
	// API keys. nil when the section is absent (the common case — most
	// configs rely on env vars). Each key falls back to its matching env
	// var when unset here; see keys.go for the accessor functions that
	// apply that precedence.
	Credentials *Credentials
}

// Credentials mirrors the [credentials] TOML table. Every field is
// optional; an empty (or absent) field means "fall back to the env var".
// Config wins over env when both are set — the file is the deliberate,
// persistent choice. Storing keys here makes the daemon launch-method-
// agnostic (brew services / systemd / k8s all read this file); operators
// who do should chmod the file 600.
//
// This is ONLY the five backend/LLM keys — the Fulminate auth token is
// NOT here; it lives in the OS keychain via domains/fulminate/auth.
type Credentials struct {
	VoyageAPIKey    string
	LinearAPIKey    string
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GeminiAPIKey    string
}

// Resolve returns the effective Section for consumer.
//
// Per-field fallback: any unset Provider, Model, CLIBin, or BaseURL on the
// per-consumer section is filled from Default. Provider and Model are
// required — if both per-consumer and Default leave one empty, Resolve
// returns an error naming the missing field. CLIBin and BaseURL are
// optional and never trigger a missing-field error.
func (c *Config) Resolve(consumer Consumer) (Section, error) {
	if c == nil {
		return Section{}, fmt.Errorf("config.Resolve: nil Config")
	}
	var per *Section
	switch consumer {
	case ConsumerSummarizer:
		per = c.Summarizer
	case ConsumerDream:
		per = c.Dream
	case ConsumerHiveSupervisor:
		per = c.Supervisor
	case ConsumerTopics:
		per = c.Topics
	default:
		return Section{}, fmt.Errorf("config.Resolve: unknown consumer %q", consumer)
	}

	out := Section{Provider: c.Default.Provider, Model: c.Default.Model, CLIBin: c.Default.CLIBin, BaseURL: c.Default.BaseURL}
	if per != nil {
		// Per-field override: a non-empty per-consumer field wins over the
		// [default] value; empty inherits. Flat (one statement per field)
		// to keep cyclomatic nesting low.
		out.Provider = coalesceProvider(per.Provider, out.Provider)
		out.Model = coalesce(per.Model, out.Model)
		out.CLIBin = coalesce(per.CLIBin, out.CLIBin)
		out.BaseURL = coalesce(per.BaseURL, out.BaseURL)
	}
	if out.Provider == "" {
		return Section{}, fmt.Errorf("config: consumer %q has no provider (set [%s].provider or [default].provider)", consumer, consumer)
	}
	if out.Model == "" {
		return Section{}, fmt.Errorf("config: consumer %q has no model (set [%s].model or [default].model)", consumer, consumer)
	}
	return out, nil
}

// coalesce returns override when it is non-empty, else fallback. Used by
// Resolve for per-field [default] inheritance of optional string fields.
func coalesce(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

// coalesceProvider is coalesce for the Provider type.
func coalesceProvider(override, fallback Provider) Provider {
	if override != "" {
		return override
	}
	return fallback
}

// active is the process-wide singleton, populated by Load /
// LoadOrAutoDetect after a successful parse + validate.
var (
	active   *Config
	activeMu sync.RWMutex
)

// Active returns the loaded config singleton.
//
// Panics with a clear message if the config has not yet been loaded —
// same fail-fast convention as store.Store(). Callsites that need to
// branch on "is config available?" should not exist; the bootstrap
// sequence is responsible for loading config before any consumer runs.
func Active() *Config {
	activeMu.RLock()
	defer activeMu.RUnlock()
	if active == nil {
		panic("config.LoadOrAutoDetect() not called")
	}
	return active
}

// Loaded reports whether a config has been installed via Load /
// LoadOrAutoDetect / SetForTest. False means a subsequent Active()
// call would panic. Intended for store-bootstrap-time degrade-not-die
// branches that want to log "no config; LLM features disabled" and
// continue, rather than panicking the process. Production bootstrap
// MUST always call LoadOrAutoDetect first; Loaded() is the test-
// friendly escape that keeps non-LLM tests free of config plumbing.
func Loaded() bool {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return active != nil
}

// setActive installs c as the singleton under the write lock. Internal:
// callers go through Load / LoadOrAutoDetect, never directly.
func setActive(c *Config) {
	activeMu.Lock()
	active = c
	activeMu.Unlock()
}

// SetForTest installs c as the singleton and returns a closure that
// restores the prior value when invoked. Use as
//
//	t.Cleanup(config.SetForTest(testCfg))
//
// SetForTest is the test-only seam for swapping in a deterministic
// config without going through Load. Concurrent-safe: both the swap and
// the restore acquire the write lock.
func SetForTest(c *Config) func() {
	activeMu.Lock()
	prior := active
	active = c
	activeMu.Unlock()
	return func() {
		activeMu.Lock()
		active = prior
		activeMu.Unlock()
	}
}
