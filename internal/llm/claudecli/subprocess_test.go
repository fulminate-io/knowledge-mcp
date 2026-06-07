// SPDX-License-Identifier: Apache-2.0

package claudecli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// TestRunCLI_StdoutCaptured verifies a 0-exit run returns stdout bytes
// verbatim (no truncation, no stderr conflation).
func TestRunCLI_StdoutCaptured(t *testing.T) {
	bin := writeFakeClaudeBin(t, `cat > /dev/null
echo '{"result":"ok"}'
echo "diagnostics" 1>&2`)

	out, err := runCLI(context.Background(), bin, nil, "ignored stdin", false)
	if err != nil {
		t.Fatalf("runCLI: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != `{"result":"ok"}` {
		t.Fatalf("stdout = %q, want %q", got, `{"result":"ok"}`)
	}
}

// TestRunCLI_StdinForwarded verifies stdin reaches the subprocess. The
// fake echoes its stdin back on stdout so we can assert on the round-trip.
func TestRunCLI_StdinForwarded(t *testing.T) {
	bin := writeFakeClaudeBin(t, `cat`)

	out, err := runCLI(context.Background(), bin, nil, "hello from caller", false)
	if err != nil {
		t.Fatalf("runCLI: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "hello from caller" {
		t.Fatalf("stdout = %q, want stdin echo", got)
	}
}

// TestRunCLI_ArgsForwarded verifies argv reaches the subprocess. The fake
// echoes "$@" so we can assert on the args after the binary name.
func TestRunCLI_ArgsForwarded(t *testing.T) {
	bin := writeFakeClaudeBin(t, `cat > /dev/null
echo "$@"`)

	out, err := runCLI(context.Background(), bin, []string{"-p", "--model", "haiku"}, "", false)
	if err != nil {
		t.Fatalf("runCLI: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "-p --model haiku" {
		t.Fatalf("argv echo = %q, want %q", got, "-p --model haiku")
	}
}

// TestRunCLI_NonZeroExit_TerminalDefault verifies that a non-zero exit
// with neutral stderr maps to a terminal LLMError (Reason "cli_exec").
// Every non-zero claude-CLI exit (other than the ctx-deadline path) now
// classifies terminal — see TestRunCLI_NonZeroExit_TerminalOnRateLimit for
// the formerly-transient rate-limit case.
func TestRunCLI_NonZeroExit_TerminalDefault(t *testing.T) {
	bin := writeFakeClaudeBin(t, `echo 'parse failure' 1>&2
exit 2`)

	_, err := runCLI(context.Background(), bin, nil, "", false)
	if err == nil {
		t.Fatalf("runCLI: expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "cli_exec" {
		t.Fatalf("Reason = %q, want %q", llmErr.Reason, "cli_exec")
	}
	if llmErr.Transient {
		t.Fatalf("expected terminal classification for neutral stderr")
	}
	// stderr must be in the error chain so operators can triage.
	if !strings.Contains(llmErr.Error(), "parse failure") {
		t.Fatalf("error %q must contain stderr signal", llmErr.Error())
	}
}

// TestRunCLI_NonZeroExit_TerminalOnRateLimit verifies that a rate-limit
// signal in stderr now classifies TERMINAL. CLI quota/rate-limit blowups
// carry no Retry-After, so retrying re-runs the same node forever; terminal
// classification sheds the node and the circuit breaker / human resume
// handles a quota wall.
func TestRunCLI_NonZeroExit_TerminalOnRateLimit(t *testing.T) {
	bin := writeFakeClaudeBin(t, `echo 'HTTP 429: rate limit exceeded' 1>&2
exit 1`)

	_, err := runCLI(context.Background(), bin, nil, "", false)
	if err == nil {
		t.Fatalf("runCLI: expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Transient {
		t.Fatalf("expected terminal classification for rate-limit stderr, got transient")
	}
}

// TestRunCLI_ContextCancel verifies that a cancelled ctx surfaces as a
// "cli_deadline" LLMError rather than an opaque exec failure.
func TestRunCLI_ContextCancel(t *testing.T) {
	bin := writeFakeClaudeBin(t, `sleep 5`)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := runCLI(ctx, bin, nil, "", false)
	if err == nil {
		t.Fatalf("runCLI: expected error, got nil")
	}
	var llmErr *llm.LLMError
	if !errors.As(err, &llmErr) {
		t.Fatalf("expected *llm.LLMError, got %T: %v", err, err)
	}
	if llmErr.Reason != "cli_deadline" {
		t.Fatalf("Reason = %q, want %q", llmErr.Reason, "cli_deadline")
	}
	if !llmErr.Transient {
		t.Fatalf("deadline-exceeded must be transient")
	}
}

// TestCLIErrorSignalTransient pins the always-terminal classification: every
// claude-CLI error — including the former transient signals (rate limit / 429
// / 503 / overloaded / usage limit / try again) — now classifies TERMINAL.
// CLI quota/auth blowups carry no Retry-After, so retrying re-discovers and
// re-runs the same node forever; shedding it and letting a human resume is the
// fix. The ctx-deadline path stays transient and is classified in runCLI, not
// here.
func TestCLIErrorSignalTransient(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty -> terminal", "", false},
		{"whitespace -> terminal", "   \n", false},
		{"rate limit lowercase -> terminal", "got rate limit", false},
		{"RATE LIMIT uppercase -> terminal", "GOT RATE LIMIT", false},
		{"429 substring -> terminal", "got HTTP 429", false},
		{"503 substring -> terminal", "503 service unavailable", false},
		{"overloaded -> terminal", "model is Overloaded", false},
		{"usage limit -> terminal", "usage limit reached", false},
		{"try again -> terminal", "please try again later", false},
		{"neutral parse error -> terminal", "parse failure: bad json", false},
		{"4xx not 429 -> terminal", "400 bad request", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliErrorSignalTransient(tt.in); got != tt.want {
				t.Fatalf("cliErrorSignalTransient(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestClassifyCLIExit verifies the stdout-envelope-first error extraction
// that fixes the "exit status 1 (stderr: )" no-message bug. claude CLI puts
// its JSON error envelope on stdout; classifyCLIExit must surface that
// message and classify off it, never producing an empty detail string.
func TestClassifyCLIExit(t *testing.T) {
	tests := []struct {
		name          string
		stdout        string
		stderr        string
		wantTransient bool
		wantContains  string
	}{
		{
			name:          "stdout envelope rate-limit -> terminal, message surfaced",
			stdout:        `{"type":"result","subtype":"error","is_error":true,"result":"rate limit exceeded, try again"}`,
			wantTransient: false,
			wantContains:  "rate limit exceeded",
		},
		{
			name:          "stdout envelope generic error -> terminal, message surfaced",
			stdout:        `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"invalid model"}`,
			wantTransient: false,
			wantContains:  "invalid model",
		},
		{
			name:          "empty stdout + empty stderr -> terminal, no-output sentinel",
			stdout:        "",
			stderr:        "",
			wantTransient: false,
			wantContains:  "no CLI output",
		},
		{
			name:          "non-JSON stdout surfaced verbatim",
			stdout:        "Please run `claude login`",
			wantTransient: false,
			wantContains:  "claude login",
		},
		{
			name:          "stderr fallback when stdout empty -> terminal",
			stderr:        "overloaded_error",
			wantTransient: false,
			wantContains:  "overloaded_error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transient, detail := classifyCLIExit([]byte(tt.stdout), tt.stderr)
			if transient != tt.wantTransient {
				t.Fatalf("transient = %v, want %v", transient, tt.wantTransient)
			}
			if detail == "" {
				t.Fatalf("detail must never be empty")
			}
			if !strings.Contains(detail, tt.wantContains) {
				t.Fatalf("detail %q does not contain %q", detail, tt.wantContains)
			}
		})
	}
}
