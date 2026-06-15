// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// installSettingsArgs builds the runInstallClaudeAssets args that redirect
// every write off the live ~/.claude (dest, CLAUDE.md, settings.json) and
// skip MCP registration — the convention for driving the real subcommand
// entrypoint hermetically.
func installSettingsArgs(dir, settings string) []string {
	return []string{
		"--no-mcp",
		"--dest", filepath.Join(dir, "clauderoot"),
		"--claude-md-dest", filepath.Join(dir, "CLAUDE.md"),
		"--claude-settings-dest", settings,
	}
}

// TestResolveClaudeSettings asserts the resolver: empty flag → the default
// ~/.claude/settings.json under HOME; a non-empty flag is tilde-expanded.
func TestResolveClaudeSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := resolveClaudeSettings("")
	if err != nil {
		t.Fatalf("empty flag: %v", err)
	}
	if want := filepath.Join(home, ".claude", "settings.json"); got != want {
		t.Errorf("empty flag → %q, want %q", got, want)
	}

	got, err = resolveClaudeSettings("~/custom/settings.json")
	if err != nil {
		t.Fatalf("tilde flag: %v", err)
	}
	if want := filepath.Join(home, "custom", "settings.json"); got != want {
		t.Errorf("tilde flag → %q, want %q", got, want)
	}
}

// TestInstallClaudeAssets_Settings_Idempotent runs the installer twice
// against a temp settings dest and asserts the two file snapshots are
// byte-identical (the headline idempotency criterion — over repeated
// installs of the already-merged file) and that there is exactly one entry
// whose command carries the sentinel marker.
func TestInstallClaudeAssets_Settings_Idempotent(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")

	if err := runInstallClaudeAssets(installSettingsArgs(dir, settings)); err != nil {
		t.Fatalf("first install: %v", err)
	}
	first, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("read after first install: %v", err)
	}

	if err := runInstallClaudeAssets(installSettingsArgs(dir, settings)); err != nil {
		t.Fatalf("second install: %v", err)
	}
	second, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("read after second install: %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Errorf("installer not idempotent:\n--first--\n%s\n--second--\n%s", first, second)
	}
	if n := countManagedEntries(t, second); n != 1 {
		t.Errorf("managed entry count = %d, want exactly 1:\n%s", n, second)
	}
}

// TestInstallClaudeAssets_Settings_NonClobber pre-seeds a settings.json with
// user top-level keys, a SessionStart event, and a Bash PreToolUse hook
// carrying unmodeled timeout + statusMessage (the live settings.json:34
// shape), runs the installer, and asserts every user key/event/hook survives
// value-equal — INCLUDING the Bash hook's timeout + statusMessage — and the
// promote-guard managed entry is present. This proves the lossless
// RawMessage passthrough END-TO-END at the CLI seam.
func TestInstallClaudeAssets_Settings_NonClobber(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	seed := []byte(`{
  "model": "x",
  "permissions": {"allow": ["Edit(z)"], "deny": ["Write(w)"]},
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {"type": "command", "command": "echo hi", "timeout": 42, "statusMessage": "user msg"}
        ]
      }
    ],
    "SessionStart": [
      {"matcher": "", "hooks": [{"type": "command", "command": "echo start"}]}
    ]
  }
}`)
	if err := os.WriteFile(settings, seed, 0o600); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}

	if err := runInstallClaudeAssets(installSettingsArgs(dir, settings)); err != nil {
		t.Fatalf("install: %v", err)
	}
	out, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("read after install: %v", err)
	}

	// User top-level keys survive value-equal.
	var seedTop, outTop map[string]json.RawMessage
	if err := json.Unmarshal(seed, &seedTop); err != nil {
		t.Fatalf("decode seed: %v", err)
	}
	if err := json.Unmarshal(out, &outTop); err != nil {
		t.Fatalf("decode out: %v\n%s", err, out)
	}
	for _, k := range []string{"model", "permissions"} {
		if !jsonValueEqual(t, seedTop[k], outTop[k]) {
			t.Errorf("top-level key %q not value-equal:\nwant %s\ngot  %s", k, seedTop[k], outTop[k])
		}
	}

	// SessionStart event survives value-equal.
	var seedHooks, outHooks map[string]json.RawMessage
	json.Unmarshal(seedTop["hooks"], &seedHooks)
	json.Unmarshal(outTop["hooks"], &outHooks)
	if !jsonValueEqual(t, seedHooks["SessionStart"], outHooks["SessionStart"]) {
		t.Errorf("SessionStart not value-equal:\nwant %s\ngot  %s",
			seedHooks["SessionStart"], outHooks["SessionStart"])
	}

	// The user Bash PreToolUse entry survives value-equal INCLUDING the
	// unmodeled timeout + statusMessage.
	seedBash := findBashEntry(t, seed)
	outBash := findBashEntry(t, out)
	if outBash == nil {
		t.Fatalf("user Bash entry dropped end-to-end:\n%s", out)
	}
	if !jsonValueEqual(t, seedBash, outBash) {
		t.Errorf("user Bash entry not value-equal end-to-end:\nwant %s\ngot  %s", seedBash, outBash)
	}
	assertHookField(t, outBash, "timeout", float64(42))
	assertHookField(t, outBash, "statusMessage", "user msg")

	// The managed promote-guard entry is present, exactly once.
	if n := countManagedEntries(t, out); n != 1 {
		t.Errorf("managed entry count = %d, want 1:\n%s", n, out)
	}
}

// TestInstallClaudeAssets_Settings_DryRun: --dry-run against an absent
// settings.json creates no file.
func TestInstallClaudeAssets_Settings_DryRun(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	args := append(installSettingsArgs(dir, settings), "--dry-run")
	if err := runInstallClaudeAssets(args); err != nil {
		t.Fatalf("dry-run install: %v", err)
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Errorf("--dry-run created settings.json; want none")
	}
}

// TestInstallClaudeAssets_DiffSettings: --diff against a settings.json
// missing the hook prints a diff and leaves the file unchanged on disk
// (read-only). Referenced by Phase 3's --diff criterion.
func TestInstallClaudeAssets_DiffSettings(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	orig := []byte(`{"model":"opus","permissions":{"allow":["Edit(x)"]}}`)
	if err := os.WriteFile(settings, orig, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	args := append(installSettingsArgs(dir, settings), "--diff")
	if err := runInstallClaudeAssets(args); err != nil {
		t.Fatalf("--diff install: %v", err)
	}
	after, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("read after --diff: %v", err)
	}
	if !bytes.Equal(after, orig) {
		t.Errorf("--diff modified settings.json on disk:\nbefore=%s\nafter =%s", orig, after)
	}
}
