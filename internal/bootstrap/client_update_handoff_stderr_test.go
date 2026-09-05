// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE LAUNCHER IS A PROCESS TOO, and it inherits the same dead stderr the child
// does. spawn_detached_stdio_test.go guards the CHILD's survival of that stream;
// these two guard the process interposed in front of it on linux, which was
// killed by its own status line before it ever reached the exec — leaving an
// upgrade with no daemon at all and nothing to observe, because the hand-off
// Releases rather than Waits.

// TestScopeLauncher_CarriesQuietSoItWritesNothingBeforeExec names the flag
// LITERALLY, and that is the point of it existing beside the sibling test in
// client_update_restart_test.go.
//
// That sibling asserts every member of scopeLauncher[1:] reaches the argv, which
// is derived from the same variable it checks — so removing --quiet from the
// variable moves BOTH sides together and the sibling stays green. This test
// fails on exactly that removal, which is the mutation that reintroduces the
// defect: without the flag systemd-run writes "Running as unit: ..." to the
// inherited stderr before execvpe, and on a stream whose reader is gone that
// write kills the launcher.
func TestScopeLauncher_CarriesQuietSoItWritesNothingBeforeExec(t *testing.T) {
	require.Contains(t, scopeLauncher, "--quiet",
		"the launcher must be told to suppress its status line: it writes that line to the "+
			"spawning daemon's stderr BEFORE it execs the child, and that stream's reader may already be gone")

	exe := "/opt/knowledge/bin/knowledge"
	name, argv := handoffArgvFor("linux", exe, "v2.0.0", func() bool { return true })
	require.Equal(t, scopeLauncher[0], name)
	require.Contains(t, argv, "--quiet",
		"the linux hand-off argv must carry the flag, not merely the variable")

	// ORDER MATTERS AND IS ASSERTED: the flag belongs to the LAUNCHER, so it has
	// to sit before the wrapped binary. After it, it is an argument to the child.
	quietAt := slices.Index(argv, "--quiet")
	exeAt := slices.Index(argv, exe)
	require.NotEqual(t, -1, exeAt, "the wrapped child must be in the argv")
	require.Less(t, quietAt, exeAt,
		"--quiet must precede the wrapped binary, or it is an argument to the child rather than to the launcher")

	// CONTROL: the arm that uses no launcher carries no launcher flag either, so
	// the assertion above is about the launcher rather than about any argv.
	_, plain := handoffArgvFor("darwin", exe, "v2.0.0", func() bool { return true })
	require.NotContains(t, plain, "--quiet",
		"the non-linux arm spawns the child directly and must carry no launcher flag")
}

// TestLauncherProbe_FailsWhenTheLauncherWritesToADeadInheritedStderr drives the
// PRODUCTION probe builder against a stand-in launcher, through a stderr whose
// reader is gone — the field condition, and the one shape the old probe could
// not see.
//
// WHY A STAND-IN RATHER THAN systemd-run. The launcher path is linux-only and
// needs a live user service manager; this machine has neither. What is under
// test here is not systemd's behavior but the PROBE'S SUBJECT: whether the
// command the probe runs inherits the stream the real hand-off would give it. A
// stand-in that writes one line to stderr reproduces the only property that
// decides the outcome, on every platform, and the Go runtime turns a SIGPIPE on
// fd 2 into a fatal signal exactly as systemd-run's default disposition does.
// The launcher's own death under systemd is observed by the hosted CI leg.
//
// THE PAIR IS THE EVIDENCE. The writing arm must fail and the silent arm must
// pass, through the same builder and the same dead pipe in the same run: a
// failing arm alone would also be produced by a probe that always fails.
func TestLauncherProbe_FailsWhenTheLauncherWritesToADeadInheritedStderr(t *testing.T) {
	self, err := os.Executable()
	require.NoError(t, err)

	runProbe := func(t *testing.T, writes bool) error {
		t.Helper()

		// A pipe whose reader is closed immediately: the container-runtime and
		// supervisor shape the whole survival suite is written against.
		pr, pw, perr := os.Pipe()
		require.NoError(t, perr)
		require.NoError(t, pr.Close())

		// The builder reads os.Stderr when it wires the command, so pointing the
		// package variable at the dead pipe is what puts the probe's subject on
		// it. Restored immediately after, and nothing else writes there meanwhile.
		prev := os.Stderr
		os.Stderr = pw
		defer func() {
			os.Stderr = prev
			_ = pw.Close()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), scopeLauncherProbeTimeout)
		defer cancel()
		cmd := launcherProbeCommand(ctx, self, nil)
		cmd.Env = append(os.Environ(), spawnSurvivalModeEnv+"=launcher-standin")
		if writes {
			cmd.Env = append(cmd.Env, spawnSurvivalLauncherWritesEnv+"=1")
		}
		return cmd.Run()
	}

	t.Run("a launcher that writes dies on the dead stream", func(t *testing.T) {
		err := runProbe(t, true)
		require.Error(t, err,
			"the probe must report a launcher that cannot survive the stream the hand-off gives it; "+
				"a probe whose output goes to the null device reports success here and the hand-off then "+
				"wraps the child in a launcher that dies before exec'ing it")
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr,
			"the failure must be the stand-in's own exit, not a setup error in this test")
	})

	t.Run("a launcher that writes nothing survives it", func(t *testing.T) {
		// THE CONTROL, and it is what makes the arm above a reading rather than a
		// probe that fails for any reason: same builder, same dead pipe, same
		// binary, one env var different.
		require.NoError(t, runProbe(t, false),
			"a launcher that writes nothing before exec must still be reported usable through a dead stream, "+
				"or the probe has stopped distinguishing the two and every linux hand-off loses its scope")
	})
}
