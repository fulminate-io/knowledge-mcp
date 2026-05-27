// SPDX-License-Identifier: Apache-2.0

package codexcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// End-to-end Generate tests. Each test boots a fake `codex` binary via the
// helpers in helpers_test.go, drives Generate, and asserts on argv, stdin,
// or response shape. Pure-parser tests live in parse_test.go.

// TestRegistered verifies the package's init() registered the factory.
// Side-effect import alone (the package compiles, init runs) should make
// llm.HasProvider return true for ProviderCodexCLI.
func TestRegistered(t *testing.T) {
	if !llm.HasProvider(llm.ProviderCodexCLI) {
		t.Fatalf("codex-cli provider not registered after import")
	}
}

// TestNewService_ResolvesOverride verifies that an explicit Config.CLIBin
// is honored — the constructor accepts a stubbed binary path without
// requiring a real `codex` install on PATH.
func TestNewService_ResolvesOverride(t *testing.T) {
	bin := writeFakeCodexBin(t, "exit 0")
	cfg := &llm.Config{
		Provider: llm.ProviderCodexCLI,
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
	if svc.Provider() != llm.ProviderCodexCLI {
		t.Fatalf("Provider() = %q, want %q", svc.Provider(), llm.ProviderCodexCLI)
	}
	if svc.cliBin != bin {
		t.Fatalf("cliBin = %q, want %q", svc.cliBin, bin)
	}
}

// TestNewService_OverrideMissing verifies that a non-existent override
// surfaces as an LLMError with Reason "cli_not_found".
func TestNewService_OverrideMissing(t *testing.T) {
	cfg := &llm.Config{
		Provider: llm.ProviderCodexCLI,
		CLIBin:   "/definitely/does/not/exist/codex-fake-binary-xyz",
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
// binary so no real `codex` install on the developer's machine influences
// the outcome.
func TestNewService_PATHFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH fallback test relies on POSIX shell")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "codex")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir)

	cfg := &llm.Config{Provider: llm.ProviderCodexCLI}
	c, err := llm.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	svc := c.(*Service)
	if svc.cliBin != stub {
		t.Fatalf("cliBin = %q, want %q", svc.cliBin, stub)
	}
}

// TestGenerate_Basic exercises the full Generate path against a fake codex
// binary that emits a canned successful-turn JSONL transcript. Verifies
// content, usage, finish reason, model, provider, and raw on the response.
func TestGenerate_Basic(t *testing.T) {
	bin, _, _ := recordingFakeCodex(t, successfulTranscript)
	svc := mustNewService(t, bin, "gpt-5-codex")

	resp, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "say hi"},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Content != "hello back" {
		t.Errorf("Content = %q, want %q", resp.Content, "hello back")
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 5 {
		t.Errorf("Usage = %+v, want input=12 output=5", resp.Usage)
	}
	if resp.FinishReason != llm.FinishReasonEndTurn {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, llm.FinishReasonEndTurn)
	}
	if resp.Model != "gpt-5-codex" {
		t.Errorf("Model = %q, want %q", resp.Model, "gpt-5-codex")
	}
	if resp.Provider != llm.ProviderCodexCLI {
		t.Errorf("Provider = %q, want %q", resp.Provider, llm.ProviderCodexCLI)
	}
	if len(resp.Raw) == 0 {
		t.Errorf("Raw is empty; expected verbatim stdout")
	}
}

// TestGenerate_PassesModelAndBaseFlags verifies that the argv codex receives
// includes the canonical exec/--json flags and the requested model.
func TestGenerate_PassesModelAndBaseFlags(t *testing.T) {
	bin, argvFile, _ := recordingFakeCodex(t, successfulTranscript)
	svc := mustNewService(t, bin, "")

	if _, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		llm.WithModel("gpt-5-codex-mini"),
	); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	args := readArgv(t, argvFile)
	wantPresent := []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
		"--ephemeral",
		"-m", "gpt-5-codex-mini",
		"-",
	}
	for _, want := range wantPresent {
		if !contains(args, want) {
			t.Errorf("argv missing %q; got %v", want, args)
		}
	}
}

// TestGenerate_ReasoningEffort verifies that ReasoningEffort lands as a
// `-c model_reasoning_effort=...` override.
func TestGenerate_ReasoningEffort(t *testing.T) {
	bin, argvFile, _ := recordingFakeCodex(t, successfulTranscript)
	svc := mustNewService(t, bin, "gpt-5-codex")

	if _, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		llm.WithReasoningEffort("high"),
	); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	args := readArgv(t, argvFile)
	wantOverride := `model_reasoning_effort="high"`
	if !contains(args, wantOverride) {
		t.Errorf("argv missing %q; got %v", wantOverride, args)
	}
	if !contains(args, "-c") {
		t.Errorf("argv missing -c flag; got %v", args)
	}
}

// TestGenerate_SystemAndUser verifies that SystemPrompt and the user
// message fold into the stdin body with explicit section labels.
func TestGenerate_SystemAndUser(t *testing.T) {
	bin, _, stdinFile := recordingFakeCodex(t, successfulTranscript)
	svc := mustNewService(t, bin, "gpt-5-codex")

	if _, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "what is 2+2?"}},
		llm.WithSystemPrompt("you are a calculator"),
	); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	body, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read stdin file: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "SYSTEM:\nyou are a calculator") {
		t.Errorf("stdin missing system block; got %q", got)
	}
	if !strings.Contains(got, "USER:\nwhat is 2+2?") {
		t.Errorf("stdin missing user block; got %q", got)
	}
}

// TestGenerate_RejectsTools verifies that non-empty Tools is rejected.
// Silent dropping would surprise callers who expect tool-use round-trip.
func TestGenerate_RejectsTools(t *testing.T) {
	bin := writeFakeCodexBin(t, "exit 0")
	svc := mustNewService(t, bin, "gpt-5-codex")

	_, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		llm.WithTools([]*schema.ToolInfo{{Name: "search", Desc: "find stuff"}}),
	)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "tools_not_supported" {
		t.Errorf("Reason = %q, want %q", llmErr.Reason, "tools_not_supported")
	}
}

// TestGenerate_RejectsMultiTurn verifies that any assistant or tool message
// in the input rejects with Reason "translate_request" wrapping a
// multiTurnError.
func TestGenerate_RejectsMultiTurn(t *testing.T) {
	bin := writeFakeCodexBin(t, "exit 0")
	svc := mustNewService(t, bin, "gpt-5-codex")

	_, err := svc.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "what is 2+2?"},
		{Role: schema.Assistant, Content: "4"},
		{Role: schema.User, Content: "and 3+3?"},
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "translate_request" {
		t.Errorf("Reason = %q, want %q", llmErr.Reason, "translate_request")
	}
	if !strings.Contains(llmErr.Cause.Error(), "single-turn") {
		t.Errorf("error message did not mention single-turn: %v", llmErr.Cause)
	}
}

// TestGenerate_EmptyMessages verifies that a zero-message request rejects
// rather than spawning a codex subprocess with no prompt body.
func TestGenerate_EmptyMessages(t *testing.T) {
	bin := writeFakeCodexBin(t, "exit 0")
	svc := mustNewService(t, bin, "gpt-5-codex")

	_, err := svc.Generate(context.Background(), nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "translate_request" {
		t.Errorf("Reason = %q, want %q", llmErr.Reason, "translate_request")
	}
}

// TestGenerate_ModelMissing verifies the no-model failure path: the
// substrate's Validate accepted the empty Config.Model (it's optional),
// so the absence has to surface at Generate time as ErrInvalidConfig.
func TestGenerate_ModelMissing(t *testing.T) {
	bin := writeFakeCodexBin(t, "exit 0")
	svc := mustNewService(t, bin, "")

	_, err := svc.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, llm.ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
}

// TestGenerate_NonZeroExit verifies that a fake codex that exits non-zero
// surfaces as LLMError("subprocess_failed"). Stderr lands in the cause
// message verbatim.
func TestGenerate_NonZeroExit(t *testing.T) {
	bin := writeFakeCodexBin(t, "echo 'bad credentials' >&2\nexit 2")
	svc := mustNewService(t, bin, "gpt-5-codex")

	_, err := svc.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "subprocess_failed" {
		t.Errorf("Reason = %q, want %q", llmErr.Reason, "subprocess_failed")
	}
	if !strings.Contains(llmErr.Cause.Error(), "bad credentials") {
		t.Errorf("error did not include stderr: %v", llmErr.Cause)
	}
}

// TestGenerate_TurnFailedEndToEnd verifies the full Generate path lifts
// codex's turn.failed JSONL event into LLMError("turn_failed"). The
// subprocess succeeds (exit 0) even though the upstream turn failed —
// that's how codex models e.g. 401 Unauthorized — so the parser is the
// layer that has to surface the failure.
func TestGenerate_TurnFailedEndToEnd(t *testing.T) {
	transcript := `{"type":"thread.started","thread_id":"x"}
{"type":"turn.started"}
{"type":"turn.failed","error":{"message":"401 Unauthorized: missing bearer"}}
`
	bin, _, _ := recordingFakeCodex(t, transcript)
	svc := mustNewService(t, bin, "gpt-5-codex")

	_, err := svc.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "turn_failed" {
		t.Errorf("Reason = %q, want %q", llmErr.Reason, "turn_failed")
	}
	if !strings.Contains(llmErr.Cause.Error(), "401") {
		t.Errorf("error did not include codex message: %v", llmErr.Cause)
	}
}

// TestGenerate_ContextCanceledIsTransient verifies that ctx cancellation
// during the subprocess surfaces as a Transient LLMError so callers
// retry on the next tick.
func TestGenerate_ContextCanceledIsTransient(t *testing.T) {
	// Fake codex that sleeps long enough to outlive the test's deadline.
	bin := writeFakeCodexBin(t, "sleep 10")
	svc := mustNewService(t, bin, "gpt-5-codex")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := svc.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !llm.IsTransient(err) {
		t.Errorf("expected transient error, got %v", err)
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "subprocess_timeout" {
		t.Errorf("Reason = %q, want %q", llmErr.Reason, "subprocess_timeout")
	}
}

// TestGenerate_OutputSchema verifies that ResponseFormat with Type=="json_schema"
// causes a tempfile to be created and --output-schema <path> to land in argv.
// The tempfile cleanup runs after Generate returns; we read the path via the
// recorded argv and assert it no longer exists.
func TestGenerate_OutputSchema(t *testing.T) {
	bin, argvFile, _ := recordingFakeCodex(t, successfulTranscript)
	svc := mustNewService(t, bin, "gpt-5-codex")

	schemaBody := map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "integer"}}}
	if _, err := svc.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		llm.WithResponseFormat(&llm.ResponseFormat{Type: "json_schema", Schema: schemaBody}),
	); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	args := readArgv(t, argvFile)
	idx := indexOf(args, "--output-schema")
	if idx == -1 {
		t.Fatalf("argv missing --output-schema; got %v", args)
	}
	if idx+1 >= len(args) {
		t.Fatalf("argv has --output-schema but no path; got %v", args)
	}
	schemaPath := args[idx+1]
	if _, err := os.Stat(schemaPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected schema tempfile %q removed after Generate, got stat err = %v", schemaPath, err)
	}
}
