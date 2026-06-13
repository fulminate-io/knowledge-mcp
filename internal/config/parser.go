// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// parseShape is the on-disk TOML structure. Pointer fields on the
// per-consumer sections preserve absent-vs-empty: nil means the section
// was omitted (full inheritance from [default]); non-nil with empty
// fields means the section is present but inherits per-field.
//
// SchemaVersion is at the top level (outside any [section]). Absent
// (zero) is treated as 1 — pre-versioning configs are forward-
// compatible. translateSection-equivalent at Parse time enforces
// the upper bound; configs that declare a version higher than this
// binary supports are rejected with an upgrade message.
type parseShape struct {
	SchemaVersion int               `toml:"schema_version"`
	Default       parseSection      `toml:"default"`
	Summarizer    *parseSection     `toml:"summarizer"`
	Dream         *parseSection     `toml:"dream"`
	Supervisor    *parseSection     `toml:"supervisor"`
	Topics        *parseSection     `toml:"topics"`
	Credentials   *parseCredentials `toml:"credentials"`
}

// parseCredentials mirrors the optional [credentials] TOML table. Pointer
// on parseShape preserves absent-vs-present; every field inside is an
// optional string that falls back to its env var when empty.
type parseCredentials struct {
	VoyageAPIKey    string `toml:"voyage_api_key"`
	LinearAPIKey    string `toml:"linear_api_key"`
	AnthropicAPIKey string `toml:"anthropic_api_key"`
	OpenAIAPIKey    string `toml:"openai_api_key"`
	GeminiAPIKey    string `toml:"gemini_api_key"`
}

// parseSection mirrors a single TOML table. Four keys:
//   - provider: stable LLM provider identifier
//   - model:    provider-specific model name
//   - cli_bin:  absolute path to the CLI binary (claude-cli/codex-cli only;
//     required for CLI providers, ignored for API providers)
//   - base_url: optional override of the API provider endpoint (API providers
//     only; empty falls back to the provider's default URL)
type parseSection struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	CLIBin   string `toml:"cli_bin"`
	BaseURL  string `toml:"base_url"`
}

// Load reads path and parses the TOML body. Returns the wrapped
// os.ReadFile / Parse error verbatim so callers can distinguish
// not-found, permission, and malformed-file conditions.
//
// On parse success, Load installs the parsed *Config into the package
// singleton via setActive so subsequent calls to Active() observe it.
// Validation is a separate step (parameterized by consumer list); the
// orchestrator LoadOrAutoDetect calls Load then Validate before allowing
// the server to proceed. If Validate fails the caller is expected to
// hard-exit, so a populated singleton does no harm.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config.Load: read %s: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config.Load: %s: %w", path, err)
	}
	setActive(cfg)
	return cfg, nil
}

// Parse unmarshals data as TOML and translates it into a Config.
//
// Provider strings are lowercased and checked against the known set;
// any non-empty unrecognized provider returns an error. Empty provider
// strings are permitted at parse time — they become the responsibility
// of Resolve / Validate, which decide whether [default] supplies the
// value or the consumer is genuinely missing one.
func Parse(data []byte) (*Config, error) {
	var raw parseShape
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config.Parse: %w", err)
	}

	// Schema version: absent = 1 (pre-versioning compatibility).
	// Higher than CurrentSchemaVersion = config was written by a
	// newer build of knowledge — fail loudly with an upgrade hint.
	// Lower than current is accepted; future migrations would land
	// here as version-conditional translation steps.
	version := raw.SchemaVersion
	if version == 0 {
		version = 1
	}
	if version > CurrentSchemaVersion {
		return nil, fmt.Errorf("config.Parse: schema_version %d is newer than this binary supports (max %d) — upgrade knowledge or downgrade the config", version, CurrentSchemaVersion)
	}

	cfg := &Config{SchemaVersion: version}

	def, err := translateSection("default", raw.Default)
	if err != nil {
		return nil, err
	}
	cfg.Default = def

	if raw.Summarizer != nil {
		s, err := translateSection(string(ConsumerSummarizer), *raw.Summarizer)
		if err != nil {
			return nil, err
		}
		cfg.Summarizer = &s
	}
	if raw.Dream != nil {
		s, err := translateSection(string(ConsumerDream), *raw.Dream)
		if err != nil {
			return nil, err
		}
		cfg.Dream = &s
	}
	if raw.Supervisor != nil {
		s, err := translateSection(string(ConsumerHiveSupervisor), *raw.Supervisor)
		if err != nil {
			return nil, err
		}
		cfg.Supervisor = &s
	}
	if raw.Topics != nil {
		s, err := translateSection(string(ConsumerTopics), *raw.Topics)
		if err != nil {
			return nil, err
		}
		cfg.Topics = &s
	}
	if raw.Credentials != nil {
		cfg.Credentials = &Credentials{
			VoyageAPIKey:    raw.Credentials.VoyageAPIKey,
			LinearAPIKey:    raw.Credentials.LinearAPIKey,
			AnthropicAPIKey: raw.Credentials.AnthropicAPIKey,
			OpenAIAPIKey:    raw.Credentials.OpenAIAPIKey,
			GeminiAPIKey:    raw.Credentials.GeminiAPIKey,
		}
	}
	return cfg, nil
}

// translateSection lowercases the provider string and checks it against
// the known Provider constants. An empty provider is allowed and
// returned as Provider("") so per-field inheritance can fill it later.
// CLIBin and BaseURL are copied verbatim — CLIBin's value-level validation
// (must be a real executable file when the provider is CLI) happens in
// Validate; BaseURL has no required-field gate (optional for all providers).
func translateSection(name string, raw parseSection) (Section, error) {
	out := Section{Model: raw.Model, CLIBin: raw.CLIBin, BaseURL: raw.BaseURL}
	if raw.Provider == "" {
		return out, nil
	}
	p := Provider(strings.ToLower(raw.Provider))
	if !p.IsValid() {
		return Section{}, fmt.Errorf("config: section [%s]: unknown provider %q (want one of: anthropic, openai, gemini, claude-cli, codex-cli)", name, raw.Provider)
	}
	out.Provider = p
	return out, nil
}
