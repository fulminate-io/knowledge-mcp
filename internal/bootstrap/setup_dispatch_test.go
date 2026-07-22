// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"flag"
	"os"
	"strings"
	"testing"
)

// --- Step a: dispatch + flags + run/update detection ----------------------

// TestSetup_FlagSet_BehaviorOnly: the FlagSet
// exposes only behavior flags — no name matches api/key/token/secret.
func TestSetup_FlagSet_BehaviorOnly(t *testing.T) {
	fs := flag.NewFlagSet("knowledge setup", flag.ContinueOnError)
	var f setupFlags
	registerSetupFlags(fs, &f)
	fs.VisitAll(func(fl *flag.Flag) {
		name := strings.ToLower(fl.Name)
		for _, banned := range []string{"key", "token", "secret", "api"} {
			if strings.Contains(name, banned) {
				t.Errorf("setup flag %q must not carry a secret-ish token %q", fl.Name, banned)
			}
		}
	})
}

// TestSetup_FlagSet_InstallShHandoffArgs: the exact
// arg set install.sh forwards after a --headless --force-script brew
// override parses cleanly (no --version, --force-script stripped).
func TestSetup_FlagSet_InstallShHandoffArgs(t *testing.T) {
	fs := flag.NewFlagSet("knowledge setup", flag.ContinueOnError)
	var f setupFlags
	registerSetupFlags(fs, &f)
	if err := fs.Parse([]string{"--no-self-update", "--headless"}); err != nil {
		t.Fatalf("Parse(install.sh handoff args) = %v; want nil", err)
	}
}

// TestSetup_Dispatch_FirstRunVsUpdate: absent config
// → write path runs; present config (no --reconfigure) → untouched.
func TestSetup_Dispatch_FirstRunVsUpdate(t *testing.T) {
	t.Run("absent config writes", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "sk-a") // give AutoDetect a provider
		emptyPATH(t)
		_ = spySelfUpdate(t, "")
		out := captureStdout(t, func() {
			if err := runSetup([]string{"--headless", "--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		_ = out
		if _, err := os.Stat(cfgPath); err != nil {
			t.Fatalf("config must be written on first run: %v", err)
		}
	})

	t.Run("present config untouched (update mode)", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		emptyPATH(t)
		_ = spySelfUpdate(t, "")
		seed := "schema_version = 1\n[default]\nprovider = \"openai\"\nmodel = \"gpt-4o-mini\"\n"
		seedConfig(t, cfgPath, seed)
		before, _ := os.ReadFile(cfgPath) //nolint:gosec // temp path
		_ = captureStdout(t, func() {
			if err := runSetup([]string{"--headless", "--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		after, _ := os.ReadFile(cfgPath) //nolint:gosec // temp path
		if string(before) != string(after) {
			t.Fatalf("update mode must leave config byte-identical:\nbefore=%q\nafter=%q", before, after)
		}
	})
}

// TestSetup_NonTTYForcesHeadless: with a non-TTY
// stdin and NO --headless flag, setup takes the headless path — proven
// by the env VOYAGE_API_KEY being PERSISTED (guided would prompt and,
// on EOF, keep the empty default).
func TestSetup_NonTTYForcesHeadless(t *testing.T) {
	cfgPath := setupHome(t)
	clearCredEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-a")
	t.Setenv("VOYAGE_API_KEY", "voy-headless")
	emptyPATH(t)
	forceTTY(t, false) // non-TTY
	_ = spySelfUpdate(t, "")
	_ = captureStdout(t, func() {
		if err := runSetup([]string{"--no-self-update", "--no-service"}); err != nil {
			t.Fatalf("runSetup: %v", err)
		}
	})
	creds := readCreds(t, cfgPath)
	if creds == nil || creds.VoyageAPIKey != "voy-headless" {
		t.Fatalf("non-TTY must select headless (persist env voyage key); got %+v", creds)
	}
}

// TestSetup_UpdateMode_SelfUpdate:
// WITHOUT --no-self-update the self-update fires once and the tail is
// reached (--no-service short-circuits); WITH --no-self-update it does
// NOT fire. Config byte-identical in both.
func TestSetup_UpdateMode_SelfUpdate(t *testing.T) {
	seed := "schema_version = 1\n[default]\nprovider = \"openai\"\nmodel = \"gpt-4o-mini\"\n"

	t.Run("without --no-self-update fires once", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		emptyPATH(t)
		claude := spyClaudeAssets(t)
		n := spySelfUpdate(t, "v9.9.9")
		seedConfig(t, cfgPath, seed)
		before, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
		_ = captureStdout(t, func() {
			if err := runSetup([]string{"--headless", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if *n != 1 {
			t.Fatalf("selfUpdate fired %d times; want 1", *n)
		}
		_ = claude                       // asset leg reached (no claude on PATH → no call, no error)
		after, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
		if string(before) != string(after) {
			t.Fatalf("update mode must leave config byte-identical")
		}
	})

	t.Run("with --no-self-update skips the leg", func(t *testing.T) {
		cfgPath := setupHome(t)
		clearCredEnv(t)
		emptyPATH(t)
		n := spySelfUpdate(t, "v9.9.9")
		seedConfig(t, cfgPath, seed)
		before, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
		_ = captureStdout(t, func() {
			if err := runSetup([]string{"--headless", "--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if *n != 0 {
			t.Fatalf("selfUpdate fired %d times with --no-self-update; want 0", *n)
		}
		after, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
		if string(before) != string(after) {
			t.Fatalf("config must be byte-identical")
		}
	})
}

// TestSetup_ReconfigureHeadless_Rewrites: headless
// --reconfigure re-runs the write path so the on-disk config CHANGES.
func TestSetup_ReconfigureHeadless_Rewrites(t *testing.T) {
	cfgPath := setupHome(t)
	clearCredEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-a")
	t.Setenv("VOYAGE_API_KEY", "voy-new")
	emptyPATH(t)
	_ = spySelfUpdate(t, "")
	seed := "schema_version = 1\n[default]\nprovider = \"openai\"\nmodel = \"gpt-4o-mini\"\n"
	seedConfig(t, cfgPath, seed)
	before, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
	_ = captureStdout(t, func() {
		if err := runSetup([]string{"--headless", "--reconfigure", "--no-self-update", "--no-service"}); err != nil {
			t.Fatalf("runSetup: %v", err)
		}
	})
	after, _ := os.ReadFile(cfgPath) //nolint:gosec // temp
	if string(before) == string(after) {
		t.Fatalf("--reconfigure must rewrite the config")
	}
	creds := readCreds(t, cfgPath)
	if creds == nil || creds.VoyageAPIKey != "voy-new" {
		t.Fatalf("--reconfigure must land the new voyage key; got %+v", creds)
	}
}
