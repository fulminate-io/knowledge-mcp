// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
