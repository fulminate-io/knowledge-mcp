// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// withHOME points os.UserHomeDir at dir for the duration of the test by
// setting HOME (POSIX) and USERPROFILE (Windows). Restored on cleanup.
func withHOME(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// TestPatchCodexToolTimeout_SetsAndPreserves: the patch lands
// mcp_servers.knowledge.tool_timeout_sec = 180 under a TMP HOME and a
// pre-existing unrelated entry/table survives the read-modify-write.
func TestPatchCodexToolTimeout_SetsAndPreserves(t *testing.T) {
	home := t.TempDir()
	withHOME(t, home)

	cfgDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	// Pre-existing content: a top-level key, an unrelated table, and an
	// unrelated mcp_servers entry — all must survive the patch.
	pre := "" +
		"model = \"o4\"\n\n" +
		"[mcp_servers.other]\n" +
		"url = \"http://127.0.0.1:9/mcp\"\n" +
		"tool_timeout_sec = 42\n\n" +
		"[some_other_table]\n" +
		"keep = true\n"
	if err := os.WriteFile(cfgPath, []byte(pre), 0o600); err != nil {
		t.Fatalf("seed config.toml: %v", err)
	}

	if err := patchCodexToolTimeout(false); err != nil {
		t.Fatalf("patchCodexToolTimeout: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read patched config.toml: %v", err)
	}
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal patched config.toml: %v", err)
	}

	servers, _ := root["mcp_servers"].(map[string]any)
	if servers == nil {
		t.Fatalf("mcp_servers table missing after patch: %v", root)
	}
	know, _ := servers["knowledge"].(map[string]any)
	if know == nil {
		t.Fatalf("mcp_servers.knowledge missing after patch: %v", servers)
	}
	if got := toInt(t, know["tool_timeout_sec"]); got != mcpToolTimeoutSec {
		t.Errorf("knowledge.tool_timeout_sec = %d, want %d", got, mcpToolTimeoutSec)
	}

	// Pre-existing entries survive.
	if got, _ := root["model"].(string); got != "o4" {
		t.Errorf("top-level model = %q, want \"o4\" (clobbered)", got)
	}
	other, _ := servers["other"].(map[string]any)
	if other == nil {
		t.Fatalf("unrelated mcp_servers.other dropped by patch: %v", servers)
	}
	if got := toInt(t, other["tool_timeout_sec"]); got != 42 {
		t.Errorf("other.tool_timeout_sec = %d, want 42 (clobbered)", got)
	}
	sot, _ := root["some_other_table"].(map[string]any)
	if sot == nil {
		t.Fatalf("unrelated [some_other_table] dropped by patch: %v", root)
	}
	if keep, _ := sot["keep"].(bool); !keep {
		t.Errorf("some_other_table.keep = %v, want true (clobbered)", sot["keep"])
	}
}

// TestPatchCodexToolTimeout_CreatesFile: with no pre-existing
// config.toml, the patch creates ~/.codex/config.toml and the table.
func TestPatchCodexToolTimeout_CreatesFile(t *testing.T) {
	home := t.TempDir()
	withHOME(t, home)

	if err := patchCodexToolTimeout(false); err != nil {
		t.Fatalf("patchCodexToolTimeout: %v", err)
	}
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config.toml not created: %v", err)
	}
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal created config.toml: %v", err)
	}
	servers, _ := root["mcp_servers"].(map[string]any)
	know, _ := servers["knowledge"].(map[string]any)
	if know == nil {
		t.Fatalf("mcp_servers.knowledge missing after create: %v", root)
	}
	if got := toInt(t, know["tool_timeout_sec"]); got != mcpToolTimeoutSec {
		t.Errorf("tool_timeout_sec = %d, want %d", got, mcpToolTimeoutSec)
	}
}

// TestPatchCodexToolTimeout_DryRunWritesNothing: dry-run prints what it
// would write but creates/modifies no file.
func TestPatchCodexToolTimeout_DryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	withHOME(t, home)

	if err := patchCodexToolTimeout(true); err != nil {
		t.Fatalf("patchCodexToolTimeout dry-run: %v", err)
	}
	cfgPath := filepath.Join(home, ".codex", "config.toml")
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("dry-run created/modified %s (stat err: %v), want no write", cfgPath, err)
	}
}

// toInt coerces a TOML-decoded numeric (int64 / int) to int for
// comparison; fails the test on any other type.
func toInt(t *testing.T, v any) int {
	t.Helper()
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	default:
		t.Fatalf("value %v (%T) is not an integer", v, v)
		return 0
	}
}
