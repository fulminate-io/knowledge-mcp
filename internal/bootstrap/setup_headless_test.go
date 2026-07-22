// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"strings"
	"testing"
)

// --- Step d: headless config ----------------------------------------------

// TestSetup_Headless_PersistsEnvCreds:
// first-run headless persists set credential env vars into [credentials],
// leaves unset keys unset, and prints/omits the reduced-accuracy warning
// on the voyage key's presence.
func TestSetup_Headless_PersistsEnvCreds(t *testing.T) {
	t.Run("voyage set persists, no warning", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		t.Setenv("VOYAGE_API_KEY", "voy-env")
		t.Setenv("ANTHROPIC_API_KEY", "ant-env")
		emptyPATH(t)
		_ = spySelfUpdate(t, "")
		out := captureStdout(t, func() {
			if err := runSetup([]string{"--headless", "--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		creds := readCreds(t, cfgPath)
		if creds == nil || creds.VoyageAPIKey != "voy-env" || creds.AnthropicAPIKey != "ant-env" {
			t.Fatalf("env creds must persist into [credentials]; got %+v", creds)
		}
		if creds.LinearAPIKey != "" || creds.OpenAIAPIKey != "" || creds.GeminiAPIKey != "" {
			t.Fatalf("unset env keys must stay unset; got %+v", creds)
		}
		if strings.Contains(out, reducedAccuracyWarning) {
			t.Fatalf("no reduced-accuracy warning when voyage is set")
		}
	})

	t.Run("no voyage warns", func(t *testing.T) {
		_ = setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "ant-env")
		emptyPATH(t)
		_ = spySelfUpdate(t, "")
		out := captureStdout(t, func() {
			if err := runSetup([]string{"--headless", "--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if !strings.Contains(out, reducedAccuracyWarning) {
			t.Fatalf("expected reduced-accuracy warning with no voyage key; got %q", out)
		}
	})

	t.Run("reconfigure differing env does not overwrite stored", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "ant-env")
		t.Setenv("VOYAGE_API_KEY", "voy-env-different")
		emptyPATH(t)
		_ = spySelfUpdate(t, "")
		seed := "schema_version = 1\n[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-4-5-20251001\"\n[credentials]\nvoyage_api_key = \"voy-stored\"\n"
		seedConfig(t, cfgPath, seed)
		_ = captureStdout(t, func() {
			if err := runSetup([]string{"--headless", "--reconfigure", "--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		creds := readCreds(t, cfgPath)
		if creds == nil || creds.VoyageAPIKey != "voy-stored" {
			t.Fatalf("stored voyage key must win over a differing env value; got %+v", creds)
		}
	})
}

// TestSetup_Headless_NoProviderDegrades: headless first-run with ZERO
// detectable providers (no CLI on PATH, no API key) must COMPLETE — it
// writes a valid, parseable UNCONFIGURED config (empty Default.Provider),
// prints the actionable no-provider note, and returns nil (exit 0). The
// degrade-not-die invariant: BM25-only, daemon still boots.
func TestSetup_Headless_NoProviderDegrades(t *testing.T) {
	cfgPath := setupHome(t)
	clearCredEnv(t) // no API keys
	emptyPATH(t)    // no claude/codex on PATH
	_ = spySelfUpdate(t, "")
	out := captureStdout(t, func() {
		if err := runSetup([]string{"--headless", "--no-self-update", "--no-service"}); err != nil {
			t.Fatalf("headless no-provider must degrade-not-die (exit 0); got err %v", err)
		}
	})
	if !strings.Contains(out, noProviderNote) {
		t.Fatalf("expected the actionable no-provider note; got %q", out)
	}
	cfg := readConfig(t, cfgPath)
	if cfg.Default.Provider != "" {
		t.Fatalf("unconfigured config must have empty Default.Provider; got %q", cfg.Default.Provider)
	}
	// A no-provider box also has no voyage key — the reduced-accuracy
	// warning still fires alongside the provider note.
	if !strings.Contains(out, reducedAccuracyWarning) {
		t.Fatalf("expected the reduced-accuracy warning with no voyage key; got %q", out)
	}
}

// TestSetup_HeadlessReconfigure_CustomizationLoss:
// an active [dream] fires the warning and PROCEEDS with no prompt (no
// hang); a default/credentials-only config prints no warning.
func TestSetup_HeadlessReconfigure_CustomizationLoss(t *testing.T) {
	dreamCfg := "schema_version = 1\n[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-4-5-20251001\"\n[dream]\nprovider = \"anthropic\"\nmodel = \"claude-opus-4-7\"\n"

	t.Run("dream section warns and proceeds", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "ant-env")
		emptyPATH(t)
		_ = spySelfUpdate(t, "")
		seedConfig(t, cfgPath, dreamCfg)
		before, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
		out := captureStdout(t, func() {
			if err := runSetup([]string{"--headless", "--reconfigure", "--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if !strings.Contains(out, customizationLossWarning) {
			t.Fatalf("customization-loss warning must fire; got %q", out)
		}
		after, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
		if string(before) == string(after) {
			t.Fatalf("headless --reconfigure must PROCEED with the rewrite")
		}
	})

	t.Run("plain config no warning", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "ant-env")
		emptyPATH(t)
		_ = spySelfUpdate(t, "")
		plain := "schema_version = 1\n[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-4-5-20251001\"\n"
		seedConfig(t, cfgPath, plain)
		out := captureStdout(t, func() {
			if err := runSetup([]string{"--headless", "--reconfigure", "--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if strings.Contains(out, customizationLossWarning) {
			t.Fatalf("no customization-loss warning for a plain config; got %q", out)
		}
	})
}
