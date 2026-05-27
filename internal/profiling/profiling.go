// SPDX-License-Identifier: Apache-2.0

// Package profiling hosts the knowledge stdio client's pprof HTTP endpoint
// and the bracketed CPU-capture control wired to the `manage` MCP ops
// pprof_start / pprof_stop.
//
// The endpoint binds loopback-only on DefaultPort (15021, one below the
// server's default 15022) — the stdio client has no HTTP server of its
// own, so it gets a dedicated port. Override via SetPort before first
// use. It is started eagerly when the client runs with --pprof, or
// lazily on the first manage(pprof_start). Standard
// net/http/pprof routes (/debug/pprof/{heap,goroutine,profile,trace,...})
// are always mounted once the server is up; /debug/pprof/capture serves
// the bytes of the most recent bracketed CPU capture so it can be pulled
// with `go tool pprof http://127.0.0.1:15021/debug/pprof/capture`.
//
// Loopback-only and unauthenticated by design — the handlers expose
// process internals, so they must never bind a routable interface.
package profiling

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	httppprof "net/http/pprof"
	runtimepprof "runtime/pprof"
	"sync"
	"time"
)

// DefaultPort is the default pprof endpoint port (one below the server's 15022).
const DefaultPort = 15021

var (
	mu          sync.Mutex
	serverUp    bool
	cpuActive   bool
	cpuBuf      *bytes.Buffer // in-progress capture; nil when idle
	lastCapture []byte        // bytes of the most recent completed capture
	lastAt      time.Time
	port        = DefaultPort
)

// Addr returns the loopback address the client's pprof endpoint binds.
func Addr() string { return fmt.Sprintf("127.0.0.1:%d", port) }

// SetPort overrides the pprof endpoint port. Must be called before
// EnsureServer or StartCPU. Not goroutine-safe — call from flag parsing only.
func SetPort(p int) { port = p }

// EnsureServer starts the loopback pprof HTTP server if it isn't already
// running. Idempotent — safe to call from the --pprof boot path and from
// StartCPU. A bind failure is logged and swallowed; the rest of the client
// keeps working.
func EnsureServer() {
	mu.Lock()
	defer mu.Unlock()
	ensureServerLocked()
}

func ensureServerLocked() {
	if serverUp {
		return
	}
	ln, err := net.Listen("tcp", Addr())
	if err != nil {
		slog.Warn("pprof: failed to bind listener; profiling endpoint disabled", "addr", Addr(), "error", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", httppprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)
	mux.HandleFunc("/debug/pprof/capture", serveCapture)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	slog.Info("pprof profiling enabled", "addr", Addr(), "path", "/debug/pprof/")
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("pprof server exited", "error", err)
		}
	}()
	serverUp = true
}

// StartCPU begins a CPU profile into an in-memory buffer, lazily starting
// the HTTP endpoint so the result is retrievable. Errors if one is already
// running or the endpoint can't bind.
func StartCPU() (addr string, err error) {
	mu.Lock()
	defer mu.Unlock()
	if cpuActive {
		return "", errors.New("a CPU profile is already running; call manage(pprof_stop) first")
	}
	ensureServerLocked()
	if !serverUp {
		return "", fmt.Errorf("pprof endpoint unavailable (could not bind %s)", Addr())
	}
	cpuBuf = &bytes.Buffer{}
	if err := runtimepprof.StartCPUProfile(cpuBuf); err != nil {
		cpuBuf = nil
		return "", fmt.Errorf("start cpu profile: %w", err)
	}
	cpuActive = true
	return Addr(), nil
}

// StopCPU stops the in-progress CPU profile and stashes the encoded bytes
// so they can be downloaded from /debug/pprof/capture. Returns the fetch
// URL and the profile size in bytes.
func StopCPU() (url string, size int, err error) {
	mu.Lock()
	defer mu.Unlock()
	if !cpuActive {
		return "", 0, errors.New("no CPU profile is running; call manage(pprof_start) first")
	}
	runtimepprof.StopCPUProfile() // blocks until the profiling goroutine flushes into cpuBuf
	lastCapture = cpuBuf.Bytes()
	lastAt = time.Now()
	cpuBuf = nil
	cpuActive = false
	return "http://" + Addr() + "/debug/pprof/capture", len(lastCapture), nil
}

func serveCapture(w http.ResponseWriter, _ *http.Request) {
	mu.Lock()
	data := lastCapture
	at := lastAt
	mu.Unlock()
	if len(data) == 0 {
		http.Error(w, "no capture available — run manage(pprof_start), reproduce the slow op, then manage(pprof_stop)", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="knowledge-cpu-%s.pprof"`, at.Format("20060102-150405")))
	_, _ = w.Write(data)
}
