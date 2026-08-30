// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// --- Step c: guided config -------------------------------------------------

// TestSetup_Guided_WritesCredentials: a scripted
// guided run writes config (0600) with the chosen provider + entered
// voyage + linear keys; the reduced-accuracy warning prints when voyage
// is skipped.
func TestSetup_Guided_WritesCredentials(t *testing.T) {
	t.Run("both credential legs written", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "sk-a") // AutoDetect default
		emptyPATH(t)
		forceTTY(t, true)
		_ = spySelfUpdate(t, "")
		withScriptedStdin(t, "openai\nvoy-guided\nlin-guided\n")
		out := captureStdout(t, func() {
			if err := runSetup([]string{"--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		info, err := os.Stat(cfgPath)
		if err != nil {
			t.Fatalf("stat config: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode = %v; want 0600", info.Mode().Perm())
		}
		data, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
		if !strings.Contains(string(data), `provider = "openai"`) {
			t.Fatalf("chosen provider not written:\n%s", data)
		}
		creds := readCreds(t, cfgPath)
		if creds == nil || creds.VoyageAPIKey != "voy-guided" || creds.LinearAPIKey != "lin-guided" {
			t.Fatalf("both credential legs must be written; got %+v", creds)
		}
		if strings.Contains(out, reducedAccuracyWarning) {
			t.Fatalf("no reduced-accuracy warning when voyage is entered")
		}
	})

	t.Run("reduced-accuracy warning when voyage skipped", func(t *testing.T) {
		_ = setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "sk-a")
		emptyPATH(t)
		forceTTY(t, true)
		_ = spySelfUpdate(t, "")
		withScriptedStdin(t, "anthropic\n\n\n") // provider, empty voyage, empty linear
		out := captureStdout(t, func() {
			if err := runSetup([]string{"--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if !strings.Contains(out, reducedAccuracyWarning) {
			t.Fatalf("expected reduced-accuracy warning when voyage skipped; got %q", out)
		}
	})
}

// TestSetup_Guided_NoProviderDegrades: guided first-run with ZERO
// detectable providers presents the provider list with NO preselection.
// Skipping the provider prompt (empty input) COMPLETES with an
// unconfigured (BM25-only) config and the no-provider note — exit 0.
func TestSetup_Guided_NoProviderDegrades(t *testing.T) {
	t.Run("skip provider → unconfigured", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		emptyPATH(t)
		forceTTY(t, true)
		_ = spySelfUpdate(t, "")
		withScriptedStdin(t, "\n\n\n") // skip provider, skip voyage, skip linear
		out := captureStdout(t, func() {
			if err := runSetup([]string{"--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("guided no-provider skip must degrade-not-die; got err %v", err)
			}
		})
		if !strings.Contains(out, noProviderNote) {
			t.Fatalf("expected the actionable no-provider note; got %q", out)
		}
		cfg := readConfig(t, cfgPath)
		if cfg.Default.Provider != "" {
			t.Fatalf("skip must yield empty Default.Provider; got %q", cfg.Default.Provider)
		}
	})

	t.Run("pick provider → provider written", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		emptyPATH(t)
		forceTTY(t, true)
		_ = spySelfUpdate(t, "")
		withScriptedStdin(t, "openai\n\n\n") // pick openai despite no detection, skip keys
		out := captureStdout(t, func() {
			if err := runSetup([]string{"--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("guided no-provider pick must complete; got err %v", err)
			}
		})
		if !strings.Contains(out, noProviderNote) {
			t.Fatalf("expected the no-provider note before the list prompt; got %q", out)
		}
		if cfg := readConfig(t, cfgPath); cfg.Default.Provider != "openai" {
			t.Fatalf("picked provider must land in the written config; got %q", cfg.Default.Provider)
		}
	})
}

// TestSetup_GuidedReconfigure_PreservesVoyage: on
// guided --reconfigure, an empty voyage answer PRESERVES the stored key;
// a new provider lands; no reduced-accuracy warning.
func TestSetup_GuidedReconfigure_PreservesVoyage(t *testing.T) {
	cfgPath := setupHome(t)
	clearCredEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-a")
	emptyPATH(t)
	forceTTY(t, true)
	_ = spySelfUpdate(t, "")
	seed := "schema_version = 1\n[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-4-5-20251001\"\n[credentials]\nvoyage_api_key = \"voy-stored\"\n"
	seedConfig(t, cfgPath, seed)
	withScriptedStdin(t, "openai\n\n\n") // new provider, skip voyage, skip linear
	out := captureStdout(t, func() {
		if err := runSetup([]string{"--reconfigure", "--no-self-update", "--no-service"}); err != nil {
			t.Fatalf("runSetup: %v", err)
		}
	})
	data, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
	if !strings.Contains(string(data), `provider = "openai"`) {
		t.Fatalf("new provider must land:\n%s", data)
	}
	creds := readCreds(t, cfgPath)
	if creds == nil || creds.VoyageAPIKey != "voy-stored" {
		t.Fatalf("stored voyage key must be preserved on empty input; got %+v", creds)
	}
	if strings.Contains(out, reducedAccuracyWarning) {
		t.Fatalf("no reduced-accuracy warning when a stored voyage key is preserved")
	}
}

// TestSetup_GuidedReconfigure_CustomizationLoss: an
// active [summarizer] section fires the customization-loss warning + a y/n
// confirm; 'n'/empty aborts byte-identical, 'y' rewrites. A
// [default]/[credentials]-only config prints NO warning.
func TestSetup_GuidedReconfigure_CustomizationLoss(t *testing.T) {
	customCfg := "schema_version = 1\n[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-4-5-20251001\"\n[summarizer]\nprovider = \"anthropic\"\nmodel = \"claude-opus-4-7\"\n"

	t.Run("empty answer aborts byte-identical", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "sk-a")
		emptyPATH(t)
		forceTTY(t, true)
		_ = spySelfUpdate(t, "")
		seedConfig(t, cfgPath, customCfg)
		before, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
		withScriptedStdin(t, "\n")        // empty confirm → abort
		out := captureStdout(t, func() {
			if err := runSetup([]string{"--reconfigure", "--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if !strings.Contains(out, customizationLossWarning) {
			t.Fatalf("customization-loss warning must fire; got %q", out)
		}
		after, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
		if string(before) != string(after) {
			t.Fatalf("abort must leave config byte-identical")
		}
	})

	t.Run("yes confirm rewrites", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "sk-a")
		emptyPATH(t)
		forceTTY(t, true)
		_ = spySelfUpdate(t, "")
		seedConfig(t, cfgPath, customCfg)
		before, _ := os.ReadFile(cfgPath)          //nolint:gosec // temp
		withScriptedStdin(t, "y\nanthropic\n\n\n") // confirm, provider, voyage, linear
		_ = captureStdout(t, func() {
			if err := runSetup([]string{"--reconfigure", "--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		after, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
		if string(before) == string(after) {
			t.Fatalf("'y' confirm must rewrite the config")
		}
		// The regenerated config carries only the template's COMMENTED
		// [summarizer] example, not an active section — Parse proves it.
		cfg, perr := config.Parse(after)
		if perr != nil {
			t.Fatalf("parse rewritten config: %v", perr)
		}
		if cfg.Summarizer != nil {
			t.Fatalf("rewrite regenerates from template — the active [summarizer] section should be gone")
		}
	})

	t.Run("no warning for default+credentials-only config", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "sk-a")
		emptyPATH(t)
		forceTTY(t, true)
		_ = spySelfUpdate(t, "")
		plain := "schema_version = 1\n[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-4-5-20251001\"\n[credentials]\nvoyage_api_key = \"voy\"\n"
		seedConfig(t, cfgPath, plain)
		withScriptedStdin(t, "anthropic\n\n\n")
		out := captureStdout(t, func() {
			if err := runSetup([]string{"--reconfigure", "--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if strings.Contains(out, customizationLossWarning) {
			t.Fatalf("no customization-loss warning for a default/credentials-only config; got %q", out)
		}
	})
}
