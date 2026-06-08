// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubBinary creates an executable file named bin in dir. Only POSIX is
// supported — windows would need .exe and a different exec.LookPath rule,
// and we don't ship windows builds.
func stubBinary(t *testing.T, dir, bin string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("PATH-based exec.LookPath stubbing is POSIX-only")
	}
	path := filepath.Join(dir, bin)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil { //nolint:gosec // test stub, not a real executable
		t.Fatalf("stub %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // test stub must be executable
		t.Fatalf("chmod stub %s: %v", path, err)
	}
}

func TestValidate_HappyAPI(t *testing.T) {
	cfg := &Config{
		Default:    Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
		Summarizer: &Section{},
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	if err := cfg.Validate([]Consumer{ConsumerSummarizer}); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestValidate_BaseURLNoAPIKey proves Gate 2: a keyless API-provider
// consumer whose resolved section carries a base_url (a local/compatible
// endpoint handling auth out-of-band) passes Validate with no key in
// [credentials] or the env var.
func TestValidate_BaseURLNoAPIKey(t *testing.T) {
	cfg := &Config{
		Default:    Section{Provider: ProviderOpenAI, Model: "gpt-5-mini", BaseURL: "http://127.0.0.1:1234/v1"},
		Summarizer: &Section{},
	}
	t.Setenv("OPENAI_API_KEY", "")
	t.Cleanup(SetForTest(cfg))
	if err := cfg.Validate([]Consumer{ConsumerSummarizer}); err != nil {
		t.Errorf("Validate with base_url and no key: %v", err)
	}
}

func TestValidate_MissingAPIKey(t *testing.T) {
	cfg := &Config{
		Default: Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	err := cfg.Validate([]Consumer{ConsumerSummarizer})
	if err == nil {
		t.Fatal("Validate: want error, got nil")
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error does not mention ANTHROPIC_API_KEY: %v", err)
	}
}

// TestValidate_CredentialsOnlyAPIKey proves a key set only in [credentials]
// (env var unset) passes Validate, matching the runtime resolver. APIKeyForProvider
// reads the loaded-config GLOBAL singleton (keys.go credentials()/Active()), NOT
// the receiver handed to Validate, so the credentials must be installed via
// t.Cleanup(SetForTest(cfg)) — setting them only on the local cfg is insufficient.
func TestValidate_CredentialsOnlyAPIKey(t *testing.T) {
	cfg := &Config{
		Default:     Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
		Summarizer:  &Section{},
		Credentials: &Credentials{AnthropicAPIKey: "cfg-key"},
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Cleanup(SetForTest(cfg))
	if err := cfg.Validate([]Consumer{ConsumerSummarizer}); err != nil {
		t.Errorf("Validate with [credentials]-only key: %v", err)
	}
}

// TestValidate_NeitherCredentialsNorEnv proves that when the key is set in
// neither [credentials] nor the env var, Validate fails with a provider-named
// message that points the operator at both ways to set it.
func TestValidate_NeitherCredentialsNorEnv(t *testing.T) {
	cfg := &Config{
		Default: Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Cleanup(SetForTest(cfg))
	err := cfg.Validate([]Consumer{ConsumerSummarizer})
	if err == nil {
		t.Fatal("Validate: want error when key set in neither [credentials] nor env, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "anthropic") {
		t.Errorf("error does not name the provider: %v", err)
	}
	if !strings.Contains(msg, "[credentials]") {
		t.Errorf("error does not mention [credentials]: %v", err)
	}
	if !strings.Contains(msg, "ANTHROPIC_API_KEY") {
		t.Errorf("error does not mention the env var: %v", err)
	}
}

func TestValidate_HappyCLI(t *testing.T) {
	dir := t.TempDir()
	stubBinary(t, dir, "claude")
	binPath := filepath.Join(dir, "claude")

	cfg := &Config{
		Default:    Section{Provider: ProviderClaudeCLI, Model: "claude-haiku-5", CLIBin: binPath},
		Summarizer: &Section{},
	}
	if err := cfg.Validate([]Consumer{ConsumerSummarizer}); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestValidate_MissingCLIBin(t *testing.T) {
	// CLI provider with empty cli_bin — must error with explicit guidance.
	cfg := &Config{
		Default: Section{Provider: ProviderClaudeCLI, Model: "claude-haiku-5"},
	}
	err := cfg.Validate([]Consumer{ConsumerSummarizer})
	if err == nil {
		t.Fatal("Validate: want error, got nil")
	}
	if !strings.Contains(err.Error(), "cli_bin is not set") {
		t.Errorf("error should mention cli_bin: %v", err)
	}
}

func TestValidate_CLIBinDoesNotExist(t *testing.T) {
	cfg := &Config{
		Default: Section{Provider: ProviderClaudeCLI, Model: "claude-haiku-5", CLIBin: "/nonexistent/path/to/claude"},
	}
	err := cfg.Validate([]Consumer{ConsumerSummarizer})
	if err == nil {
		t.Fatal("Validate: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no such file") && !strings.Contains(err.Error(), "cannot find") {
		t.Errorf("error should mention missing path: %v", err)
	}
}

func TestValidate_CLIBinNotExecutable(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "claude-not-exec")
	if err := os.WriteFile(binPath, []byte("dummy"), 0o600); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write: %v", err)
	}
	cfg := &Config{
		Default: Section{Provider: ProviderClaudeCLI, Model: "claude-haiku-5", CLIBin: binPath},
	}
	err := cfg.Validate([]Consumer{ConsumerSummarizer})
	if err == nil {
		t.Fatal("Validate: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not executable") {
		t.Errorf("error should mention not-executable: %v", err)
	}
}

func TestValidate_NoModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	cfg := &Config{
		Default: Section{Provider: ProviderAnthropic},
	}
	err := cfg.Validate([]Consumer{ConsumerSummarizer})
	if err == nil {
		t.Fatal("Validate: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no model") {
		t.Errorf("error does not mention missing model: %v", err)
	}
}

func TestValidate_NoProvider(t *testing.T) {
	cfg := &Config{
		Default: Section{Model: "claude-haiku-5"},
	}
	err := cfg.Validate([]Consumer{ConsumerSummarizer})
	if err == nil {
		t.Fatal("Validate: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no provider") {
		t.Errorf("error does not mention missing provider: %v", err)
	}
}

func TestValidate_DreamSkippedWhenNotInList(t *testing.T) {
	// Dream config is broken (no env var set), but dream is not in
	// the consumer list — Validate must not flag it.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("OPENAI_API_KEY", "")
	cfg := &Config{
		Default: Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
		Dream:   &Section{Provider: ProviderOpenAI, Model: "gpt-5-mini"},
	}
	if err := cfg.Validate([]Consumer{ConsumerSummarizer}); err != nil {
		t.Errorf("Validate (dream not in list): %v", err)
	}
}

func TestValidate_AggregatesAllErrors(t *testing.T) {
	// Both consumers fail — expect both errors joined.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	cfg := &Config{
		Default: Section{Provider: ProviderAnthropic, Model: "claude-haiku-5"},
		Dream:   &Section{Provider: ProviderOpenAI, Model: "gpt-5-mini"},
	}
	err := cfg.Validate([]Consumer{ConsumerSummarizer, ConsumerDream})
	if err == nil {
		t.Fatal("Validate: want error, got nil")
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("missing ANTHROPIC_API_KEY error: %v", err)
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("missing OPENAI_API_KEY error: %v", err)
	}
}

func TestValidate_NilConfig(t *testing.T) {
	var cfg *Config
	if err := cfg.Validate([]Consumer{ConsumerSummarizer}); err == nil {
		t.Fatal("Validate(nil): want error")
	}
}
