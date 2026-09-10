// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcphost_env_baseline_test.go — the custom-collector spawn's environment
// contract, and the loud-failure observable beside it.
//
// THE CONTRACT IS THE ENTRY'S BLOCK AND NOTHING ELSE. A stdio child receives
// exactly the NAME=value pairs its config entry supplies; the daemon originates
// no environment of its own and looks nothing up. A platform baseline of six
// proxy names and two unix trust-root names, passed to every child regardless of
// its entry, was built on this branch and WITHDRAWN by the owner as an
// over-complication of a contract that has to stay simple. The test below is
// what keeps it withdrawn: it is a REGRESSION PIN, not a description of
// something the code does, and its whole value is that it goes red the day
// someone reintroduces a baseline.

// withdrawnBaselineNames are the eight names the retired baseline carried. They
// are listed here as the SUBJECT OF A NEGATIVE ASSERTION — the child must see
// none of them — so the list is deliberately literal rather than read from any
// production variable, which no longer exists.
var withdrawnBaselineNames = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
}

// TestRunMCP_StdioChildSeesOnlyTheBlocksNames pins the withdrawn state end to
// end: an entry supplying ONE variable, with all eight retired baseline names
// planted in the parent, yields a child holding that one and none of the eight.
//
// THE PLANT IS WHAT MAKES THE ABSENCES REAL. This host holds none of the eight
// naturally, so an unplanted run would report every name absent against ANY
// implementation, baseline or not. With them planted, the only way the child can
// lack them is that the spawn declined to pass them.
//
// THE BLOCK'S NAME IS THE SAME-RUN KNOWN-POSITIVE. Without it, a probe that
// reached no child at all, or one whose report was empty, would satisfy all
// eight absences.
func TestRunMCP_StdioChildSeesOnlyTheBlocksNames(t *testing.T) {
	const suppliedName = "FUL1776_PROVIDER_TOKEN"
	for _, n := range withdrawnBaselineNames {
		t.Setenv(n, "t18-planted-"+n)
	}

	reg := stdioDefEnv(t, stubModeEnvReport, map[string]string{suppliedName: "provider-token"})
	asked := append([]string{suppliedName}, withdrawnBaselineNames...)
	res, _, err := RunMCP(context.Background(), reg, map[string]any{"names": asked}, "probe", nil)
	require.NoError(t, err)
	report := envReport(t, res)

	assert.Equal(t, "true", report[suppliedName]["present"],
		"the entry's own name must arrive, or the eight absences below are the absence of a working probe")
	assert.Equal(t, "provider-token", report[suppliedName]["value"])

	for _, n := range withdrawnBaselineNames {
		assert.Equal(t, "false", report[n]["present"],
			"%s is set in the daemon's environment and the entry does not carry it, so it must NOT reach the child: the spawn adds nothing of its own", n)
	}
}

// TestStubHarness_AMarkedChildWithNoModeExitsInsteadOfRunningTheSuite is the
// harness's own guard, and it is not decoration.
//
// THE FAILURE IT PREVENTS IS UNBOUNDED. The stdio provider is this test binary
// re-execed, and its mode arrives through the entry's env block — the very
// mechanism these tests exercise. Before the marker, a child that did not
// receive the block fell through to running the WHOLE SUITE, which spawns more
// children, each doing the same: a fork bomb reachable from any change to the
// env path. It was reached once, deliberately, and took the machine to a load
// average in the hundreds within four minutes.
//
// The spawn here is DIRECT rather than through the transport, because the
// property is about the binary's own entry point.
func TestStubHarness_AMarkedChildWithNoModeExitsInsteadOfRunningTheSuite(t *testing.T) {
	self, err := os.Executable()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, stubArgvMarker) //nolint:gosec // the test binary's own path.
	cmd.Env = []string{}                                  // the block did not arrive
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	require.Error(t, runErr, "a marked child with no mode must FAIL rather than run the suite")
	var exit *exec.ExitError
	require.ErrorAs(t, runErr, &exit)
	assert.Equal(t, 9, exit.ExitCode(), "the guard's own exit code, so the cause is readable from a process table")
	assert.Contains(t, stderr.String(), stubModeEnv, "and it must name the variable that did not arrive")
	assert.NotContains(t, stderr.String(), "--- PASS", "it must not have run a single test")
}

// TestRunMCP_TransportFailureIsLoudNotAnEmptyCompleteGraph pins the distinction
// a silent transport failure would erase: a provider that could not speak must
// surface an ERROR, never a successful result with zero nodes and WalkComplete
// true, which every downstream reader would then treat as an accurate picture of
// the source.
//
// THE SAME-RUN CONTRAST is the last arm: a provider that legitimately FOUND
// nothing IS admitted, empty and complete. That is what makes the two failure
// arms evidence about failure rather than about emptiness.
//
// THE TWO FAILURE ARMS ARE DELIBERATELY THE SAME FIXTURES TestRunMCP_Provider-
// RuntimeFailures already drives, and the duplication is the point rather than
// an oversight. That test asks whether each failure is refused, with stronger
// refusal matchers, and it is the right owner of that question. THIS test asks a
// different one: whether a failure and an empty-but-honest walk are
// DISTINGUISHABLE. That question needs all three arms in ONE run, and borrowing
// two of them is cheaper and more honest than inventing near-duplicates of
// fixtures that already exist.
func TestRunMCP_TransportFailureIsLoudNotAnEmptyCompleteGraph(t *testing.T) {
	t.Run("the provider never speaks", func(t *testing.T) {
		res, _, err := RunMCP(context.Background(), stdioDef(t, stubModeExitBeforeHandshake), nil, "board", nil)
		require.Error(t, err, "a provider that cannot be reached must fail LOUD")
		assert.Contains(t, err.Error(), "handshake", "the error must name the condition")
		assert.Nil(t, res, "nothing may be admitted from a provider that never spoke — an empty result asserting completeness is the silent failure this row exists to prevent")
	})

	t.Run("the provider dies with the call in flight", func(t *testing.T) {
		res, _, err := RunMCP(context.Background(), stdioDef(t, stubModeExitMidSession), nil, "board", nil)
		require.Error(t, err, "a provider that died mid-call must fail LOUD")
		assert.Contains(t, err.Error(), "calling tool")
		assert.Nil(t, res)
	})

	t.Run("CONTRAST: a provider that legitimately found nothing is admitted", func(t *testing.T) {
		res, _, err := RunMCP(context.Background(), stdioDef(t, stubModeEmptyGraph), nil, "board", nil)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Empty(t, res.Nodes, "an empty walk from a provider that ran is a legitimate result")
	})
}
