// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The spawned child MUST OUTLIVE THE PROCESS THAT SPAWNED IT, and its output
// MUST REACH THAT PROCESS'S INHERITED STDERR. Both halves of that property are
// what this file guards.
//
// Until this test existed the property was stated in three comments and asserted
// by nothing, so any change that gave exec.Cmd a non-*os.File writer — the
// natural way to reach for a second sink — would compile, pass every other test,
// and ship a server that dies whenever its parent exits.
const (
	spawnSurvivalModeEnv     = "KNOWLEDGE_TEST_SPAWN_SURVIVAL_MODE"
	spawnSurvivalBinEnv      = "KNOWLEDGE_TEST_SPAWN_SURVIVAL_BIN"
	spawnSurvivalLivenessEnv = "KNOWLEDGE_TEST_SPAWN_SURVIVAL_LIVENESS"
	spawnSurvivalStorageEnv  = "KNOWLEDGE_TEST_SPAWN_SURVIVAL_STORAGE"

	// spawnSurvivalChildMarker is what the child writes to its stderr. It is a
	// fixed token so the test can tell the child's output from the parent's.
	spawnSurvivalChildMarker = "spawn-survival-child-stderr"
)

// maybeRunSpawnSurvivalHelper turns this test binary into the parent or the
// child of a real spawnServer call, then exits.
//
// IT RUNS FROM TestMain, BEFORE FLAG PARSING, and that placement is required:
// spawnServer invokes the child with the real server argv (--port, --root,
// --graph-storage, --log-file), which a test binary's own flag set would reject
// as unknown flags. Returning early here means those flags are never parsed.
func maybeRunSpawnSurvivalHelper() {
	switch os.Getenv(spawnSurvivalModeEnv) {
	case "parent":
		// The child inherits this process's environment at Start, so flipping the
		// mode here is what makes the grandchild run the child arm.
		if err := os.Setenv(spawnSurvivalModeEnv, "child"); err != nil {
			fmt.Fprintln(os.Stderr, "parent: setenv:", err)
			os.Exit(2)
		}
		pid, err := spawnServer(SpawnArgs{
			BinPath:      os.Getenv(spawnSurvivalBinEnv),
			Port:         0,
			Root:         ".",
			GraphStorage: os.Getenv(spawnSurvivalStorageEnv),
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "parent: spawnServer:", err)
			os.Exit(2)
		}
		// The pid goes to STDOUT so the test can reap the child. The child's own
		// output must never land here — that is a separate assertion.
		fmt.Println(pid)
		os.Exit(0)

	case "child":
		liveness := os.Getenv(spawnSurvivalLivenessEnv)
		for i := range 60 {
			// Writing to stderr is half the property under test; appending to an
			// independent file is the other half, so "still alive" stays
			// observable even if the stderr fd were broken.
			fmt.Fprintf(os.Stderr, "%s %d\n", spawnSurvivalChildMarker, i)
			f, err := os.OpenFile(liveness, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // path supplied by the test
			if err == nil {
				fmt.Fprintf(f, "alive %d\n", i)
				_ = f.Close()
			}
			time.Sleep(100 * time.Millisecond)
		}
		os.Exit(0)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // path is under t.TempDir()
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

// TestSpawnServer_ChildOutlivesParentAndWritesToInheritedStderr is the guard for
// spawnServer's central process property.
//
// THE PROPERTY: a process spawned by spawnServer keeps running after the process
// that spawned it has exited, and everything it writes reaches the file
// descriptor that spawning process's STDERR pointed at — never the one its
// STDOUT pointed at, which carries this process's user-facing CLI output.
//
// WHY GROWTH RATHER THAN PRESENCE. The liveness file is sampled TWICE after the
// parent has exited and the second count must EXCEED the first. A single
// non-empty check would pass against a child that wrote once and then died with
// its parent — which is exactly the failure mode being guarded, so presence
// cannot distinguish it from success.
func TestSpawnServer_ChildOutlivesParentAndWritesToInheritedStderr(t *testing.T) {
	dir := t.TempDir()
	storage := t.TempDir()
	liveness := filepath.Join(dir, "liveness")
	stderrPath := filepath.Join(dir, "parent-stderr")
	stdoutPath := filepath.Join(dir, "parent-stdout")

	self, err := os.Executable()
	require.NoError(t, err)

	errF, err := os.Create(stderrPath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	defer errF.Close()
	outF, err := os.Create(stdoutPath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	defer outF.Close()

	parent := exec.Command(self)
	parent.Env = append(os.Environ(),
		spawnSurvivalModeEnv+"=parent",
		spawnSurvivalBinEnv+"="+self,
		spawnSurvivalLivenessEnv+"="+liveness,
		spawnSurvivalStorageEnv+"="+storage,
	)
	parent.Stdout = outF
	parent.Stderr = errF
	require.NoError(t, parent.Run(), "the spawning process must exit cleanly")
	// The parent has now EXITED. Everything below observes a child whose
	// spawning process is gone.

	pidOut, err := os.ReadFile(stdoutPath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidOut)))
	require.NoError(t, err, "the parent must report the spawned pid on stdout, got %q", pidOut)
	t.Cleanup(func() {
		if p, perr := os.FindProcess(pid); perr == nil {
			_ = p.Kill()
		}
	})

	time.Sleep(300 * time.Millisecond)
	first := countLines(t, liveness)
	require.Positive(t, first, "the child must be writing at all — a zero here means it never started")

	time.Sleep(700 * time.Millisecond)
	second := countLines(t, liveness)
	require.Greater(t, second, first,
		"the child must STILL be writing after its spawning process exited (saw %d then %d)", first, second)

	// The child's output reached the fd the parent's STDERR pointed at.
	stderrBytes, err := os.ReadFile(stderrPath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	require.Contains(t, string(stderrBytes), spawnSurvivalChildMarker,
		"the child's output must reach the inherited stderr — this is the sink a supervisor or container runtime captures")

	// KNOWN-NEGATIVE, and a real invariant rather than symmetry: this process
	// writes user-facing CLI output to stdout, so a child's log lines landing
	// there would corrupt what the user reads and anything parsing it.
	require.NotContains(t, string(pidOut), spawnSurvivalChildMarker,
		"the child must NEVER write to the spawning process's stdout")
}
