// SPDX-License-Identifier: Apache-2.0

// lifecycle_subcommand.go — `knowledge start / stop / status` CLI
// subcommands. These are user-facing terminal commands that DO write
// to stdout — that's the explicit carve-out from the stdout-discipline
// lifecycle.go enforces, whose helpers keep stdout clear for exactly
// this. Output here is human-readable text users read and pipe.
//
// `start`  — spawn knowledge-server (idempotent: if already running,
//            prints status and exits 0).
// `stop`   — POST /shutdown to the running server, wait for drain.
// `status` — dial the server, print PID + uptime + graph stats; if
//            unreachable, print "not running" and exit 1.

package bootstrap

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// lifecycleFlags carries the subset of clientFlags the lifecycle
// subcommands respect. Keeping them here so the subcommand parser
// doesn't have to re-derive defaults from the MCP-mode path.
type lifecycleFlags struct {
	port         int
	root         string
	graphStorage string
	timeout      time.Duration // stop-only: how long to wait for drain
	pprof        bool          // start-only: mount the spawned server's /debug/pprof/
}

// registerLifecycleFlags registers the lifecycle-subcommand flags on fs,
// binding each into f. Pure register-only seam (no fs.Parse, no positional
// validation, no tilde-expansion) — shared by parseLifecycleFlags (the live
// CLI path) and the docs generator, which VisitAll's the FlagSet to render
// flag tables. The `name` param drives the stop-only --timeout branch so the
// generator can request the start/status (no --timeout) vs stop (--timeout)
// variant. Mirrors how registerConfigFlags is shared by ParseFlags + runServe.
//
// --pprof is START-ONLY on the same per-name branch, because it is an argument
// to a SPAWN: stop and status act on a server that is already running and whose
// argv is settled, so offering them the flag would advertise a knob that could
// not take effect.
func registerLifecycleFlags(fs *flag.FlagSet, f *lifecycleFlags, name string) {
	fs.IntVar(&f.port, "port", graphclient.DefaultPort, "TCP port the graph server listens on")
	fs.StringVar(&f.root, "root", ".", "Project root directory")
	fs.StringVar(&f.graphStorage, "graph-storage", "~/.knowledge/", "Directory for graph storage")
	if name == "stop" {
		fs.DurationVar(&f.timeout, "timeout", 30*time.Second, "Max wait for graceful shutdown")
	}
	if name == "start" {
		fs.BoolVar(&f.pprof, "pprof", false, "Start the server with its /debug/pprof/ handlers mounted on the --port listener (loopback only)")
	}
}

// parseLifecycleFlags parses a subset of flags from `args` and
// returns the resolved lifecycleFlags. The flag set uses
// flag.ContinueOnError so unknown flags surface as errors (rather
// than calling os.Exit) — caller decides how to surface them.
func parseLifecycleFlags(name string, args []string) (lifecycleFlags, error) {
	fs := flag.NewFlagSet("knowledge "+name, flag.ContinueOnError)
	var f lifecycleFlags
	registerLifecycleFlags(fs, &f, name)
	if err := fs.Parse(args); err != nil {
		return lifecycleFlags{}, err
	}
	f.graphStorage = expandTilde(f.graphStorage)
	return f, nil
}

// runStart launches knowledge-server. Idempotent — if a server is
// already healthy on the configured port, prints "already running"
// and returns nil rather than spawning a duplicate.
//
// On success, prints the spawned PID and the path the server is
// logging to. On failure (binary not found, healthcheck timeout),
// returns the error so runAuthSubcommand surfaces it on stderr +
// exit 1.
func runStart(args []string) error {
	f, err := parseLifecycleFlags("start", args)
	if err != nil {
		return err
	}

	gc := graphclient.NewGraphClient(f.port)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	healthy := gc.HealthyCtx(ctx)
	cancel()
	if healthy {
		fmt.Fprintf(os.Stdout, "knowledge-server already running on port %d\n", f.port)
		return nil
	}

	binPath, err := findServerBinary()
	if err != nil {
		return err
	}
	pid, err := spawnServer(SpawnArgs{
		BinPath:      binPath,
		Port:         f.port,
		Root:         f.root,
		GraphStorage: f.graphStorage,
		Pprof:        f.pprof,
	})
	if err != nil {
		return err
	}

	if err := waitForServer(f.port, 15*time.Second); err != nil {
		return fmt.Errorf("spawned PID %d but server did not become healthy: %w", pid, err)
	}

	logPath := filepath.Join(f.graphStorage, "server.log")
	fmt.Fprintf(os.Stdout, "knowledge-server started (pid %d, port %d, log %s)\n", pid, f.port, logPath)
	return nil
}

// runStop POSTs /shutdown to the running server, then polls Healthy()
// inverse until the server stops responding (or the deadline elapses).
// The /shutdown endpoint is a graceful drain — server completes
// in-flight requests, then exits.
//
// Status code handling:
//   - 200 OK: server accepted the shutdown; we wait for it to stop.
//   - 403 Forbidden: server refused (rare; possibly a future auth
//     check). Surfaced verbatim.
//   - 409 Conflict (already_shutting_down): another runStop is mid-
//     drain. Treat as success-equivalent; wait for the existing
//     drain to complete.
//   - 503 Service Unavailable / connection refused: server isn't
//     running. runStop is idempotent — return nil.
//   - Any other status: surface a loud error naming the code +
//     port so the user knows the port may be occupied by an
//     unrelated service.
func runStop(args []string) error {
	f, err := parseLifecycleFlags("stop", args)
	if err != nil {
		return err
	}

	gc := graphclient.NewGraphClient(f.port)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	if !gc.HealthyCtx(ctx) {
		cancel()
		fmt.Fprintf(os.Stdout, "knowledge-server not running on port %d (nothing to stop)\n", f.port)
		return nil
	}
	cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/shutdown", f.port)
	httpClient := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("build shutdown request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// Connection-refused on POST means the server vanished
		// between Healthy and POST — treat as already-stopped.
		fmt.Fprintf(os.Stdout, "knowledge-server stopped (server unreachable during shutdown POST: %v)\n", err)
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		// Fall through to drain wait.
	case http.StatusForbidden:
		return fmt.Errorf("shutdown forbidden by server: %s", body)
	case http.StatusServiceUnavailable:
		fmt.Fprintf(os.Stdout, "knowledge-server already stopping (503): %s\n", body)
		return nil
	case http.StatusConflict:
		fmt.Fprintf(os.Stdout, "knowledge-server already draining (409); waiting for completion\n")
		// Fall through to drain wait.
	default:
		return fmt.Errorf("unexpected /shutdown response %d (port %d may be occupied by an unrelated service): %s", resp.StatusCode, f.port, body)
	}

	// Poll TCP-connect until refused (means the listener is gone, i.e.,
	// process actually exited). Health checks would still answer "yes"
	// during graceful drain — we want the harder "process is gone"
	// signal. dialWithTimeout with a short budget is the cheapest probe.
	addr := fmt.Sprintf("127.0.0.1:%d", f.port)
	end := time.Now().Add(f.timeout)
	for time.Now().Before(end) {
		conn, err := dialWithTimeout(addr, 200*time.Millisecond)
		if err != nil {
			fmt.Fprintf(os.Stdout, "knowledge-server stopped on port %d\n", f.port)
			return nil
		}
		_ = conn.Close()
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("knowledge-server still listening on port %d after %s; shutdown may be wedged", f.port, f.timeout)
}

// runStatus dials the server and prints PID / port / graph stats.
// Exit 1 when the server is unreachable so scripts can branch on
// `knowledge status` exit code.
func runStatus(args []string) error {
	f, err := parseLifecycleFlags("status", args)
	if err != nil {
		return err
	}

	gc := graphclient.NewGraphClient(f.port)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	healthy := gc.HealthyCtx(ctx)
	cancel()
	if !healthy {
		fmt.Fprintf(os.Stdout, "knowledge-server: not running on port %d\n", f.port)
		os.Exit(1)
	}

	status, err := gc.Status()
	if err != nil {
		return fmt.Errorf("server is up but Status RPC failed: %w", err)
	}
	fmt.Fprintf(os.Stdout, "knowledge-server: running on port %d\n", f.port)
	if pid, ok := status["pid"].(float64); ok {
		fmt.Fprintf(os.Stdout, "  pid:           %d\n", int64(pid))
	}
	if nodes, ok := status["nodes"].(float64); ok {
		fmt.Fprintf(os.Stdout, "  nodes:         %d\n", int64(nodes))
	}
	if edges, ok := status["edges"].(float64); ok {
		fmt.Fprintf(os.Stdout, "  edges:         %d\n", int64(edges))
	}
	if vecs, ok := status["binary_vectors"].(float64); ok {
		fmt.Fprintf(os.Stdout, "  binary vecs:   %d\n", int64(vecs))
	}
	if path, ok := status["graph_path"].(string); ok {
		fmt.Fprintf(os.Stdout, "  graph path:    %s\n", path)
	}
	if pid, ok := status["pid"].(float64); ok {
		owner, hint := identifyServerOwner(int64(pid))
		if owner != "" {
			fmt.Fprintf(os.Stdout, "  managed by:    %s\n", owner)
		}
		if hint != "" {
			fmt.Fprintf(os.Stdout, "  warning:       %s\n", hint)
		}
	}
	return nil
}

// identifyServerOwner figures out which lifecycle layer owns the
// running server (PID `runningPID`) and returns a label for display
// plus an optional warning when state across layers is inconsistent
// (e.g., stale PID file, brew services job pointing at a different
// PID).
//
// Sources checked, in order:
//
//  1. ~/.knowledge/server.pid — written by `make start` and the
//     `knowledge start` subcommand.
//  2. launchctl list of homebrew.mxcl.knowledge — written by
//     `brew services start`. Read via `launchctl list <label>` to
//     avoid parsing the full list output.
//
// When neither source claims the running PID, returns "external
// (auto-spawn or manual)" — e.g. the serve daemon's local auto-spawn
// (maybeSpawnLocalServer), or a `bin/knowledge-server` invoked directly.
func identifyServerOwner(runningPID int64) (owner, warning string) {
	makePID := readMakePIDFile()
	brewPID := launchctlPID()

	switch {
	case makePID == runningPID && brewPID == runningPID:
		// Both layers agree — unusual but consistent.
		return "make start + brew services (both PID files match)", ""
	case makePID == runningPID:
		owner = "make start (~/.knowledge/server.pid)"
		if brewPID > 0 {
			warning = fmt.Sprintf("brew services job is registered with PID %d but %d is running — `brew services stop knowledge` to clear", brewPID, runningPID)
		}
		return owner, warning
	case brewPID == runningPID:
		owner = "brew services (launchd: homebrew.mxcl.knowledge)"
		if makePID > 0 {
			warning = fmt.Sprintf("~/.knowledge/server.pid contains stale PID %d (process gone) — safe to delete", makePID)
		}
		return owner, warning
	default:
		owner = "external (auto-spawn or manual)"
		switch {
		case makePID > 0 && brewPID > 0:
			warning = fmt.Sprintf("both ~/.knowledge/server.pid (%d) and brew services (%d) reference dead processes", makePID, brewPID)
		case makePID > 0:
			warning = fmt.Sprintf("~/.knowledge/server.pid contains stale PID %d (process gone) — safe to delete", makePID)
		case brewPID > 0:
			warning = fmt.Sprintf("brew services job is registered with PID %d but %d is running", brewPID, runningPID)
		}
		return owner, warning
	}
}

// readMakePIDFile returns the PID stored in ~/.knowledge/server.pid,
// or 0 when the file is absent / unreadable / contains junk. The
// PID is returned regardless of whether the process is alive — the
// caller decides whether to flag a stale file.
func readMakePIDFile() int64 {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(filepath.Join(home, ".knowledge", "server.pid"))
	if err != nil {
		return 0
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return pid
}

// launchctlPID returns the PID of the running brew services job,
// or 0 when the job isn't loaded / launchctl isn't available /
// parsing fails. Uses `launchctl list <label>` (returns plist text
// containing PID = N when running, "PID" absent when loaded but
// not running).
func launchctlPID() int64 {
	out, err := exec.Command("launchctl", "list", "homebrew.mxcl.knowledge").CombinedOutput()
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "\"PID\" =") {
			continue
		}
		// Format: "PID" = 12345;
		parts := strings.Split(line, "=")
		if len(parts) != 2 {
			continue
		}
		raw := strings.TrimSuffix(strings.TrimSpace(parts[1]), ";")
		pid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0
		}
		return pid
	}
	return 0
}
