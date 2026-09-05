// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// AFTER A REAL HANDOFF, EACH LINE EXACTLY ONCE, STAMPED, IN BOTH SINKS.
//
// The upgrade chain is three processes deep and each link is a different
// binary's decision: the pre-upgrade daemon spawns the handoff child, the
// handoff child spawns the upgraded daemon, and what each child writes where is
// decided by TWO parties — the spawner picks the child's stdio, and the child's
// own --log-file decides whether it TEES. Get those two out of step and the
// result is not a compile error or a failed assertion, it is a log file with
// every line in it twice.
//
// THAT IS NOT HYPOTHETICAL. It was observed in a container: post-handoff lines
// appeared twice, identical text and identical millisecond, only in the shared
// log file and with no pid or instance stamp, while the pre-handoff daemon's
// lines appeared once and stamped. Two writers landing on one path is what that
// shape means, and the unit tests for either spawn in isolation cannot see it,
// because neither one is wrong on its own.
//
// THE INVARIANT THIS FILE PINS: a spawned child's stdio must never resolve to
// the same file as the --log-file it is told to open, because the child tees
// those two together.

// logRecordLine matches a slog text record, so the assertions below count
// RECORDS rather than any incidental line in the file.
var logRecordLine = regexp.MustCompile(`^time=\S+ level=\S+ msg=`)

// TestHandoffChain_EachLineOnceAndStampedInBothSinks drives the real chain:
// handOffRestart -> restart-daemon -> spawnDaemonProcess -> serve.
//
// Only the unit install and the port-binding restart sequence are replaced. The
// two spawns, both logging setups and the argv that connects them are the
// shipped ones.
func TestHandoffChain_EachLineOnceAndStampedInBothSinks(t *testing.T) {
	home := t.TempDir()
	storage := filepath.Join(home, ".knowledge")
	require.NoError(t, os.MkdirAll(storage, 0o750))
	logPath := filepath.Join(storage, daemonLogFileName)

	dir := t.TempDir()
	liveness := filepath.Join(dir, "liveness")
	// The top process's stderr is a FILE, mirroring a container runtime or
	// supervisor capturing the daemon's stream. It is deliberately NOT the log
	// file: that is the invariant under test.
	capturePath := filepath.Join(dir, "captured-stderr")

	self, err := os.Executable()
	require.NoError(t, err)

	captureF, err := os.Create(capturePath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	defer captureF.Close()
	outF, err := os.Create(filepath.Join(dir, "captured-stdout")) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	defer outF.Close()

	parent := exec.Command(self)
	parent.Env = append(os.Environ(),
		spawnSurvivalModeEnv+"=handoff-parent",
		spawnSurvivalBinEnv+"="+self,
		spawnSurvivalLivenessEnv+"="+liveness,
		spawnSurvivalStorageEnv+"="+storage,
		spawnSurvivalHandoffWorkEnv+"=spawn-daemon",
		"HOME="+home,
	)
	parent.Stdout = outF
	parent.Stderr = captureF
	require.NoError(t, parent.Run(), "the pre-upgrade daemon stand-in must exit cleanly")

	// Wait for the upgraded daemon at the end of the chain to be logging.
	pid := waitForChildPID(t, liveness)
	t.Cleanup(func() {
		if p, perr := os.FindProcess(pid); perr == nil {
			_ = p.Kill()
		}
	})
	waitForLogContaining(t, logPath, spawnSurvivalChildMarker)
	time.Sleep(300 * time.Millisecond) // let a few more records land

	logBody := readSink(t, logPath)
	captureBody := readSink(t, capturePath)

	// ACCEPTANCE ONE: EACH LINE EXACTLY ONCE, in each sink. A tee whose two
	// writers resolve to one file writes every record twice with the same
	// timestamp, which is precisely what a duplicate-record count finds.
	requireNoDuplicateRecords(t, logPath, logBody)
	requireNoDuplicateRecords(t, capturePath, captureBody)

	// ACCEPTANCE TWO: EVERY RECORD STAMPED. A shared log file with unattributable
	// lines is the diagnostic gap this stamp closes, and the upgraded daemon is
	// exactly the process whose lines went unstamped in the container.
	requireEveryRecordStamped(t, logPath, logBody)

	// ACCEPTANCE THREE: the upgraded daemon's lines are in the DURABLE sink and
	// in the INHERITED STREAM. Both, through two spawns.
	require.Contains(t, logBody, spawnSurvivalChildMarker,
		"the upgraded daemon's lines must reach the durable log file")
	require.Contains(t, captureBody, spawnSurvivalChildMarker,
		"the upgraded daemon's lines must also reach the stream it inherited through the handoff — this is what `docker logs` shows")

	// AND THE TWO PROCESSES ARE TELLABLE APART in the file they share. The
	// handoff child and the daemon it spawned both write here; without distinct
	// instance stamps a reader cannot say which wrote which line.
	require.Contains(t, logBody, spawnSurvivalHandoffMarker,
		"the handoff child's own line must be in the shared file too")
	instances := distinctInstances(logBody)
	require.GreaterOrEqual(t, len(instances), 2,
		"the handoff child and the upgraded daemon must carry DIFFERENT instance stamps in the file they share; saw %v", instances)
}

// TestHandoffChain_MixedVersionChildStillWritesEachLineOnce is the regression
// test for the shape actually observed in a container, and an upgrade is the
// one moment it can occur.
//
// THE OBSERVED SHAPE: post-handoff records appeared TWICE, byte-identical and
// at the same millisecond, only in the shared log file, with nothing in the
// captured stream. Reproduced by composing a spawner that points the child's
// stdio AT the daemon log file with a spawned binary that opens that same log
// file itself: two writers, one file, every record doubled. Measured on the
// composition: 16 records, 8 distinct, 8 duplicated, 0 bytes captured.
//
// NEITHER HALF IS WRONG ALONE, which is why no unit test on either spawn could
// see it. The composition is what breaks, and an upgrade is where a newer
// spawner meets an older spawned binary. This test drives the real chain with
// the child behaving like that older binary, and requires the doubling not to
// happen — which holds here because no spawner points a child's stdio at a log
// file any more.
func TestHandoffChain_MixedVersionChildStillWritesEachLineOnce(t *testing.T) {
	home := t.TempDir()
	storage := filepath.Join(home, ".knowledge")
	require.NoError(t, os.MkdirAll(storage, 0o750))
	logPath := filepath.Join(storage, daemonLogFileName)

	dir := t.TempDir()
	liveness := filepath.Join(dir, "liveness")
	capturePath := filepath.Join(dir, "captured-stderr")

	self, err := os.Executable()
	require.NoError(t, err)
	captureF, err := os.Create(capturePath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	defer captureF.Close()
	outF, err := os.Create(filepath.Join(dir, "captured-stdout")) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	defer outF.Close()

	parent := exec.Command(self)
	parent.Env = append(os.Environ(),
		spawnSurvivalModeEnv+"=handoff-parent",
		spawnSurvivalBinEnv+"="+self,
		spawnSurvivalLivenessEnv+"="+liveness,
		spawnSurvivalStorageEnv+"="+storage,
		spawnSurvivalHandoffWorkEnv+"=spawn-daemon",
		// The child opens the daemon log ITSELF, the way a build predating this
		// spawner does.
		spawnSurvivalOlderBinaryLogEnv+"="+logPath,
		"HOME="+home,
	)
	parent.Stdout = outF
	parent.Stderr = captureF
	require.NoError(t, parent.Run())

	pid := waitForChildPID(t, liveness)
	t.Cleanup(func() {
		if p, perr := os.FindProcess(pid); perr == nil {
			_ = p.Kill()
		}
	})
	waitForLogContaining(t, logPath, spawnSurvivalChildMarker)
	time.Sleep(300 * time.Millisecond)

	requireNoDuplicateRecords(t, logPath, readSink(t, logPath))
	require.Contains(t, readSink(t, capturePath), spawnSurvivalChildMarker,
		"even an older child must reach the inherited stream, because this spawner hands it that stream rather than a file")
}

// TestHandoffChain_TheDaemonsStdioIsNeverItsOwnLogFile states the mechanism
// directly, so a regression that reintroduces the doubled write fails with a
// readable message instead of only as a duplicate-count mismatch.
//
// THE CHECK IS ON THE FILE, NOT THE PATH STRING: a spawner could hand over a
// different path that resolves to the same file.
func TestHandoffChain_TheDaemonsStdioIsNeverItsOwnLogFile(t *testing.T) {
	knowledgeDir(t)
	stubDaemonOwner(t, daemonOwnerNone, 0)
	_ = stubStartServerBare(t)
	stubHealth15022(t, true)
	stubProbeDaemon(t, "v1.2.3")

	// The cmd POINTER is kept so the streams the spawn assigns to it can be read
	// back after the call; the seam hands it over before those assignments.
	var spawned *exec.Cmd
	var argv []string
	prev := daemonExecCommand
	daemonExecCommand = func(_ string, args ...string) *exec.Cmd {
		argv = args
		spawned = exec.Command("/bin/echo")
		return spawned
	}
	t.Cleanup(func() { daemonExecCommand = prev })

	_ = captureStdout(t, func() {
		require.NoError(t, restartSequence("v1.2.3", outcomeBare))
	})
	require.NotNil(t, spawned, "the restart must have reached the daemon spawn")

	logArg := argvValue(append([]string{""}, argv...), "--log-file")
	require.NotEmpty(t, logArg, "the daemon must be told a --log-file; argv %q", argv)

	// THE SINK HAS TO EXIST FOR THIS COMPARISON TO MEAN ANYTHING. Without it
	// os.Stat fails and an identity check would be skipped rather than made,
	// which is a vacuous pass wearing an assertion's clothes.
	require.NoError(t, os.WriteFile(logArg, nil, 0o600))
	logInfo, err := os.Stat(logArg)
	require.NoError(t, err)

	// BOTH STREAMS MUST BE *os.File, or exec.Cmd builds a parent-lifetime pipe.
	stderrFile, ok := spawned.Stderr.(*os.File)
	require.True(t, ok, "the child's stderr must be an *os.File, got %T", spawned.Stderr)
	stdoutFile, ok := spawned.Stdout.(*os.File)
	require.True(t, ok, "the child's stdout must be an *os.File, got %T", spawned.Stdout)

	for name, f := range map[string]*os.File{"stderr": stderrFile, "stdout": stdoutFile} {
		info, serr := f.Stat()
		require.NoError(t, serr)
		require.False(t, os.SameFile(info, logInfo),
			"the daemon's inherited %s must not be the same file as its --log-file (%s): the child tees the two together, so every record would land twice",
			name, logArg)
	}

	// POSITIVE CONTROL for os.SameFile, so the two assertions above mean "these
	// are different files" rather than "this comparison never returns true".
	require.True(t, os.SameFile(logInfo, mustStat(t, logArg)),
		"control: os.SameFile must report identity when the two paths are the same file")
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info
}

func readSink(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // under t.TempDir()
	require.NoError(t, err, "expected a sink at %s", path)
	return string(b)
}

// waitForLogContaining polls a sink until it carries want, so the assertions
// run against a chain that has actually reached its last process.
func waitForLogContaining(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), want) { //nolint:gosec // under t.TempDir()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never carried %q", path, want)
}

// requireNoDuplicateRecords fails when any complete log record appears more than
// once. Two writers resolving to one file produce byte-identical records,
// timestamp included, so exact repetition is the signature.
func requireNoDuplicateRecords(t *testing.T, path, body string) {
	t.Helper()
	seen := map[string]int{}
	for line := range strings.SplitSeq(body, "\n") {
		if !logRecordLine.MatchString(line) {
			continue
		}
		seen[line]++
	}
	require.NotEmpty(t, seen, "no log records found in %s — the assertion below would pass vacuously", path)
	var dupes []string
	for line, n := range seen {
		if n > 1 {
			dupes = append(dupes, line)
		}
	}
	require.Empty(t, dupes,
		"%s carries %d record(s) written more than once — a tee whose two sinks resolve to one file: %v", path, len(dupes), dupes)
}

// requireEveryRecordStamped fails when any record lacks pid or instance.
func requireEveryRecordStamped(t *testing.T, path, body string) {
	t.Helper()
	var unstamped []string
	records := 0
	for line := range strings.SplitSeq(body, "\n") {
		if !logRecordLine.MatchString(line) {
			continue
		}
		records++
		if !strings.Contains(line, "pid=") || !strings.Contains(line, "instance=") {
			unstamped = append(unstamped, line)
		}
	}
	require.Positive(t, records, "no log records found in %s — the assertion below would pass vacuously", path)
	require.Empty(t, unstamped,
		"%s carries %d record(s) with no pid/instance stamp, so no reader can attribute them to a process: %v", path, len(unstamped), unstamped)
}

// distinctInstances collects the instance stamps present in a sink.
func distinctInstances(body string) []string {
	found := map[string]struct{}{}
	for _, m := range instanceField.FindAllStringSubmatch(body, -1) {
		found[m[1]] = struct{}{}
	}
	out := make([]string, 0, len(found))
	for k := range found {
		out = append(out, k)
	}
	return out
}
