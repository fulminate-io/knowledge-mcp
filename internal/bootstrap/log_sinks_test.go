// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE WRITER MUST KEEP BOTH SINKS AND SURVIVE LOSING ONE.
//
// These are the unit-level halves of the property the spawn survival tests
// exercise end to end. They do NOT prove the signal behavior: a write to a
// broken pipe on any descriptor OTHER than 1 or 2 already returns EPIPE without
// raising anything, so a pipe handed to the tee here fails the same way with or
// without the signal disposition. The fd-1-and-2 death is a process-level
// property and is asserted where it lives, by the survival tests.

// failingWriter is a stderr stand-in that fails every write with a named cause.
type failingWriter struct {
	err   error
	calls int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.calls++
	return 0, w.err
}

// failAfterWriter accepts writes until a threshold and fails every one after,
// so a test can place a stream's death at a chosen point in a run.
type failAfterWriter struct {
	after int
	err   error
	calls int
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls <= w.after {
		return len(p), nil
	}
	return 0, w.err
}

// countingWriter records what it received, in order.
type countingWriter struct{ got []string }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.got = append(w.got, string(p))
	return len(p), nil
}

// TestResilientTee_BothSinksReceiveEveryLineWhileStderrIsAlive is the owner's
// both-sinks property at the writer level.
func TestResilientTee_BothSinksReceiveEveryLineWhileStderrIsAlive(t *testing.T) {
	file, stream := &countingWriter{}, &countingWriter{}
	tee := newResilientTee(file, stream)

	for _, line := range []string{"first\n", "second\n", "third\n"} {
		n, err := tee.Write([]byte(line))
		require.NoError(t, err)
		require.Equal(t, len(line), n)
	}

	require.Equal(t, []string{"first\n", "second\n", "third\n"}, file.got,
		"the durable sink must receive every line")
	require.Equal(t, file.got, stream.got,
		"the stderr stream must receive exactly the same lines — this is the sink a supervisor and `docker logs` read")
}

// TestResilientTee_RetiresStderrOnceAndKeepsWritingTheFile is the resilience
// half: a stream that fails costs the stream, not the process and not the file.
func TestResilientTee_RetiresStderrOnceAndKeepsWritingTheFile(t *testing.T) {
	file := &countingWriter{}
	stream := &failingWriter{err: syscall.EPIPE}
	tee := newResilientTee(file, stream, "pid", 4242, "instance", "ABCD1234")

	for _, line := range []string{"first\n", "second\n", "third\n"} {
		_, err := tee.Write([]byte(line))
		require.NoError(t, err, "a dead stream must not surface as a write error — the file took the line")
	}

	body := strings.Join(file.got, "")
	require.Contains(t, body, "first\n")
	require.Contains(t, body, "second\n")
	require.Contains(t, body, "third\n")

	// THE DEGRADE IS RECORDED, ONCE, WHERE THE OPERATOR READS. Not silent, and
	// not once per line.
	require.Equal(t, 1, strings.Count(body, stderrRetiredMsg),
		"the retirement must be recorded exactly once in the durable sink; got %q", body)
	require.Contains(t, body, "broken pipe", "the notice must name the cause")
	require.Contains(t, body, "pid=4242", "the notice must be attributable like every other record")
	require.Contains(t, body, "instance=ABCD1234")

	// THE STREAM IS NOT RETRIED. One failed write is enough: a stream that failed
	// for a reason this process cannot fix fails on every later line.
	require.Equal(t, 1, stream.calls,
		"the stream must be written to once and then given up; got %d attempts", stream.calls)
}

// TestResilientTee_WritesTheDurableSinkFirst pins the ordering the whole design
// rests on. With the order reversed an io.MultiWriter stops at the first error,
// so a dead stream would cost the FILE every subsequent line — the failure this
// writer exists to make impossible.
func TestResilientTee_WritesTheDurableSinkFirst(t *testing.T) {
	file := &countingWriter{}
	// This stderr fails with something that is NOT EPIPE, to show the ordering
	// holds for any cause rather than for the pipe case only.
	tee := newResilientTee(file, &failingWriter{err: errors.New("stream is wedged")})

	_, err := tee.Write([]byte("the line that must not be lost\n"))
	require.NoError(t, err)
	require.Contains(t, strings.Join(file.got, ""), "the line that must not be lost",
		"the durable sink must be written BEFORE the stream that can fail")
}

// TestResilientTee_SurvivesWithNoDurableSink covers the configuration the
// retirement notice cannot reach: one sink, and its reader is gone. The writer
// must still not blow up, because the process it serves is a daemon.
func TestResilientTee_SurvivesWithNoDurableSink(t *testing.T) {
	stream := &failingWriter{err: syscall.EPIPE}
	tee := newResilientTee(nil, stream)

	n, err := tee.Write([]byte("a line with nowhere to go\n"))
	require.NoError(t, err)
	require.Equal(t, len("a line with nowhere to go\n"), n)

	_, err = tee.Write([]byte("and another\n"))
	require.NoError(t, err)
	require.Equal(t, 1, stream.calls, "the stream is given up here too; there is simply nowhere to record it")
}

// TestSetupLoggingIgnoresSIGPIPE proves the process-level disposition is
// installed, which is what turns a write to a broken fd 2 into an EPIPE the
// writer above can handle instead of a fatal signal.
func TestSetupLoggingIgnoresSIGPIPE(t *testing.T) {
	withRestoredDefaultLogger(t)

	// CONTROL FIRST, and it is a real one: signal.Ignored reports a live
	// disposition, so a signal this code never touches must read as NOT ignored
	// through the same instrument.
	//
	// THE CONTROL SIGNAL CLEARS TWO BARS AT ONCE, and that is why it is neither
	// the obvious SIGUSR2 nor the obvious SIGHUP.
	//
	// IT MUST EXIST ON WINDOWS. syscall/types_windows.go defines exactly SIGHUP,
	// SIGINT, SIGQUIT, SIGILL, SIGTRAP, SIGABRT, SIGBUS, SIGFPE, SIGKILL,
	// SIGSEGV, SIGPIPE, SIGALRM and SIGTERM. SIGUSR2, the control this test used
	// before the windows release leg was fixed, is not among them and makes the
	// package uncompilable for windows/amd64.
	//
	// IT MUST SIT OUTSIDE THE RUNTIME'S INHERITED-SIG_IGN EXCEPTION SET, which
	// is the bar SIGHUP fails. signal.Ignored reads the runtime's ignored bitmap
	// (runtime/sigqueue.go signal_ignored), and signal.Ignore is NOT that
	// bitmap's only writer: runtime/signal_unix.go initsig calls sigInitIgnored
	// whenever sigInstallGoHandler returns false on an INHERITED _SIG_IGN, and
	// sigInstallGoHandler returns false in that case for exactly _SIGHUP and
	// _SIGINT. So under nohup — or under any parent that ignores SIGHUP, which
	// in this repo is the ordinary way a long command is run — a SIGHUP control
	// reads as already ignored before this test does anything, and the assertion
	// below fires on a signal the code never touched. REPRODUCED rather than
	// reasoned: with SIGHUP here, `nohup go test -run
	// TestSetupLoggingIgnoresSIGPIPE` exits 1 while a plain run passes.
	//
	// SIGALRM CLEARS BOTH BARS: types_windows.go defines it, it is in neither
	// arm of that switch, and nothing anywhere in this tree passes it to
	// signal.Ignore or signal.Notify.
	require.False(t, signal.Ignored(syscall.SIGALRM),
		"control: the instrument must report an untouched signal as not ignored")

	setupLogging(&Config{LogFile: filepath.Join(t.TempDir(), daemonLogFileName)}, new(slog.LevelVar))

	require.True(t, signal.Ignored(syscall.SIGPIPE),
		"a process spawned to outlive its spawner must not die on a write to a broken fd 1 or 2")
}

// TestSetupLoggingRoutesCrashOutputToTheLogFile checks the third channel a dead
// stderr would cost: the runtime writes panic traces and fatal-error notices
// straight to fd 2, so they need a duplicate destination that is a file.
func TestSetupLoggingRoutesCrashOutputToTheLogFile(t *testing.T) {
	withRestoredDefaultLogger(t)
	logPath := filepath.Join(t.TempDir(), daemonLogFileName)

	// THROUGH THE SEAM, NOT BY INSPECTING THE HANDLE. Asserting that the file it
	// opened is the log file passes just as happily against a version that opens
	// it and routes nothing to it; what has to be observed is the call.
	var routed []string
	prev := setCrashOutput
	setCrashOutput = func(f *os.File) error {
		routed = append(routed, f.Name())
		return nil
	}
	t.Cleanup(func() { setCrashOutput = prev })

	// Rotation off here, so the sink IS the file handle and this test owns it.
	sink := openDurableLogSink(logPath, logRotation{})
	require.NotNil(t, sink, "the durable sink must open")
	if c, ok := sink.(io.Closer); ok {
		t.Cleanup(func() { _ = c.Close() })
	}

	require.Equal(t, []string{logPath}, routed,
		"panic traces and fatal-error notices must be duplicated into the operator's log file, since the runtime writes them to fd 2 — which may be the stream that just died")

	// CONTROL: the seam fires only when a sink actually opens, so the assertion
	// above reports a routed sink rather than a call that always happens.
	routed = nil
	require.Nil(t, openDurableLogSink(filepath.Join(t.TempDir(), "no-such-dir", daemonLogFileName), logRotation{}))
	require.Empty(t, routed, "control: an unopenable sink must route no crash output")
}

// TestCrashTraceReachesTheCurrentFileAfterARotation is the end-to-end the
// install-count proxy could never make.
//
// EVERY PART OF IT IS REAL: the shipped setupLogging, a real rotator with
// COMPRESSION ON (the shipped default), a log seeded near the bound so the
// rotation fires on the first record, the mill's compress-and-unlink, a dead
// fd 2, and a genuine panic. The trace has exactly one place left to land.
//
// TWO THINGS IT CATCHES THAT NOTHING ELSE DID. Compression is what UNLINKS the
// rotated file, so without it the old descriptor still points at something and
// the defect looks survivable — every earlier fixture ran with it off. And the
// seeded log is the RESTART case, where the byte-counting version rotated on
// write one and did not re-point for another full bound; measured then as a
// trace in neither file.
func TestCrashTraceReachesTheCurrentFileAfterARotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, daemonLogFileName)
	// JUST UNDER THE 1 MB BOUND, so the child's FIRST record crosses it and the
	// rotation is deterministic rather than a function of how many records the
	// child happens to write.
	require.NoError(t, os.WriteFile(logPath, make([]byte, (1<<20)-2048), 0o600))

	self, err := os.Executable()
	require.NoError(t, err)

	pr, pw, err := os.Pipe()
	require.NoError(t, err)

	child := exec.Command(self)
	child.Env = append(os.Environ(),
		spawnSurvivalModeEnv+"=rotate-crash-child",
		spawnSurvivalStorageEnv+"="+logPath,
		spawnSurvivalRotateSizeEnv+"=1",
	)
	child.Stdout = os.Stdout
	child.Stderr = pw // a raw fd, as a spawn hands it
	require.NoError(t, child.Start())
	require.NoError(t, pr.Close()) // the reader goes away: fd 2 is now dead
	require.NoError(t, pw.Close())
	_ = child.Wait()
	require.NotEqual(t, 0, child.ProcessState.ExitCode(), "the child must actually have crashed")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	compressed := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gz") {
			compressed = true
		}
	}
	require.True(t, compressed,
		"the run must have rotated AND compressed, or the unlink that makes this defect fatal never happened; saw %v", entries)

	b, err := os.ReadFile(logPath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	body := string(b)
	require.Contains(t, body, "past the compression",
		"the ordinary record after the rotation must be in the current file, so the assertions below read a live sink")
	require.Contains(t, body, "panic: deliberate panic after a rotation",
		"the panic must reach the CURRENT log file — fd 2 is dead and the pre-rotation inode was unlinked by the compression, so this is the only place left")
	require.Contains(t, body, "goroutine ",
		"and with its goroutine trace, not only the panic line")
}

// TestUnopenablePathIsRefusedWithRotationOnToo pins the ordering the code calls
// load-bearing and nothing observed.
//
// WHY IT MATTERS, AND WHY IT WAS INVISIBLE. A rotator touches the filesystem on
// its FIRST WRITE, never at construction, so building one for an unopenable path
// hands the caller a perfectly good-looking writer for a path it can never
// write. Opening first is what makes a nil mean "unusable". Every other
// unopenable-path assertion passes logRotation{}, which takes the non-rotating
// branch, so returning the rotator ahead of the probe left the whole suite
// green.
//
// The one caller with precondition semantics builds a zero-value rotation policy
// today, so this is a guarantee kept for the next one rather than a live defect.
func TestUnopenablePathIsRefusedWithRotationOnToo(t *testing.T) {
	unopenable := filepath.Join(t.TempDir(), "no-such-dir", daemonLogFileName)

	require.Nil(t, openDurableLogSink(unopenable, logRotation{MaxSizeMB: 50, MaxFiles: 3, MaxAgeDays: 30, Compress: true}),
		"a rotating sink must be refused for a path that cannot be opened, not handed back unwritten")

	// CONTROL: the same rotating policy on a USABLE path does return a sink, so
	// the nil above is a property of the path and not of rotation being on.
	require.NotNil(t, openDurableLogSink(filepath.Join(t.TempDir(), daemonLogFileName), logRotation{MaxSizeMB: 50}))
}

// TestCrashOutputReachesTheLogFileWhenStderrIsDead observes the crash report
// itself, in a real process, which the seam assertion above cannot do.
//
// WHY BOTH EXIST. The seam proves the call is made with the right file and costs
// nothing. This one proves the RUNTIME honors it under the condition that
// matters: fd 2 is a pipe with no reader, so the crash report the runtime writes
// there goes nowhere, and the durable file is the only place an operator could
// read it. A process that actually panics is not something the seam can stand in
// for.
func TestCrashOutputReachesTheLogFileWhenStderrIsDead(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), daemonLogFileName)

	self, err := os.Executable()
	require.NoError(t, err)

	pr, pw, err := os.Pipe()
	require.NoError(t, err)

	child := exec.Command(self)
	child.Env = append(os.Environ(),
		spawnSurvivalModeEnv+"=crash-child",
		spawnSurvivalStorageEnv+"="+logPath,
	)
	child.Stdout = os.Stdout
	// *os.File, so the child inherits a raw fd 2 exactly as a real spawn hands it.
	child.Stderr = pw
	require.NoError(t, child.Start())
	// The reader goes away, so the runtime's own crash write has nowhere to land.
	require.NoError(t, pr.Close())
	require.NoError(t, pw.Close())
	_ = child.Wait()

	require.NotEqual(t, 0, child.ProcessState.ExitCode(), "the child must actually have crashed")

	b, err := os.ReadFile(logPath) //nolint:gosec // under t.TempDir()
	require.NoError(t, err)
	body := string(b)

	require.Contains(t, body, "crash-child: about to panic",
		"the ordinary log record must be in the file too, so the assertions below are reading a live sink")
	require.Contains(t, body, "panic: deliberate panic with a dead stderr",
		"the panic message must reach the durable file — fd 2 is a closed pipe, so this is the only place it can be read")
	require.Contains(t, body, "goroutine ",
		"the crash report must carry its goroutine trace, not only the panic line")
}
