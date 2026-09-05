// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE DAEMON'S DURABLE LOG IS ROTATED, on the server's terms.
//
// It is the durable half of the both-sinks property, so a container that runs
// for weeks would otherwise grow it without bound inside the user's mounted
// volume. The two binaries cannot share the implementation — the only
// cross-module contract is generated protobuf — so what is held identical is the
// flag names, the defaults and the meaning of zero.

// TestRotationFromConfigRefusesNegativeBounds is the bad-input arm.
//
// ZERO IS A CHOICE AND NEGATIVE IS AN ERROR. Zero max-size means "never rotate",
// which the server spells the same way; a negative bound has no meaning, and
// reading it as "disabled" would retire an operator's log retention without
// telling them.
func TestRotationFromConfigRefusesNegativeBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		flag string
	}{
		{"size", Config{LogRotateMaxSizeMB: -1}, "log-rotate-max-size-mb"},
		{"files", Config{LogRotateMaxFiles: -1}, "log-rotate-max-files"},
		{"age", Config{LogRotateMaxAgeDay: -1}, "log-rotate-max-age-days"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rotationFromConfig(&tc.cfg)
			require.Error(t, err, "a negative %s bound must be refused", tc.name)
			require.Contains(t, err.Error(), tc.flag, "the error must name the flag; got %v", err)
		})
	}

	// CONTROL: zero everywhere is accepted, because "never rotate" and "prune by
	// the other bound only" are legitimate configurations rather than bad input.
	rot, err := rotationFromConfig(&Config{})
	require.NoError(t, err)
	require.Zero(t, rot.MaxSizeMB)
}

// TestServeRefusesToStartOnANonsensicalRetentionPolicy is the bad-input
// invariant at the daemon.
//
// MEASURED BEFORE THIS: `--log-file X --log-rotate-max-size-mb -1` STARTED the
// daemon, answered /mcp with HTTP 200, and created no file at X. The operator
// asked for a durable sink by passing --log-file and got none, with the only
// notice on the stream this seam exists because it may be dead. A typo in a sign
// silently produced the single-sink state the branch was written to make
// impossible.
//
// AN INVALID --log-level STAYS A WARNING, and that difference is deliberate: it
// has a meaningful fallback, info, that costs nothing durable. A refused
// retention policy has no fallback that keeps the operator's file.
func TestServeRefusesToStartOnANonsensicalRetentionPolicy(t *testing.T) {
	withRestoredDefaultLogger(t)

	t.Run("with a log file, the error names the flag and the path", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), daemonLogFileName)
		err := installServeLogging(&Config{LogFile: logPath, LogRotateMaxSizeMB: -1}, new(slog.LevelVar))
		require.Error(t, err, "the daemon must refuse to start rather than serve without the sink it was asked for")
		require.Contains(t, err.Error(), "log-rotate-max-size-mb", "the error must name the flag")
		require.Contains(t, err.Error(), logPath, "and the durable log it is refusing to start without")
		require.NoFileExists(t, logPath, "and nothing may have been created on the way out")
	})

	t.Run("with no log file, the error names no file", func(t *testing.T) {
		err := installServeLogging(&Config{LogRotateMaxSizeMB: -1}, new(slog.LevelVar))
		require.Error(t, err, "a meaningless retention bound is bad input whether or not a sink was named")
		require.Contains(t, err.Error(), "log-rotate-max-size-mb")
		// THE T4 RIDER: the message used to read "refusing the log file :",
		// naming an empty path for a file the operator never asked for.
		require.NotContains(t, err.Error(), "durable log :",
			"the message must not name an empty path when no --log-file was given")
	})

	t.Run("control: a valid bound starts", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), daemonLogFileName)
		require.NoError(t, installServeLogging(&Config{LogFile: logPath, LogRotateMaxSizeMB: 50}, new(slog.LevelVar)),
			"control: the refusals above are properties of the bound, not of a daemon that never starts")
		require.FileExists(t, logPath)
	})

	t.Run("control: the normal path is untouched", func(t *testing.T) {
		// No rotation flags at all, which is every default invocation including
		// the container's. This must behave exactly as it did before.
		require.NoError(t, installServeLogging(&Config{}, new(slog.LevelVar)))
	})
}

// TestSetupLoggingRefusesTheFileSinkOnANonsensicalPolicy proves the refusal
// reaches the caller rather than being swallowed: a caller that treats the
// durable sink as a precondition must fail, not proceed with an unrotated file.
func TestSetupLoggingRefusesTheFileSinkOnANonsensicalPolicy(t *testing.T) {
	withRestoredDefaultLogger(t)
	logPath := filepath.Join(t.TempDir(), daemonLogFileName)

	require.Nil(t, setupLogging(&Config{LogFile: logPath, LogRotateMaxSizeMB: -1}, new(slog.LevelVar)),
		"a refused retention policy must refuse the sink, not silently write an unrotated file")
	require.NoFileExists(t, logPath, "and it must not have created the file on the way out")

	// CONTROL: the same path with a valid policy DOES open, so the nil above is a
	// property of the policy rather than of the path.
	require.NotNil(t, setupLogging(&Config{LogFile: logPath, LogRotateMaxSizeMB: 50}, new(slog.LevelVar)))
}

// runRotateChild drives the REAL rotator in a child process and returns the
// directory it wrote into.
//
// A CHILD BECAUSE OF THE LEAK GATE, not for isolation: lumberjack starts a
// background mill goroutine on its first rotation and that goroutine outlives
// the writer, while this package's goleak gate keeps a deliberately empty
// allowlist. Running the rotator where the gate cannot see it keeps both the
// real library under test and the gate's teeth.
func runRotateChild(t *testing.T, maxSizeMB int) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, daemonLogFileName)

	self, err := os.Executable()
	require.NoError(t, err)

	child := exec.Command(self)
	child.Env = append(os.Environ(),
		spawnSurvivalModeEnv+"=rotate-child",
		spawnSurvivalStorageEnv+"="+logPath,
		spawnSurvivalRotateSizeEnv+"="+strconv.Itoa(maxSizeMB),
	)
	out, err := child.CombinedOutput()
	require.NoError(t, err, "the rotate child failed: %s", out)
	return dir
}

// TestCrashOutputFollowsTheRotation is the residual the reviewer named, closed
// rather than documented.
//
// WHY IT IS NOT COSMETIC. SetCrashOutput duplicates a descriptor. A rotation
// RENAMES the active file, so that descriptor follows the renamed inode, and
// compression then rewrites the backup as .gz and REMOVES the original — leaving
// the crash descriptor on an unlinked inode. A panic after the first
// rotate-and-compress would be written to a deleted file and reach nobody, which
// is the sink-nobody-reads defect in the one channel this seam exists to keep.
//
// THE WRAPPER IS TESTED AGAINST A REAL FILE BEING RENAMED, which is exactly
// what a rotation does to it, and without a real rotator: a rotator would add
// its background mill goroutine to a package whose leak gate keeps an empty
// allowlist. The rotator and this wrapper run together in the rotate-child arm.
//
// THE OLD VERSION OF THIS TEST COULD NOT SEE THE DEFECT. It drove an in-memory
// writer with a byte counter, so no file existed whose size could matter, and
// the seeding bug — the counter starting at zero while lumberjack seeds from the
// file on disk — was invisible to it by construction.
func TestCrashOutputFollowsTheRotation(t *testing.T) {
	var routed []string
	prev := setCrashOutput
	setCrashOutput = func(f *os.File) error {
		routed = append(routed, f.Name())
		return nil
	}
	t.Cleanup(func() { setCrashOutput = prev })

	dir := t.TempDir()
	path := filepath.Join(dir, daemonLogFileName)
	require.NoError(t, os.WriteFile(path, []byte("existing\n"), 0o600))

	sink := followCrashOutput(&countingWriter{}, path, 1)

	// THE FILE HAS NOT MOVED: nothing to re-point.
	_, err := sink.Write([]byte("a record\n"))
	require.NoError(t, err)
	require.Empty(t, routed, "while the path still names the installed file the descriptor must be left alone")

	// A ROTATION, done the way lumberjack does it: the active file is RENAMED
	// out of the way and a new one takes the path.
	require.NoError(t, os.Rename(path, filepath.Join(dir, "knowledge-daemon-rotated.log")))
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	_, err = sink.Write([]byte("after the rotation\n"))
	require.NoError(t, err)
	require.Equal(t, []string{path}, routed,
		"once the path names a different file the descriptor must be re-pointed at it")

	// AND NOT AGAIN while that file stays put, so the re-point tracks rotations
	// rather than firing on every record.
	routed = nil
	_, err = sink.Write([]byte("and another\n"))
	require.NoError(t, err)
	require.Empty(t, routed, "a settled file must not be re-pointed on every write")
}

// TestCrashFollowingSurvivesARestartOntoAPartFullLog is the seeding defect,
// which the byte-counting version got wrong on every restart.
//
// MEASURED ON THAT VERSION: a 900 KB log under a 1 MB bound rotates on the FIRST
// write, because lumberjack seeds its size from the file on disk, while the
// wrapper's counter started at zero and would not re-point for another full
// bound. A real panic 180 records later left the trace in neither file. At the
// shipped 50 MB bound with a 40 MB log at startup that is a 40 MB blind window.
func TestCrashFollowingSurvivesARestartOntoAPartFullLog(t *testing.T) {
	var routed []string
	prev := setCrashOutput
	setCrashOutput = func(f *os.File) error {
		routed = append(routed, f.Name())
		return nil
	}
	t.Cleanup(func() { setCrashOutput = prev })

	dir := t.TempDir()
	path := filepath.Join(dir, daemonLogFileName)
	// NEAR THE BOUND ALREADY, which is what a restart onto a live log looks
	// like: the rotator would rotate on its first record.
	require.NoError(t, os.WriteFile(path, make([]byte, 900<<10), 0o600))

	// WRAPPED WITHOUT A REAL ROTATOR, and the rotation is performed by hand as a
	// rename: a live rotator starts a background mill goroutine that outlives the
	// writer, and this package's leak gate keeps an empty allowlist. The real
	// rotator on a real seeded file runs in the child that
	// TestCrashTraceReachesTheCurrentFileAfterARotation drives.
	sink := followCrashOutput(&countingWriter{}, path, 1)
	require.NoError(t, os.Rename(path, filepath.Join(dir, "knowledge-daemon-rotated.log")))
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	_, err := sink.Write([]byte("the first record after the rotation\n"))
	require.NoError(t, err)
	require.Equal(t, []string{path}, routed,
		"the descriptor must follow on the very NEXT write, whatever the byte count since this process started; a counter seeded at zero would have waited a further full bound")
}

// crashRouteCount reads how many times the rotate-child installed crash output.
func crashRouteCount(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, daemonLogFileName+crashRouteCountSuffix)) //nolint:gosec // under t.TempDir()
	require.NoError(t, err, "the rotate child must have recorded its crash-route count")
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	require.NoError(t, err)
	return n
}

// TestOpenDurableLogSinkWiresTheCrashFollowing asserts the WIRING, which the
// wrapper's own unit test cannot see.
//
// A sink built straight from the rotator, with no wrapper around it, passes
// every other rotation assertion here: the files rotate, the notice lands, no
// record doubles. The only observable difference is that crash output is
// installed ONCE and then left on an inode the rotation renamed away.
func TestOpenDurableLogSinkWiresTheCrashFollowing(t *testing.T) {
	rotated := runRotateChild(t, 1)
	require.Greater(t, crashRouteCount(t, rotated), 1,
		"with rotation on, crash output must be installed at open AND re-installed after the rotation")

	// CONTROL, same child and same instrument: with rotation off nothing renames
	// the file, so exactly one install is correct and more would be churn.
	unrotated := runRotateChild(t, 0)
	require.Equal(t, 1, crashRouteCount(t, unrotated),
		"with rotation off the descriptor is never invalidated, so it is installed once")
}

// TestRetirementNoticeLandsInTheCurrentFileAfterARotation is the property a
// rotated sink makes non-obvious.
//
// THE NOTICE IS THE ONE RECORD THAT MUST NOT GO MISSING: it is what tells an
// operator the stream is gone and the file is now the only sink. Written through
// a stale handle it would land in a renamed backup, or after compression in a
// deleted inode, and the degrade this seam exists to announce would be silent
// again. The child writes past the rotation bound with a LIVE stream and only
// then loses it, so the notice is necessarily written after the rename.
func TestRetirementNoticeLandsInTheCurrentFileAfterARotation(t *testing.T) {
	dir := runRotateChild(t, 1)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	entries = logFiles(entries)
	require.Greater(t, len(entries), 1,
		"the run must actually have rotated, or this proves nothing about a post-rotation notice; saw %v", names(entries))

	b, err := os.ReadFile(filepath.Join(dir, daemonLogFileName)) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	current := string(b)
	require.Equal(t, 1, strings.Count(current, stderrRetiredMsg),
		"the retirement notice must be in the CURRENT file exactly once; a rotated backup is not where an operator looks")

	// AND THE FILE KEEPS RECEIVING AFTER IT. A notice that is the last line would
	// mean the sink stopped at the moment it announced it was the only one left.
	require.Greater(t, strings.Count(current[strings.Index(current, stderrRetiredMsg):], "\n"), 1,
		"records must continue after the notice; the file is the surviving sink, not a headstone")
}

// TestRotationWritesNoRecordTwice pins that adding a rotating sink added no
// second copy of anything. A doubled record is what a tee whose two writers
// resolve to one file produces, and rotation moves files underneath that tee, so
// the two have to be checked together rather than separately.
func TestRotationWritesNoRecordTwice(t *testing.T) {
	dir := runRotateChild(t, 1)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	entries = logFiles(entries)

	seen := map[string]int{}
	records := 0
	for _, e := range entries {
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // under t.TempDir()
		require.NoError(t, rerr)
		for line := range strings.SplitSeq(string(b), "\n") {
			if !logRecordLine.MatchString(line) {
				continue
			}
			records++
			seen[line]++
		}
	}
	require.Positive(t, records, "no records found across %v — the check below would pass vacuously", names(entries))

	var dupes []string
	for line, n := range seen {
		if n > 1 {
			dupes = append(dupes, line)
		}
	}
	require.Empty(t, dupes, "records written more than once across the rotated set: %v", dupes)
}

// TestDurableSinkRotatesAtTheSizeBound drives the real rotator.
//
// THE BOUND IS SMALL BY CONFIGURATION, not by a fake: lumberjack's unit is
// megabytes and its smallest meaningful bound is 1, so the child writes a little
// over 1 MB rather than the 50 MB the default would need. That is the real code
// path with the real library at its smallest real setting.
func TestDurableSinkRotatesAtTheSizeBound(t *testing.T) {
	dir := runRotateChild(t, 1)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	entries = logFiles(entries)
	var active, rotated int
	for _, e := range entries {
		switch {
		case e.Name() == daemonLogFileName:
			active++
		case strings.HasPrefix(e.Name(), "knowledge-daemon-"):
			rotated++
		}
	}
	require.Equal(t, 1, active, "the active log must still be at its own name; saw %v", names(entries))
	require.Positive(t, rotated, "crossing the size bound must have produced a rotated file; saw %v", names(entries))

	// AND THE ACTIVE FILE IS BOUNDED, which is the property that matters: an
	// unrotated sink would hold everything the child wrote.
	st, err := os.Stat(filepath.Join(dir, daemonLogFileName))
	require.NoError(t, err)
	require.Less(t, st.Size(), int64(2<<20),
		"the active log must be bounded by the rotation size, not carry the whole run")
}

// TestDurableSinkDoesNotRotateWhenTheBoundIsZero is the other direction, so the
// test above reports rotation rather than a writer that always splits files.
func TestDurableSinkDoesNotRotateWhenTheBoundIsZero(t *testing.T) {
	dir := runRotateChild(t, 0)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	entries = logFiles(entries)
	require.Len(t, entries, 1, "rotation off must leave exactly one file; saw %v", names(entries))

	st, err := os.Stat(filepath.Join(dir, daemonLogFileName))
	require.NoError(t, err)
	require.Greater(t, st.Size(), int64(1<<20),
		"rotation off must let the single file exceed the size a rotator would have split at")
}

// logFiles drops the rotate-child's bookkeeping sidecar, which is not a log and
// must never be mistaken for a rotated one — counting it would let a run with NO
// rotation satisfy a "more than one file" check.
func logFiles(entries []os.DirEntry) []os.DirEntry {
	out := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), crashRouteCountSuffix) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
