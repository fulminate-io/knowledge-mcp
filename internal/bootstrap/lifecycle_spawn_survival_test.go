// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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
//
// THE INHERITED STDERR IS THE OPERATOR'S LIVE READ PATH: what a supervisor
// redirects and what `docker logs` shows. Keeping it is a decision the
// repository already made once, after a docker gate caught its absence, so a
// change that quietly retires it fails HERE. The other half of the property —
// that the child survives that stream's reader going away — lives in
// spawn_detached_stdio_test.go, on the harness below.
//
// THE CHILD ARM RUNS THE PRODUCTION LOGGING PATH, and that is load-bearing. It
// calls setupLogging with the --log-file its own argv carries and then logs
// through slog, so what the tests observe is the shipped writer's behavior
// rather than a stand-in the harness wrote.
const (
	spawnSurvivalModeEnv     = "KNOWLEDGE_TEST_SPAWN_SURVIVAL_MODE"
	spawnSurvivalBinEnv      = "KNOWLEDGE_TEST_SPAWN_SURVIVAL_BIN"
	spawnSurvivalLivenessEnv = "KNOWLEDGE_TEST_SPAWN_SURVIVAL_LIVENESS"
	spawnSurvivalStorageEnv  = "KNOWLEDGE_TEST_SPAWN_SURVIVAL_STORAGE"

	// spawnSurvivalHandoffWorkEnv selects what the restart-daemon child does in
	// place of the real unit install and port-binding restart. "loop" makes the
	// child itself the process under observation; "spawn-daemon" makes it go on
	// to the REAL spawnDaemonProcess, which is the rest of the upgrade chain.
	spawnSurvivalHandoffWorkEnv = "KNOWLEDGE_TEST_SPAWN_SURVIVAL_HANDOFF_WORK"

	// spawnSurvivalOlderBinaryLogEnv makes a spawned child behave like a build
	// that predates the current spawner: it opens the daemon log itself rather
	// than waiting to be told via --log-file. An upgrade is exactly where a newer
	// spawner meets an older spawned binary.
	spawnSurvivalOlderBinaryLogEnv = "KNOWLEDGE_TEST_SPAWN_SURVIVAL_OLDER_BINARY_LOG"

	// spawnSurvivalLauncherWritesEnv makes the launcher-standin arm write one
	// line to its inherited stderr before exiting, the way systemd-run writes its
	// "Running as unit" status line before it execs the wrapped child. Unset, the
	// same arm writes nothing, which is the control.
	spawnSurvivalLauncherWritesEnv = "KNOWLEDGE_TEST_SPAWN_SURVIVAL_LAUNCHER_WRITES"

	// spawnSurvivalRotateSizeEnv is the rotation bound the rotate-child arm runs
	// with, in megabytes. Zero means rotation off.
	spawnSurvivalRotateSizeEnv = "KNOWLEDGE_TEST_SPAWN_SURVIVAL_ROTATE_MB"

	// spawnSurvivalDiagSuffix names the sidecar a PARENT arm writes its own
	// failure into. It cannot use stderr: the tests deliberately hand the parent a
	// pipe whose reader is already closed, so anything it prints there is thrown
	// away — which is exactly why a CI red here read only "the child never
	// started" with no cause attached.
	spawnSurvivalDiagSuffix = ".parent-diag"

	// crashRouteCountSuffix names the sidecar the rotate-child writes its
	// crash-output install count into, so the parent can assert the WIRING —
	// that the sink openDurableLogSink returns re-points across a rotation —
	// rather than only the wrapper in isolation.
	crashRouteCountSuffix = ".crash-routes"

	// spawnSurvivalChildMarker is the message the child logs. It is a fixed token
	// so a test can tell the child's output from the parent's.
	spawnSurvivalChildMarker = "spawn-survival-child-stderr"

	// spawnSurvivalHandoffMarker is what the restart-daemon child logs before it
	// spawns the daemon, so a test can tell the two processes' lines apart in a
	// file they both write to.
	spawnSurvivalHandoffMarker = "spawn-survival-handoff-child"
)

// maybeRunSpawnSurvivalHelper turns this test binary into the parent or the
// child of a real detached spawn, then exits. One helper serves all three
// production spawn sites — spawnServer, spawnDaemonProcess and handOffRestart —
// so there is exactly one harness for the shared property.
//
// IT RUNS FROM TestMain, BEFORE FLAG PARSING, and that placement is required:
// the spawns invoke the child with real argv (--port, --root, --graph-storage,
// --http-port, --log-level, --target-version), which a test binary's own flag
// set would reject as unknown flags. Returning early here means those flags are
// never parsed.
// parentDiag records why a parent arm failed, where the test can read it.
func parentDiag(format string, a ...any) {
	if liveness := os.Getenv(spawnSurvivalLivenessEnv); liveness != "" {
		_ = os.WriteFile(liveness+spawnSurvivalDiagSuffix, fmt.Appendf(nil, format, a...), 0o600) //nolint:gosec // the parent test's own t.TempDir() path
	}
}

func maybeRunSpawnSurvivalHelper() {
	mode := os.Getenv(spawnSurvivalModeEnv)
	if mode == "" {
		return
	}
	// The child inherits this process's environment at Start, so flipping the
	// mode here is what makes every grandchild run the child arm.
	if mode != "child" {
		if err := os.Setenv(spawnSurvivalModeEnv, "child"); err != nil {
			fmt.Fprintln(os.Stderr, "parent: setenv:", err)
			os.Exit(2)
		}
	}

	switch mode {
	case "rotate-child":
		// THE ROTATOR RUNS IN A CHILD, and that is the leak gate's doing rather
		// than a preference: lumberjack starts a background mill goroutine on its
		// first rotation, that goroutine outlives the writer, and this package's
		// goleak gate keeps a deliberately empty allowlist. A child that exits
		// takes the goroutine with it, so the REAL rotator is exercised and the
		// gate keeps its teeth. The parent reads the directory afterwards.
		size, _ := strconv.Atoi(os.Getenv(spawnSurvivalRotateSizeEnv))
		logPath := os.Getenv(spawnSurvivalStorageEnv)
		// COUNT THE CRASH-OUTPUT INSTALLS. One at open, and one more per rotation
		// if the sink openDurableLogSink returns actually re-points. The parent
		// reads the count from a sidecar, which is how the WIRING gets asserted
		// rather than only the wrapper in isolation.
		crashRoutes := 0
		prevCrash := setCrashOutput
		setCrashOutput = func(f *os.File) error { crashRoutes++; return prevCrash(f) }

		sink := openDurableLogSink(logPath, logRotation{
			MaxSizeMB: size, MaxFiles: 3, MaxAgeDays: 30,
		})
		if sink == nil {
			fmt.Fprintln(os.Stderr, "rotate-child: the sink did not open")
			os.Exit(2)
		}
		// THE STREAM DIES AFTER THE ROTATION AND BEFORE THE NEXT ONE, and the
		// arithmetic is load-bearing rather than incidental.
		//
		// Each record is ~1.1 KB, so a 1 MB bound rotates at roughly record 950.
		// The stream is killed at 1500, which is past that rotation, and the run
		// stops at 1600 — leaving the post-rotation file around 700 KB, well under
		// the bound, so NO second rotation can carry the notice off into a backup.
		// A run that simply wrote "lots" would move the notice somewhere different
		// on any change to the record size.
		const (
			records      = 1600
			killStreamAt = 1500
		)
		stream := &failAfterWriter{after: killStreamAt, err: syscall.EPIPE}
		logger := slog.New(slog.NewTextHandler(
			newResilientTee(sink, stream),
			&slog.HandlerOptions{Level: slog.LevelInfo},
		))
		filler := strings.Repeat("x", 1024)
		for i := range records {
			logger.Info(filler, "i", i)
		}
		if c, ok := sink.(io.Closer); ok {
			_ = c.Close()
		}
		// BEFORE os.Exit, which runs no deferred function.
		//nolint:gosec // logPath is the parent test's own t.TempDir() path, passed on the env var this helper is driven by
		if werr := os.WriteFile(logPath+crashRouteCountSuffix, []byte(strconv.Itoa(crashRoutes)), 0o600); werr != nil {
			fmt.Fprintln(os.Stderr, "rotate-child: record the crash-route count:", werr)
			os.Exit(2)
		}
		os.Exit(0)

	case "rotate-crash-child":
		// THE WHOLE CHAIN, WITH COMPRESSION ON: the real setupLogging, a real
		// rotator, a real rotation, the mill's compress-and-UNLINK, and then a
		// real panic with fd 2 already dead. Compression is the shipped default
		// and it is what makes the old descriptor's inode disappear rather than
		// merely be renamed, so a fixture that leaves it off never reproduces the
		// condition the crash-following exists for.
		//
		// The parent seeds the log near the bound, so the rotation fires on the
		// FIRST record — which is the restart case the byte-counting version got
		// wrong.
		var rotLvl slog.LevelVar
		rotSize, _ := strconv.Atoi(os.Getenv(spawnSurvivalRotateSizeEnv))
		setupLogging(&Config{
			LogFile:            os.Getenv(spawnSurvivalStorageEnv),
			LogRotateMaxSizeMB: rotSize,
			LogRotateMaxFiles:  3,
			LogRotateMaxAgeDay: 30,
			LogRotateCompress:  true,
		}, &rotLvl)
		rotFiller := strings.Repeat("x", 1024)
		for i := range 20 {
			slog.Info(rotFiller, "i", i)
		}
		// The mill compresses and UNLINKS the rotated file on its own goroutine.
		time.Sleep(2 * time.Second)
		slog.Info("rotate-crash-child: past the compression, about to panic")
		panic("deliberate panic after a rotation")

	case "crash-child":
		// A REAL PANIC WITH A DEAD STDERR. The runtime writes its crash report
		// straight to fd 2; this arm exists so a test can observe that the report
		// ALSO reaches the durable file, which is the only place it can be read
		// once that stream is gone. The log path arrives on the storage env var.
		var crashLvl slog.LevelVar
		setupLogging(&Config{LogFile: os.Getenv(spawnSurvivalStorageEnv)}, &crashLvl)
		slog.Info("crash-child: about to panic")
		panic("deliberate panic with a dead stderr")

	case "launcher-standin":
		// A STAND-IN FOR THE TRANSIENT-SCOPE LAUNCHER, used by the probe-shape
		// test in client_update_handoff_stderr_test.go. It reproduces the ONE
		// property that matters about systemd-run here — whether the program
		// writes to its inherited stderr before it would exec the wrapped child —
		// on a platform where no service manager exists. The env var selects the
		// arm, so both the writing and the silent form come from one binary and
		// one code path, which is what makes them a matched pair rather than two
		// different programs.
		if os.Getenv(spawnSurvivalLauncherWritesEnv) != "" {
			fmt.Fprintln(os.Stderr, "launcher-standin: Running as unit: knowledge-test.scope")
		}
		os.Exit(0)

	case "parent":
		pid, err := spawnServer(SpawnArgs{
			BinPath:      os.Getenv(spawnSurvivalBinEnv),
			Port:         0,
			Root:         ".",
			GraphStorage: os.Getenv(spawnSurvivalStorageEnv),
		})
		if err != nil {
			parentDiag("spawnServer failed: %v", err)
			fmt.Fprintln(os.Stderr, "parent: spawnServer:", err)
			os.Exit(2)
		}
		// The pid goes to STDOUT so the test can reap the child. The child's own
		// output must never land here — that is a separate assertion.
		fmt.Println(pid)
		os.Exit(0)

	case "daemon-parent":
		if err := spawnDaemonProcess(os.Getenv(spawnSurvivalStorageEnv)); err != nil {
			parentDiag("spawnDaemonProcess failed: %v", err)
			fmt.Fprintln(os.Stderr, "parent: spawnDaemonProcess:", err)
			os.Exit(2)
		}
		os.Exit(0)

	case "handoff-parent":
		// handOffRestart spawns the path the install just wrote; in this arm that
		// is this test binary, so the grandchild is the child arm below.
		installedClientPath = func() (string, error) { return os.Getenv(spawnSurvivalBinEnv), nil }
		// THE ARGV IS RECORDED BEFORE THE SPAWN, because it is the thing that
		// differs by platform: on linux the handoff wraps the child in a
		// transient-scope launcher when one is usable, and a CI red that printed
		// only "the child never started" could not say which argv had been tried.
		hoName, hoArgv := restartHandoffArgv(os.Getenv(spawnSurvivalBinEnv), "v0.0.0-spawn-survival")
		parentDiag("handoff argv: %s %q (GOOS=%s, scope launcher usable=%v)", hoName, hoArgv, runtime.GOOS, scopeLauncherUsable())
		if err := (&client{}).handOffRestart("v0.0.0-spawn-survival"); err != nil {
			parentDiag("handoff argv: %s %q\nhandOffRestart failed: %v", hoName, hoArgv, err)
			fmt.Fprintln(os.Stderr, "parent: handOffRestart:", err)
			os.Exit(2)
		}
		os.Exit(0)

	case "child":
		// THE HANDOFF CHILD RUNS THE REAL VERB, with only the restart work
		// stubbed: the flag parse, the logging setup and the dispatch are the
		// shipped ones, because the logging setup is exactly what is under test.
		if len(os.Args) > 2 && os.Args[1] == restartDaemonVerb {
			installServiceUnitsAndRestartFn = func(string) error {
				// SPAWN-DAEMON IS THE REST OF THE REAL CHAIN. Only the unit install
				// and the port-binding restart sequence are replaced; the spawn
				// itself is the shipped one, so the daemon that comes out is
				// configured exactly as an upgrade configures it.
				if os.Getenv(spawnSurvivalHandoffWorkEnv) == "spawn-daemon" {
					slog.Info(spawnSurvivalHandoffMarker, "step", "about to spawn the upgraded daemon")
					return spawnDaemonProcess(os.Getenv(spawnSurvivalStorageEnv))
				}
				spawnSurvivalChildLoop(os.Getenv(spawnSurvivalLivenessEnv))
				return nil
			}
			// THE STORAGE DIRECTORY IS RESOLVED THE WAY THE DISPATCH RESOLVES IT,
			// from this process's own HOME — which the parent has pointed at a temp
			// directory. Passing the env var straight through would skip the
			// resolution the production dispatch actually performs.
			storage, serr := serviceGraphStorage()
			if serr != nil {
				fmt.Fprintln(os.Stderr, "child: serviceGraphStorage:", serr)
				os.Exit(2)
			}
			if err := runRestartDaemon(os.Args[2:], storage); err != nil {
				fmt.Fprintln(os.Stderr, "child: runRestartDaemon:", err)
				os.Exit(2)
			}
			os.Exit(0)
		}
		// The daemon and server arms configure logging the way their spawner told
		// them to: the --log-file the argv carries, read from the argv rather than
		// recomputed, so a spawn that stopped passing one is visible here.
		logFile := argvValue(os.Args, "--log-file")
		// OLDER-BINARY ARM. An upgrade runs a NEWER spawner against an OLDER
		// spawned binary, and an older binary opens the daemon log itself instead
		// of waiting to be told. Simulating that is how a mixed-version chain gets
		// tested at all; see TestHandoffChain_MixedVersionChildStillWritesEachLineOnce.
		if logFile == "" {
			logFile = os.Getenv(spawnSurvivalOlderBinaryLogEnv)
		}
		var lvl slog.LevelVar
		setupLogging(&Config{LogFile: logFile}, &lvl)
		spawnSurvivalChildLoop(os.Getenv(spawnSurvivalLivenessEnv))
		os.Exit(0)
	}
}

// spawnSurvivalChildLoop is the child's work: prove it is alive, then log.
//
// LIVENESS FIRST, THEN THE LOG WRITE, and the order is the measurement. The log
// write is the operation under test; recording "I reached iteration i"
// beforehand into an independent file is what distinguishes a child that died ON
// that write from one that never started at all. Reversed, the two are
// indistinguishable.
func spawnSurvivalChildLoop(liveness string) {
	for i := range 60 {
		f, err := os.OpenFile(liveness, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // path supplied by the test
		if err == nil {
			fmt.Fprintf(f, "alive %d pid=%d\n", i, os.Getpid())
			_ = f.Close()
		}
		slog.Info(spawnSurvivalChildMarker, "i", i)
		time.Sleep(100 * time.Millisecond)
	}
}

// argvValue returns the value following flag in argv, or "" when absent.
func argvValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
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
// AND THE DURABLE SINK RECEIVES IT TOO. Both, not either: the stderr stream is
// the operator's live read path under a supervisor or a container runtime, and
// the file is what survives the process. A change that satisfies one assertion
// by retiring the other sink is the defect this pair exists to catch, in
// whichever direction it is made.
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

	// SINK ONE: the child's output reached the fd the parent's STDERR pointed at.
	// This is the stream a supervisor redirects and a container runtime captures,
	// and it is the only channel an operator has while the process runs inside
	// one.
	stderrBytes, err := os.ReadFile(stderrPath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	require.Contains(t, string(stderrBytes), spawnSurvivalChildMarker,
		"the child's output must reach the inherited stderr — this is the sink a supervisor or container runtime captures")

	// SINK TWO: the same lines reached the durable file the child was told to
	// open, which is what an operator reads after the stream is gone.
	logBytes, err := os.ReadFile(serverLogPath(storage)) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	require.Contains(t, string(logBytes), spawnSurvivalChildMarker,
		"the child's output must also reach its durable log file at %s", serverLogPath(storage))

	// KNOWN-NEGATIVE, and a real invariant rather than symmetry: this process
	// writes user-facing CLI output to stdout, so a child's log lines landing
	// there would corrupt what the user reads and anything parsing it.
	require.NotContains(t, string(pidOut), spawnSurvivalChildMarker,
		"the child must NEVER write to the spawning process's stdout")
}
