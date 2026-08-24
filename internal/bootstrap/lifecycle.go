// SPDX-License-Identifier: Apache-2.0

// lifecycle.go — server-discovery + spawn helpers for the `knowledge` client
// binary and the knowledge start/stop/status CLI subcommands.
//
// findServerBinary locates the knowledge-server executable (same-dir
// then $PATH); spawnServer launches it with a bare exec.Command and
// no SysProcAttr — no Setsid, no Setpgid, no session-leader creation.
// waitForServer polls for HealthService.Check readiness.
//
// Why no Setsid: macOS Tahoe (26.x) has a kernel bug where
// kern.max_task_pmem overflows to 0 on high-RAM systems, which causes
// runningboardd to mishandle the memory-assertion path that session-
// leader creation rides. spawning with Setsid: true reproducibly
// crashed the dev laptop (Apple Community thread 256222481). Plain
// fork+exec rides a different code path and works fine. We don't
// actually need Setsid for our use case anyway: when the parent exits
// the kernel reparents the child to launchd for free, without any
// session-detachment ceremony. That holds however the parent was
// started — from a terminal for the CLI subcommands, or from a service
// manager. (An earlier form of this note also argued the parent has no
// controlling terminal because it is driven over stdio pipes; that was
// a stdio-era claim, and it is not true of the CLI subcommands.)
//
// STDOUT-DISCIPLINE, stated as the observable rather than as architecture:
// this binary's stdout carries USER-FACING CLI OUTPUT — human-readable text
// people read and pipe. So helpers in this file route ALL diagnostics through
// slog (which writes to stderr by default). NO fmt.Print*, os.Stdout.Write*,
// or log.Print* in this file; `grep -nE 'os\.Stdout|fmt\.Print' lifecycle.go`
// returns this line and nothing else. CLI-subcommand callers (knowledge
// start/stop/status) write to stdout from their own files
// (lifecycle_subcommand.go) — that's the carve-out.

package bootstrap

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// serverBinaryName is the basename of the graph server executable.
// Windows appends .exe in findServerBinary.
const serverBinaryName = "knowledge-server"

// ServerBinaryNotFoundError is returned by findServerBinary when neither
// the same-directory sibling nor a $PATH lookup resolves the
// knowledge-server binary. Callers can type-assert to surface
// SearchedDir + LookPathErr in the recovery message.
type ServerBinaryNotFoundError struct {
	// SearchedDir is the directory we tried first (the directory of the
	// running knowledge binary). Empty when os.Executable() itself failed.
	SearchedDir string
	// ExecutableErr is the error returned by os.Executable when
	// discovery of the running binary's path failed. Nil when
	// SearchedDir is set.
	ExecutableErr error
	// LookPathErr is the error returned by exec.LookPath on the $PATH
	// fallback. Always populated — we only return this error type
	// when both lookups failed.
	LookPathErr error
}

func (e *ServerBinaryNotFoundError) Error() string {
	if e.ExecutableErr != nil {
		return fmt.Sprintf("knowledge-server not found: os.Executable failed (%v) and $PATH lookup failed (%v); run `knowledge install` to download it", e.ExecutableErr, e.LookPathErr)
	}
	return fmt.Sprintf("knowledge-server not found in same directory as the knowledge binary (%s) or in $PATH (%v); run `knowledge install` to download it", e.SearchedDir, e.LookPathErr)
}

// getExecutable is a stubbable alias for os.Executable. Tests override
// it to point findServerBinary at a fake knowledge binary in a tempdir
// without re-execing the test binary itself.
var getExecutable = os.Executable

// findServerBinary returns the absolute path to knowledge-server.
// Lookup order:
//
//  1. Same directory as the running knowledge binary (os.Executable() →
//     filepath.Dir → join "knowledge-server" + ".exe" on Windows).
//     The canonical install layout puts both binaries side-by-side
//     in bin/ or under a Homebrew prefix.
//  2. exec.LookPath fallback so users with knowledge-server elsewhere
//     on $PATH still get a working spawn.
func findServerBinary() (string, error) {
	binName := serverBinaryName
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}

	exe, exeErr := getExecutable()
	if path, ok := lookupSibling(exe, exeErr, binName); ok {
		return path, nil
	}

	p, lpErr := exec.LookPath(binName)
	if lpErr == nil {
		return p, nil
	}

	searched := ""
	if exeErr == nil {
		searched = filepath.Dir(exe)
	}
	return "", &ServerBinaryNotFoundError{
		SearchedDir:   searched,
		ExecutableErr: exeErr,
		LookPathErr:   lpErr,
	}
}

// serverBinaryInstalled reports whether findServerBinary can resolve the
// knowledge-server executable (sibling or $PATH). The boolean is all the
// doctor server-down hint needs to distinguish "not installed" (suggest
// `knowledge install`) from "installed but not running" (suggest
// `knowledge start`).
func serverBinaryInstalled() bool {
	_, err := findServerBinary()
	return err == nil
}

// lookupSibling tries to resolve binName next to the given executable
// path. Returns ("", false) when exeErr is set, when the candidate
// doesn't exist, or when stat fails. Returns the absolute path
// otherwise.
func lookupSibling(exe string, exeErr error, binName string) (string, bool) {
	if exeErr != nil {
		return "", false
	}
	// Resolve symlinks so a Homebrew-style symlink in /opt/homebrew/bin
	// pointing at the real Cellar location finds its sibling.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	candidate := filepath.Join(filepath.Dir(exe), binName)
	info, statErr := os.Stat(candidate)
	if statErr != nil || info.IsDir() {
		return "", false
	}
	if abs, err := filepath.Abs(candidate); err == nil {
		return abs, true
	}
	return candidate, true
}

// SpawnArgs captures everything spawnServer needs to know to launch
// the knowledge-server binary. Centralizing it here so the auto-spawn
// path (ensureServerReachable) and the explicit-spawn path
// (lifecycle_subcommand.go runStart) both build the same argv shape.
type SpawnArgs struct {
	// BinPath is the absolute path to knowledge-server.
	BinPath string
	// Port is the TCP port the server should listen on.
	Port int
	// Root is the project root the server should consider its default
	// active repo. Defaults to "." but the CLI passes through whatever
	// --root the knowledge client got.
	Root string
	// GraphStorage is the directory where graph .bin files live. The
	// server-spawn log file lands inside this directory too so test
	// runs with t.TempDir() roots don't pollute the dev's real log.
	GraphStorage string
	// Pprof appends --pprof to the spawned server's argv, mounting its
	// /debug/pprof/ handlers on the server's OWN main mux (the --port
	// listener), which binds loopback. The server has no separate profiling
	// port: cmd/knowledge-server/internal/server/server.go mountRoutes
	// registers the handlers beside the Connect services.
	//
	// It is threaded from the daemon's own --pprof rather than being a
	// second knob, so one flag describes one intent — "profile this
	// installation" — across both processes.
	Pprof bool
}

// spawnServer launches knowledge-server as a regular subprocess of the
// caller. BOTH of the child's streams are pointed at THIS PROCESS'S STDERR and
// stdin is /dev/null.
//
// STDERR, NEVER STDOUT, and that is the original invariant preserved rather than
// a detail. THIS PROCESS WRITES USER-FACING CLI OUTPUT TO STDOUT — `knowledge
// start/stop/status` and `knowledge setup` print human-readable text people read
// and pipe (lifecycle_subcommand.go and setup_restart.go, both callers of this
// function; 16 non-test files under internal/bootstrap write to stdout). A
// child's log lines interleaved into that would corrupt what the user sees and
// anything parsing it. Pointing at stderr keeps the child's output off that
// channel while still delivering it somewhere a supervisor or container runtime
// can read.
//
// A DIFFERENT BINARY has a stricter form of the same constraint, and it is
// cmd/frontend rather than this one: in --stdio mode ONLY, the frontend's own
// stdout IS a newline-framed JSON-RPC channel, and it protects it the same way
// (cmd/frontend/daemon.go). `knowledge` itself does not serve MCP over stdio at
// all — bootstrap.Run returns an error saying exactly that.
//
// The child's DURABLE log file is no longer opened here: the server opens it
// itself via --log-file and TEES it with its inherited stderr, so both sinks get
// every line. That direction is required, not stylistic — a tee on this side
// would mean a non-*os.File writer on exec.Cmd, hence a pipe with a
// parent-lifetime copier, which a child designed to outlive us cannot survive.
//
// NO SysProcAttr is set — the child is a regular
// fork+exec child of the parent. When the parent exits, the kernel
// reparents the child to launchd (or systemd / init on other Unixes)
// without any session-detachment work from us.
//
// After Start succeeds, we call cmd.Process.Release() to free Go's
// internal Wait bookkeeping. The kernel handles process reaping
// independently — we never call Wait, so Release is the right
// signal that "we don't care anymore." Without Release, Go retains
// a goroutine + pipe until the process exits.
//
// Returns the PID of the spawned process. We deliberately don't return
// *os.Process because Release() zeros its Pid field on completion;
// capturing the PID here before Release keeps the value usable for the
// caller's diagnostic output. Errors from Start are wrapped with the
// binary path so the recovery message names what we tried to launch.
// serverSpawnArgv is the exact argv the client starts knowledge-server with.
//
// IT IS A NAMED FUNCTION SO A TEST CAN ASSERT EVERY TOKEN. Inline, the profiling
// opt-in below is invisible to any check short of running a real spawn: an argv
// that never gains --pprof and one that always carries it both compile, both
// start a working server, and the difference shows up only as a profiling
// endpoint that is missing when asked for or open when it was not.
// serverLogPath is the server's durable log file, inside GraphStorage so a test
// run with a t.TempDir() root never writes to the dev's real log.
//
// The SERVER opens it, via --log-file, rather than this process opening it and
// handing over the fd — see spawnServer for why that direction is load-bearing.
func serverLogPath(graphStorage string) string {
	return filepath.Join(expandTilde(graphStorage), "server.log")
}

func serverSpawnArgv(args SpawnArgs) []string {
	argv := []string{
		"--port", fmt.Sprintf("%d", args.Port),
		"--root", args.Root,
		"--graph-storage", args.GraphStorage,
		// The server tees this file WITH its stderr; it does not replace stderr.
		// Both sinks matter: the file is the durable record, and the inherited
		// stderr is what a supervisor or container runtime captures.
		"--log-file", serverLogPath(args.GraphStorage),
	}
	// Appended only when asked for, so the default spawn argv stays exactly the
	// three flags above.
	if args.Pprof {
		argv = append(argv, "--pprof")
	}
	return argv
}

func spawnServer(args SpawnArgs) (int, error) {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return 0, fmt.Errorf("open devnull: %w", err)
	}

	cmd := exec.Command(args.BinPath, serverSpawnArgv(args)...) //nolint:gosec // BinPath resolved via findServerBinary; argv constructed from validated flags
	cmd.Stdin = devNull
	// BOTH STREAMS ARE THIS PROCESS'S OWN STDERR, AND BOTH MUST STAY *os.File.
	// exec.Cmd hands the child a RAW FD only for an *os.File; for any other
	// io.Writer it creates a pipe and drains it with a PARENT-LIFETIME copier
	// goroutine. This child is spawned to OUTLIVE us (see the docstring above),
	// so such a pipe would kill it the moment we exit — measured: the child died
	// on its first write and the log file ended up empty too. Do not "improve"
	// this into an io.MultiWriter to get a second sink; the server tees its own
	// log file internally via --log-file, which is the only place a tee is safe.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// NO SysProcAttr. See file docstring — bare fork+exec is sufficient
	// and avoids the Setsid kernel-bug path on macOS Tahoe.

	if err := cmd.Start(); err != nil {
		_ = devNull.Close()
		return 0, fmt.Errorf("start %s: %w", args.BinPath, err)
	}
	// Parent no longer needs the devnull handle — child has its own
	// dup at the kernel level.
	_ = devNull.Close()

	// Capture PID BEFORE Release — Release zeros the Pid field.
	pid := cmd.Process.Pid

	// Release frees Go's wait bookkeeping. We never call Wait on a
	// spawned server — its lifecycle is independent of this process.
	_ = cmd.Process.Release()
	return pid, nil
}

// waitForServer polls the loopback graph server on port until either
// HealthService.Check returns nil or the deadline elapses.
//
// Outer poll cadence: 50ms. Each iteration first does a TCP DialTimeout
// (200ms) — cheap rejection of "nothing listens here" so we don't pay
// the connect-go HTTP/2 setup cost on every iteration. A successful
// TCP connect proves the listener is bound but does NOT prove
// mountRoutes has installed the connect handlers (server.go: Listen
// opens before Run mounts). On TCP success we issue HealthService.Check
// with a per-attempt 2s context; Check returning nil is the
// load-bearing readiness signal.
//
// Returns nil when Check succeeds; returns a deadline-exceeded error
// naming port + total deadline when the budget elapses.
func waitForServer(port int, deadline time.Duration) error {
	end := time.Now().Add(deadline)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	gc := graphclient.NewGraphClient(port)

	for time.Now().Before(end) {
		conn, err := dialWithTimeout(addr, 200*time.Millisecond)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		_ = conn.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		ok := gc.HealthyCtx(ctx)
		cancel()
		if ok {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("knowledge-server on port %d did not become healthy within %s", port, deadline)
}

// dialWithTimeout is a thin wrapper around net.DialTimeout pulled out
// so tests can stub the TCP probe without wiring a fake listener.
var dialWithTimeout = func(addr string, timeout time.Duration) (closer, error) {
	return net.DialTimeout("tcp", addr, timeout)
}

// closer is the minimal subset of net.Conn waitForServer relies on —
// just Close. Using an interface keeps dialWithTimeout's signature
// stub-friendly without exporting the entire net.Conn surface.
type closer interface {
	Close() error
}

// ensureServerReachable is the load-bearing entry point maybeSpawnLocalServer
// calls — AFTER constructing the client and ONLY when the active backend is
// local (logged-out). A logged-in user routes every op to cloud via Dispatch
// and never needs a local server, so maybeSpawnLocalServer skips this call for
// them. It probes the graph server first; if the server is healthy, returns
// immediately. If the server is unreachable, attempts to spawn one via
// findServerBinary + spawnServer, then waits up to 10s for the new
// server to become healthy.
//
// Returns nil when the server is reachable (whether pre-existing or
// freshly spawned). Returns a wrapped error when the server is down
// AND we couldn't bring one up — the caller surfaces this through
// the MCP host so the user sees a clear recovery hint.
//
// The Healthy() pre-check uses a 1s context so a wedged-but-listening
// server fails the probe quickly enough to not blow the full
// auto-spawn deadline. Skips spawning when we hit the wedged case
// (TCP open but Check times out) — spawning a second server on the
// same port would just fail with "address already in use." Caller
// gets a "server is unhealthy" error in that case.
// pprof rides through to the spawned server's argv. It applies ONLY to a server
// this call spawns: a server already healthy on the port returns at the probe
// above, and its profiling state is whatever it was started with. That is the
// honest behaviour — this function cannot re-flag a process it did not start —
// and it is why the OSS gate starts its container cold rather than adopting a
// running one.
func ensureServerReachable(port int, root, graphStorage string, pprof bool) error {
	gc := graphclient.NewGraphClient(port)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	healthy := gc.HealthyCtx(ctx)
	cancel()
	if healthy {
		return nil
	}

	// Server unreachable. Try to spawn one.
	binPath, err := findServerBinary()
	if err != nil {
		return fmt.Errorf("knowledge-server not running and not found: %w", err)
	}
	if _, err := spawnServer(SpawnArgs{
		BinPath:      binPath,
		Port:         port,
		Root:         root,
		GraphStorage: graphStorage,
		Pprof:        pprof,
	}); err != nil {
		return fmt.Errorf("spawn knowledge-server: %w", err)
	} //nolint:wsl // single-statement block is fine here
	if err := waitForServer(port, 10*time.Second); err != nil {
		return fmt.Errorf("spawned knowledge-server did not become healthy: %w", err)
	}
	return nil
}
