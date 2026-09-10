// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// externalseam_run_test.go — the seam and its runner observed over WHOLE RUNS of
// the eleven, in a child process. The scoping test observes the command a
// registration carries; these observe what actually happens when the eleven are
// driven against an external command: which tests ran, which were skipped, by
// name, and with the reason printed.
//
// THE CHILD IS THIS TEST BINARY re-execed with a -run filter, which is the same
// mechanism the stub provider itself uses. seamNestedEnv marks it so a child can
// never spawn a child of its own.

// runChild runs this test binary over a -run filter and returns its combined
// output and exit code. The environment is this process's, minus the seam
// variables, plus whatever the caller sets — so a case's configuration is
// exactly what it declares.
func runChild(t *testing.T, filter string, env map[string]string) (string, int) {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)

	cmd := exec.Command(self, "-test.run", filter, "-test.v")
	base := []string{}
	for _, kv := range os.Environ() {
		name := kv
		if before, _, ok := strings.Cut(kv, "="); ok {
			name = before
		}
		if name == externalCollectorEnv || name == unemulatedModesEnv || name == seamNestedEnv {
			continue
		}
		base = append(base, kv)
	}
	for k, v := range env {
		base = append(base, k+"="+v)
	}
	cmd.Env = base
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); !ok {
			t.Fatalf("running the child: %v\n%s", err, out)
		}
		code = exitErr.ExitCode()
	}
	return string(out), code
}

// asExitError is errors.As for *exec.ExitError, kept here so the helper above
// reads as one thing.
func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

// writeExternalConformanceStub writes a shell command that IS an external
// collector as far as the seam is concerned — a different executable with a
// different argv — and that serves the sixteen modes by execing this test binary
// in its stub role.
//
// IT IS NOT A SECOND CONFORMANCE STUB, deliberately. The eleven assert the Go
// stub's exact payloads, so a hand-written second implementation would be a copy
// of it that drifts, and this ticket ships no per-language stub (each port
// supplies its own). What this proves is the SEAM: that the eleven really dial
// the command the variable names, on a real process boundary, rather than being
// redirected in name only. Every dial is recorded in a log the caller reads, so
// a run in which the seam did nothing is distinguishable from one in which it
// worked.
func writeExternalConformanceStub(t *testing.T) (command, logPath string) {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	logPath = filepath.Join(dir, "dials.log")
	command = filepath.Join(dir, "external-collector.sh")
	// The env block is the child's WHOLE environment, so the log path and the
	// binary are baked into the script rather than read from it.
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"${%s}\" >> %q\nexec %q %q \"$@\"\n",
		stubModeEnv, logPath, self, stubArgvMarker)
	require.NoError(t, os.WriteFile(command, []byte(script), 0o755)) //nolint:gosec // the seam dials this file, so the fixture needs the executable bit
	return command, logPath
}

// dialingRunFilter is the -run expression selecting exactly the eleven, DERIVED
// from the one declaration rather than written out a second time.
func dialingRunFilter() string {
	return "^(" + strings.Join(dialingTests, "|") + ")$"
}

var resultLine = regexp.MustCompile(`^(\s*)--- (PASS|FAIL|SKIP): (\S+) \(`)

// resultLines renders a `go test -v` run as its result lines with the durations
// stripped, so two runs are comparable.
func resultLines(out string) []string {
	var lines []string
	for line := range strings.SplitSeq(out, "\n") {
		if m := resultLine.FindStringSubmatch(line); m != nil {
			lines = append(lines, m[1]+"--- "+m[2]+": "+m[3])
		}
	}
	return lines
}

// dialedModes reads the modes the external stub was asked to serve.
func dialedModes(t *testing.T, logPath string) []string {
	t.Helper()
	body, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading the external collector's dial log: %v", err)
	}
	var out []string
	for line := range strings.SplitSeq(string(body), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// TestExternalCollectorSeam_DrivesTheDialingTestsAgainstAnExternalCommand is the
// seam's whole point, and the two halves are asserted in the same run: the
// dialing tests produce the SAME result lines as they do against the Go stub,
// and the external command was actually dialed by each of them.
func TestExternalCollectorSeam_DrivesTheDialingTestsAgainstAnExternalCommand(t *testing.T) {
	if os.Getenv(seamNestedEnv) != "" {
		t.Skip("this test spawns a child run of the eleven; it does not run inside one")
	}
	command, logPath := writeExternalConformanceStub(t)

	offOut, offCode := runChild(t, dialingRunFilter(), map[string]string{seamNestedEnv: "1"})
	require.Equal(t, 0, offCode, "the eleven must pass against the Go stub with the seam off\n%s", offOut)
	require.Empty(t, dialedModes(t, logPath),
		"with the seam off the external command must not be dialed at all; without this the run below proves nothing")

	onOut, onCode := runChild(t, dialingRunFilter(), map[string]string{
		seamNestedEnv:        "1",
		externalCollectorEnv: command,
	})
	assert.Equal(t, 0, onCode, "the eleven must pass against the external collector\n%s", onOut)

	assert.Equal(t, resultLines(offOut), resultLines(onOut),
		"the seam changes which provider is dialed and nothing else: every test and subtest result must be identical")

	dialed := dialedModes(t, logPath)
	assert.GreaterOrEqual(t, len(dialed), len(dialingTests),
		"each of the eleven dials the provider at least once; %d dials were recorded", len(dialed))
	known := map[string]bool{}
	for _, m := range stubModes {
		known[m] = true
	}
	for _, m := range dialed {
		assert.True(t, known[m], "the external collector was asked to serve %q, which is not a stub mode", m)
	}
	assert.Contains(t, dialed, stubModeConforming, "the conforming mode is what the happy cell dials")
}

// TestExternalCollectorSeam_SkipsAnUnemulatedModeByNameWithTheReason is R1's
// skip protocol: a mode the external collector cannot emulate ends the test that
// drives it, BY NAME, with the mode and the command printed. A silent skip and a
// pass are the same output to a reader, which is why the reason is asserted.
func TestExternalCollectorSeam_SkipsAnUnemulatedModeByNameWithTheReason(t *testing.T) {
	if os.Getenv(seamNestedEnv) != "" {
		t.Skip("this test spawns a child run of the eleven; it does not run inside one")
	}
	command, logPath := writeExternalConformanceStub(t)

	out, code := runChild(t, dialingRunFilter(), map[string]string{
		seamNestedEnv:        "1",
		externalCollectorEnv: command,
		unemulatedModesEnv:   stubModeEnvReport + "," + stubModeExitMidSession,
	})
	require.Equal(t, 0, code, "a declared-unemulated mode is a skip, never a failure\n%s", out)

	// THE FOUR ENVIRONMENT TESTS drive env-report from a top-level body, so each
	// is skipped WHOLE and by name.
	for _, name := range []string{
		"TestRunMCP_StdioEnvironmentIsTheEntrysBlock",
		"TestRunMCP_StdioNameAbsentFromTheBlockIsAbsentInTheChild",
		"TestRunMCP_StdioBlockValueBeatsTheDaemonsOwn",
		"TestRunMCP_StdioEmptyValueArrivesPresentAndEmpty",
	} {
		assert.Contains(t, resultLines(out), "--- SKIP: "+name,
			"%s drives the unemulated env-report mode and must be skipped by name", name)
	}
	assert.Contains(t, out,
		fmt.Sprintf("stub mode %q is not emulated by the external collector %q", stubModeEnvReport, command),
		"the skip must print the mode AND the command; a bare skip tells a reader nothing about what went unproven")
	assert.Contains(t, out,
		fmt.Sprintf("stub mode %q is not emulated by the external collector %q", stubModeExitMidSession, command),
		"every declared mode's skip carries its own reason")

	assert.NotContains(t, dialedModes(t, logPath), stubModeEnvReport,
		"a skipped mode must never be dialed; a skip that still ran the provider would be a report about nothing")
	assert.Contains(t, resultLines(out), "--- PASS: TestRunMCP_ConformingProvider_BothTransports",
		"the tests driving emulated modes still run")
}
