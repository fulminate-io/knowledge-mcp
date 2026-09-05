// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE RESTART VERB MUST NOT BE ABLE TO WRITE A DIRECTORY IT WAS NOT GIVEN.
//
// MEASURED, AND THIS IS WHY THE FILE EXISTS: when the verb resolved its log
// directory from the ambient $HOME, one run of this package's suite appended 505
// lines to the OPERATOR'S live ~/.knowledge/knowledge-daemon.log, including
// records of upgrades that never happened — because the verb's success path is
// driven by a test's own known-positive control, and setupLogging then routes
// every later record in the binary through slog.SetDefault into that file. On
// the commit before, the same run appended nothing.
//
// TWO THINGS MAKE IT UNREACHABLE NOW, and both are asserted here: the directory
// is a PARAMETER, so a caller cannot arrive at the live path by omission, and an
// unnamed directory is an ERROR rather than a fallback.

// stubRestartWork replaces the unit install and the port-binding restart, which
// is everything about the verb except the part under test.
func stubRestartWork(t *testing.T) {
	t.Helper()
	prev := installServiceUnitsAndRestartFn
	installServiceUnitsAndRestartFn = func(string) error { return nil }
	t.Cleanup(func() { installServiceUnitsAndRestartFn = prev })
}

// TestRestartDaemonVerb_LogsOnlyUnderTheDirectoryItIsGiven is the containment
// assertion.
func TestRestartDaemonVerb_LogsOnlyUnderTheDirectoryItIsGiven(t *testing.T) {
	knowledgeDir(t) // HOME is isolated, so "the ambient path" below is a temp one
	withRestoredDefaultLogger(t)
	stubRestartWork(t)

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	ambient := daemonLogPath(filepath.Join(home, ".knowledge"))
	given := t.TempDir()

	require.NoError(t, runRestartDaemon([]string{"--" + restartTargetVersionFlag, "v9.9.9"}, given))

	// IT WROTE WHERE IT WAS TOLD.
	wrote := filepath.Join(given, daemonLogFileName)
	require.FileExists(t, wrote)
	body, err := os.ReadFile(wrote) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	require.Contains(t, string(body), "restart handoff completed",
		"the outcome must be recorded in the directory the caller named")

	// AND NOWHERE ELSE. The ambient $HOME path is where the verb used to resolve
	// its sink from; nothing may have created it.
	require.NoFileExists(t, ambient,
		"the verb must not write a directory it was not given — this is the path that reached the operator's live store")

	// POSITIVE CONTROL, same run, same assertion, same instrument. Pointed AT the
	// ambient directory the verb writes there, so the NoFileExists above means
	// "did not write" rather than "this check could never fire".
	require.NoError(t, runRestartDaemon([]string{"--" + restartTargetVersionFlag, "v9.9.9"}, filepath.Join(home, ".knowledge")))
	require.FileExists(t, ambient,
		"control: given that directory the verb DOES write it, so the absence asserted above is a reading")
}

// TestRestartDaemonVerb_RefusesAnUnnamedLogDirectory is the by-omission half.
//
// An empty directory must be an ERROR. If it fell back to the ambient home
// instead, every caller that simply forgot to pass one — a test, a new
// dispatch site — would silently reach the operator's store again, which is the
// exact shape of the defect this replaced.
func TestRestartDaemonVerb_RefusesAnUnnamedLogDirectory(t *testing.T) {
	knowledgeDir(t)
	withRestoredDefaultLogger(t)

	var reached int
	prev := installServiceUnitsAndRestartFn
	installServiceUnitsAndRestartFn = func(string) error { reached++; return nil }
	t.Cleanup(func() { installServiceUnitsAndRestartFn = prev })

	err := runRestartDaemon([]string{"--" + restartTargetVersionFlag, "v9.9.9"}, "")
	require.Error(t, err, "an unnamed log directory must be refused, never defaulted to the ambient home")
	require.Contains(t, err.Error(), "graph storage directory")
	require.Zero(t, reached, "the restart must not run when the outcome has nowhere to be recorded")

	home, herr := os.UserHomeDir()
	require.NoError(t, herr)
	require.NoFileExists(t, daemonLogPath(filepath.Join(home, ".knowledge")),
		"a refused call must not have created the ambient sink on its way out")
}

// TestHandOffRestart_RefusesBeforeForkingWhenTheChildCouldNotRecordIt covers
// the two arms on the PARENT side, and the point of both is WHERE the failure is
// reported.
//
// Once the child is running, this process is about to be stopped by it. A child
// that finds it cannot open the durable log has only the inherited stderr to say
// so on — the stream that may already be dead — so the upgrade would fail with
// the reason reaching nobody. Checked before the fork, the refusal happens while
// a live daemon with a working log still holds the error.
func TestHandOffRestart_RefusesBeforeForkingWhenTheChildCouldNotRecordIt(t *testing.T) {
	stubSpawn := func(t *testing.T, spawned *int) {
		t.Helper()
		prev, prevPath := daemonExecCommand, installedClientPath
		daemonExecCommand = func(string, ...string) *exec.Cmd { *spawned++; return exec.Command("true") }
		installedClientPath = func() (string, error) { return "/nonexistent/knowledge", nil }
		t.Cleanup(func() { daemonExecCommand, installedClientPath = prev, prevPath })
	}

	t.Run("the durable sink cannot be opened", func(t *testing.T) {
		knowledgeDir(t)
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		// A DIRECTORY standing where the log FILE must go: it satisfies a stat and
		// fails the open, which is why the probe opens rather than stats.
		require.NoError(t, os.MkdirAll(daemonLogPath(filepath.Join(home, ".knowledge")), 0o750))

		spawned := 0
		stubSpawn(t, &spawned)

		err = (&client{}).handOffRestart("v9.9.9")
		require.Error(t, err, "an unopenable durable sink must refuse the handoff")
		require.Contains(t, err.Error(), "could not record",
			"the error must say what the child would have been unable to do; got %v", err)
		require.Zero(t, spawned, "nothing may be forked once the child is known to have nowhere to record the outcome")
	})

	t.Run("the graph storage directory cannot be resolved", func(t *testing.T) {
		t.Setenv("HOME", "")
		spawned := 0
		stubSpawn(t, &spawned)

		err := (&client{}).handOffRestart("v9.9.9")
		require.Error(t, err, "an unresolvable graph storage directory must refuse the handoff")
		require.Zero(t, spawned, "nothing may be forked when the child's log location is unknown")
	})

	t.Run("control: a writable sink forks", func(t *testing.T) {
		knowledgeDir(t)
		spawned := 0
		stubSpawn(t, &spawned)

		require.NoError(t, (&client{}).handOffRestart("v9.9.9"))
		require.Equal(t, 1, spawned,
			"control: with a writable sink the handoff DOES fork, so the refusals above are properties of the sink rather than of a handoff that never forks")
	})
}

// TestClientLoggingEntryPointsWriteOnlyWhereTheyAreTold covers the CENSUS rather
// than one verb.
//
// THE CENSUS, by ast over cmd/knowledge and cmd/frontend non-test sources:
// setupLogging has exactly TWO call sites, daemon.go's runServe and
// client_update_restart.go's runRestartDaemon; debug.SetCrashOutput has exactly
// ONE, inside openDurableLogSink, which only setupLogging reaches. Those are
// every client path that installs a logger or crash output, and each is driven
// here under an isolated HOME.
func TestClientLoggingEntryPointsWriteOnlyWhereTheyAreTold(t *testing.T) {
	t.Run("serve with no --log-file installs no ambient sink", func(t *testing.T) {
		knowledgeDir(t)
		withRestoredDefaultLogger(t)
		home, err := os.UserHomeDir()
		require.NoError(t, err)

		// runServe's default: the flag is registered empty, so a daemon started
		// without one has no durable sink it could put in the wrong place.
		require.Nil(t, setupLogging(&Config{}, new(slog.LevelVar)))
		require.NoFileExists(t, daemonLogPath(filepath.Join(home, ".knowledge")),
			"a daemon with no --log-file must not invent one under the operator's store")
	})

	t.Run("serve with an explicit --log-file writes only there", func(t *testing.T) {
		knowledgeDir(t)
		withRestoredDefaultLogger(t)
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		given := filepath.Join(t.TempDir(), "explicit.log")

		require.NotNil(t, setupLogging(&Config{LogFile: given}, new(slog.LevelVar)))
		slog.Info("a record from the configured daemon")

		require.FileExists(t, given)
		require.NoFileExists(t, daemonLogPath(filepath.Join(home, ".knowledge")),
			"the operator's store is not a second destination")
	})

	t.Run("restart-daemon through the real dispatch writes only under its HOME", func(t *testing.T) {
		knowledgeDir(t)
		withRestoredDefaultLogger(t)
		stubRestartWork(t)
		home, err := os.UserHomeDir()
		require.NoError(t, err)

		prevArgs := os.Args
		os.Args = []string{"knowledge", restartDaemonVerb, "--" + restartTargetVersionFlag, "v9.9.9"}
		handled, code := RunSubcommand()
		os.Args = prevArgs

		require.True(t, handled)
		require.Equal(t, 0, code)
		// The dispatch resolves the operator's directory, which under this test IS
		// the isolated one. That is exactly what isolating it is for.
		require.FileExists(t, daemonLogPath(filepath.Join(home, ".knowledge")))
	})
}

// TestRestartDaemonVerb_RefusesWhenTheDurableSinkCannotBeOpened covers the arm
// that makes the durable record a precondition rather than a nicety.
//
// THE POINT OF THIS PROCESS'S LOGGING is that an unattended upgrade's outcome
// survives a stderr that may already be dead. Continuing without the file would
// let the restart succeed or fail with the record reaching nobody, which is the
// defect this whole change exists to close, one level up.
func TestRestartDaemonVerb_RefusesWhenTheDurableSinkCannotBeOpened(t *testing.T) {
	knowledgeDir(t)
	withRestoredDefaultLogger(t)

	var reached int
	prev := installServiceUnitsAndRestartFn
	installServiceUnitsAndRestartFn = func(string) error { reached++; return nil }
	t.Cleanup(func() { installServiceUnitsAndRestartFn = prev })

	// A DIRECTORY where the log FILE must go: the open cannot succeed, on any
	// platform, without a race.
	storage := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(storage, daemonLogFileName), 0o750))

	err := runRestartDaemon([]string{"--" + restartTargetVersionFlag, "v9.9.9"}, storage)
	require.Error(t, err, "an unopenable durable sink must fail the verb")
	require.Contains(t, err.Error(), "recorded nowhere",
		"the error must say what was lost, not just that a file did not open; got %v", err)
	require.Zero(t, reached, "the restart must not run when its outcome cannot be recorded")

	// CONTROL: the same call with a usable directory runs the restart, so the
	// refusal above is a property of the unopenable sink rather than of a verb
	// that always errors.
	require.NoError(t, runRestartDaemon([]string{"--" + restartTargetVersionFlag, "v9.9.9"}, t.TempDir()))
	require.Equal(t, 1, reached, "control: a usable sink must let the restart proceed")
}
