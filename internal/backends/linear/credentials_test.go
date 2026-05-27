// SPDX-License-Identifier: Apache-2.0

package linear

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// TestEnabled_ConfigCredentials: a loaded config with [credentials].
// linear_api_key set enables the backend even with the env var unset.
func TestEnabled_ConfigCredentials(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "")
	cfg := &config.Config{
		SchemaVersion: 1,
		Default:       config.Section{Provider: config.ProviderClaudeCLI, Model: "m", CLIBin: "/x"},
		Credentials:   &config.Credentials{LinearAPIKey: "lin_api_from_config"},
	}
	t.Cleanup(config.SetForTest(cfg))

	if !Enabled() {
		t.Error("Enabled() = false with config linear_api_key set, want true")
	}
	if got := NewClient().APIKey; got != "lin_api_from_config" {
		t.Errorf("NewClient().APIKey = %q; want lin_api_from_config", got)
	}
	if got := New().Client.APIKey; got != "lin_api_from_config" {
		t.Errorf("New().Client.APIKey = %q; want lin_api_from_config", got)
	}
}

// TestEnabled_EnvFallback: no [credentials] section, LINEAR_API_KEY env set
// — the env fallback enables the backend and seeds the client key.
func TestEnabled_EnvFallback(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "lin_api_from_env")
	cfg := &config.Config{
		SchemaVersion: 1,
		Default:       config.Section{Provider: config.ProviderClaudeCLI, Model: "m", CLIBin: "/x"},
		// no Credentials
	}
	t.Cleanup(config.SetForTest(cfg))

	if !Enabled() {
		t.Error("Enabled() = false with LINEAR_API_KEY env set, want true")
	}
	if got := NewClient().APIKey; got != "lin_api_from_env" {
		t.Errorf("NewClient().APIKey = %q; want lin_api_from_env", got)
	}
}

// TestEnabled_BothEmpty: no config credentials and no env var → disabled.
func TestEnabled_BothEmpty(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "")
	cfg := &config.Config{
		SchemaVersion: 1,
		Default:       config.Section{Provider: config.ProviderClaudeCLI, Model: "m", CLIBin: "/x"},
	}
	t.Cleanup(config.SetForTest(cfg))

	if Enabled() {
		t.Error("Enabled() = true with no key anywhere, want false")
	}
	if got := NewClient().APIKey; got != "" {
		t.Errorf("NewClient().APIKey = %q; want empty", got)
	}
}

// TestEnabled_NoConfigLoaded: with no config singleton installed, Enabled()
// must not panic — it falls back to the env var.
func TestEnabled_NoConfigLoaded(t *testing.T) {
	t.Cleanup(config.SetForTest(nil))
	t.Setenv("LINEAR_API_KEY", "lin_api_no_config")
	if config.Loaded() {
		t.Fatal("config.Loaded() = true after SetForTest(nil)")
	}
	if !Enabled() {
		t.Error("Enabled() = false with env set and no config, want true")
	}
}
