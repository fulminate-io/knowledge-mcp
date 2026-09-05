// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"runtime/debug"
	"sync"
)

// defaultClientMemLimit is the soft heap ceiling (GOMEMLIMIT) the knowledge
// client imposes on itself when nothing else has set one. A collect
// spikes the heap — the chunker holds every file's parse results live
// until upload — and with the default
// GOGC the runtime lets the heap run to 2× live before collecting, then
// (on macOS) the freed pages linger in RSS. A 4 GiB soft limit makes GC
// press earlier so the footprint stays bounded. It's a *soft* limit: if
// a genuinely large repo's live working set exceeds it the runtime goes
// over rather than crash (collects just get GC-heavier). In a container,
// override via the GOMEMLIMIT env var in the pod manifest, sized to the
// per-collect working set you want to allow.
const defaultClientMemLimit = 4 << 30 // 4 GiB

// applyMemoryLimit imposes defaultClientMemLimit unless a GOMEMLIMIT env
// var already configured one. SetMemoryLimit(-1) returns the active
// limit without changing it; the zero-config default is math.MaxInt64.
func applyMemoryLimit() {
	if debug.SetMemoryLimit(-1) != math.MaxInt64 {
		return // GOMEMLIMIT env var already in effect — respect it
	}
	debug.SetMemoryLimit(defaultClientMemLimit)
	slog.Info("soft memory limit applied", "bytes", defaultClientMemLimit, "override_env", "GOMEMLIMIT")
}

// setupLogging points slog at stderr, and ADDITIONALLY at the --log-file path
// when that flag is set. Both sinks receive every line: the writer is tee'd,
// never replaced.
//
// THIS DOC ONCE CLAIMED "stderr (always) plus an optional log file" WHILE THE
// CODE REPLACED THE WRITER — the claim is now true because the code makes it
// true, not because the words were left alone. The file is the durable record;
// stderr is the stream a process supervisor redirects and a container runtime
// captures. Removing either sink reproduces the sink-nobody-reads defect.
//
// THE TEE IS RESILIENT RATHER THAN PLAIN, and that is what lets a process keep
// an inherited stderr it is designed to outlive: the file is written FIRST,
// SIGPIPE is ignored so a broken fd 2 returns EPIPE instead of killing the
// process, and a stream that fails is retired with one line recorded in the
// file. spawn_detached_stdio.go carries the measurement; log_sinks.go has the
// writer.
//
// ONLY TWO PROCESSES REACH THIS FUNCTION, and they are exactly the ones spawned
// to outlive their spawner: the daemon (runServe) and the restart handoff child
// (runRestartDaemon). An ordinary CLI invocation never calls it, so it keeps the
// default SIGPIPE behavior that makes `knowledge ... | head` stop early.
//
// EVERY RECORD CARRIES pid AND instance, and that is attribution rather than
// decoration: several processes legitimately append to one log file — the daemon
// and the handoff child that restarts it write to the same path, and containers
// sharing a bind-mounted store share it too — and a line that names no process
// cannot be attributed to one. pid alone is not enough, because containers have
// their own pid namespaces and two daemons can both be pid 6; instance is
// per-process random, so any two writers are distinguishable.
//
// IT RETURNS THE DURABLE SINK, or nil when none was configured or the configured
// one could not be opened. Callers whose whole reason for configuring logging is
// a record that survives a dead stderr treat a nil as a precondition failure;
// see installServeLogging and runRestartDaemon, which both refuse.
func setupLogging(cfg *Config, lvl *slog.LevelVar) io.Writer {
	attrs := []any{"pid", os.Getpid(), "instance", processInstanceID()}
	// A NONSENSICAL RETENTION POLICY IS REPORTED AND THE FILE SINK IS REFUSED,
	// never quietly read as "rotation off" — that would retire the operator's log
	// retention without saying so. Every caller in the tree turns this nil into a
	// refusal; the report exists for the one that has already started logging.
	rot, err := rotationFromConfig(cfg)
	if err != nil {
		// NAMING NO FILE WHEN NONE WAS ASKED FOR: the operator who passed a bad
		// rotation flag and no --log-file was told "refusing the log file :".
		if cfg.LogFile != "" {
			fmt.Fprintf(os.Stderr, "refusing the log file %s: %v\n", cfg.LogFile, err)
		} else {
			fmt.Fprintf(os.Stderr, "refusing to configure log rotation: %v\n", err)
		}
		return detachedProcessLogging("", logRotation{}, lvl, attrs...)
	}
	return detachedProcessLogging(cfg.LogFile, rot, lvl, attrs...)
}

// installServeLogging is `knowledge serve`'s logging setup, and it REFUSES ON
// BAD INPUT rather than starting without the sink the operator asked for.
//
// A retention bound with no meaning is a command line the operator got wrong.
// The alternative to refusing is a daemon that serves normally with no durable
// log — the single-sink state this whole seam exists to make impossible — while
// the only notice of it goes to a stream that may already be dead. The restart
// handoff already refuses on this condition; this makes the daemon agree.
//
// AN INVALID --log-level IS DELIBERATELY DIFFERENT and stays a warning at the
// caller: it has a meaningful fallback, info, that costs nothing durable. A
// refused rotation policy has no fallback that keeps the operator's file.
//
// NOTHING ON THE NORMAL PATH CHANGES. Valid bounds, and the absence of every
// rotation flag, reach setupLogging exactly as before.
func installServeLogging(cfg *Config, lvl *slog.LevelVar) error {
	if _, err := rotationFromConfig(cfg); err != nil {
		if cfg.LogFile != "" {
			return fmt.Errorf("knowledge serve: refusing to start without the durable log %s: %w", cfg.LogFile, err)
		}
		return fmt.Errorf("knowledge serve: %w", err)
	}
	setupLogging(cfg, lvl)
	return nil
}

// processInstanceID is a short random identifier minted once per process.
//
// crypto/rand.Text is the source rather than a time or pid seed: two daemons
// started in the same second, in different containers, from the same image, are
// exactly the pair that has to be told apart, and both of those seeds collide
// for them.
func processInstanceID() string {
	instanceIDOnce.Do(func() { instanceID = rand.Text()[:8] })
	return instanceID
}

var (
	instanceIDOnce sync.Once
	instanceID     string
)

// Run is the entry point cmd/knowledge/main.go falls through to when the
// invocation is bare `knowledge` — no recognized subcommand.
//
// Post-cutover there is no per-session stdio MCP serving: editors
// and dream workers connect to the one shared `knowledge serve` daemon over
// its loopback streamable-HTTP MCP endpoint. Bare `knowledge` therefore no
// longer serves MCP over stdin/stdout — it returns a clear error directing the
// caller to the daemon (run `knowledge serve`, or `brew services start
// knowledge`). The lifecycle subcommands (start/stop/status), install-asset
// subcommands, login/logout, doctor, and serve are dispatched upstream in
// RunSubcommand and never reach here.
func Run(_ Config) error {
	return fmt.Errorf("`knowledge` no longer serves MCP over stdio; run the shared daemon with `knowledge serve` " +
		"(or `brew services start knowledge`) and point your editor at its MCP endpoint — " +
		"`knowledge install-claude-assets` / `install-codex-assets` wire it for you")
}
