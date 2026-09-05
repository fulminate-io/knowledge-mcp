// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"

	"gopkg.in/natefinch/lumberjack.v2"
)

// THE LOG WRITER THAT SURVIVES ITS STDERR. Read spawn_detached_stdio.go first
// for why both sinks are required and why the child must outlive the fd.
//
// THIS CODE IS DELIBERATELY DUPLICATED in cmd/knowledge-server's bootstrap
// package. The two binaries share no hand-written package — the only
// cross-module contract is generated protobuf — and both spawn or are spawned
// with an inherited stderr that can die under them, so both need this. A shared
// utility package would be a third home for a contract that has two.

// stderrRetiredMsg is the one line recorded in the durable sink when the stderr
// stream is given up. It is a constant because a test asserts on it and an
// operator greps for it.
const stderrRetiredMsg = "log stderr sink closed; continuing with the file sink only"

// setCrashOutput is the debug.SetCrashOutput seam, so a test can observe that
// crash routing was installed ON THE LOG FILE without provoking a real crash.
// Asserting the opened handle's identity instead would pass just as happily
// against a version that opened the file and routed nothing to it.
//
//nolint:gochecknoglobals // overridable seam for testability.
var setCrashOutput = func(f *os.File) error { return debug.SetCrashOutput(f, debug.CrashOptions{}) }

// resilientTee writes every log record to a durable file sink and to the
// inherited stderr stream, and gives the stream up rather than the process when
// it breaks.
//
// ORDER IS THE WHOLE POINT: the FILE IS WRITTEN FIRST. A record that reaches
// only one sink must reach the durable one, and stderr is the sink that can die.
// io.MultiWriter cannot express this — it stops at the first error, so with
// stderr in front a broken stream would cost the file every subsequent line.
type resilientTee struct {
	mu     sync.Mutex
	file   io.Writer // durable sink; nil when no --log-file is configured
	stderr io.Writer // supervisor / container stream; nil once retired
	attrs  []any     // pid + instance, so the retirement notice is attributable
}

// newResilientTee builds the writer. Either sink may be nil.
func newResilientTee(file, stderr io.Writer, attrs ...any) *resilientTee {
	return &resilientTee{file: file, stderr: stderr, attrs: attrs}
}

// Write fans the record out, file first.
//
// It reports len(p) written whenever the record reached the durable sink, since
// a retired stderr is a state this writer handles rather than an error its
// caller can act on — slog discards handler errors regardless, which is exactly
// why the degrade has to be recorded in the log rather than returned.
func (t *resilientTee) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var fileErr error
	if t.file != nil {
		if _, err := t.file.Write(p); err != nil {
			fileErr = err
		}
	}
	if t.stderr != nil {
		if _, err := t.stderr.Write(p); err != nil {
			t.retireStderrLocked(err)
		}
	}
	return len(p), fileErr
}

// retireStderrLocked drops the stream and records why, exactly once.
//
// ANY error retires it, not EPIPE alone. A stream that has failed once for a
// reason this process cannot fix will fail on every subsequent line, and the
// alternative is one duplicate error notice per log record. The cause is named
// in the notice, so EPIPE (the reader went away) stays distinguishable from
// anything else.
//
// WHEN THERE IS NO FILE SINK the notice has nowhere to go and the process is
// left logging nowhere. That is a configuration with exactly one sink whose
// reader has gone; before this writer existed the same configuration KILLED the
// process instead, which is strictly worse and equally silent.
func (t *resilientTee) retireStderrLocked(cause error) {
	t.stderr = nil
	if t.file == nil {
		return
	}
	// Written straight to the file sink rather than through this writer: it must
	// not re-enter Write, and the stream it would describe is already gone.
	notice := slog.New(slog.NewTextHandler(t.file, &slog.HandlerOptions{Level: slog.LevelWarn}))
	notice.With(t.attrs...).Warn(stderrRetiredMsg, "err", cause.Error())
}

// sigpipeOnce keeps the process-global signal disposition a one-time install.
var sigpipeOnce sync.Once

// ignoreSIGPIPE makes a write to a broken fd 1 or 2 return EPIPE to the caller
// instead of killing the process.
//
// WITHOUT IT, Go's runtime treats SIGPIPE on those two descriptors specially: it
// resets the handler to SIG_DFL and re-raises, so the process dies with wait
// status 13 mid-write. Writes to any OTHER descriptor already return EPIPE, so
// this changes nothing for sockets or ordinary files.
//
// IT IS INSTALLED ONLY BY PROCESSES SPAWNED TO OUTLIVE THEIR SPAWNER — the
// daemon and the restart handoff child. An ordinary CLI invocation must keep
// dying on a broken stdout, because that is what makes `knowledge ... | head`
// stop early instead of computing output nobody will read.
func ignoreSIGPIPE() {
	sigpipeOnce.Do(func() { signal.Ignore(syscall.SIGPIPE) })
}

// openDurableLogSink opens the durable log file and routes crash output to it,
// or returns nil having reported the failure on stderr.
//
// CRASH OUTPUT GOES HERE TOO, and that is the third channel a dead stderr would
// otherwise cost. A panic trace and the runtime's fatal-error notices are
// written by the runtime straight to fd 2, which may be the stream that just
// died; SetCrashOutput duplicates this file's descriptor and the runtime writes
// the report there IN ADDITION to stderr. Measured: with stderr a broken pipe,
// the panic trace still lands in this file. A failure to install it is reported
// and the process continues, because losing crash duplication is not a reason to
// refuse to run.
// THE OPEN IS ALSO THE PROBE, and that ordering is load-bearing once rotation
// exists. A lumberjack rotator does not touch the filesystem until its first
// write, so a caller that needs to know NOW whether the sink is usable — the
// restart handoff, whose whole point is a recorded outcome — would get a
// non-nil writer for a path it can never write. Opening first answers that, and
// the same handle is what crash output needs.
func openDurableLogSink(logPath string, rot logRotation) io.Writer {
	path := expandTilde(logPath)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // path derived from the resolved graph-storage directory
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open log file %s: %v\n", path, err)
		return nil
	}
	if err := setCrashOutput(f); err != nil {
		fmt.Fprintf(os.Stderr, "failed to route crash output to %s: %v\n", path, err)
	}
	if rot.MaxSizeMB <= 0 {
		// Rotation off by explicit configuration: the handle IS the sink, and it
		// stays open for the life of the process.
		return f
	}
	// ROTATION ON. The crash-output descriptor was duplicated by the runtime, so
	// this handle has done its work and is closed; the rotator opens its own.
	//
	// AND THE CRASH DESCRIPTOR IS RE-POINTED AFTER EACH ROTATION, which is not a
	// nicety. A rotation RENAMES the active file, so the duplicated descriptor
	// follows the renamed inode; compression then rewrites that backup as .gz and
	// REMOVES the original, leaving the crash descriptor pointing at an unlinked
	// inode. A panic after the first rotation-and-compress would be written to a
	// deleted file and reach nobody — the sink-nobody-reads defect, in the exact
	// channel this seam exists to preserve.
	if cerr := f.Close(); cerr != nil {
		fmt.Fprintf(os.Stderr, "failed to close the probe handle for %s: %v\n", path, cerr)
	}
	return followCrashOutput(rotatingWriter(path, rot), path, rot.MaxSizeMB)
}

// crashFollowingSink wraps the rotating writer and keeps the crash-output
// descriptor pointed at the CURRENT log file.
//
// IT OBSERVES THE ROTATION RATHER THAN PREDICTING IT, and that is a correction.
// The first version counted bytes and re-pointed on its own arithmetic, on the
// reasoning that it "counts the same bytes as the rotator". That is FALSE on
// every restart: lumberjack SEEDS its size from the file already on disk and
// this counter started at zero. Measured — a daemon opening a 900 KB log under a
// 1 MB bound rotates on its FIRST write, and the arithmetic would not re-point
// for another full bound; a real panic 180 records later put the trace in
// neither file. At the shipped 50 MB bound with a 40 MB log at startup that is a
// 40 MB blind window.
//
// SO IT ASKS THE QUESTION THAT CANNOT DRIFT: is the file at this path still the
// file the crash descriptor points at? A stat answers that from the filesystem,
// so it is right whatever caused the rotation — the size bound, a restart onto a
// part-full file, an explicit rotate, an operator moving the file. Arithmetic
// can only ever be right about the one cause it models.
//
// THE COST IS ONE STAT PER RECORD on the durable-sink path, and it is not free:
// measured on this platform, a representative record costs 1129 ns/op to write
// and 3488 ns/op to write-then-stat, so the stat is about 2.4 microseconds and
// roughly triples the per-record cost of the durable sink. At the rate a daemon
// logs — tens to hundreds of records a second — that is a few hundred
// microseconds per second, and it buys a guarantee arithmetic cannot give. It
// would matter at tens of thousands of records a second, which this log is not,
// and it is paid only by a sink that actually rotates: a non-rotating one is
// never wrapped, because nothing renames it.
type crashFollowingSink struct {
	mu        sync.Mutex
	w         io.Writer
	path      string
	installed os.FileInfo // the file crash output points at; nil means unknown
}

func (s *crashFollowingSink) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)

	s.mu.Lock()
	defer s.mu.Unlock()
	cur, statErr := os.Stat(s.path)
	if statErr != nil {
		// The path is gone entirely; there is nothing to re-point at, and the
		// write that recreates it will be observed then.
		return n, err
	}
	if s.installed != nil && os.SameFile(s.installed, cur) {
		return n, err
	}
	s.repointLocked()
	return n, err
}

// repointLocked installs crash output on whatever file the path names now.
func (s *crashFollowingSink) repointLocked() {
	//nolint:gosec // s.path is the same operator-configured log path this sink was constructed for and already opened once
	f, oerr := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if oerr != nil {
		// Reported into the log itself: stderr may be the stream that already
		// died, and this is precisely a message about crash output going missing.
		fmt.Fprintf(s.w, "failed to re-point crash output onto %s: %v\n", s.path, oerr)
		return
	}
	defer f.Close()
	if serr := setCrashOutput(f); serr != nil {
		fmt.Fprintf(s.w, "failed to re-point crash output onto %s: %v\n", s.path, serr)
		return
	}
	if info, ierr := f.Stat(); ierr == nil {
		s.installed = info
	}
}

// followCrashOutput wraps a durable sink so its crash descriptor tracks
// rotations. A NON-ROTATING sink is returned unchanged: nothing renames it, so
// the per-write stat would buy nothing.
func followCrashOutput(w io.Writer, path string, maxSizeMB int) io.Writer {
	if w == nil || maxSizeMB <= 0 {
		return w
	}
	s := &crashFollowingSink{w: w, path: path}
	// Seeded from the file crash output was just installed on, so the first
	// write does not re-point needlessly. An absent file leaves it nil and the
	// first write installs.
	//
	//nolint:gosec // path is the operator-configured log path this caller has already opened
	if info, err := os.Stat(path); err == nil {
		s.installed = info
	}
	return s
}

// rotatingWriter is the size/backup/age-bounded sink itself.
func rotatingWriter(path string, rot logRotation) io.Writer {
	return &lumberjack.Logger{
		Filename:   path,
		MaxSize:    rot.MaxSizeMB,
		MaxBackups: rot.MaxFiles,
		MaxAge:     rot.MaxAgeDays,
		Compress:   rot.Compress,
	}
}

// logRotation is the retention policy for the durable sink, mirroring the
// server's flags rather than sharing its code.
type logRotation struct {
	MaxSizeMB  int
	MaxFiles   int
	MaxAgeDays int
	Compress   bool
}

// rotationFromConfig reads the policy off the parsed config and REFUSES a
// nonsensical one rather than coercing it.
//
// ZERO IS A CHOICE AND NEGATIVE IS AN ERROR. Zero max-size means "never rotate",
// which an operator may legitimately want and the server spells the same way;
// zero backups and zero age mean "prune by the other bound only". A NEGATIVE
// value means the operator asked for something with no meaning, and silently
// reading it as "off" would retire their log retention without telling them.
func rotationFromConfig(cfg *Config) (logRotation, error) {
	for _, f := range []struct {
		flag string
		v    int
	}{
		{"log-rotate-max-size-mb", cfg.LogRotateMaxSizeMB},
		{"log-rotate-max-files", cfg.LogRotateMaxFiles},
		{"log-rotate-max-age-days", cfg.LogRotateMaxAgeDay},
	} {
		if f.v < 0 {
			return logRotation{}, fmt.Errorf("--%s is %d: a negative retention bound has no meaning, and reading it as 'disabled' would retire log retention without saying so", f.flag, f.v)
		}
	}
	return logRotation{
		MaxSizeMB:  cfg.LogRotateMaxSizeMB,
		MaxFiles:   cfg.LogRotateMaxFiles,
		MaxAgeDays: cfg.LogRotateMaxAgeDay,
		Compress:   cfg.LogRotateCompress,
	}, nil
}

// detachedProcessLogging is the setup every spawned-to-outlive client process
// runs: tolerate a dying stderr, then point slog at both sinks.
//
// Returns the durable sink, or nil when none is configured or it could not be
// opened, having reported that on stderr.
func detachedProcessLogging(logPath string, rot logRotation, lvl *slog.LevelVar, attrs ...any) io.Writer {
	ignoreSIGPIPE()
	var fileSink io.Writer
	if logPath != "" {
		if w := openDurableLogSink(logPath, rot); w != nil {
			fileSink = w
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(
		newResilientTee(fileSink, os.Stderr, attrs...),
		&slog.HandlerOptions{Level: lvl},
	)).With(attrs...))
	return fileSink
}
