// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// codexInstallArgs drives the real install-codex-assets subcommand entirely
// inside dir: split roots, AGENTS.md and the codex config all under t.TempDir()
// via setBootstrapHome, and no MCP registration. NEVER point these at the
// developer's live ~/.agents or ~/.codex.
func codexInstallArgs(dir string, extra ...string) []string {
	return append([]string{
		"--no-mcp",
		"--skills-dest", filepath.Join(dir, "skills"),
		"--agents-dest", filepath.Join(dir, "agents"),
		"--agents-md-dest", filepath.Join(dir, "AGENTS.md"),
	}, extra...)
}

// codexPreToolUse decodes the config.toml at path and returns its
// hooks.PreToolUse entries.
func codexPreToolUse(t *testing.T, path string) []any {
	t.Helper()
	var root map[string]any
	if err := toml.Unmarshal(mustRead(t, path), &root); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	hooks, _ := root["hooks"].(map[string]any)
	entries, _ := hooks["PreToolUse"].([]any)
	return entries
}

// TestInstallCodexAssets_WritesSessionHook is R6's install row: the verb ALWAYS
// writes the hook (owner: "codex hook for insurance"), as a `command` handler —
// Codex 0.154.0 has no `http` handler type, so a copy of the Claude shape would
// be rejected — pointed at the daemon's Codex hook endpoint on --mcp-port.
func TestInstallCodexAssets_WritesSessionHook(t *testing.T) {
	home := t.TempDir()
	setBootstrapHome(t, home)
	const port = 20004

	out := captureStdout(t, func() {
		if err := runInstallCodexAssets(codexInstallArgs(t.TempDir(), "--mcp-port", strconv.Itoa(port))); err != nil {
			t.Errorf("install: %v", err)
		}
	})

	cfg := filepath.Join(home, ".codex", "config.toml")
	entries := codexPreToolUse(t, cfg)
	if len(entries) != 1 {
		t.Fatalf("PreToolUse has %d entries, want 1:\n%s", len(entries), mustRead(t, cfg))
	}
	entry := entries[0].(map[string]any)
	if got := entry["matcher"]; got != codexHookMatcher {
		t.Errorf("matcher = %v, want %q", got, codexHookMatcher)
	}
	handler := entry["hooks"].([]any)[0].(map[string]any)
	if got := handler["type"]; got != "command" {
		t.Errorf("handler type = %v, want \"command\" — codex has no http handler type", got)
	}
	command, _ := handler["command"].(string)
	want := "http://127.0.0.1:" + strconv.Itoa(port) + "/hook/codex"
	if !strings.Contains(command, want) {
		t.Errorf("hook command does not name %q:\n%s", want, command)
	}
	if !strings.Contains(out, "/hooks") {
		t.Errorf("installer output does not print the manual trust step:\n%s", out)
	}
}

// TestInstallCodexAssets_PrintsTheManualTrustStep is THE MANUAL-STEP ROW, on
// the write path AND on --dry-run. Codex skips an untrusted hook silently and
// no installer-side write grants trust, so this printed line is the only thing
// standing between a user and a hook that does nothing. Dropping it is the
// mutation that reds this test.
func TestInstallCodexAssets_PrintsTheManualTrustStep(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run("dry-run="+strconv.FormatBool(dryRun), func(t *testing.T) {
			home := t.TempDir()
			setBootstrapHome(t, home)
			args := codexInstallArgs(t.TempDir())
			if dryRun {
				args = append(args, "--dry-run")
			}
			out := captureStdout(t, func() {
				if err := runInstallCodexAssets(args); err != nil {
					t.Errorf("install: %v", err)
				}
			})
			for _, want := range []string{"INERT", "/hooks", "trust"} {
				if !strings.Contains(out, want) {
					t.Errorf("installer output does not name %q:\n%s", want, out)
				}
			}
			cfg := filepath.Join(home, ".codex", "config.toml")
			if _, err := os.Stat(cfg); dryRun && !os.IsNotExist(err) {
				t.Errorf("--dry-run wrote %s", cfg)
			}
		})
	}
}

// TestInstallCodexAssets_HookIsIdempotentAndPreserving: a re-run refreshes the
// managed entry in place rather than appending a second, an unrelated user
// [hooks] entry and an unrelated top-level table both survive, and a port
// change rewrites the managed entry rather than stranding the old one.
func TestInstallCodexAssets_HookIsIdempotentAndPreserving(t *testing.T) {
	home := t.TempDir()
	setBootstrapHome(t, home)
	cfg := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o750); err != nil {
		t.Fatal(err)
	}
	seed := "model = \"gpt-5\"\n\n" +
		"[[hooks.PreToolUse]]\nmatcher = \"Bash\"\n\n" +
		"[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = \"echo user\"\n"
	if err := os.WriteFile(cfg, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, port := range []int{20005, 20005, 20006} {
		captureStdout(t, func() {
			if err := runInstallCodexAssets(codexInstallArgs(t.TempDir(), "--mcp-port", strconv.Itoa(port))); err != nil {
				t.Errorf("install at port %d: %v", port, err)
			}
		})
	}

	entries := codexPreToolUse(t, cfg)
	if len(entries) != 2 {
		t.Fatalf("PreToolUse has %d entries, want 2 (the user's and exactly one managed):\n%s",
			len(entries), mustRead(t, cfg))
	}
	if got := entries[0].(map[string]any)["matcher"]; got != "Bash" {
		t.Errorf("the user's entry was not preserved first: matcher = %v", got)
	}
	body := string(mustRead(t, cfg))
	if !strings.Contains(body, "model = 'gpt-5'") && !strings.Contains(body, `model = "gpt-5"`) {
		t.Errorf("an unrelated top-level key was dropped:\n%s", body)
	}
	if strings.Contains(body, "20005") {
		t.Errorf("a re-install at a new port left the old port behind:\n%s", body)
	}
	if !strings.Contains(body, "20006") {
		t.Errorf("the managed entry was not refreshed to the current port:\n%s", body)
	}
}

// TestCheckCodexHook covers the three on-disk states the doctor can see:
// absent → warn with the install remediation; installed and current → ok,
// naming the manual trust step; hand-edited → warn with the update remediation.
// It cannot see whether the hook ever FIRED — that is the daemon's per-call
// report on manage(status), which TestAddHarnessSessionJSON_Vocabulary covers.
func TestCheckCodexHook(t *testing.T) {
	home := t.TempDir()
	setBootstrapHome(t, home)
	cfg := filepath.Join(home, ".codex", "config.toml")

	const port = 20007
	if got := checkCodexHook(port, true); got.status != statusWarn || !strings.Contains(got.detail, "install-codex-assets") {
		t.Errorf("no config.toml: status=%v detail=%q, want warn naming the install", got.status, got.detail)
	}

	captureStdout(t, func() {
		if err := runInstallCodexAssets(codexInstallArgs(t.TempDir(), "--mcp-port", strconv.Itoa(port))); err != nil {
			t.Errorf("install: %v", err)
		}
	})
	got := checkCodexHook(port, true)
	if got.status != statusOK {
		t.Errorf("installed at port %d: status=%v msg=%q, want ok", port, got.status, got.msg)
	}
	if !strings.Contains(got.detail, "/hooks") {
		t.Errorf("ok detail does not name the manual trust step: %q", got.detail)
	}
	if !strings.Contains(got.msg, strconv.Itoa(port)) {
		t.Errorf("ok msg does not name the installed port %d: %q", port, got.msg)
	}

	// THE WRONG-PORT ARM: the same in-sync hook, checked by a daemon serving a
	// DIFFERENT port. The hook posts where nothing listens, so every Codex
	// session falls back to codex-meta or none.
	const otherPort = 20098
	wrong := checkCodexHook(otherPort, true)
	if wrong.status != statusWarn {
		t.Errorf("hook at port %d checked by a daemon on %d: status=%v msg=%q, want warn",
			port, otherPort, wrong.status, wrong.msg)
	}
	for _, want := range []string{strconv.Itoa(port), strconv.Itoa(otherPort)} {
		if !strings.Contains(wrong.msg, want) {
			t.Errorf("wrong-port warning %q does not name port %s", wrong.msg, want)
		}
	}

	// THE POLL PATH: unknown served port, correct non-default install → ok.
	pollPort, pollKnown := (&client{}).mcpHTTPPort()
	if pollKnown {
		t.Fatalf("the poll path claims to know the served port (%d); this row exists because it does not", pollPort)
	}
	if poll := checkCodexHook(pollPort, pollKnown); poll.status != statusOK {
		t.Errorf("poll path on a correct install at port %d: status=%v msg=%q, want ok", port, poll.status, poll.msg)
	}

	edited := strings.Replace(string(mustRead(t, cfg)), "--max-time 2", "--max-time 99", 1)
	if err := os.WriteFile(cfg, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := checkCodexHook(port, true); got.status != statusWarn {
		t.Errorf("hand-edited hook: status=%v, want warn", got.status)
	}
}

// TestCheckCodexHook_IsReachable pins the check into the doctor's own check
// slice — defaultChecks IS THE ONLY PLACE a check becomes reachable, so a check
// written and never registered would pass its own test and run for nobody.
func TestCheckCodexHook_IsReachable(t *testing.T) {
	setBootstrapHome(t, t.TempDir())
	for _, c := range defaultChecks(1, graphclient.DefaultMCPHTTPPort, true, filepath.Join(t.TempDir(), "no-such-config")) {
		if c.name == "codex-hook" {
			return
		}
	}
	t.Error("the codex-hook check must be in defaultChecks, or it is written and never runs")
}

// TestInstallCodexAssets_RefusesToClobberAnUnexpectedHooksType is the
// warn-and-skip row. `hooks` is expected to be a table and `hooks.PreToolUse` a
// list; a comma-ok assert that discarded the mismatch would silently REPLACE
// whatever the user actually had — `hooks = "x"`, or a `[[hooks]]`
// array-of-tables — and their configuration is not ours to reinterpret.
//
// The file must come back BYTE-IDENTICAL, which is the assertion that catches a
// clobber a status code cannot: the installer reports success either way
// (patching is non-fatal by contract), so the only observable is the file.
func TestInstallCodexAssets_RefusesToClobberAnUnexpectedHooksType(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed string
	}{
		{name: "hooks is a string", seed: "model = 'gpt-5'\nhooks = \"x\"\n"},
		{name: "hooks is an array of tables", seed: "model = 'gpt-5'\n\n[[hooks]]\nname = \"mine\"\n"},
		{
			name: "hooks.PreToolUse is a string",
			seed: "model = 'gpt-5'\n\n[hooks]\nPreToolUse = \"not-a-list\"\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			setBootstrapHome(t, home)
			cfg := filepath.Join(home, ".codex", "config.toml")
			if err := os.MkdirAll(filepath.Dir(cfg), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, []byte(tc.seed), 0o600); err != nil {
				t.Fatal(err)
			}

			captureStdout(t, func() {
				if err := runInstallCodexAssets(codexInstallArgs(t.TempDir())); err != nil {
					t.Errorf("install: %v", err)
				}
			})

			if got := string(mustRead(t, cfg)); got != tc.seed {
				t.Errorf("the installer rewrote a config it did not understand:\n--before--\n%s\n--after--\n%s",
					tc.seed, got)
			}
		})
	}
}
