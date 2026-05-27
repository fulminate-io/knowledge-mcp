// SPDX-License-Identifier: Apache-2.0

package claudecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// writeFakeClaudeBin writes an executable POSIX shell script to a tempdir
// and returns its absolute path. Tests that need to exercise the subprocess
// path inject this via Config.CLIBin so the resolver returns the stub
// instead of resolving a real `claude` binary on the developer's PATH.
//
// scriptBody is appended verbatim to a `#!/bin/sh` shebang. Tests pass
// transcripts that mimic the CLI's `--output-format json` shape.
//
// 0o700 because the file MUST be executable; gosec flags >0o600 on
// os.WriteFile so we annotate the call site.
func writeFakeClaudeBin(t *testing.T, scriptBody string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake claude bin relies on POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude")
	content := "#!/bin/sh\n" + scriptBody + "\n"
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write fake claude bin: %v", err)
	}
	return path
}

// TestRegistered verifies the package's init() registered the factory.
// Side-effect import alone (the package compiles, init runs) should make
// llm.HasProvider return true for ProviderClaudeCLI.
func TestRegistered(t *testing.T) {
	if !llm.HasProvider(llm.ProviderClaudeCLI) {
		t.Fatalf("claude-cli provider not registered after import")
	}
}

// TestNewService_ResolvesOverride verifies that an explicit Config.CLIBin
// is honored — the constructor accepts a stubbed binary path without
// requiring a real `claude` install on PATH.
func TestNewService_ResolvesOverride(t *testing.T) {
	bin := writeFakeClaudeBin(t, "exit 0")
	cfg := &llm.Config{
		Provider: llm.ProviderClaudeCLI,
		CLIBin:   bin,
	}
	c, err := llm.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	svc, ok := c.(*Service)
	if !ok {
		t.Fatalf("expected *Service, got %T", c)
	}
	if svc.Provider() != llm.ProviderClaudeCLI {
		t.Fatalf("Provider() = %q, want %q", svc.Provider(), llm.ProviderClaudeCLI)
	}
	if svc.cliBin != bin {
		t.Fatalf("cliBin = %q, want %q", svc.cliBin, bin)
	}
}

// TestNewService_OverrideMissing verifies that a non-existent override
// surfaces as an LLMError with Reason "cli_not_found".
func TestNewService_OverrideMissing(t *testing.T) {
	cfg := &llm.Config{
		Provider: llm.ProviderClaudeCLI,
		CLIBin:   "/definitely/does/not/exist/claude-fake-binary-xyz",
	}
	_, err := llm.NewClient(context.Background(), cfg)
	if err == nil {
		t.Fatalf("NewClient: expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "cli_not_found" {
		t.Fatalf("Reason = %q, want %q", llmErr.Reason, "cli_not_found")
	}
	if llmErr.Transient {
		t.Fatalf("expected terminal error, got transient")
	}
}

// TestNewService_PATHFallback verifies that an empty CLIBin falls back to
// PATH resolution. We isolate PATH to a tempdir holding only our fake
// binary so no real `claude` install on the developer's machine influences
// the outcome.
func TestNewService_PATHFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH fallback test relies on POSIX shell")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "claude")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir)

	cfg := &llm.Config{Provider: llm.ProviderClaudeCLI}
	c, err := llm.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	svc := c.(*Service)
	if svc.cliBin != stub {
		t.Fatalf("cliBin = %q, want %q", svc.cliBin, stub)
	}
}
