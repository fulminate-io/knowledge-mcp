// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// externalseam_runner_test.go — THE RUNNER a port's CI leg calls, and the
// refusals that keep a misconfigured seam from reading as a pass. Split from
// externalseam_run_test.go, which keeps the whole-run OBSERVATIONS of the seam
// itself; this file is the invocation surface and its bad-input arms. No
// assertion moved with the split.

// --- the runner: one invocation a port's CI leg can call ---------------------

// TestExternalCollectorContract_AgainstTheDeclaredCommand IS the runner (see
// `make test-collector-contract`). With the seam variable set it drives the
// eleven against that command in a child process and reports PER TEST; with it
// unset it skips, so the ordinary suite is unaffected.
//
// IT ASSERTS THAT ELEVEN TESTS RAN, from the child's own output. A -run
// expression that matched nothing exits 0, so a filter that had gone stale would
// otherwise report a collector as conforming having run no test at all.
func TestExternalCollectorContract_AgainstTheDeclaredCommand(t *testing.T) {
	if os.Getenv(seamNestedEnv) != "" {
		t.Skip("the runner drives a child run; it does not run inside one")
	}
	command, _, set, err := externalCollector()
	require.NoError(t, err)
	if !set {
		t.Skipf("%s is unset: set it to a collector command to drive the %d provider-dialing contract tests against it",
			externalCollectorEnv, len(dialingTests))
	}
	if _, err := exec.LookPath(command); err != nil {
		t.Fatalf("the external collector command %q cannot be executed: %v", command, err)
	}
	// READ FOR THE CACHE KEY AND FOR NOTHING ELSE. Reading it is what puts it in
	// the key, which is what makes a second identical invocation of the make
	// target run rather than replay a report about a run that never happened.
	if id := os.Getenv(runIDEnv); id != "" {
		t.Logf("run id %s (read so this run is not served from the test cache)", id)
	} else {
		t.Logf("%s is unset: an identical re-run of this invocation will be served from the test cache and will dial nobody", runIDEnv)
	}

	out, _ := runChild(t, dialingRunFilter(), map[string]string{
		seamNestedEnv:        "1",
		externalCollectorEnv: os.Getenv(externalCollectorEnv),
		unemulatedModesEnv:   os.Getenv(unemulatedModesEnv),
	})
	report, err := dialingReport(out, dialingTests)
	for _, name := range dialingTests {
		t.Logf("%-8s %s", report[name], name)
	}
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// dialingReport maps each wanted test to its outcome and errors when the run did
// not account for every one of them, or when any of them failed.
func dialingReport(out string, want []string) (map[string]string, error) {
	outcomes := map[string]string{}
	for _, line := range resultLines(out) {
		if strings.HasPrefix(line, " ") {
			continue // a subtest; the top-level result is the accounting unit
		}
		parts := strings.SplitN(strings.TrimPrefix(line, "--- "), ": ", 2)
		if len(parts) != 2 {
			continue
		}
		outcomes[parts[1]] = parts[0]
	}
	report := map[string]string{}
	var missing, failed []string
	for _, name := range want {
		outcome, ran := outcomes[name]
		if !ran {
			report[name] = "NOT RUN"
			missing = append(missing, name)
			continue
		}
		report[name] = outcome
		if outcome == "FAIL" {
			failed = append(failed, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(failed)
	switch {
	case len(missing) > 0:
		return report, fmt.Errorf(
			"the run reported no result for %d of the %d provider-dialing tests (%s); a -run expression matching nothing exits 0, so a run that tested nothing must be caught here",
			len(missing), len(want), strings.Join(missing, ", "))
	case len(failed) > 0:
		return report, fmt.Errorf("the external collector failed %d of the %d provider-dialing tests: %s",
			len(failed), len(want), strings.Join(failed, ", "))
	}
	return report, nil
}

// TestExternalCollectorSeam_TheRunnerRefusesARunThatTestedNothing is the runner's
// completeness assertion, on its own: a report missing a test, and a report
// carrying a failure, are both errors that name what happened.
func TestExternalCollectorSeam_TheRunnerRefusesARunThatTestedNothing(t *testing.T) {
	var passing strings.Builder
	for _, name := range dialingTests {
		passing.WriteString("--- PASS: " + name + " (0.01s)\n")
	}
	full := passing.String()
	_, err := dialingReport(full, dialingTests)
	require.NoError(t, err, "a complete run is accepted")

	_, err = dialingReport("", dialingTests)
	require.Error(t, err, "a run that matched no test at all must not read as a pass")
	assert.Contains(t, err.Error(), dialingTests[0])

	short := strings.Replace(full, "--- PASS: "+dialingTests[3]+" (0.01s)\n", "", 1)
	_, err = dialingReport(short, dialingTests)
	require.Error(t, err, "a run missing one test must be caught by name")
	assert.Contains(t, err.Error(), dialingTests[3])

	failing := strings.Replace(full, "--- PASS: "+dialingTests[5], "--- FAIL: "+dialingTests[5], 1)
	report, err := dialingReport(failing, dialingTests)
	require.Error(t, err)
	assert.Equal(t, "FAIL", report[dialingTests[5]])
	assert.Contains(t, err.Error(), dialingTests[5])
}

// TestExternalCollectorSeam_TheRunnerRefusesACommandItCannotExecute is R2's loud
// arm: a CI leg that named a command that is not there gets a failure naming it,
// never eleven skips or a green run.
func TestExternalCollectorSeam_TheRunnerRefusesACommandItCannotExecute(t *testing.T) {
	if os.Getenv(seamNestedEnv) != "" {
		t.Skip("this test spawns a child run of the runner; it does not run inside one")
	}
	const missing = "/ful1819/definitely-not-a-collector"
	out, code := runChild(t, "^TestExternalCollectorContract_AgainstTheDeclaredCommand$",
		map[string]string{externalCollectorEnv: missing})
	assert.Equal(t, 1, code, "%s", out)
	assert.Contains(t, out, missing, "the failure must name the command that could not be executed")
	assert.Contains(t, out, "cannot be executed")
}

// TestExternalCollectorSeam_TheRunIDKeepsTheRunnerOutOfTheTestCache is the
// direction for the hazard the run id exists to close: `go test` caches, so an
// identical second invocation of the runner replays its report having dialed
// nobody. Both halves run here, because the second is meaningless without the
// first: with the run id HELD FIXED the second invocation is served from the
// cache and the external command is not dialed again, and with the run id
// varying — which is what `make test-collector-contract` does — both invocations
// dial.
//
// IT DRIVES `go test` RATHER THAN THE BINARY, because the cache belongs to the
// go command; a re-exec of this binary is never cached and would prove nothing.
func TestExternalCollectorSeam_TheRunIDKeepsTheRunnerOutOfTheTestCache(t *testing.T) {
	if os.Getenv(seamNestedEnv) != "" {
		t.Skip("this test drives `go test`, which drives this package; it does not run inside one of its own children")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("no go toolchain on PATH to drive the cache observation: %v", err)
	}
	command, logPath := writeExternalConformanceStub(t)

	run := func(runID string) string {
		t.Helper()
		cmd := exec.Command("go", "test", ".", "-run",
			"^TestExternalCollectorContract_AgainstTheDeclaredCommand$")
		cmd.Env = append(os.Environ(),
			externalCollectorEnv+"="+command,
			unemulatedModesEnv+"=",
			seamNestedEnv+"=",
			runIDEnv+"="+runID)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "the runner must pass against the external command\n%s", out)
		return string(out)
	}

	// THE CONTROL: the same run id twice. The second is served from the cache and
	// dials nothing, which is exactly the state a CI leg would be in without a
	// per-invocation value.
	run("fixed-run-id")
	before := len(dialedModes(t, logPath))
	require.Positive(t, before, "the first invocation must dial the external command")
	cached := run("fixed-run-id")
	assert.Contains(t, cached, "(cached)", "an identical invocation is served from the test cache")
	assert.Len(t, dialedModes(t, logPath), before,
		"and it dials nobody, which is the hazard: the per-test report is replayed over a run that never happened")

	// THE FIX: a run id that moves, which is what the make target sets.
	fresh := run("run-id-one")
	assert.NotContains(t, fresh, "(cached)")
	afterFirst := len(dialedModes(t, logPath))
	assert.Greater(t, afterFirst, before, "a fresh run id dials again")
	second := run("run-id-two")
	assert.NotContains(t, second, "(cached)",
		"and a SECOND invocation differing only in the run id is fresh too, which is what a re-run of the target is")
	assert.Greater(t, len(dialedModes(t, logPath)), afterFirst,
		"so every invocation of the target dials the collector rather than replaying a report about one that did not")
}

// --- bad seam input errors, never degrades ----------------------------------

// TestExternalCollectorSeam_RefusesAnUnknownUnemulatedMode is the bad-input arm:
// a skip list naming a string that is not one of the sixteen modes is a typo
// whose only quiet outcome is a suite that skipped nothing and reported a
// collector as conforming.
func TestExternalCollectorSeam_RefusesAnUnknownUnemulatedMode(t *testing.T) {
	_, err := parseUnemulatedModes("conforming, empty-graph")
	require.NoError(t, err, "the sixteen mode names parse, spaces and all")

	_, err = parseUnemulatedModes("conforming,env_report")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "env_report")
	assert.Contains(t, err.Error(), unemulatedModesEnv)

	require.Error(t, validateSeamEnvironmentFor(t, "/bin/sh", "not-a-mode"),
		"the same refusal is what TestMain runs before any test does")
	require.Error(t, validateSeamEnvironmentFor(t, "", "conforming"),
		"modes declared unemulated with no external collector name nothing at all")
	require.Error(t, validateSeamEnvironmentFor(t, "   ", ""),
		"a command variable set to whitespace names no command")
	require.NoError(t, validateSeamEnvironmentFor(t, "/bin/sh", "conforming"))
}

// validateSeamEnvironmentFor runs the TestMain-time validation over one
// configuration. An empty value means the variable is unset.
func validateSeamEnvironmentFor(t *testing.T, command, modes string) error {
	t.Helper()
	if command == "" {
		require.NoError(t, os.Unsetenv(externalCollectorEnv))
	} else {
		t.Setenv(externalCollectorEnv, command)
	}
	if modes == "" {
		require.NoError(t, os.Unsetenv(unemulatedModesEnv))
	} else {
		t.Setenv(unemulatedModesEnv, modes)
	}
	return validateSeamEnvironment()
}

// TestExternalCollectorSeam_TestMainRefusesAMisconfiguredSeamBeforeAnyTestRuns
// proves the validation is WIRED, not merely written: a child with a bad skip
// list exits non-zero having run nothing.
func TestExternalCollectorSeam_TestMainRefusesAMisconfiguredSeamBeforeAnyTestRuns(t *testing.T) {
	if os.Getenv(seamNestedEnv) != "" {
		t.Skip("this test spawns a child; it does not run inside one")
	}
	const harmless = "^TestChildEnv_EmptyBlockIsAnEmptyEnvironmentNotInheritance$"
	out, code := runChild(t, harmless, map[string]string{
		seamNestedEnv:        "1",
		externalCollectorEnv: "/bin/sh",
		unemulatedModesEnv:   "conformng",
	})
	assert.Equal(t, 8, code, "a misconfigured seam must refuse before the suite runs\n%s", out)
	assert.Contains(t, out, "conformng")
	assert.NotContains(t, out, "--- PASS:", "nothing may run under a seam that cannot mean what it says")

	out, code = runChild(t, harmless, map[string]string{
		seamNestedEnv:      "1",
		unemulatedModesEnv: stubModeConforming,
	})
	assert.Equal(t, 8, code, "%s", out)
	assert.Contains(t, out, "there is no external collector")
}
