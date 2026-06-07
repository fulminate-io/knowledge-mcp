// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphtypecrud"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// maxStdoutBytes caps how much stdout the external collector binary may emit.
// 64 MiB is large enough for a real collection's node payload (tens of
// thousands of nodes with summaries) yet bounded enough to refuse a pathological
// or runaway binary instead of buffering its output without limit. It is a var
// (not a const) only so the exec tests can shrink the cap to exercise the
// overflow path with a tiny stub; production never reassigns it.
var maxStdoutBytes = 64 << 20 // 64 MiB

// providerAPIKeyDropList names the client-side LLM provider API-key environment
// variables that are stripped from the external binary's environment. A
// registered third-party collector has no business reading the user's
// Anthropic / OpenAI / Gemini / Google credentials, so they are scrubbed before
// exec via llm.ChildEnv — the same env-scrub primitive the claude-cli / codex-cli
// providers use to keep their subprocess off paid-API billing.
var providerAPIKeyDropList = []string{
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"GEMINI_API_KEY",
	"GOOGLE_API_KEY",
}

// Run executes the registered external collector binary described by spec,
// feeding it paramsJSON over the configured transport (stdin or a named flag),
// and parses its JSON stdout into a *Result.
//
// The exit-handling STRUCTURE mirrors the claude-cli runCLI (separate
// stdout/stderr buffers, ctx.Err() precedence over the exit code, stderr
// surfaced verbatim on a non-zero exit), but DELIBERATELY does not reuse
// claude's classifyCLIExit / parseCLIErrorEnvelope: those parse claude's own
// JSON error envelope and derive a rate-limit transience classification that is
// meaningless for an arbitrary third-party binary. This path has no transience
// model and is not coupled to llm.LLMError. Every failure fails LOUD: a deadline,
// a non-zero exit (with stderr), an over-cap stdout, or malformed JSON all return
// a non-nil error and a nil *Result.
func Run(ctx context.Context, spec *knowledgev1.CollectorSpec, paramsJSON []byte) (*Result, error) {
	if spec == nil {
		return nil, fmt.Errorf("externalcollector: nil CollectorSpec")
	}

	// (1) Resolve the binary. binary_path is documented absolute; guard for it
	// explicitly so a misregistered relative path fails louder than a bare
	// PATH-lookup miss, then run it through LookPath (which also verifies
	// executability).
	binPath := spec.GetBinaryPath()
	if !filepath.IsAbs(binPath) {
		return nil, fmt.Errorf("externalcollector: collector binary_path %q must be absolute", binPath)
	}
	bin, err := exec.LookPath(binPath)
	if err != nil {
		return nil, fmt.Errorf("externalcollector: collector binary %q not executable: %w", binPath, err)
	}

	// (2) Determine the param transport. REUSE the same parser the registration
	// validator uses so the transport grammar can never drift between register
	// and run.
	kind, flagName, err := graphtypecrud.ParseParamTransport(spec.GetParamTransport())
	if err != nil {
		return nil, fmt.Errorf("externalcollector: %w", err)
	}

	// (3) Build args + decide where params go.
	var args []string
	if kind == "flag" {
		args = []string{"--" + flagName, string(paramsJSON)}
	}

	// (4) Construct the command. cwd = os.TempDir() (mirrors runCLI) so the
	// binary does not auto-detect project config from the caller's cwd. The
	// env is scrubbed of provider API keys so the plugin cannot read them.
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = llm.ChildEnv(providerAPIKeyDropList)
	cmd.Dir = os.TempDir()

	// (5) stdin transport feeds params on stdin.
	if kind == "stdin" {
		cmd.Stdin = bytes.NewReader(paramsJSON)
	}

	// (6) Capture stdout into a BOUNDED writer and stderr into a separate
	// buffer. stdout is the data channel (the JSON envelope), stderr is the
	// log channel — conflating them would corrupt the parse.
	stdout := &limitedBuffer{cap: maxStdoutBytes}
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	// (7) Run. ctx cancellation/deadline takes precedence over exit-code
	// reasoning; a non-zero exit surfaces the binary's stderr verbatim.
	runErr := cmd.Run()
	if cerr := ctx.Err(); cerr != nil {
		return nil, fmt.Errorf("externalcollector: collector %q: %w (stderr: %s)", binPath, cerr, stderr.String())
	}
	if stdout.truncated {
		return nil, fmt.Errorf("externalcollector: collector %q stdout exceeded %d bytes", binPath, maxStdoutBytes)
	}
	if runErr != nil {
		return nil, fmt.Errorf("externalcollector: collector %q failed: %w (stderr: %s)", binPath, runErr, stderr.String())
	}

	// (8) Parse stdout into a *Result. Malformed JSON fails loud, carrying a
	// bounded prefix of the offending output so the operator can triage.
	out := stdout.buf.Bytes()
	var r Result
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("externalcollector: collector %q emitted malformed JSON: %w (stdout prefix: %s)",
			binPath, err, stdoutPrefix(out))
	}
	return &r, nil
}

// stdoutPrefix returns a short, single-line-safe prefix of the binary's stdout
// for inclusion in a parse-error message. Bounded so a multi-megabyte garbage
// dump does not flood the error.
func stdoutPrefix(b []byte) string {
	const max = 256
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}

// limitedBuffer is a write-side bounded buffer: it appends to buf until buf
// reaches cap bytes, after which further writes are dropped and truncated is set.
// It mirrors the overflow-as-error semantics of the read-side limitedReader
// (collector/web/github_materializer_fetch.go) — signal the overflow as a
// distinct condition rather than silently truncating — but is genuinely net-new
// because the captured stream is write-side (cmd.Stdout is an io.Writer fed by
// the child), so a read-side limiter would require a pipe + drain goroutine.
//
// Write never returns an error (returning a short count or an error from an
// os/exec output writer can surface as an opaque exec failure that masks the
// real "binary ran away" condition); instead it records truncated=true and Run
// converts that into the explicit over-cap error AFTER the process exits.
type limitedBuffer struct {
	buf       bytes.Buffer
	cap       int
	truncated bool
}

// Write appends as many bytes as fit under cap, drops the rest, and records
// truncation. It always reports len(p) consumed so exec does not treat the cap
// as an I/O error mid-stream.
func (w *limitedBuffer) Write(p []byte) (int, error) {
	remaining := w.cap - w.buf.Len()
	if remaining <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}
