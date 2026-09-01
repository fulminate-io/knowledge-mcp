// SPDX-License-Identifier: Apache-2.0

// setup_restart.go — the restart sequence for `knowledge setup`: stop
// the old 15023 daemon (routed by owner) BEFORE spawning a replacement,
// (re)start the 15022 graph server and the 15023 serve daemon, then
// health-verify both and assert the daemon reports the installed target
// version (a stale survivor must not pass a green liveness probe). Brew-
// managed installs defer entirely.

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// daemonOwnerKind classifies who currently manages the running 15023
// daemon so the restart stops it with the right mechanism.
type daemonOwnerKind int

const (
	daemonOwnerNone daemonOwnerKind = iota // no daemon on 15023 (first install)
	daemonOwnerUnit                        // launchd/systemd unit-managed
	daemonOwnerBare                        // bare fork+exec process
)

// Restart seams — stubbable so setup_restart_test.go can spy the stop
// mechanism, the daemon spawn argv, the health probes, and the version
// identity check without touching real processes or the network.
var (
	// daemonExecCommand is the fork seam for spawnDaemonProcess (mirrors
	// lifecycle.go's getExecutable/dialWithTimeout stubs).
	daemonExecCommand = exec.Command

	// startServerBare (re)starts the 15022 graph server on the
	// bare-process branch via the existing runStart path.
	startServerBare = func() error { return runStart(nil) }

	// signalDaemonStop SIGTERMs a bare daemon PID. os.Process.Signal
	// compiles cross-platform (SIGTERM delivery is a no-op-ish on Windows,
	// where the bare-daemon stop path is not reached anyway).
	signalDaemonStop = func(pid int) error {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("find daemon process %d: %w", pid, err)
		}
		return proc.Signal(syscall.SIGTERM)
	}

	// probeDaemon15023 returns the running daemon's reported version. Seam
	// over probeDaemonVersion for the identity check.
	probeDaemon15023 = probeDaemonVersion

	// daemonOwner classifies the running 15023 daemon.
	daemonOwner = defaultDaemonOwner

	// health15022 reports 15022 graph-server liveness.
	health15022 = func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return graphclient.NewGraphClient(graphclient.DefaultPort).HealthyCtx(ctx)
	}

	// daemonPIDOnPort resolves the PID listening on a loopback TCP port
	// (best-effort via lsof; 0 when none / lsof absent).
	daemonPIDOnPort = func(port int) int {
		out, err := exec.Command("lsof", "-ti", fmt.Sprintf("tcp:%d", port), "-sTCP:LISTEN").Output()
		if err != nil {
			return 0
		}
		first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		pid, err := strconv.Atoi(strings.TrimSpace(first))
		if err != nil {
			return 0
		}
		return pid
	}

	// stopServerListener gracefully stops a pre-existing 15022 server via
	// the lifecycle POST /shutdown path — owner-agnostic (works for a
	// bare, make-started, or unit-managed server).
	stopServerListener = func() error { return runStop(nil) }
)

// stopPreExistingListeners frees 15022 + 15023 of whatever currently
// holds them BEFORE the units are (re)loaded, owner-agnostically: a
// graceful HTTP shutdown for the server, and a SIGTERM to the PID
// holding the daemon port (a bare `knowledge serve`, or a stale
// instance). This is what lets `knowledge setup` cleanly MIGRATE a box
// that was running a bare/dev daemon — without it, the survivor keeps
// the port and the version-identity check false-fails.
func stopPreExistingListeners() {
	if health15022() {
		if err := stopServerListener(); err != nil {
			fmt.Fprintf(os.Stdout, "knowledge setup: could not stop the existing graph server (continuing): %v\n", err)
		}
	}
	if pid := daemonPIDOnPort(graphclient.DefaultMCPHTTPPort); pid > 0 {
		_ = signalDaemonStop(pid)
		// Give the kernel a moment to release the port before the unit
		// (or the bare fallback) tries to bind it.
		waitForReady(func() bool { return daemonPIDOnPort(graphclient.DefaultMCPHTTPPort) == 0 }, 5*time.Second)
	}
}

// restartReadinessTimeout bounds how long the restart verify polls for
// the server + daemon to come up after a (re)start. Generous because the
// daemon re-initializes the graph, runs the LLM precheck, and starts its
// pipeline on boot. A package var so tests can shorten it for the
// never-ready failure paths.
var restartReadinessTimeout = 30 * time.Second

// restartReadinessInterval is the poll cadence for the readiness waits.
var restartReadinessInterval = 200 * time.Millisecond

// waitForReady polls probe until it reports true or the deadline elapses.
func waitForReady(probe func() bool, deadline time.Duration) bool {
	end := time.Now().Add(deadline)
	for {
		if probe() {
			return true
		}
		if !time.Now().Before(end) {
			return false
		}
		time.Sleep(restartReadinessInterval)
	}
}

// waitForDaemonReady polls the daemon version probe until it RESPONDS
// (ok) or the deadline elapses, returning the reported version. It does
// NOT judge the version — the caller compares it against the target
// (a responding-but-stale daemon is a hard failure, not a wait state).
func waitForDaemonReady(probe func(int) (string, bool), port int, deadline time.Duration) (string, bool) {
	end := time.Now().Add(deadline)
	for {
		if v, ok := probe(port); ok {
			return v, true
		}
		if !time.Now().Before(end) {
			return "", false
		}
		time.Sleep(restartReadinessInterval)
	}
}

// defaultDaemonOwner classifies the running 15023 daemon: none when no
// process listens, unit when our launchd/systemd unit claims it, else
// bare. Best-effort — the real-box manual smoke validates the live
// launchd/systemd behavior.
func defaultDaemonOwner() (daemonOwnerKind, int) {
	pid := daemonPIDOnPort(graphclient.DefaultMCPHTTPPort)
	if pid <= 0 {
		return daemonOwnerNone, 0
	}
	if daemonUnitActive() {
		return daemonOwnerUnit, pid
	}
	return daemonOwnerBare, pid
}

// daemonUnitActive reports whether our persistence unit currently
// manages the 15023 daemon.
func daemonUnitActive() bool {
	switch serviceGOOS {
	case "darwin":
		return runLaunchctl("print", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdDaemonLabel)) == nil
	case "linux":
		out, _ := exec.Command("systemctl", "--user", "is-active", systemdDaemonUnit).CombinedOutput()
		return strings.TrimSpace(string(out)) == "active"
	}
	return false
}

// restartSequence stops the old 15023 daemon (routed by owner, before
// any spawn), (re)starts the 15022 server + 15023 daemon, then
// health-verifies 15022 and asserts the 15023 daemon reports
// targetVersion. Brew-managed installs defer entirely.
func restartSequence(targetVersion string, outcome serviceOutcome) error {
	if outcome == outcomeBrew {
		fmt.Fprintln(os.Stdout, "knowledge setup: brew services owns the daemon; manage via `brew services restart knowledge`")
		return nil
	}

	// Stop the OLD daemon BEFORE spawning a replacement — skip-if-absent
	// on a first install where nothing owns 15023.
	if kind, pid := daemonOwner(); kind != daemonOwnerNone {
		switch kind {
		case daemonOwnerUnit:
			stopDaemonUnit()
		case daemonOwnerBare:
			_ = signalDaemonStop(pid)
		}
	}

	// (Re)start the 15022 server and the 15023 daemon.
	if err := startServerAndDaemon(outcome); err != nil {
		return err
	}

	// Health-verify 15022 liveness — POLL until ready (a unit kickstart /
	// systemctl restart returns before the server has re-bound, and the
	// server does a graph load on start; a single probe races the restart).
	if !waitForReady(health15022, restartReadinessTimeout) {
		return fmt.Errorf("knowledge setup: graph server (port %d) did not become healthy within %s after restart", graphclient.DefaultPort, restartReadinessTimeout)
	}
	// Identity-check 15023: the freshly-restarted daemon must report the
	// installed target version — a MATCH proves the intended version is
	// live; a MISMATCH means a stale daemon survived a failed stop. POLL
	// for the daemon to RESPOND first (it re-initializes graph + LLM
	// precheck + pipeline on start, so it lags the server), then check the
	// version identity ONCE — a responding-but-wrong-version daemon is a
	// hard failure, not a readiness state.
	got, ok := waitForDaemonReady(probeDaemon15023, graphclient.DefaultMCPHTTPPort, restartReadinessTimeout)
	if !ok {
		return fmt.Errorf("knowledge setup: daemon (port %d) did not respond within %s after restart", graphclient.DefaultMCPHTTPPort, restartReadinessTimeout)
	}
	if got != targetVersion {
		return fmt.Errorf("knowledge setup: daemon (port %d) reports version %q but expected %q — a stale daemon may have survived the restart", graphclient.DefaultMCPHTTPPort, got, targetVersion)
	}

	fmt.Fprintln(os.Stdout, "knowledge setup: restarted — MCP clients reconnect to the daemon's endpoint on their next session")
	return nil
}

// startServerAndDaemon (re)starts the 15022 server + 15023 daemon: a
// unit kickstart when a persistence unit is loaded, else the bare-process
// path (runStart for the server + spawnDaemonProcess for the daemon).
func startServerAndDaemon(outcome serviceOutcome) error {
	if outcome == outcomeUnit {
		return kickstartUnits()
	}
	if err := startServerBare(); err != nil {
		return fmt.Errorf("knowledge setup: start graph server: %w", err)
	}
	graphStorage, err := serviceGraphStorage()
	if err != nil {
		return err
	}
	if err := spawnDaemonProcess(graphStorage); err != nil {
		return fmt.Errorf("knowledge setup: spawn daemon: %w", err)
	}
	return nil
}

// stopDaemonUnit stops the unit-managed 15023 daemon by owner GOOS.
func stopDaemonUnit() {
	switch serviceGOOS {
	case "darwin":
		_ = runLaunchctl("bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdDaemonLabel))
	case "linux":
		_ = runSystemctlUser("stop", systemdDaemonUnit)
	}
}

// kickstartUnits restarts the loaded persistence units so the new
// binaries take effect (the outcomeUnit branch — validated by the
// real-box manual smoke). Server first, then daemon, unchanged.
func kickstartUnits() error {
	for _, u := range []struct{ label, unit string }{
		{launchdServerLabel, systemdServerUnit},
		{launchdDaemonLabel, systemdDaemonUnit},
	} {
		if err := kickstartUnit(u.label, u.unit); err != nil {
			return err
		}
	}
	return nil
}

// kickstartUnit restarts ONE loaded persistence unit, named per platform
// (launchd label on darwin, systemd unit on linux). Extracted from
// kickstartUnits so a caller that must cycle only the DAEMON — the account
// switch, which has no business interrupting the single-tenant 15022 server —
// can do so without cycling both.
func kickstartUnit(label, unit string) error {
	switch serviceGOOS {
	case "darwin":
		if err := runLaunchctl("kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)); err != nil {
			return fmt.Errorf("launchctl kickstart %s: %w", label, err)
		}
	case "linux":
		if err := runSystemctlUser("restart", unit); err != nil {
			return fmt.Errorf("systemctl --user restart %s: %w", unit, err)
		}
	}
	return nil
}

// spawnDaemonProcess forks the running knowledge client as the 15023
// serve daemon: a bare fork+exec with NO SysProcAttr (the kernel
// reparents the child to launchd/systemd for free — see lifecycle.go's
// no-Setsid caveat). Mirrors spawnServer's shape (log-file via
// os.OpenFile, stdin=/dev/null, Release after start). The argv
// matches the canonical brew service block invocation.
func spawnDaemonProcess(graphStorage string) error {
	exe, err := getExecutable()
	if err != nil {
		return fmt.Errorf("resolve client path for daemon: %w", err)
	}
	// The DAEMON opens this path itself, via the --log-file below, and tees it
	// with its inherited stderr. This process no longer opens it: holding a
	// write handle it never writes to would create the file as a side effect and
	// say nothing about whether the daemon could actually use it.
	logPath := filepath.Join(expandTilde(graphStorage), "knowledge-daemon.log")
	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open devnull: %w", err)
	}

	argv := []string{
		"serve",
		"--http-port", strconv.Itoa(graphclient.DefaultMCPHTTPPort),
		"--log-level", "info",
		"--log-file", logPath,
	}
	cmd := daemonExecCommand(exe, argv...)
	cmd.Stdin = devNull
	// BOTH STREAMS ARE THIS PROCESS'S OWN STDERR, and both must stay *os.File —
	// the same constraint spawnServer documents. exec.Cmd passes a raw fd only
	// for an *os.File; any other io.Writer becomes a pipe drained by a
	// parent-lifetime goroutine, which a daemon spawned to outlive us cannot
	// survive. The daemon tees its own --log-file (already in the argv above)
	// with this inherited stderr, so the durable record is kept there.
	//
	// STDERR, NEVER STDOUT: this process writes user-facing CLI output to stdout
	// — the restart notice printed above is one of its lines — and a daemon's log
	// lines interleaved into it would corrupt what the operator reads.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// NO SysProcAttr — bare fork+exec, per lifecycle.go's no-Setsid caveat.

	if err := cmd.Start(); err != nil {
		_ = devNull.Close()
		return fmt.Errorf("start daemon %s: %w", exe, err)
	}
	_ = devNull.Close()

	_ = cmd.Process.Release()
	return nil
}

// serviceGraphStorage returns ~/.knowledge — the daemon's graph storage
// + log directory.
func serviceGraphStorage() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".knowledge"), nil
}
