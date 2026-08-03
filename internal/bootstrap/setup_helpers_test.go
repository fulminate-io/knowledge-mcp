// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// --- test seams -----------------------------------------------------------

// setupHome points HOME at a fresh temp dir and returns the config path
// under it. Also resets the config singleton on cleanup.
func setupHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Cleanup(config.SetForTest(nil))
	return filepath.Join(dir, ".knowledge", "config")
}

// clearCredEnv unsets the five credential env vars so a test controls
// exactly which are visible to AutoDetect / the headless persist path.
// providerKeyEnv (provider_env_test.go) is the one list, so this and the
// suite-wide TestMain neutralization cannot drift apart.
func clearCredEnv(t *testing.T) {
	t.Helper()
	for _, k := range providerKeyEnv {
		t.Setenv(k, "")
	}
}

// forceTTY overrides the stdin-is-a-terminal seam.
func forceTTY(t *testing.T, v bool) {
	t.Helper()
	prev := stdinIsTTY
	stdinIsTTY = func() bool { return v }
	t.Cleanup(func() { stdinIsTTY = prev })
}

// spySelfUpdate replaces the self-update leg with a counter that returns
// the supplied tag and never hits the network.
func spySelfUpdate(t *testing.T, tag string) *int {
	t.Helper()
	prev := selfUpdate
	n := 0
	selfUpdate = func([]string) (string, error) { n++; return tag, nil }
	t.Cleanup(func() { selfUpdate = prev })
	return &n
}

// spyClaudeAssets replaces the claude asset installer with a spy that
// records the arg slice it was handed. Returns a pointer to the recorded
// call list.
func spyClaudeAssets(t *testing.T) *[][]string {
	t.Helper()
	prev := claudeAssetsFn
	var calls [][]string
	claudeAssetsFn = func(args []string) error { calls = append(calls, args); return nil }
	t.Cleanup(func() { claudeAssetsFn = prev })
	return &calls
}

func spyCodexAssets(t *testing.T) *[][]string {
	t.Helper()
	prev := codexAssetsFn
	var calls [][]string
	codexAssetsFn = func(args []string) error { calls = append(calls, args); return nil }
	t.Cleanup(func() { codexAssetsFn = prev })
	return &calls
}

// fakeBinsOnPATH writes exit-0 stubs named `names` into a temp dir and
// makes it the ONLY entry on PATH.
func fakeBinsOnPATH(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // test stub must be executable
			t.Fatalf("write fake %s: %v", n, err)
		}
	}
	t.Setenv("PATH", dir)
}

// emptyPATH points PATH at an empty temp dir so no CLI resolves.
func emptyPATH(t *testing.T) { t.Helper(); t.Setenv("PATH", t.TempDir()) }

// withScriptedStdin swaps os.Stdin for a temp file holding script.
func withScriptedStdin(t *testing.T, script string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin-*")
	if err != nil {
		t.Fatalf("temp stdin: %v", err)
	}
	if _, err := f.WriteString(script); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek stdin: %v", err)
	}
	prev := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = prev; _ = f.Close() })
}

func seedConfig(t *testing.T, cfgPath, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
}

func readCreds(t *testing.T, cfgPath string) *config.Credentials {
	t.Helper()
	return readConfig(t, cfgPath).Credentials
}

// readConfig reads + parses the written config, failing the test on any
// read/parse error. Proves the written file is valid TOML (the degrade
// path must still produce a parseable config).
func readConfig(t *testing.T, cfgPath string) *config.Config {
	t.Helper()
	data, err := os.ReadFile(cfgPath) //nolint:gosec // test path under temp HOME
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}
