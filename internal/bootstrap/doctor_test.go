// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// writeConfig writes body to a temp config file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// stubExecutable creates an executable file named bin in dir and returns
// its absolute path. POSIX-only (matches the config-package idiom).
func stubExecutable(t *testing.T, dir, bin string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("executable-bit stubbing is POSIX-only")
	}
	path := filepath.Join(dir, bin)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // test stub must be executable
		t.Fatalf("stub %s: %v", path, err)
	}
	return path
}

// resetConfigSingleton clears the active config so a test's config.Load
// doesn't leak into the next test through the package singleton.
func resetConfigSingleton(t *testing.T) {
	t.Helper()
	t.Cleanup(config.SetForTest(nil))
}

// --- (a) checkServer failure-count surfacing via interpretServerStatus ---

func TestInterpretServerStatus_FailuresWarn(t *testing.T) {
	status := map[string]any{
		"pid": float64(123), "nodes": float64(10), "edges": float64(20),
		"summary_failed": float64(5), "embed_failed": float64(0),
	}
	res := interpretServerStatus(15022, status)
	if res.status != statusWarn {
		t.Fatalf("status = %v, want statusWarn", res.status)
	}
	if !strings.Contains(res.msg, "5") {
		t.Errorf("msg %q should contain the failure count 5", res.msg)
	}
	if !strings.Contains(res.detail, "clear_llm_failures") {
		t.Errorf("detail %q should name clear_llm_failures", res.detail)
	}
}

func TestInterpretServerStatus_ZeroFailuresOK(t *testing.T) {
	status := map[string]any{
		"pid": float64(123), "nodes": float64(10), "edges": float64(20),
		"summary_failed": float64(0), "embed_failed": float64(0),
	}
	res := interpretServerStatus(15022, status)
	if res.status != statusOK {
		t.Fatalf("status = %v, want statusOK", res.status)
	}
	if res.detail != "" {
		t.Errorf("OK line should have no detail, got %q", res.detail)
	}
}

// --- (b) checkVoyage config-credential path ---

func TestCheckVoyage_ConfigCredentialEnablesVector(t *testing.T) {
	resetConfigSingleton(t)
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	path := writeConfig(t, `
[default]
provider = "anthropic"
model = "claude-haiku-5"

[credentials]
voyage_api_key = "pa-from-config"
`)
	res := checkVoyage(path)
	if res.status != statusOK {
		t.Fatalf("status = %v, want statusOK (config-only key should enable vector)", res.status)
	}
}

func TestCheckVoyage_NeitherSetBM25(t *testing.T) {
	resetConfigSingleton(t)
	t.Setenv("VOYAGE_API_KEY", "")
	path := writeConfig(t, `
[default]
provider = "anthropic"
model = "claude-haiku-5"
`)
	res := checkVoyage(path)
	if res.status != statusInfo {
		t.Fatalf("status = %v, want statusInfo (BM25-only)", res.status)
	}
	if !strings.Contains(res.msg, "BM25-only") {
		t.Errorf("msg %q should mention BM25-only mode", res.msg)
	}
}

// --- (c) checkConfig dream coverage ---

func TestCheckConfig_DreamInvalidErrs(t *testing.T) {
	resetConfigSingleton(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	// summarizer is valid (anthropic); dream points at a CLI provider with
	// no cli_bin → must error and mention the dream consumer.
	path := writeConfig(t, `
[default]
provider = "anthropic"
model = "claude-haiku-5"

[dream]
provider = "claude-cli"
model = "claude-haiku-5"
`)
	res := checkConfig(path)
	if res.status != statusErr {
		t.Fatalf("status = %v, want statusErr (dream cli_bin missing)", res.status)
	}
	if !strings.Contains(res.detail, "dream") {
		t.Errorf("detail %q should mention the dream consumer", res.detail)
	}
}

func TestCheckConfig_BothValidNamesBoth(t *testing.T) {
	resetConfigSingleton(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	path := writeConfig(t, `
[default]
provider = "anthropic"
model = "claude-haiku-5"

[dream]
provider = "openai"
model = "gpt-5-mini"
`)
	res := checkConfig(path)
	if res.status != statusOK {
		t.Fatalf("status = %v, want statusOK; detail=%q", res.status, res.detail)
	}
	if !strings.Contains(res.msg, "summarizer=") || !strings.Contains(res.msg, "dream=") {
		t.Errorf("msg %q should name both summarizer and dream", res.msg)
	}
}

// --- (d) checkConsumerCLIs provider relabel ---

func TestCheckConsumerCLIs_ProviderLabeledRows(t *testing.T) {
	dir := t.TempDir()
	codexBin := stubExecutable(t, dir, "codex")
	claudeBin := stubExecutable(t, dir, "claude")
	resetConfigSingleton(t)
	path := writeConfig(t, `
[default]
provider = "anthropic"
model = "claude-haiku-5"

[summarizer]
provider = "codex-cli"
model = "gpt-5-codex"
cli_bin = "`+codexBin+`"

[dream]
provider = "claude-cli"
model = "claude-haiku-5"
cli_bin = "`+claudeBin+`"
`)
	rows := checkConsumerCLIs(path)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	var sawCodex, sawClaude bool
	for _, r := range rows {
		if r.status != statusOK {
			t.Errorf("row %q status = %v, want statusOK", r.name, r.status)
		}
		if r.name == "claude-cli" {
			t.Errorf("row name %q is the hardcoded literal — must be provider-labeled", r.name)
		}
		if strings.Contains(r.name, "codex-cli") {
			sawCodex = true
		}
		if strings.Contains(r.name, "claude-cli") {
			sawClaude = true
		}
	}
	if !sawCodex {
		t.Error("expected a row labeled with codex-cli")
	}
	if !sawClaude {
		t.Error("expected a row labeled with claude-cli")
	}
}

func TestCheckConsumerCLIs_MissingBinErrs(t *testing.T) {
	resetConfigSingleton(t)
	path := writeConfig(t, `
[default]
provider = "anthropic"
model = "claude-haiku-5"

[summarizer]
provider = "claude-cli"
model = "claude-haiku-5"
cli_bin = "/nonexistent/path/to/claude"
`)
	rows := checkConsumerCLIs(path)
	var summarizerErr bool
	for _, r := range rows {
		if strings.Contains(r.name, "summarizer") && r.status == statusErr {
			summarizerErr = true
		}
	}
	if !summarizerErr {
		t.Fatal("summarizer row should be statusErr for a nonexistent cli_bin")
	}
}

// --- (e) #6 install-vs-running branch ---

func TestCheckServer_BinaryAbsentSuggestsInstall(t *testing.T) {
	tmp := t.TempDir()
	withStubExecutable(t, filepath.Join(tmp, "stdio_stub"))
	withPATH(t, tmp) // no knowledge-server sibling, none on PATH
	res := checkServer(pickFreePort(t))
	if res.status != statusInfo {
		t.Fatalf("status = %v, want statusInfo (server down)", res.status)
	}
	if !strings.Contains(res.detail, "knowledge install") {
		t.Errorf("detail %q should name `knowledge install` when binary absent", res.detail)
	}
}

func TestCheckServer_BinaryPresentSuggestsStart(t *testing.T) {
	tmp := t.TempDir()
	stubExecutable(t, tmp, serverBinaryName)
	withStubExecutable(t, filepath.Join(tmp, "stdio_stub"))
	withPATH(t, tmp)
	res := checkServer(pickFreePort(t))
	if res.status != statusInfo {
		t.Fatalf("status = %v, want statusInfo (server down)", res.status)
	}
	if !strings.Contains(res.detail, "knowledge start") {
		t.Errorf("detail %q should name `knowledge start` when binary present", res.detail)
	}
	if strings.Contains(res.detail, "knowledge install") {
		t.Errorf("detail %q should NOT suggest install when binary present", res.detail)
	}
}

// --- (f) --deep opt-in ---

func TestRunDoctor_DefaultNoDeepNoPing(t *testing.T) {
	resetConfigSingleton(t)
	// A config whose API provider would fail a reachability ping (missing
	// key). The default run must NOT fire checkProvidersDeep, so the run
	// stays clean (no statusErr from a provider ping). We assert runDoctor
	// returns without error on the default path — it never reaches the
	// provider ping because --deep is absent.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	path := writeConfig(t, `
[default]
provider = "anthropic"
model = "claude-haiku-5"
`)
	// runDoctor writes to stdout and calls os.Exit only on errCount>0;
	// the default path here produces no err-status check, so it returns nil.
	if err := runDoctor([]string{"--config-file", path, "--port", "1"}); err != nil {
		t.Fatalf("runDoctor default path: %v", err)
	}
}

func TestCheckProvidersDeep_RunAllErrorMapsErr(t *testing.T) {
	resetConfigSingleton(t)
	// API provider with no key → precheck.RunAll surfaces a resolve/ping
	// error → checkProvidersDeep maps it to statusErr.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	path := writeConfig(t, `
[default]
provider = "anthropic"
model = "claude-haiku-5"
`)
	res := checkProvidersDeep(path)
	if res.status != statusErr {
		t.Fatalf("status = %v, want statusErr (missing key)", res.status)
	}
}
