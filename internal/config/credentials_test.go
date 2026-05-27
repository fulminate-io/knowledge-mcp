// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

// TestParse_CredentialsSection parses a [credentials] table and checks
// every field round-trips onto Config.Credentials.
func TestParse_CredentialsSection(t *testing.T) {
	body := `
[default]
provider = "claude-cli"
model = "claude-opus-4-7"
cli_bin = "/usr/local/bin/claude"

[credentials]
voyage_api_key    = "voy-xxx"
linear_api_key    = "lin_api_xxx"
anthropic_api_key = "sk-ant-xxx"
openai_api_key    = "sk-oai-xxx"
gemini_api_key    = "gm-xxx"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Credentials == nil {
		t.Fatal("Credentials is nil; expected populated")
	}
	if cfg.Credentials.VoyageAPIKey != "voy-xxx" {
		t.Errorf("VoyageAPIKey = %q", cfg.Credentials.VoyageAPIKey)
	}
	if cfg.Credentials.LinearAPIKey != "lin_api_xxx" {
		t.Errorf("LinearAPIKey = %q", cfg.Credentials.LinearAPIKey)
	}
	if cfg.Credentials.AnthropicAPIKey != "sk-ant-xxx" {
		t.Errorf("AnthropicAPIKey = %q", cfg.Credentials.AnthropicAPIKey)
	}
	if cfg.Credentials.OpenAIAPIKey != "sk-oai-xxx" {
		t.Errorf("OpenAIAPIKey = %q", cfg.Credentials.OpenAIAPIKey)
	}
	if cfg.Credentials.GeminiAPIKey != "gm-xxx" {
		t.Errorf("GeminiAPIKey = %q", cfg.Credentials.GeminiAPIKey)
	}
}

// TestParse_CredentialsAbsent confirms the section is optional: Config
// from a body with no [credentials] table has a nil Credentials pointer.
func TestParse_CredentialsAbsent(t *testing.T) {
	body := `
[default]
provider = "anthropic"
model = "claude-haiku-5"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Credentials != nil {
		t.Errorf("Credentials = %+v; want nil", cfg.Credentials)
	}
}

// TestAccessors_ConfigWins: when [credentials] supplies a key, the
// accessor returns it even if the env var is also set (config wins).
func TestAccessors_ConfigWins(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "env-voyage")
	t.Setenv("LINEAR_API_KEY", "env-linear")
	t.Setenv("ANTHROPIC_API_KEY", "env-anthropic")
	t.Setenv("OPENAI_API_KEY", "env-openai")
	t.Setenv("GEMINI_API_KEY", "env-gemini")

	cfg := &Config{
		SchemaVersion: 1,
		Default:       Section{Provider: ProviderClaudeCLI, Model: "m", CLIBin: "/x"},
		Credentials: &Credentials{
			VoyageAPIKey:    "cfg-voyage",
			LinearAPIKey:    "cfg-linear",
			AnthropicAPIKey: "cfg-anthropic",
			OpenAIAPIKey:    "cfg-openai",
			GeminiAPIKey:    "cfg-gemini",
		},
	}
	t.Cleanup(SetForTest(cfg))

	if got := VoyageAPIKey(); got != "cfg-voyage" {
		t.Errorf("VoyageAPIKey() = %q; want cfg-voyage", got)
	}
	if got := LinearAPIKey(); got != "cfg-linear" {
		t.Errorf("LinearAPIKey() = %q; want cfg-linear", got)
	}
	if got := APIKeyForProvider(ProviderAnthropic); got != "cfg-anthropic" {
		t.Errorf("APIKeyForProvider(anthropic) = %q; want cfg-anthropic", got)
	}
	if got := APIKeyForProvider(ProviderOpenAI); got != "cfg-openai" {
		t.Errorf("APIKeyForProvider(openai) = %q; want cfg-openai", got)
	}
	if got := APIKeyForProvider(ProviderGemini); got != "cfg-gemini" {
		t.Errorf("APIKeyForProvider(gemini) = %q; want cfg-gemini", got)
	}
}

// TestAccessors_EnvFallback_NoCredentialsSection: a loaded config with no
// [credentials] section falls back to the env vars.
func TestAccessors_EnvFallback_NoCredentialsSection(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "env-voyage")
	t.Setenv("LINEAR_API_KEY", "env-linear")
	t.Setenv("ANTHROPIC_API_KEY", "env-anthropic")

	cfg := &Config{
		SchemaVersion: 1,
		Default:       Section{Provider: ProviderClaudeCLI, Model: "m", CLIBin: "/x"},
	}
	t.Cleanup(SetForTest(cfg))

	if got := VoyageAPIKey(); got != "env-voyage" {
		t.Errorf("VoyageAPIKey() = %q; want env-voyage", got)
	}
	if got := LinearAPIKey(); got != "env-linear" {
		t.Errorf("LinearAPIKey() = %q; want env-linear", got)
	}
	if got := APIKeyForProvider(ProviderAnthropic); got != "env-anthropic" {
		t.Errorf("APIKeyForProvider(anthropic) = %q; want env-anthropic", got)
	}
}

// TestAccessors_EmptyCredentialKeyFallsBackToEnv: a [credentials] section
// present but with an empty value for a key falls back to the env var for
// that key — only non-empty config values win.
func TestAccessors_EmptyCredentialKeyFallsBackToEnv(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "env-voyage")
	t.Setenv("LINEAR_API_KEY", "env-linear")
	t.Setenv("GEMINI_API_KEY", "env-gemini")

	cfg := &Config{
		SchemaVersion: 1,
		Default:       Section{Provider: ProviderClaudeCLI, Model: "m", CLIBin: "/x"},
		Credentials: &Credentials{
			// All empty — the section exists but every key is unset.
			VoyageAPIKey: "",
			LinearAPIKey: "",
			GeminiAPIKey: "",
		},
	}
	t.Cleanup(SetForTest(cfg))

	if got := VoyageAPIKey(); got != "env-voyage" {
		t.Errorf("VoyageAPIKey() = %q; want env-voyage (fallback)", got)
	}
	if got := LinearAPIKey(); got != "env-linear" {
		t.Errorf("LinearAPIKey() = %q; want env-linear (fallback)", got)
	}
	if got := APIKeyForProvider(ProviderGemini); got != "env-gemini" {
		t.Errorf("APIKeyForProvider(gemini) = %q; want env-gemini (fallback)", got)
	}
}

// TestAccessors_NoConfigLoaded: when no config has been loaded (Loaded()
// is false), the accessors must not panic — they fall straight back to the
// env var. This guards the test-friendly / early-bootstrap path.
func TestAccessors_NoConfigLoaded(t *testing.T) {
	t.Cleanup(SetForTest(nil)) // explicitly unload, restore prior on cleanup
	t.Setenv("VOYAGE_API_KEY", "env-voyage")
	t.Setenv("LINEAR_API_KEY", "env-linear")
	t.Setenv("OPENAI_API_KEY", "env-openai")

	if Loaded() {
		t.Fatal("Loaded() = true after SetForTest(nil); want false")
	}
	if got := VoyageAPIKey(); got != "env-voyage" {
		t.Errorf("VoyageAPIKey() = %q; want env-voyage", got)
	}
	if got := LinearAPIKey(); got != "env-linear" {
		t.Errorf("LinearAPIKey() = %q; want env-linear", got)
	}
	if got := APIKeyForProvider(ProviderOpenAI); got != "env-openai" {
		t.Errorf("APIKeyForProvider(openai) = %q; want env-openai", got)
	}
}

// TestAPIKeyForProvider_CLIProvidersEmpty: CLI providers never carry a key
// — even with [credentials] populated and env vars set.
func TestAPIKeyForProvider_CLIProvidersEmpty(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-anthropic")
	cfg := &Config{
		SchemaVersion: 1,
		Default:       Section{Provider: ProviderClaudeCLI, Model: "m", CLIBin: "/x"},
		Credentials:   &Credentials{AnthropicAPIKey: "cfg-anthropic"},
	}
	t.Cleanup(SetForTest(cfg))

	if got := APIKeyForProvider(ProviderClaudeCLI); got != "" {
		t.Errorf("APIKeyForProvider(claude-cli) = %q; want empty", got)
	}
	if got := APIKeyForProvider(ProviderCodexCLI); got != "" {
		t.Errorf("APIKeyForProvider(codex-cli) = %q; want empty", got)
	}
}
