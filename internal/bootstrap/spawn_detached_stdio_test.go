// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A DETACHED CHILD'S LIFETIME MUST NOT DEPEND ON A FILE DESCRIPTION IT
// INHERITED FROM THE PROCESS IT IS SPAWNED TO OUTLIVE.
//
// Measured defect this file guards: after a self-upgrade handoff the new daemon
// inherited its spawner's stderr, that stderr was a pipe (the pre-upgrade
// daemon's `docker exec -d` stdio), the reader went away about twelve seconds
// after the exec parent exited, and the daemon's next log line took SIGPIPE.
// Go's runtime resets a fatal signal on fd 1 or 2 to SIG_DFL and re-raises, so
// the process died with wait status 13, no shutdown line and no panic. The same
// shape armed the respawned knowledge-server and the handoff child itself.
//
// EVERY TEST HERE DRIVES THE PRODUCTION SPAWN FUNCTION, never a copy of it, and
// gives the spawning process a stderr that is a pipe whose reader is already
// gone. The child logs through the shipped setupLogging, so what is observed is
// the shipped writer. On the defective tree the child dies on its first log
// write; the property asserted is that it stays alive, keeps working, its output
// lands in the durable log file an operator reads, and giving up the dead stream
// is recorded there exactly once.
//
// THE STREAM IS NOT GIVEN UP TO ACHIEVE THIS. Its presence while alive is
// asserted by TestSpawnServer_ChildOutlivesParentAndWritesToInheritedStderr,
// which shares this harness; the two files together say the child keeps both
// sinks and survives losing one.

// childPIDLine parses the pid the child arm records on every liveness line.
var childPIDLine = regexp.MustCompile(`pid=(\d+)`)

// detachedSpawnResult is what a harness run hands back for assertions.
type detachedSpawnResult struct {
	childPID  int
	liveness  string
	parentOut string // bytes the spawning process wrote to ITS stdout
	// There is deliberately no parentErr: the spawning process's stderr is the
	// pipe whose reader this harness closes, so nothing that reaches it is
	// readable by anyone. That unreadability IS the condition under test.
}

// runDetachedSpawnWithDeadPipe re-executes this test binary in the named parent
// arm with its STDERR pointed at a pipe whose read end is closed immediately,
// waits for that parent to exit, and returns the grandchild it left behind.
//
// THE DEAD PIPE IS THE WHOLE POINT. It is the container-runtime and
// wrapper-supervisor shape from the field: an fd the spawning process owns,
// whose reader outlives neither it nor the child. A test that handed the parent
// a file could not fail on the defective tree, which is exactly why the defect
// shipped past the existing survival test.
func runDetachedSpawnWithDeadPipe(t *testing.T, mode, storage string, extraEnv ...string) detachedSpawnResult {
	t.Helper()

	dir := t.TempDir()
	liveness := filepath.Join(dir, "liveness")
	stdoutPath := filepath.Join(dir, "parent-stdout")

	self, err := os.Executable()
	require.NoError(t, err)

	outF, err := os.Create(stdoutPath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	defer outF.Close()

	pr, pw, err := os.Pipe()
	require.NoError(t, err)

	parent := exec.Command(self)
	parent.Env = append(os.Environ(),
		spawnSurvivalModeEnv+"="+mode,
		spawnSurvivalBinEnv+"="+self,
		spawnSurvivalLivenessEnv+"="+liveness,
		spawnSurvivalStorageEnv+"="+storage,
	)
	parent.Env = append(parent.Env, extraEnv...)
	parent.Stdout = outF
	// *os.File on both sides: exec.Cmd hands the child a RAW FD only for an
	// *os.File. Any other writer would become a pipe drained by a
	// parent-lifetime goroutine, which would change what is under test.
	parent.Stderr = pw
	require.NoError(t, parent.Start())

	// The reader goes away while the spawning process is still running — the
	// field ordering. Both ends held by this test are released here so the only
	// remaining write ends belong to the parent and its detached child.
	require.NoError(t, pr.Close())
	require.NoError(t, pw.Close())
	require.NoError(t, parent.Wait(), "the spawning process must exit cleanly")
	// The parent has now EXITED. Everything below observes a child whose
	// spawning process is gone and whose inherited pipe has no reader.

	res := detachedSpawnResult{liveness: liveness}
	outBytes, err := os.ReadFile(stdoutPath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	res.parentOut = string(outBytes)

	res.childPID = waitForChildPID(t, liveness)
	t.Cleanup(func() {
		if p, perr := os.FindProcess(res.childPID); perr == nil {
			_ = p.Kill()
		}
	})
	return res
}

// waitForChildPID polls the liveness file for the pid the child records before
// its first stderr write. A child that never records one never started, which
// is a different failure from the one under test and is reported as such.
func waitForChildPID(t *testing.T, liveness string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(liveness) //nolint:gosec // under t.TempDir()
		if err == nil {
			if m := childPIDLine.FindSubmatch(b); m != nil {
				pid, cerr := strconv.Atoi(string(m[1]))
				require.NoError(t, cerr)
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	// THE FAILURE EXPLAINS ITSELF. The parent's stderr is the pipe this harness
	// deliberately closed, so anything it printed there is gone; it writes its own
	// diagnosis to a sidecar instead, and a CI red that says only "never started"
	// is a red nobody can act on from the log alone.
	diag := "(no parent diagnostic was recorded)"
	if b, derr := os.ReadFile(liveness + spawnSurvivalDiagSuffix); derr == nil { //nolint:gosec // under t.TempDir()
		diag = string(b)
	}
	t.Fatalf("the spawned child never recorded a pid in %s — it never started.\nparent diagnostic: %s\n%s",
		liveness, diag, launcherPostMortem())
	return 0
}

// launcherPostMortem is what the spawn's own exit status cannot say.
//
// THE CHILD'S EXIT ERROR IS NOT OBSERVABLE HERE BY CONSTRUCTION: the handoff
// RELEASES the child rather than waiting for it, which is the whole point of a
// process meant to outlive its spawner, and the child's stderr is the pipe this
// harness deliberately closed. So neither its status nor its output can be read
// back through the spawn.
//
// The one process whose failure IS reproducible is the launcher the handoff
// wraps the child in on linux. Running it here on a trivial command, on the
// failure path only, recovers exactly the message the lost stderr would have
// carried — a launcher present but unable to place a child says so, and that
// sentence is the whole diagnosis of this class of red.
func launcherPostMortem() string {
	if runtime.GOOS != "linux" {
		return "launcher post-mortem: not applicable (the launcher is a linux-only step)"
	}
	if _, err := exec.LookPath(scopeLauncher[0]); err != nil {
		return "launcher post-mortem: " + scopeLauncher[0] + " is not on PATH, so no launcher was involved"
	}
	ctx, cancel := context.WithTimeout(context.Background(), scopeLauncherProbeTimeout)
	defer cancel()
	argv := append(append([]string{}, scopeLauncher[1:]...), "/bin/true")
	out, err := exec.CommandContext(ctx, scopeLauncher[0], argv...).CombinedOutput() //nolint:gosec // a fixed argv, no caller input
	return fmt.Sprintf("launcher post-mortem: %s %q\n  exit error: %v\n  captured output: %s",
		scopeLauncher[0], argv, err, strings.TrimSpace(string(out)))
}

// requireChildStillWorking is the assertion the defect fails.
//
// WHY GROWTH AND A SIGNAL PROBE RATHER THAN PRESENCE. A single non-empty check
// passes against a child that wrote one line and then died on its first stderr
// write, which IS the defect. The liveness file must keep growing, and signal 0
// must still find the process, after the spawning process is gone.
func requireChildStillWorking(t *testing.T, res detachedSpawnResult) {
	t.Helper()

	time.Sleep(300 * time.Millisecond)
	first := countLines(t, res.liveness)
	require.Positive(t, first, "the child must be writing at all — a zero here means it never started")

	time.Sleep(700 * time.Millisecond)
	second := countLines(t, res.liveness)

	p, err := os.FindProcess(res.childPID)
	require.NoError(t, err)
	// Signal 0 delivers nothing and reports whether the process still exists —
	// the only liveness probe available for a child this process never parented.
	alive := p.Signal(syscall.Signal(0)) == nil

	require.True(t, alive,
		"pid %d is GONE after its spawning process exited: the child died rather than outliving it "+
			"(it reached %d liveness lines; on the defective tree it dies on the stderr write that follows the last one, "+
			"with SIGPIPE on the inherited pipe whose reader had closed)", res.childPID, second)
	require.Greater(t, second, first,
		"the child must STILL be doing work after its spawning process exited (saw %d liveness lines then %d)", first, second)
}

// requireOperatorLogHasChildOutput is the other half of the requirement:
// surviving the dead stream must not cost the operator the lines. The durable
// file is what is left once the stream is gone, so the child's output has to be
// in it, and the retirement itself has to be recorded there exactly once.
func requireOperatorLogHasChildOutput(t *testing.T, logPath string) {
	t.Helper()
	b, err := os.ReadFile(logPath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err, "the child's durable log file must exist at %s", logPath)
	body := string(b)
	require.Contains(t, body, spawnSurvivalChildMarker,
		"the operator-visible log at %s must carry the child's output", logPath)
	require.Equal(t, 1, strings.Count(body, stderrRetiredMsg),
		"giving up the dead stderr stream must be recorded in %s exactly once, not silently and not per line", logPath)
}

// TestSpawnDaemonProcess_ChildSurvivesADeadInheritedStderrPipe is the guard for
// the measured defect: the upgraded daemon spawned by the handoff child.
func TestSpawnDaemonProcess_ChildSurvivesADeadInheritedStderrPipe(t *testing.T) {
	storage := t.TempDir()
	res := runDetachedSpawnWithDeadPipe(t, "daemon-parent", storage)
	requireChildStillWorking(t, res)
	requireOperatorLogHasChildOutput(t, filepath.Join(storage, "knowledge-daemon.log"))
	require.NotContains(t, res.parentOut, spawnSurvivalChildMarker,
		"the daemon must never write to the spawning process's stdout, which carries user-facing CLI output")
}

// TestSpawnServer_ChildSurvivesADeadInheritedStderrPipe is the same guard for
// the knowledge-server the same handoff respawns.
func TestSpawnServer_ChildSurvivesADeadInheritedStderrPipe(t *testing.T) {
	storage := t.TempDir()
	res := runDetachedSpawnWithDeadPipe(t, "parent", storage)
	requireChildStillWorking(t, res)
	requireOperatorLogHasChildOutput(t, filepath.Join(storage, "server.log"))
	// The parent arm prints the spawned pid on stdout; the child's own marker
	// must not be there beside it.
	require.NotContains(t, res.parentOut, spawnSurvivalChildMarker,
		"the server must never write to the spawning process's stdout")
}

// TestHandOffRestart_ChildSurvivesADeadInheritedStderrPipe covers the third
// process in the same chain. The handoff child is spawned by a daemon it is
// about to stop, so it outlives its spawner by construction; if it dies on the
// inherited pipe mid-restart the upgrade leaves NO daemon running at all.
func TestHandOffRestart_ChildSurvivesADeadInheritedStderrPipe(t *testing.T) {
	home := t.TempDir()
	storage := filepath.Join(home, ".knowledge")
	require.NoError(t, os.MkdirAll(storage, 0o750))

	res := runDetachedSpawnWithDeadPipe(t, "handoff-parent", storage, "HOME="+home)
	requireChildStillWorking(t, res)
	requireOperatorLogHasChildOutput(t, filepath.Join(storage, "knowledge-daemon.log"))
}

// TestSpawnDaemonProcess_UncreatableLogDirErrorsAndForksNothing pins the
// failure arm. A spawned child cannot be given a second sink after the fact, so
// a log directory that cannot be created is reported before the fork rather than
// producing a daemon that reports a failed open and then runs on one sink.
func TestSpawnDaemonProcess_UncreatableLogDirErrorsAndForksNothing(t *testing.T) {
	// A regular FILE standing where the storage DIRECTORY should be: creating
	// the log directory inside it cannot succeed, on any platform, without a
	// race.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(blocked, []byte("x"), 0o600))

	calls := spyDaemonExec(t)

	err := spawnDaemonProcess(blocked)
	require.Error(t, err, "an uncreatable log directory must fail the spawn loudly")
	require.Contains(t, err.Error(), daemonLogFileName,
		"the error must name the sink it could not prepare; got %v", err)
	require.Empty(t, *calls, "nothing may be forked once the child's durable sink is known to be unusable")
}

// TestSpawnedChildrenAreToldTheirLogFile guards the sink the survival tests read
// and the rotation the server hangs off the same flag.
//
// IT IS AN ARGV ASSERTION ON PURPOSE. The durable sink exists because the child
// is TOLD to open it; a change that stops passing --log-file leaves both
// survival tests passing — the stream is still there — while retiring the
// durable half of the log and, on the server, its size cap, backup count and age
// prune along with it.
func TestSpawnedChildrenAreToldTheirLogFile(t *testing.T) {
	t.Run("the daemon", func(t *testing.T) {
		knowledgeDir(t)
		stubDaemonOwner(t, daemonOwnerNone, 0)
		_ = stubStartServerBare(t)
		stubHealth15022(t, true)
		stubProbeDaemon(t, "v1.2.3")
		calls := spyDaemonExec(t)

		_ = captureStdout(t, func() {
			require.NoError(t, restartSequence("v1.2.3", outcomeBare))
		})
		require.Len(t, *calls, 1)
		argv := (*calls)[0]
		require.Contains(t, argv, "--log-file")
		require.Contains(t, argv, daemonLogPath(mustGraphStorage(t)),
			"the daemon must be told the durable sink an operator reads; argv %q", argv)
	})

	t.Run("the server", func(t *testing.T) {
		argv := serverSpawnArgv(SpawnArgs{BinPath: "/app/knowledge-server", Port: 15022, Root: "/w", GraphStorage: "/data/.knowledge"})
		require.Contains(t, argv, "--log-file")
		require.Contains(t, argv, "/data/.knowledge/server.log",
			"the server must be told its durable sink — the flag is also what builds its log rotator; argv %q", argv)
	})
}

func mustGraphStorage(t *testing.T) string {
	t.Helper()
	s, err := serviceGraphStorage()
	require.NoError(t, err)
	return s
}
