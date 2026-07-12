// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// TestHeadlessFlag_RegisteredInServeSurface asserts --headless is exposed on the
// serve flag surface (so the downstream `knowledge serve --help | grep -- --headless`
// Dockerfile smoke passes) AND that the four existing --no-* gates keep their names
// and false defaults — headless is purely additive.
func TestHeadlessFlag_RegisteredInServeSurface(t *testing.T) {
	var serveFS *flag.FlagSet
	for _, d := range DocFlagSets() {
		if d.BlockName == "flags-serve" {
			serveFS = d.FlagSet
		}
	}
	if serveFS == nil {
		t.Fatal("DocFlagSets() has no flags-serve entry")
	}
	want := map[string]string{
		"headless":               "false",
		"no-worker-runtime":      "false",
		"no-propagation-runtime": "false",
		"skip-llm-precheck":      "false",
		"no-llm-pipeline":        "false",
	}
	for name, def := range want {
		f := serveFS.Lookup(name)
		if f == nil {
			t.Errorf("serve flag surface missing --%s", name)
			continue
		}
		if f.DefValue != def {
			t.Errorf("--%s default = %q, want %q", name, f.DefValue, def)
		}
	}
}

// TestParseFlags_HeadlessParses confirms --headless binds to cfg.Headless and that
// ParseFlags does NOT expand the umbrella (applyHeadless owns that, in runServe).
func TestParseFlags_HeadlessParses(t *testing.T) {
	cfg, err := ParseFlags([]string{"--headless"})
	if err != nil {
		t.Fatalf("ParseFlags(--headless): %v", err)
	}
	if !cfg.Headless {
		t.Error("cfg.Headless = false after --headless")
	}
	if cfg.NoWorkerRuntime {
		t.Error("ParseFlags expanded the umbrella; that is applyHeadless' job, not the parser's")
	}
}

// TestApplyHeadless_ImpliesFullGateSet is the "headless implies the full set"
// guarantee: --headless must expand into all seven gate bools so an embedded
// daemon skips every background content + coordination loop.
func TestApplyHeadless_ImpliesFullGateSet(t *testing.T) {
	cfg := Config{Headless: true}
	applyHeadless(&cfg)

	checks := []struct {
		name string
		got  bool
	}{
		{"NoWorkerRuntime", cfg.NoWorkerRuntime},
		{"NoPropagationRuntime", cfg.NoPropagationRuntime},
		{"SkipLLMPrecheck", cfg.SkipLLMPrecheck},
		{"NoLLMPipeline", cfg.NoLLMPipeline},
		{"NoHiveMonitor", cfg.NoHiveMonitor},
		{"NoHiveReaper", cfg.NoHiveReaper},
		{"NoTranscriptUpload", cfg.NoTranscriptUpload},
	}
	for _, c := range checks {
		if !c.got {
			t.Errorf("applyHeadless(Headless:true): %s = false, want true", c.name)
		}
	}
}

// TestApplyHeadless_NoOpWhenDisabled confirms the flag is purely additive: with
// Headless false, applyHeadless leaves every gate bool at its zero value so a
// normal serve is unaffected.
func TestApplyHeadless_NoOpWhenDisabled(t *testing.T) {
	cfg := Config{Headless: false}
	applyHeadless(&cfg)

	if cfg.NoWorkerRuntime || cfg.NoPropagationRuntime || cfg.SkipLLMPrecheck ||
		cfg.NoLLMPipeline || cfg.NoHiveMonitor || cfg.NoHiveReaper || cfg.NoTranscriptUpload {
		t.Errorf("applyHeadless(Headless:false) set a gate bool: %+v", cfg)
	}
}

// TestLoadHeadlessConfig_LoadsCredentialsOnlyFile is the config-first guarantee:
// a bare [credentials] template (no [default] provider section) loads via
// config.Load, and the file values WIN over the *_API_KEY env vars. Proves the
// headless path resolves credentials config-first without a summarizer section —
// which config.LoadOrAutoDetect would have rejected.
func TestLoadHeadlessConfig_LoadsCredentialsOnlyFile(t *testing.T) {
	// Start unloaded and restore the prior singleton after the test.
	t.Cleanup(config.SetForTest(nil))

	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".knowledge")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "[credentials]\nvoyage_api_key = \"cfg-voyage\"\nlinear_api_key = \"cfg-linear\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Env values that must LOSE to the file (config-first precedence).
	t.Setenv("VOYAGE_API_KEY", "env-voyage")
	t.Setenv("LINEAR_API_KEY", "env-linear")

	loadHeadlessConfig()

	if !config.Loaded() {
		t.Fatal("config.Loaded() = false after loadHeadlessConfig with a present config file")
	}
	if got := config.VoyageAPIKey(); got != "cfg-voyage" {
		t.Errorf("VoyageAPIKey() = %q, want cfg-voyage (config must win over env)", got)
	}
	if got := config.LinearAPIKey(); got != "cfg-linear" {
		t.Errorf("LinearAPIKey() = %q, want cfg-linear (config must win over env)", got)
	}
}

// TestLoadHeadlessConfig_MissingFileDegrades is the degrade-not-die guarantee:
// against a non-existent config path, loadHeadlessConfig does not panic, leaves
// config unloaded, and writes NO starter file (proving config.Load, not
// LoadOrAutoDetect). Credentials then fall back to the env var via credOrEnv.
func TestLoadHeadlessConfig_MissingFileDegrades(t *testing.T) {
	t.Cleanup(config.SetForTest(nil))

	home := t.TempDir() // empty — no .knowledge/config inside
	t.Setenv("HOME", home)
	t.Setenv("VOYAGE_API_KEY", "env-voyage")

	loadHeadlessConfig() // must not panic

	if config.Loaded() {
		t.Fatal("config.Loaded() = true after loadHeadlessConfig against a missing file; want unloaded")
	}
	if _, err := os.Stat(filepath.Join(home, ".knowledge", "config")); !os.IsNotExist(err) {
		t.Errorf("config file exists at the default path; config.Load must never write a starter (stat err=%v)", err)
	}
	if got := config.VoyageAPIKey(); got != "env-voyage" {
		t.Errorf("VoyageAPIKey() = %q, want env-voyage fallback", got)
	}
}
