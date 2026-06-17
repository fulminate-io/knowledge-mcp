// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse_RoundTrip(t *testing.T) {
	body := `
[default]
provider = "anthropic"
model = "claude-haiku-5"

[summarizer]
model = "claude-opus-4-7"

[dream]
provider = "openai"
model = "gpt-5-mini"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Default.Provider != ProviderAnthropic {
		t.Errorf("Default.Provider = %q; want %q", cfg.Default.Provider, ProviderAnthropic)
	}
	if cfg.Default.Model != "claude-haiku-5" {
		t.Errorf("Default.Model = %q", cfg.Default.Model)
	}
	if cfg.Summarizer == nil {
		t.Fatal("Summarizer is nil; expected populated")
	}
	if cfg.Summarizer.Provider != "" {
		t.Errorf("Summarizer.Provider = %q; want empty (inherited)", cfg.Summarizer.Provider)
	}
	if cfg.Summarizer.Model != "claude-opus-4-7" {
		t.Errorf("Summarizer.Model = %q", cfg.Summarizer.Model)
	}
	if cfg.Dream == nil {
		t.Fatal("Dream is nil; expected populated")
	}
	if cfg.Dream.Provider != ProviderOpenAI {
		t.Errorf("Dream.Provider = %q", cfg.Dream.Provider)
	}
}

func TestParse_SupervisorSection(t *testing.T) {
	body := `
[default]
provider = "anthropic"
model = "claude-haiku-5"

[supervisor]
provider = "openai"
model = "gpt-5"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Supervisor == nil {
		t.Fatal("Supervisor is nil; expected populated")
	}
	if cfg.Supervisor.Provider != ProviderOpenAI {
		t.Errorf("Supervisor.Provider = %q; want %q", cfg.Supervisor.Provider, ProviderOpenAI)
	}
	if cfg.Supervisor.Model != "gpt-5" {
		t.Errorf("Supervisor.Model = %q", cfg.Supervisor.Model)
	}
}

func TestParse_SupervisorAbsentIsNil(t *testing.T) {
	body := `
[default]
provider = "anthropic"
model = "claude-haiku-5"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Supervisor != nil {
		t.Errorf("Supervisor = %+v; want nil when section absent", cfg.Supervisor)
	}
}

// TestParse_TopicsSection covers the [topics] consumer (the similarity lever's
// topic-summary LLM): a model-only section parses, and Resolve(ConsumerTopics)
// overrides the model while inheriting provider/cli_bin from [default] — the
// exact "topic summaries on an opus-class model, pipeline summarizer untouched"
// configuration shape.
func TestParse_TopicsSection(t *testing.T) {
	body := `
[default]
provider = "claude-cli"
model = "claude-haiku-5"
cli_bin = "/usr/local/bin/claude"

[topics]
model = "claude-opus-5"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Topics == nil {
		t.Fatal("Topics is nil; expected populated")
	}
	sec, err := cfg.Resolve(ConsumerTopics)
	if err != nil {
		t.Fatalf("Resolve(topics): %v", err)
	}
	if sec.Model != "claude-opus-5" {
		t.Errorf("topics Model = %q; want the [topics] override", sec.Model)
	}
	if sec.Provider != ProviderClaudeCLI {
		t.Errorf("topics Provider = %q; want claude-cli inherited from [default]", sec.Provider)
	}
	if sec.CLIBin != "/usr/local/bin/claude" {
		t.Errorf("topics CLIBin = %q; want inherited from [default]", sec.CLIBin)
	}
}

// TestParse_TopicsAbsentInheritsDefault asserts an absent [topics] section
// resolves to [default] wholesale (nil pointer → full inheritance).
func TestParse_TopicsAbsentInheritsDefault(t *testing.T) {
	body := `
[default]
provider = "anthropic"
model = "claude-haiku-5"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Topics != nil {
		t.Errorf("Topics = %+v; want nil when section absent", cfg.Topics)
	}
	sec, err := cfg.Resolve(ConsumerTopics)
	if err != nil {
		t.Fatalf("Resolve(topics): %v", err)
	}
	if sec.Model != "claude-haiku-5" || sec.Provider != ProviderAnthropic {
		t.Errorf("absent [topics] must inherit [default]; got %+v", sec)
	}
}

func TestParse_BaseURL(t *testing.T) {
	body := `
[default]
provider = "openai"
model = "gpt-5-mini"
base_url = "http://127.0.0.1:1234/v1"

[summarizer]
base_url = "http://127.0.0.1:9999/v1"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Default.BaseURL != "http://127.0.0.1:1234/v1" {
		t.Errorf("Default.BaseURL = %q; want %q", cfg.Default.BaseURL, "http://127.0.0.1:1234/v1")
	}
	if cfg.Summarizer == nil {
		t.Fatal("Summarizer is nil; expected populated")
	}
	if cfg.Summarizer.BaseURL != "http://127.0.0.1:9999/v1" {
		t.Errorf("Summarizer.BaseURL = %q; want %q", cfg.Summarizer.BaseURL, "http://127.0.0.1:9999/v1")
	}
}

// TestParse_SummarizerFallbackChain covers the additive [[summarizer.fallback]]
// ordered chain: a [summarizer] table with two nested fallback tables parses
// into Section.Fallback as a len-2 slice IN ORDER, each entry carrying its own
// provider/model and the per-field inheritance contract (an entry that omits
// cli_bin keeps it empty so ResolveChain fills it from [default] later).
func TestParse_SummarizerFallbackChain(t *testing.T) {
	body := `
[default]
provider = "claude-cli"
model = "claude-haiku-5"
cli_bin = "/usr/local/bin/claude"

[summarizer]
model = "claude-opus-5"

[[summarizer.fallback]]
provider = "openai"
model = "gpt-5-mini"

[[summarizer.fallback]]
provider = "gemini"
model = "gemini-2.5-flash"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Summarizer == nil {
		t.Fatal("Summarizer is nil; expected populated")
	}
	if got := len(cfg.Summarizer.Fallback); got != 2 {
		t.Fatalf("Fallback len = %d; want 2", got)
	}
	if cfg.Summarizer.Fallback[0].Provider != ProviderOpenAI {
		t.Errorf("Fallback[0].Provider = %q; want %q", cfg.Summarizer.Fallback[0].Provider, ProviderOpenAI)
	}
	if cfg.Summarizer.Fallback[0].Model != "gpt-5-mini" {
		t.Errorf("Fallback[0].Model = %q; want gpt-5-mini", cfg.Summarizer.Fallback[0].Model)
	}
	if cfg.Summarizer.Fallback[1].Provider != ProviderGemini {
		t.Errorf("Fallback[1].Provider = %q; want %q", cfg.Summarizer.Fallback[1].Provider, ProviderGemini)
	}
	if cfg.Summarizer.Fallback[1].Model != "gemini-2.5-flash" {
		t.Errorf("Fallback[1].Model = %q; want gemini-2.5-flash", cfg.Summarizer.Fallback[1].Model)
	}
	// cli_bin is omitted on both fallback entries — it stays empty here and is
	// inherited from [default] only at ResolveChain time.
	if cfg.Summarizer.Fallback[0].CLIBin != "" {
		t.Errorf("Fallback[0].CLIBin = %q; want empty (inherited at resolve time)", cfg.Summarizer.Fallback[0].CLIBin)
	}
}

// TestParse_FallbackUnknownProvider asserts a fallback entry with an
// unrecognized provider trips the SAME unknown-provider error the single-section
// translate already produces — the per-entry loop reuses translateSection, so
// validation is not duplicated.
func TestParse_FallbackUnknownProvider(t *testing.T) {
	body := `
[default]
provider = "anthropic"
model = "claude-haiku-5"

[summarizer]
model = "claude-opus-5"

[[summarizer.fallback]]
provider = "bogus"
model = "x"
`
	_, err := Parse([]byte(body))
	if err == nil {
		t.Fatal("Parse(fallback unknown provider): want error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error does not mention unknown provider: %v", err)
	}
}

func TestParse_Malformed(t *testing.T) {
	// Unclosed bracket — go-toml/v2 rejects this.
	body := `[default
provider = "anthropic"
`
	_, err := Parse([]byte(body))
	if err == nil {
		t.Fatal("Parse(malformed): want error, got nil")
	}
	if !strings.Contains(err.Error(), "config.Parse") {
		t.Errorf("error not wrapped by Parse: %v", err)
	}
}

func TestParse_UnknownProvider(t *testing.T) {
	body := `
[default]
provider = "bedrock"
model = "claude-haiku-5"
`
	_, err := Parse([]byte(body))
	if err == nil {
		t.Fatal("Parse(unknown provider): want error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error does not mention unknown provider: %v", err)
	}
}

func TestParse_PerConsumerOnly(t *testing.T) {
	// No [default] — per-consumer section provides everything.
	body := `
[summarizer]
provider = "openai"
model = "gpt-5-mini"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Default.Provider != "" || cfg.Default.Model != "" {
		t.Errorf("Default not empty: %+v", cfg.Default)
	}
	if cfg.Summarizer == nil || cfg.Summarizer.Provider != ProviderOpenAI {
		t.Errorf("Summarizer wrong: %+v", cfg.Summarizer)
	}
}

func TestParse_ProviderLowercased(t *testing.T) {
	body := `
[default]
provider = "OpenAI"
model = "gpt-5-mini"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Default.Provider != ProviderOpenAI {
		t.Errorf("Default.Provider = %q; want lowercased %q", cfg.Default.Provider, ProviderOpenAI)
	}
}

func TestParse_SchemaVersionAbsent(t *testing.T) {
	// Configs without schema_version are pre-versioning files; treated as v1.
	body := `
[default]
provider = "anthropic"
model = "claude-haiku-5"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1 (default)", cfg.SchemaVersion)
	}
}

func TestParse_SchemaVersionExplicit(t *testing.T) {
	body := `
schema_version = 1

[default]
provider = "anthropic"
model = "claude-haiku-5"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", cfg.SchemaVersion)
	}
}

func TestParse_SchemaVersionFutureRejected(t *testing.T) {
	// schema_version > CurrentSchemaVersion = config from a newer build.
	// Reject with an upgrade hint.
	body := `
schema_version = 9999

[default]
provider = "anthropic"
model = "claude-haiku-5"
`
	_, err := Parse([]byte(body))
	if err == nil {
		t.Fatal("Parse: want error for future schema_version, got nil")
	}
	if !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Errorf("error should name the version mismatch: %v", err)
	}
}

// TestParse_HealthProbeInterval covers the top-level health_probe_interval key
// (a Go duration string): a valid value parses to that Duration, an absent key
// leaves the field zero (the consumer defaults it downstream), and a malformed
// value returns a Parse error that names the key so the operator can fix it.
func TestParse_HealthProbeInterval(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		body := `
health_probe_interval = "5m"

[default]
provider = "anthropic"
model = "claude-haiku-5"
`
		cfg, err := Parse([]byte(body))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if cfg.HealthProbeInterval != 5*time.Minute {
			t.Errorf("HealthProbeInterval = %s; want 5m", cfg.HealthProbeInterval)
		}
	})

	t.Run("absent", func(t *testing.T) {
		body := `
[default]
provider = "anthropic"
model = "claude-haiku-5"
`
		cfg, err := Parse([]byte(body))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if cfg.HealthProbeInterval != 0 {
			t.Errorf("HealthProbeInterval = %s; want 0 (defaulted downstream)", cfg.HealthProbeInterval)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		body := `
health_probe_interval = "nope"

[default]
provider = "anthropic"
model = "claude-haiku-5"
`
		_, err := Parse([]byte(body))
		if err == nil {
			t.Fatal("Parse(malformed duration): want error, got nil")
		}
		if !strings.Contains(err.Error(), "health_probe_interval") {
			t.Errorf("error does not name the key: %v", err)
		}
	})
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("Load(missing): want error, got nil")
	}
	if !strings.Contains(err.Error(), "config.Load") {
		t.Errorf("error not wrapped by Load: %v", err)
	}
}

func TestLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	body := `
[default]
provider = "anthropic"
model = "claude-haiku-5"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Default.Provider != ProviderAnthropic {
		t.Errorf("Default.Provider = %q", cfg.Default.Provider)
	}
}
