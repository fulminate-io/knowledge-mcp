// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
)

// A PROCESS SPAWNED TO OUTLIVE ITS SPAWNER KEEPS THE INHERITED STDERR AND
// SURVIVES ITS DEATH. This file is the standing account of why, because the
// property is easy to break in either direction.
//
// TWO REQUIREMENTS, BOTH BINDING.
//
// ONE, BOTH SINKS. Every daemon and server spawned BY THIS PACKAGE writes its
// log lines to its durable --log-file AND to the stderr it inherits from its
// spawner. The file is what an operator reads after the fact and is rotated; the
// inherited stderr is what a process supervisor redirects and what `docker logs`
// shows. In the shipped container that fd is PID 1's stderr: the frontend spawns
// the daemon with it (cmd/frontend/daemon.go), the daemon spawns the server with
// it, and the container's captured output is the only channel an operator has
// while it runs. A file inside an ephemeral container is a record nobody can
// reach, so removing that stream reproduces the sink-nobody-reads defect this
// repository already fixed once.
//
// ONE SPAWN SITE SITS OUTSIDE THAT SENTENCE, AND IT IS THE CONTAINER'S OWN
// DAEMON. This doc claimed otherwise and was wrong. cmd/frontend's daemonArgv
// passes serve / --http-port / --graph-storage / --log-level and NO --log-file,
// so the daemon PID 1 starts has exactly ONE sink: the stream. It survives that
// stream dying, by TWO below — and then logs nowhere, with the notice saying the
// stream is gone having nowhere to go either. Measured with that exact argv: the
// daemon stayed up and kept serving and no log file was written anywhere.
//
// THAT GAP IS RECORDED HERE RATHER THAN CLOSED HERE. Giving that spawn the
// durable half is a one line change to a file this package does not own, and it
// carries a cost that deserves a decision rather than an assumption: the client
// writes its log unrotated, so a long-lived container would grow that file
// without bound inside the user's mounted volume.
//
// TWO, THE CHILD OUTLIVES THE FD. That inherited stderr belongs to a process the
// child is designed to outlive. When it is a pipe, its reader goes away — in the
// field, about twelve seconds after the spawning process exited — and a write to
// fd 1 or 2 on a broken pipe raises SIGPIPE, which the Go runtime resets to
// SIG_DFL and re-raises. The process dies with wait status 13, no shutdown line,
// no panic and no fatal line, because it never ran another instruction.
// Measured: an upgraded daemon served four requests and then died.
//
// HOW BOTH HOLD AT ONCE. They are reconciled in the CHILD's logging setup, not
// by taking the stream away:
//
//   - The process ignores SIGPIPE, so a write to a broken fd 1 or 2 returns
//     EPIPE to the caller instead of killing it. Measured both ways: with the
//     signal ignored the write returns EPIPE and the process exits 0; without
//     it the same write leaves wait status 13.
//   - The log writer writes the DURABLE FILE FIRST and the stderr stream
//     second, so a dead stream can never cost the file a line.
//   - On the first failed stderr write the stream is retired and one line
//     recording that is written to the file. Not a silent degrade: the event is
//     logged where the operator reads, with the cause named.
//   - Crash output is duplicated to the log file via debug.SetCrashOutput, so a
//     panic trace survives a dead stderr too.
//
// WHAT NOT TO DO INSTEAD. Pointing the child's stdio at the log file removes the
// supervisor's stream and the container's, and drops the server's log rotation
// with it, since --log-file is the switch that builds the rotator. Pointing it
// at /dev/null loses every crash notice. Handing exec.Cmd any non-*os.File
// writer to tee from the SPAWNER's side makes it create a pipe drained by a
// PARENT-LIFETIME copier goroutine, which kills the child by another route —
// which is why every tee in this system lives inside the child.

// daemonLogFileName is the daemon's durable log, the file a bare install's
// operator reads. It is the sink for the daemon itself and for the restart
// handoff child, and it is the path the dev harness names.
//
// The SERVICE UNITS name their own files instead: launchd writes
// <label>.out.log and <label>.err.log (renderLaunchdPlist), and a systemd unit
// sets no log path at all, so its journal takes the stream.
const daemonLogFileName = "knowledge-daemon.log"

// daemonLogPath resolves that file inside the graph storage directory.
func daemonLogPath(graphStorage string) string {
	return filepath.Join(expandTilde(graphStorage), daemonLogFileName)
}

// ensureLogDir guarantees the directory a spawned child will open its
// --log-file in exists, so the child comes up with two sinks rather than
// reporting a failed open and running on stderr alone.
//
// AN ERROR HERE FAILS THE SPAWN, naming the path. A child cannot be given a
// second sink after the fact, and a daemon that starts with one sink is the
// state this seam exists to avoid; a directory that cannot be created is a
// condition to report, not to start a half-configured daemon past.
func ensureLogDir(logPath string) error {
	dir := filepath.Dir(logPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the log directory %s for the spawned child's %s: %w", dir, filepath.Base(logPath), err)
	}
	return nil
}

// ensureDurableLogWritable proves, BEFORE a fork, that the child will be able to
// open the durable sink it is about to be told to use.
//
// AN OPEN, NOT A STAT, and with the child's own flags. A directory that exists
// but is unwritable, and a path where a directory stands in the file's place,
// both satisfy a stat and both fail the open. The failure has to surface HERE,
// on the side that still has a working log to report it on, rather than inside a
// child whose only channel is a stream that may already be dead.
//
// O_CREATE|O_APPEND, never O_TRUNC: this creates the file when absent and adds
// nothing to one that exists. The handle is closed immediately; the child opens
// its own.
func ensureDurableLogWritable(logPath string) error {
	if err := ensureLogDir(logPath); err != nil {
		return err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // path derived from the resolved graph-storage directory
	if err != nil {
		return fmt.Errorf("open the durable log %s: %w", logPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close the durable log %s after the writability probe: %w", logPath, err)
	}
	return nil
}
