// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

// TestParse_RetentionSection parses a [retention] table and checks the
// sessions field round-trips onto Config.Retention.
func TestParse_RetentionSection(t *testing.T) {
	body := `
[default]
provider = "claude-cli"
model = "claude-opus-4-7"
cli_bin = "/usr/local/bin/claude"

[retention]
sessions = "7d"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Retention == nil {
		t.Fatal("Retention is nil; expected populated")
	}
	if cfg.Retention.Sessions != "7d" {
		t.Errorf("Sessions = %q; want 7d", cfg.Retention.Sessions)
	}
}

// TestParse_RetentionAbsent confirms the section is optional: Config
// from a body with no [retention] table has a nil Retention pointer.
func TestParse_RetentionAbsent(t *testing.T) {
	body := `
[default]
provider = "anthropic"
model = "claude-haiku-5"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Retention != nil {
		t.Errorf("Retention = %+v; want nil", cfg.Retention)
	}
}

// TestRetentionAccessors_ConfigWins: when [retention] supplies a key,
// the accessor returns it. Sanity-check: env vars are NOT consulted —
// retention has no env-var fallback by design. We assert that even
// setting RETENTION_SESSIONS in the environment cannot influence the
// accessor (it would never read it).
func TestRetentionAccessors_ConfigWins(t *testing.T) {
	t.Setenv("RETENTION_SESSIONS", "should-not-be-read")

	cfg := &Config{
		SchemaVersion: 1,
		Default:       Section{Provider: ProviderClaudeCLI, Model: "m", CLIBin: "/x"},
		Retention: &Retention{
			Sessions: "7d",
		},
	}
	t.Cleanup(SetForTest(cfg))

	if got := RetentionSessions(); got != "7d" {
		t.Errorf("RetentionSessions() = %q; want 7d", got)
	}
}

// TestRetentionAccessors_NoEnvFallback_NoRetentionSection: a loaded
// config with no [retention] section returns empty strings — env vars
// are ignored even when set (no fallback by design).
func TestRetentionAccessors_NoEnvFallback_NoRetentionSection(t *testing.T) {
	t.Setenv("RETENTION_SESSIONS", "7d")

	cfg := &Config{
		SchemaVersion: 1,
		Default:       Section{Provider: ProviderClaudeCLI, Model: "m", CLIBin: "/x"},
	}
	t.Cleanup(SetForTest(cfg))

	if got := RetentionSessions(); got != "" {
		t.Errorf("RetentionSessions() = %q; want empty (no env fallback)", got)
	}
}

// TestRetentionAccessors_EmptyFieldFallsThroughToEmpty: a [retention]
// section present but with empty values returns empty strings (not env
// var values — retention has no env fallback).
func TestRetentionAccessors_EmptyFieldFallsThroughToEmpty(t *testing.T) {
	cfg := &Config{
		SchemaVersion: 1,
		Default:       Section{Provider: ProviderClaudeCLI, Model: "m", CLIBin: "/x"},
		Retention:     &Retention{Sessions: ""},
	}
	t.Cleanup(SetForTest(cfg))

	if got := RetentionSessions(); got != "" {
		t.Errorf("RetentionSessions() = %q; want empty", got)
	}
}

// TestRetentionAccessors_NoConfigLoaded: when no config has been loaded
// (Loaded() is false), the accessors must not panic and must return
// empty strings.
func TestRetentionAccessors_NoConfigLoaded(t *testing.T) {
	t.Cleanup(SetForTest(nil))

	if Loaded() {
		t.Fatal("Loaded() = true after SetForTest(nil); want false")
	}
	if got := RetentionSessions(); got != "" {
		t.Errorf("RetentionSessions() = %q; want empty", got)
	}
}

// TestParse_RetentionAndCredentialsCoexist: both optional sections
// coexist in the same file without interfering.
func TestParse_RetentionAndCredentialsCoexist(t *testing.T) {
	body := `
[default]
provider = "claude-cli"
model = "claude-opus-4-7"
cli_bin = "/usr/local/bin/claude"

[credentials]
voyage_api_key = "voy-xxx"

[retention]
sessions = "3d"
`
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Credentials == nil || cfg.Credentials.VoyageAPIKey != "voy-xxx" {
		t.Errorf("Credentials.VoyageAPIKey = %+v; want voy-xxx", cfg.Credentials)
	}
	if cfg.Retention == nil || cfg.Retention.Sessions != "3d" {
		t.Errorf("Retention = %+v; want {3d}", cfg.Retention)
	}
}
