// SPDX-License-Identifier: Apache-2.0

package codexcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// runCLI shells out to the codex binary, pipes stdin in, and returns stdout
// on success. Stderr is captured and folded into the LLMError on non-zero
// exit so operators see codex's diagnostic output (auth failures, network
// errors, malformed prompts) rather than a bare exit code.
//
// Context handling: ctx is wired to exec.CommandContext so caller-imposed
// timeouts and cancellations land on the subprocess (SIGKILL on timeout,
// SIGINT on cancel). When the context is canceled while the subprocess is
// running, the returned error is classified Transient — the caller may
// retry on the next collector tick.
//
// No truncation: stdout and stderr are read in full (io.ReadAll) regardless
// of size. Large codex responses (verbose --json event streams) MUST land
// on the parser intact so it can find the trailing turn.completed event.
// See feedback_no_truncation_for_llm.
func runCLI(ctx context.Context, cliBin string, args []string, stdin string, inheritWorkdir bool) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cliBin, args...)
	// OPENAI_API_KEY is stripped from the subprocess env so the `codex`
	// CLI authenticates via the user's ChatGPT-account login, never
	// API-key billing — the codex-cli provider exists for exactly that.
	// Same hazard + fix as claudecli (see llm.ChildEnv). When cmd.Env is
	// non-nil, exec uses it verbatim instead of inheriting os.Environ().
	cmd.Env = llm.ChildEnv([]string{"OPENAI_API_KEY"})
	// Default cwd = os.TempDir() to suppress codex-cli's project-
	// config auto-detection. Same reasoning as claudecli's runCLI:
	// non-agentic single-shot calls (summarizer, startup precheck)
	// must NOT inherit project context. Dream-worker callers opt in
	// via llm.WithInheritWorkdir.
	if !inheritWorkdir {
		cmd.Dir = os.TempDir()
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if stdin != "" {
		stdinPipe, err := cmd.StdinPipe()
		if err != nil {
			return nil, &llm.LLMError{Reason: "subprocess_setup", Cause: fmt.Errorf("stdin pipe: %w", err)}
		}
		go func() {
			defer stdinPipe.Close()
			_, _ = io.WriteString(stdinPipe, stdin)
		}()
	}

	err := cmd.Run()
	if err != nil {
		// Context canceled / deadline exceeded → transient. Callers using
		// llm.IsTransient retry on the next tick.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, &llm.LLMError{
				Transient: true,
				Reason:    "subprocess_timeout",
				Cause:     fmt.Errorf("codex: %w (stderr: %s)", ctxErr, trimToLine(stderrBuf.String())),
			}
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, &llm.LLMError{
				Transient: false,
				Reason:    "subprocess_failed",
				Cause: fmt.Errorf("codex exited %d: %s",
					exitErr.ExitCode(), trimToLine(stderrBuf.String())),
			}
		}
		return nil, &llm.LLMError{
			Transient: false,
			Reason:    "subprocess_error",
			Cause:     fmt.Errorf("codex run: %w (stderr: %s)", err, trimToLine(stderrBuf.String())),
		}
	}

	return stdoutBuf.Bytes(), nil
}

// trimToLine collapses stderr into a compact single-line representation so
// the LLMError message stays log-friendly. Stderr is preserved verbatim
// in its content (no character truncation), only newlines are normalized
// to ` | `. Empty input returns an empty string so the caller's format
// string doesn't show "(stderr: )" twice.
func trimToLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.ReplaceAll(s, "\n", " | ")
}
