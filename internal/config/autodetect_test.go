// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		name string
		addr net.Addr
		want bool
	}{
		{name: "tcp 127.0.0.1", addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}, want: true},
		{name: "tcp ::1", addr: &net.TCPAddr{IP: net.ParseIP("::1")}, want: true},
		{name: "tcp 0.0.0.0", addr: &net.TCPAddr{IP: net.IPv4(0, 0, 0, 0)}, want: false},
		{name: "tcp 192.168.1.1", addr: &net.TCPAddr{IP: net.IPv4(192, 168, 1, 1)}, want: false},
		{name: "tcp nil ip", addr: &net.TCPAddr{IP: nil}, want: false},
		{name: "unix", addr: &net.UnixAddr{Name: "/tmp/k.sock", Net: "unix"}, want: true},
		{name: "nil", addr: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLoopback(tc.addr); got != tc.want {
				t.Errorf("IsLoopback(%v) = %v; want %v", tc.addr, got, tc.want)
			}
		})
	}
}

// fakeDeps builds a detectDeps backed by in-memory env + PATH. Both maps
// are keyed by the lookup key (env var name / binary name); a key with a
// non-empty value means "available". An absent or empty entry means "not
// available". This keeps the tests hermetic — process state is never
// touched.
func fakeDeps(env, paths map[string]string) detectDeps {
	return detectDeps{
		getenv: func(k string) string { return env[k] },
		lookPath: func(bin string) (string, error) {
			if p, ok := paths[bin]; ok && p != "" {
				return p, nil
			}
			return "", &exec.Error{Name: bin, Err: exec.ErrNotFound}
		},
	}
}

func TestDetectLocal_Precedence(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		paths    map[string]string
		wantProv Provider
	}{
		{
			name:     "claude wins over everything",
			env:      map[string]string{"ANTHROPIC_API_KEY": "sk-a", "OPENAI_API_KEY": "sk-o", "GEMINI_API_KEY": "sk-g"},
			paths:    map[string]string{"claude": "/bin/claude", "codex": "/bin/codex"},
			wantProv: ProviderClaudeCLI,
		},
		{
			name:     "codex wins when claude absent",
			env:      map[string]string{"ANTHROPIC_API_KEY": "sk-a"},
			paths:    map[string]string{"codex": "/bin/codex"},
			wantProv: ProviderCodexCLI,
		},
		{
			name:     "anthropic wins when no CLI",
			env:      map[string]string{"ANTHROPIC_API_KEY": "sk-a", "OPENAI_API_KEY": "sk-o"},
			paths:    nil,
			wantProv: ProviderAnthropic,
		},
		{
			name:     "openai when only openai env",
			env:      map[string]string{"OPENAI_API_KEY": "sk-o"},
			paths:    nil,
			wantProv: ProviderOpenAI,
		},
		{
			name:     "gemini when only gemini env",
			env:      map[string]string{"GEMINI_API_KEY": "sk-g"},
			paths:    nil,
			wantProv: ProviderGemini,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := detectLocal(fakeDeps(tc.env, tc.paths))
			if err != nil {
				t.Fatalf("detectLocal: %v", err)
			}
			if got.Provider != tc.wantProv {
				t.Errorf("Provider = %q; want %q", got.Provider, tc.wantProv)
			}
			if got.Model != defaultModels[tc.wantProv] {
				t.Errorf("Model = %q; want %q", got.Model, defaultModels[tc.wantProv])
			}
		})
	}
}

func TestDetectServer_Precedence(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		paths    map[string]string
		wantProv Provider
	}{
		{
			name:     "anthropic preferred over openai+gemini",
			env:      map[string]string{"ANTHROPIC_API_KEY": "sk-a", "OPENAI_API_KEY": "sk-o", "GEMINI_API_KEY": "sk-g"},
			paths:    nil,
			wantProv: ProviderAnthropic,
		},
		{
			name:     "openai when no anthropic",
			env:      map[string]string{"OPENAI_API_KEY": "sk-o", "GEMINI_API_KEY": "sk-g"},
			paths:    nil,
			wantProv: ProviderOpenAI,
		},
		{
			name:     "gemini when only gemini",
			env:      map[string]string{"GEMINI_API_KEY": "sk-g"},
			paths:    nil,
			wantProv: ProviderGemini,
		},
		{
			name:     "claude on PATH ignored in server precedence",
			env:      map[string]string{"OPENAI_API_KEY": "sk-o"},
			paths:    map[string]string{"claude": "/bin/claude", "codex": "/bin/codex"},
			wantProv: ProviderOpenAI,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := detectServer(fakeDeps(tc.env, tc.paths))
			if err != nil {
				t.Fatalf("detectServer: %v", err)
			}
			if got.Provider != tc.wantProv {
				t.Errorf("Provider = %q; want %q", got.Provider, tc.wantProv)
			}
		})
	}
}

func TestDetectLocal_NoneAvailable(t *testing.T) {
	deps := fakeDeps(nil, nil)
	_, err := detectLocal(deps)
	if err == nil {
		t.Fatal("detectLocal(empty): want error, got nil")
	}
	if !strings.Contains(err.Error(), "no provider available") {
		t.Errorf("error does not mention no-provider-available: %v", err)
	}
}

func TestDetectServer_NoneAvailable(t *testing.T) {
	deps := fakeDeps(nil, nil)
	_, err := detectServer(deps)
	if err == nil {
		t.Fatal("detectServer(empty): want error, got nil")
	}
}

// TestAutoDetect_Precedence exercises the exported AutoDetect wrapper
// against real PATH/env fixtures (it uses defaultDetectDeps internally,
// so the seam is the process env, not an injected detectDeps). A
// loopback addr must take localPrecedence (claude-cli on PATH wins);
// a non-loopback addr must take serverPrecedence (CLI excluded, so the
// anthropic API key wins).
func TestAutoDetect_Precedence(t *testing.T) {
	dir := t.TempDir()
	stubClaude(t, dir)
	t.Setenv("PATH", dir)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	loopback, err := AutoDetect(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 15022})
	if err != nil {
		t.Fatalf("AutoDetect(loopback): %v", err)
	}
	if loopback.Provider != ProviderClaudeCLI {
		t.Errorf("AutoDetect(loopback).Provider = %q; want %q", loopback.Provider, ProviderClaudeCLI)
	}

	server, err := AutoDetect(&net.TCPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 15022})
	if err != nil {
		t.Fatalf("AutoDetect(non-loopback): %v", err)
	}
	if server.Provider != ProviderAnthropic {
		t.Errorf("AutoDetect(non-loopback).Provider = %q; want %q (server precedence excludes CLI)", server.Provider, ProviderAnthropic)
	}
}

// TestLoadOrAutoDetect_FirstRun runs the orchestrator end-to-end against
// a tempdir with ANTHROPIC_API_KEY set. The walk picks anthropic, the
// starter is rendered, and Validate succeeds (anthropic is API + key
// present). The singleton install happens in Load; restore it via
// SetForTest(nil) cleanup.
func TestLoadOrAutoDetect_FirstRun(t *testing.T) {
	t.Cleanup(SetForTest(nil))
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "knowledge", "config")

	// Hermetic env: only ANTHROPIC_API_KEY present.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	// Empty PATH so neither claude nor codex resolve.
	t.Setenv("PATH", t.TempDir())

	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 15022}
	cfg, wroteStarter, err := LoadOrAutoDetect(cfgPath, addr)
	if err != nil {
		t.Fatalf("LoadOrAutoDetect: %v", err)
	}
	if !wroteStarter {
		t.Errorf("wroteStarter = false; want true on first run")
	}
	if cfg == nil {
		t.Fatal("cfg is nil")
	}
	if cfg.Default.Provider != ProviderAnthropic {
		t.Errorf("Default.Provider = %q; want %q", cfg.Default.Provider, ProviderAnthropic)
	}
	if cfg.Default.Model != string(defaultModels[ProviderAnthropic]) {
		t.Errorf("Default.Model = %q; want %q", cfg.Default.Model, defaultModels[ProviderAnthropic])
	}

	// File exists and contains the detected provider.
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	if !strings.Contains(string(body), `provider = "anthropic"`) {
		t.Errorf("written config missing anthropic provider:\n%s", body)
	}

	// Singleton populated.
	if Active() != cfg {
		t.Errorf("Active() = %p; want %p (singleton not installed)", Active(), cfg)
	}
}

// TestLoadOrAutoDetect_Existing pre-writes a config and asserts that
// LoadOrAutoDetect reads it without invoking auto-detect.
func TestLoadOrAutoDetect_Existing(t *testing.T) {
	t.Cleanup(SetForTest(nil))
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")

	body := `
[default]
provider = "openai"
model = "gpt-4o-mini"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	cfg, wroteStarter, err := LoadOrAutoDetect(cfgPath, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("LoadOrAutoDetect: %v", err)
	}
	if wroteStarter {
		t.Errorf("wroteStarter = true; want false when file exists")
	}
	if cfg.Default.Provider != ProviderOpenAI {
		t.Errorf("Default.Provider = %q; want %q", cfg.Default.Provider, ProviderOpenAI)
	}
}

// TestLoadOrAutoDetect_LoopbackVsServer asserts the precedence list
// chosen by IsLoopback(bindAddr). The two subcases share env state but
// differ in bindAddr; the resulting written-file contents must reflect
// the different walks (loopback → CLI providers eligible; server →
// API-only).
func TestLoadOrAutoDetect_LoopbackVsServer(t *testing.T) {
	t.Run("loopback chooses local precedence", func(t *testing.T) {
		t.Cleanup(SetForTest(nil))
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config")

		// Stub claude on PATH AND set ANTHROPIC_API_KEY. localPrecedence
		// picks claude-cli first; the starter is written with claude-cli
		// even though anthropic is also available.
		stubClaude(t, dir)
		t.Setenv("PATH", dir)
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")
		t.Setenv("OPENAI_API_KEY", "")
		t.Setenv("GEMINI_API_KEY", "")

		_, _, err := LoadOrAutoDetect(cfgPath, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatalf("LoadOrAutoDetect: %v", err)
		}
		body, readErr := os.ReadFile(cfgPath)
		if readErr != nil {
			t.Fatalf("read written config: %v", readErr)
		}
		if !strings.Contains(string(body), `provider = "claude-cli"`) {
			t.Errorf("loopback walk did not pick claude-cli:\n%s", body)
		}
	})

	t.Run("non-loopback chooses server precedence", func(t *testing.T) {
		t.Cleanup(SetForTest(nil))
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config")

		// Same env, but bindAddr is non-loopback: claude on PATH must be
		// ignored (serverPrecedence excludes CLI providers).
		stubClaude(t, dir)
		t.Setenv("PATH", dir)
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")
		t.Setenv("OPENAI_API_KEY", "")
		t.Setenv("GEMINI_API_KEY", "")

		cfg, wroteStarter, err := LoadOrAutoDetect(cfgPath, &net.TCPAddr{IP: net.IPv4(0, 0, 0, 0)})
		if err != nil {
			t.Fatalf("LoadOrAutoDetect (server): %v", err)
		}
		if !wroteStarter {
			t.Errorf("wroteStarter = false; want true")
		}
		if cfg.Default.Provider != ProviderAnthropic {
			t.Errorf("Default.Provider = %q; want %q (server precedence skips CLI)", cfg.Default.Provider, ProviderAnthropic)
		}
		body, readErr := os.ReadFile(cfgPath)
		if readErr != nil {
			t.Fatalf("read written config: %v", readErr)
		}
		if !strings.Contains(string(body), `provider = "anthropic"`) {
			t.Errorf("server walk did not pick anthropic:\n%s", body)
		}
	})
}

// TestLoadOrAutoDetect_StatError exercises the "stat returned a non
// not-exist error" branch by pointing the path through a non-directory
// component.
func TestLoadOrAutoDetect_StatError(t *testing.T) {
	t.Cleanup(SetForTest(nil))
	dir := t.TempDir()
	notDir := filepath.Join(dir, "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	// Path through the regular file as if it were a directory.
	cfgPath := filepath.Join(notDir, "config")

	_, _, err := LoadOrAutoDetect(cfgPath, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err == nil {
		t.Fatal("LoadOrAutoDetect: want stat error, got nil")
	}
	// Should NOT be wrapped as a not-exist error — the orchestrator
	// returned the actual stat failure, not the not-exist branch.
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("error wraps os.ErrNotExist; expected a stat error: %v", err)
	}
}

// stubClaude creates an executable file named "claude" in dir.
func stubClaude(t *testing.T, dir string) {
	t.Helper()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil { //nolint:gosec // test stub, not a real executable
		t.Fatalf("stub claude: %v", err)
	}
	if err := os.Chmod(bin, 0o700); err != nil { //nolint:gosec // test stub must be executable
		t.Fatalf("chmod stub claude: %v", err)
	}
}
