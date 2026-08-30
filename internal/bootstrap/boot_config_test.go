// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// writeBootConfigHome points HOME at a fresh temp dir and, when body is
// non-empty, writes it to <home>/.knowledge/config. Returns the home dir so a
// caller can stat the config path. It also resets the config singleton for the
// duration of the test, so each case starts unloaded.
func writeBootConfigHome(t *testing.T, body string) string {
	t.Helper()
	t.Cleanup(config.SetForTest(nil))

	home := t.TempDir()
	t.Setenv("HOME", home)
	if body == "" {
		return home
	}
	cfgDir := filepath.Join(home, ".knowledge")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return home
}

// TestLoadBootConfig_NormalServeResolvesCredentialsFromConfig is the ticket's
// named success criterion: a NORMAL (non-headless) serve resolves [credentials]
// from ~/.knowledge/config. Before the config load was promoted to daemon boot,
// a normal serve loaded config only as a side-effect of the worker runtime, so
// deleting that runtime without this would have left every non-headless daemon
// resolving keys env-only by accident.
//
// The file value and the env value are DIFFERENT strings on purpose: a fixture
// where they match cannot tell a working load from no load at all.
func TestLoadBootConfig_NormalServeResolvesCredentialsFromConfig(t *testing.T) {
	body := "[default]\nprovider = \"anthropic\"\nmodel = \"claude-opus-4-7\"\n\n" +
		"[credentials]\nvoyage_api_key = \"cfg-voyage\"\nanthropic_api_key = \"cfg-anthropic\"\n"
	writeBootConfigHome(t, body)

	// Env values that must LOSE to the file (config-first precedence).
	t.Setenv("VOYAGE_API_KEY", "env-voyage")
	t.Setenv("ANTHROPIC_API_KEY", "env-anthropic")

	loadBootConfig(Config{Headless: false, Port: 15022})

	if !config.Loaded() {
		t.Fatal("config.Loaded() = false after loadBootConfig on a normal serve with a present config file")
	}
	if got := config.VoyageAPIKey(); got != "cfg-voyage" {
		t.Errorf("VoyageAPIKey() = %q, want cfg-voyage (config must win over env)", got)
	}
}

// TestLoadBootConfig_HeadlessNeverWritesAStarter is the AXIS-DISCRIMINATING
// test, and the whole reason loadBootConfig has two arms: config.Load never
// writes, LoadOrAutoDetect auto-detects and writes a starter file when the path
// is absent. The supervisor owns config placement for an embedded daemon, so
// the headless arm must never write one.
//
// This test, and only this test, goes red if loadBootConfig is ever
// "simplified" into a single unconditional LoadOrAutoDetect — nothing else in
// the suite observes the write. It also pins the degrade-not-die posture: no
// panic, config left unloaded, credentials falling back to the env var.
func TestLoadBootConfig_HeadlessNeverWritesAStarter(t *testing.T) {
	home := writeBootConfigHome(t, "") // empty — no .knowledge/config inside
	t.Setenv("VOYAGE_API_KEY", "env-voyage")

	loadBootConfig(Config{Headless: true}) // must not panic

	if _, err := os.Stat(filepath.Join(home, ".knowledge", "config")); !os.IsNotExist(err) {
		t.Errorf("config file exists at the default path; the headless arm must never write a starter (stat err=%v)", err)
	}
	if config.Loaded() {
		t.Fatal("config.Loaded() = true after the headless arm ran against a missing file; want unloaded")
	}
	if got := config.VoyageAPIKey(); got != "env-voyage" {
		t.Errorf("VoyageAPIKey() = %q, want env-voyage fallback", got)
	}
}

// TestLoadBootConfig_NormalServeWritesAStarterWhenAbsent is the paired
// positive. Without it, the headless test above is also satisfied by a loader
// that writes nothing on ANY path — which would silently drop the auto-detect
// behaviour a normal serve has today.
func TestLoadBootConfig_NormalServeWritesAStarterWhenAbsent(t *testing.T) {
	home := writeBootConfigHome(t, "") // empty — no .knowledge/config inside

	loadBootConfig(Config{Headless: false, Port: 15022})

	if _, err := os.Stat(filepath.Join(home, ".knowledge", "config")); err != nil {
		t.Errorf("no config file at the default path; a normal serve must auto-detect and write a starter (stat err=%v)", err)
	}
}

// TestLoadBootConfig_HeadlessLoadsCredentialsOnlyFile is the config-first
// guarantee for the headless arm: a bare [credentials] template (no [default]
// provider section) loads via config.Load, and the file values WIN over the
// *_API_KEY env vars.
//
// CHARACTERIZATION GUARD, not a red-first gate: it was green before this
// ticket as TestLoadHeadlessConfig_LoadsCredentialsOnlyFile and is green after,
// re-pointed at loadBootConfig's headless arm.
func TestLoadBootConfig_HeadlessLoadsCredentialsOnlyFile(t *testing.T) {
	body := "[credentials]\nvoyage_api_key = \"cfg-voyage\"\nlinear_api_key = \"cfg-linear\"\n"
	writeBootConfigHome(t, body)

	// Env values that must LOSE to the file (config-first precedence).
	t.Setenv("VOYAGE_API_KEY", "env-voyage")
	t.Setenv("LINEAR_API_KEY", "env-linear")

	loadBootConfig(Config{Headless: true})

	if !config.Loaded() {
		t.Fatal("config.Loaded() = false after the headless arm ran against a present config file")
	}
	if got := config.VoyageAPIKey(); got != "cfg-voyage" {
		t.Errorf("VoyageAPIKey() = %q, want cfg-voyage (config must win over env)", got)
	}
	if got := config.LinearAPIKey(); got != "cfg-linear" {
		t.Errorf("LinearAPIKey() = %q, want cfg-linear (config must win over env)", got)
	}
}
