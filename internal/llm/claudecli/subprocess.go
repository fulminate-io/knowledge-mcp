// SPDX-License-Identifier: Apache-2.0

package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// runCLI shells out to the claude binary with the supplied argv and stdin,
// returning stdout bytes (the JSON reply) on success.
//
// Behavior:
//
//   - exec.CommandContext binds the per-call ctx so a cancelled or
//     timed-out caller terminates the subprocess promptly. ctx.Err() is
//     consulted on failure so a deadline-exceeded run surfaces as a
//     transient LLMError rather than an opaque exec failure.
//   - stdout and stderr are captured into separate buffers. The CLI emits
//     its structured JSON on stdout and human-readable diagnostics on
//     stderr; conflating the two would break the parser. No truncation
//     anywhere — operators need the full diagnostic when triaging.
//   - MAX_THINKING_TOKENS=0 is appended to the inherited env so the
//     `-p` invocation does not spin a thinking pass we can't surface to
//     callers (the CLI doesn't expose thinking blocks via JSON output in
//     prompt mode). Mirrors store/summarize_cli.go.
//   - Exit-code classification: ctx-deadline → transient cli_deadline.
//     Otherwise heuristic match on stderr for rate-limit / overloaded /
//     429 / 503 → transient cli_exec; anything else terminal cli_exec.
//     Heuristic mirrors store/summarize_cli.go which has run for months.
func runCLI(ctx context.Context, bin string, args []string, stdin string, inheritWorkdir bool) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	// ANTHROPIC_API_KEY is stripped from the subprocess env so the
	// `claude` CLI always authenticates via the user's local login
	// (`claude login`), never API-key billing. The claude-cli provider
	// exists precisely to use the user's subscription; an
	// ANTHROPIC_API_KEY accidentally present in knowledge-server's env
	// (inherited from a parent shell or a launchd plist) would silently
	// route every summarization call through the paid API at API rates.
	// (Real incident, May 2026 — see feedback_default_claude_cli_for_workers.)
	cmd.Env = llm.ChildEnv([]string{"ANTHROPIC_API_KEY"}, "MAX_THINKING_TOKENS=0")
	// Default cwd = os.TempDir() to suppress claude-cli's project-
	// config auto-detection. claude-cli reads `.mcp.json` from the
	// cwd at startup and spawns the configured MCP servers as child
	// processes BEFORE issuing the API call. When runCLI fires from
	// inside knowledge-server with cwd = the project root (which has
	// `.mcp.json` → command:"knowledge"), claude-cli spawns a child
	// `knowledge` stdio client that dials back to the same server
	// we're running in — recursive trap that adds ~30s per call and
	// risks deadlocks. The summarizer + startup precheck are
	// non-agentic and must NOT inherit project context.
	//
	// Dream-worker callers pass inheritWorkdir=true (see
	// llm.WithInheritWorkdir): they're explicitly running the LLM
	// inside the project, want `.mcp.json` auto-detection, and accept
	// the cold-start cost.
	if !inheritWorkdir {
		cmd.Dir = os.TempDir()
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr == nil {
		return stdout.Bytes(), nil
	}

	// Context deadline / cancellation takes precedence over exit-code
	// reasoning — exec.Run typically returns "signal: killed" or
	// "context deadline exceeded" depending on platform; consult ctx.Err()
	// directly so the classification is deterministic.
	if cerr := ctx.Err(); cerr != nil {
		return nil, &llm.LLMError{
			Transient: errors.Is(cerr, context.DeadlineExceeded),
			Reason:    "cli_deadline",
			Cause:     fmt.Errorf("claude CLI: %w (stderr: %s)", cerr, stderr.String()),
		}
	}

	// claude CLI in `-p --output-format json` mode writes its diagnostics —
	// including the structured error envelope — to STDOUT, not stderr. On a
	// non-zero exit stderr is almost always empty; the real reason (rate
	// limit, usage cap, auth failure, schema rejection) sits in the stdout
	// JSON. Surfacing only stderr discards the message and produces the
	// useless "exit status 1 (stderr: )" we used to log. Classify + message
	// off the stdout envelope first, falling back to stderr then raw stdout.
	transient, detail := classifyCLIExit(stdout.Bytes(), stderr.String())
	return nil, &llm.LLMError{
		Transient: transient,
		Reason:    "cli_exec",
		Cause:     fmt.Errorf("claude CLI: %w (%s)", runErr, detail),
	}
}

// classifyCLIExit derives the transient/terminal classification and a
// human-readable detail string from a non-zero claude-CLI exit. It prefers
// the stdout JSON envelope (where `-p --output-format json` puts errors),
// then non-empty stderr, then raw stdout. The returned detail is never
// empty — an opaque exit with no output at all yields "(no CLI output)" so
// the operator log always carries SOMETHING actionable.
//
// Classification is signal-driven, NOT empty-default-transient: a non-zero
// exit we cannot positively identify as a rate-limit / overload is treated
// as TERMINAL. This matches llm.IsTransient's own philosophy (unknown
// failure modes are terminal so one bad node never burns infinite worker
// time) and is the fix for the empty-stderr → infinite-retry loop.
func classifyCLIExit(stdoutBytes []byte, stderrStr string) (transient bool, detail string) {
	stdout := strings.TrimSpace(string(stdoutBytes))
	stderr := strings.TrimSpace(stderrStr)

	// Envelope first: claude writes {"type":"result","is_error":...,"result":...}
	// to stdout even on failure paths.
	if msg, ok := parseCLIErrorEnvelope(stdoutBytes); ok {
		return cliErrorSignalTransient(msg), fmt.Sprintf("stdout: %s", msg)
	}
	// stdout present but not a parseable envelope (auth dialog leaked, schema
	// drift, partial write) — surface it verbatim, no truncation.
	if stdout != "" && stderr != "" {
		return cliErrorSignalTransient(stderr + " " + stdout), fmt.Sprintf("stderr: %s; stdout: %s", stderr, stdout)
	}
	if stdout != "" {
		return cliErrorSignalTransient(stdout), fmt.Sprintf("stdout: %s", stdout)
	}
	if stderr != "" {
		return cliErrorSignalTransient(stderr), fmt.Sprintf("stderr: %s", stderr)
	}
	return false, "(no CLI output)"
}

// parseCLIErrorEnvelope returns the human-readable message from a claude-CLI
// JSON result envelope on stdout, plus ok=true when stdout parsed as an
// envelope carrying an error / result / subtype. ok=false when stdout is
// empty or not a recognizable envelope (caller falls back to raw text).
func parseCLIErrorEnvelope(stdoutBytes []byte) (msg string, ok bool) {
	if len(bytes.TrimSpace(stdoutBytes)) == 0 {
		return "", false
	}
	var env cliResultEnvelope
	if err := json.Unmarshal(stdoutBytes, &env); err != nil {
		return "", false
	}
	if !env.IsError && env.Result == "" && env.Subtype == "" {
		return "", false
	}
	if m := strings.TrimSpace(env.Result); m != "" {
		return m, true
	}
	return env.Subtype, true
}

// cliErrorSignalTransient reports whether the supplied diagnostic text
// carries a positive transient signal — a rate-limit or server-overload
// marker (case-insensitive substring match).
//
// Empty / unrecognized text returns FALSE (terminal). This is the deliberate
// inversion of the prior empty-stderr → transient default, which caused an
// infinite retry loop: claude CLI writes nothing to stderr on failure (its
// JSON error envelope goes to stdout), so EVERY non-zero exit hit the empty
// branch, was classified transient, never got a failure marker, and was
// re-discovered + retried every collector tick forever. Callers that have
// the stdout envelope must pass its text here so a genuine 429/overload is
// still recognized; anything we cannot positively identify as transient is
// terminal so a single bad node never burns infinite worker time (matches
// llm.IsTransient's unknown-is-terminal philosophy).
func cliErrorSignalTransient(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "overloaded") ||
		strings.Contains(lower, "429") ||
		strings.Contains(lower, "503") ||
		strings.Contains(lower, "usage limit") ||
		strings.Contains(lower, "try again")
}
