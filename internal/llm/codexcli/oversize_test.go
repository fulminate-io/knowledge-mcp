// SPDX-License-Identifier: Apache-2.0

package codexcli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// oversize_test.go — the transport arm of the oversize-input condition. codex
// refuses an over-length prompt at turn/start, before any model call, and says
// so in ONE text line carrying a machine-readable `data:` JSON suffix. That
// suffix is the only structured form the refusal takes: the stdout --json stream
// carries no typed event for it and the JSON-RPC code is likewise text. So the
// transport parses the suffix and stamps a typed Reason, and the pipeline never
// looks at the message at all.
//
// The fixture line below is the VERBATIM shape a live codex-cli 0.153.4
// rejection produced; the numbers are the fixture's own.
const oversizeStderrLine = `Error: turn/start: turn/start failed: Input exceeds the maximum length of 1048576 characters. (code -32602), data: {"input_error_code":"input_too_large","max_chars":1048576,"actual_chars":1608836}`

// runCLIExitError drives the real subprocess path against a fake codex that
// writes the given bodies and exits non-zero, and returns the *llm.LLMError it
// produced. A nil error, or an error of another type, fails the test — there is
// no arm of this file where a non-zero exit is expected to succeed.
func runCLIExitError(t *testing.T, stdoutBody, stderrBody string) *llm.LLMError {
	t.Helper()
	script := ""
	if stdoutBody != "" {
		script += fmt.Sprintf("printf '%%s\\n' %q\n", stdoutBody)
	}
	if stderrBody != "" {
		script += fmt.Sprintf("printf '%%s\\n' %q >&2\n", stderrBody)
	}
	script += "exit 1"
	bin := writeFakeCodexBin(t, script)

	_, err := runCLI(context.Background(), bin, []string{"exec"}, "prompt body", false)
	if err == nil {
		t.Fatal("runCLI err = nil; want an error from a non-zero exit")
	}
	le, ok := errors.AsType[*llm.LLMError](err)
	if !ok {
		t.Fatalf("runCLI err type = %T; want *llm.LLMError", err)
	}
	return le
}

// TestRunCLI_OversizeRejection_InputClasses is the T11 input-class matrix. Each
// row is a shape the rejection can arrive in, and the two that MUST NOT be read
// as oversize are the point of the table: a matcher that fired on any non-zero
// exit carrying the word "data" would make every codex failure look like an
// oversize one and stop the summary axis retrying anything.
func TestRunCLI_OversizeRejection_InputClasses(t *testing.T) {
	cases := []struct {
		name       string
		stdout     string
		stderr     string
		wantReason string
		wantActual int
		wantLimit  int
	}{{
		name:       "well_formed_suffix_on_stderr",
		stderr:     oversizeStderrLine,
		wantReason: llm.ReasonInputTooLarge,
		wantActual: 1608836,
		wantLimit:  1048576,
	}, {
		name:       "suffix_is_not_valid_json",
		stderr:     `Error: turn/start failed: Input exceeds the maximum length. (code -32602), data: {input_error_code: input_too_large, max_chars: 1048576`,
		wantReason: "subprocess_failed",
	}, {
		name:       "suffix_missing_input_error_code",
		stderr:     `Error: turn/start failed: something else. (code -32602), data: {"max_chars":1048576,"actual_chars":1608836}`,
		wantReason: "subprocess_failed",
	}, {
		name:       "suffix_names_another_condition",
		stderr:     `Error: turn/start failed: bad request. (code -32602), data: {"input_error_code":"input_malformed","max_chars":1048576,"actual_chars":12}`,
		wantReason: "subprocess_failed",
	}, {
		name:       "rejection_arrives_on_stdout_as_an_error_event",
		stdout:     `{"type":"error","message":"turn/start failed: Input exceeds the maximum length of 1048576 characters. (code -32602), data: {\"input_error_code\":\"input_too_large\",\"max_chars\":1048576,\"actual_chars\":2000000}"}`,
		wantReason: llm.ReasonInputTooLarge,
		wantActual: 2000000,
		wantLimit:  1048576,
	}, {
		name:       "plain_non_zero_exit_with_neither",
		stderr:     "Error: codex: not logged in",
		wantReason: "subprocess_failed",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			le := runCLIExitError(t, tc.stdout, tc.stderr)
			if le.Reason != tc.wantReason {
				t.Fatalf("Reason = %q; want %q (stdout=%q stderr=%q cause=%v)",
					le.Reason, tc.wantReason, tc.stdout, tc.stderr, le.Cause)
			}
			if le.Transient {
				t.Errorf("Transient = true; want false — a refused request is not retried as-is on any of these rows")
			}
			if le.InputChars != tc.wantActual {
				t.Errorf("InputChars = %d; want %d", le.InputChars, tc.wantActual)
			}
			if le.MaxInputChars != tc.wantLimit {
				t.Errorf("MaxInputChars = %d; want %d", le.MaxInputChars, tc.wantLimit)
			}
			if le.Cause == nil {
				t.Error("Cause = nil; want the provider's own diagnostic preserved for the operator")
			}
		})
	}
}

// TestRunCLI_OversizeRejection_CauseCarriesTheProviderText asserts the stamped
// error still carries codex's own line verbatim, and that InputTooLargeOf — the
// typed read every consumer uses — returns the parsed numbers. The numbers are
// the ones the TEST states, not values read back out of the parser.
func TestRunCLI_OversizeRejection_CauseCarriesTheProviderText(t *testing.T) {
	le := runCLIExitError(t, "", oversizeStderrLine)

	actual, limit, ok := llm.InputTooLargeOf(le)
	if !ok {
		t.Fatalf("InputTooLargeOf(err) ok = false; want true for Reason %q", le.Reason)
	}
	if actual != 1608836 || limit != 1048576 {
		t.Errorf("InputTooLargeOf = (%d, %d); want (1608836, 1048576)", actual, limit)
	}
	// A PLAIN strings.Contains, deliberately. This is a TEST assertion about an
	// operator-facing message, not a control-flow decision, and the corpus check
	// that forbids deciding a provider condition from message text scans no test
	// files at all — its runs report test_files_scanned=0. A hand-rolled substring
	// scan here bought nothing and read as though something had required it.
	const wantFragment = "Input exceeds the maximum length of 1048576 characters"
	if got := le.Error(); !strings.Contains(got, wantFragment) {
		t.Errorf("Error() = %q; want it to preserve the provider's own line %q", got, wantFragment)
	}
}
