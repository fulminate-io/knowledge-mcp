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
//     Every other non-zero exit → terminal cli_exec. CLI quota/auth blowups
//     carry no Retry-After and only a human can clear them, so they shed the
//     node rather than retry forever (see cliErrorSignalTransient).
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
	// cwd at startup and connects to the configured MCP servers
	// BEFORE issuing the API call. When runCLI fires from inside
	// knowledge-server with cwd = the project root (whose `.mcp.json`
	// registers the knowledge daemon's loopback HTTP MCP endpoint),
	// the CLI would open an extra connection to that daemon for a
	// summarizer/precheck turn that wants no tools at all — wasted
	// startup. The summarizer + startup precheck are non-agentic and
	// must NOT inherit project context. (Tool-use callers pass their
	// own --strict-mcp-config blob via buildMCPConfig, which points at
	// the same daemon over HTTP — see translate.go.)
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
// Classification is always TERMINAL for a non-zero exit (cliErrorSignalTransient
// returns false unconditionally): CLI quota/auth/overload failures have no
// programmatic reset hint, so retrying re-discovers and re-runs the same node
// forever. Shedding the node matches llm.IsTransient's unknown-is-terminal
// philosophy and lets the circuit breaker + human resume handle a quota wall.
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

// cliErrorSignalTransient reports whether a non-zero claude-CLI exit should
// classify transient. It ALWAYS returns false: every claude-CLI error —
// including rate-limit / usage-limit / 429 / 503 / overloaded / "try again" —
// is now terminal.
//
// The CLI authenticates via the user's local subscription login, so a
// quota/auth blowup has no Retry-After and no programmatic reset hint: the
// subprocess cannot tell us when the user's limit refills. Treating those
// signals as transient previously meant the summary/embed pipeline re-
// discovered the same node and re-ran the CLI every collector tick forever,
// burning worker time against a wall that only a human can clear. Terminal
// classification stamps a failure reason and sheds the node instead; the
// pipeline-level circuit breaker pauses the worker pool on a zero-success
// storm, and the operator clears the failure (clear_llm_failures) or resumes
// (resume_pipeline) once the quota refills. The ctx-deadline path
// (cli_deadline) stays transient and is classified separately in runCLI — it
// never reaches here.
//
// The string parameter is retained so the five call sites keep their
// (string) bool shape; the argument is now unused.
func cliErrorSignalTransient(_ string) bool {
	return false
}
