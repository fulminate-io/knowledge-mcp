// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"strings"
	"testing"
)

// --- Step e: conditional claude/codex asset install -----------------------

// TestSetup_Assets_ConditionalOnPATH: neither CLI on PATH → no installer
// runs, no error; a fake claude on PATH → the claude installer runs; a
// fake codex on PATH → the codex installer runs.
func TestSetup_Assets_ConditionalOnPATH(t *testing.T) {
	baseArgs := []string{"--headless", "--no-self-update", "--no-service"}

	t.Run("neither present: no installer, no error", func(t *testing.T) {
		_ = setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "ant-env")
		emptyPATH(t)
		_ = spySelfUpdate(t, "")
		claude := spyClaudeAssets(t)
		codex := spyCodexAssets(t)
		_ = captureStdout(t, func() {
			if err := runSetup(baseArgs); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if len(*claude) != 0 || len(*codex) != 0 {
			t.Fatalf("no installer should run when neither CLI is present; claude=%v codex=%v", *claude, *codex)
		}
	})

	t.Run("claude present: claude installer runs", func(t *testing.T) {
		_ = setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "ant-env")
		fakeBinsOnPATH(t, "claude")
		_ = spySelfUpdate(t, "")
		claude := spyClaudeAssets(t)
		codex := spyCodexAssets(t)
		_ = captureStdout(t, func() {
			if err := runSetup(baseArgs); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if len(*claude) != 1 {
			t.Fatalf("claude installer must run once; got %d calls", len(*claude))
		}
		if len(*codex) != 0 {
			t.Fatalf("codex installer must NOT run (no codex on PATH); got %d", len(*codex))
		}
	})

	t.Run("codex present: codex installer runs", func(t *testing.T) {
		_ = setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "ant-env")
		fakeBinsOnPATH(t, "codex")
		_ = spySelfUpdate(t, "")
		claude := spyClaudeAssets(t)
		codex := spyCodexAssets(t)
		_ = captureStdout(t, func() {
			if err := runSetup(baseArgs); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if len(*codex) != 1 {
			t.Fatalf("codex installer must run once; got %d calls", len(*codex))
		}
		if len(*claude) != 0 {
			t.Fatalf("claude installer must NOT run (no claude on PATH); got %d", len(*claude))
		}
	})
}

// TestSetup_Assets_CuratedArgs: setup forwards ONLY
// installer-understood flags — ["--no-mcp"] when --no-mcp is set, empty
// otherwise — never its raw args. The real installer accepts ["--no-mcp"]
// (no "flag provided but not defined").
func TestSetup_Assets_CuratedArgs(t *testing.T) {
	t.Run("no-mcp forwarded", func(t *testing.T) {
		_ = setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "ant-env")
		fakeBinsOnPATH(t, "claude")
		_ = spySelfUpdate(t, "")
		claude := spyClaudeAssets(t)
		_ = captureStdout(t, func() {
			if err := runSetup([]string{"--headless", "--no-self-update", "--no-service", "--no-mcp"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if len(*claude) != 1 || len((*claude)[0]) != 1 || (*claude)[0][0] != "--no-mcp" {
			t.Fatalf("expected forwarded args [--no-mcp]; got %v", *claude)
		}
	})

	t.Run("no flags forwarded → empty slice", func(t *testing.T) {
		_ = setupHome(t)
		clearCredEnv(t)
		t.Setenv("ANTHROPIC_API_KEY", "ant-env")
		fakeBinsOnPATH(t, "claude")
		_ = spySelfUpdate(t, "")
		claude := spyClaudeAssets(t)
		_ = captureStdout(t, func() {
			if err := runSetup([]string{"--headless", "--no-self-update", "--no-service"}); err != nil {
				t.Fatalf("runSetup: %v", err)
			}
		})
		if len(*claude) != 1 || len((*claude)[0]) != 0 {
			t.Fatalf("expected empty forwarded args; got %v", *claude)
		}
	})

	t.Run("real installer accepts [--no-mcp] with a fake claude", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		fakeBinsOnPATH(t, "claude")
		if err := runInstallClaudeAssets([]string{"--no-mcp"}); err != nil {
			t.Fatalf("runInstallClaudeAssets([--no-mcp]) = %v; want nil (no flag error)", err)
		}
	})
}

// TestSetup_OSSHygiene: setup's user-facing strings reference no private
// host, no ticket identifiers, and no telemetry. Guards the OSS-shipped
// surface.
func TestSetup_OSSHygiene(t *testing.T) {
	forbidden := []string{"dev.fulminate.io", "fulminate.io/", "FUL-", "linear.app", "internal-only"}
	for _, name := range []string{"setup.go", "setup_prompts.go", "setup_service.go"} {
		data, err := os.ReadFile(name) //nolint:gosec // reading own package source
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, bad := range forbidden {
			if strings.Contains(string(data), bad) {
				t.Errorf("%s contains OSS-forbidden token %q", name, bad)
			}
		}
	}
}
