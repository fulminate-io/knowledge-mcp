// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime/debug"
)

// defaultClientMemLimit is the soft heap ceiling (GOMEMLIMIT) the stdio
// client imposes on itself when nothing else has set one. A collect
// spikes the heap — the chunker holds every file's results until upload,
// and the precise Go call-graph build loads a whole module + dependency
// closure (ASTs + type info + SSA) live at once — and with the default
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

// setupLogging configures slog to write to stderr (always) plus an
// optional log file. Factored out of Run to keep Run readable.
func setupLogging(cfg *Config, lvl *slog.LevelVar) {
	logWriter := os.Stderr
	if cfg.LogFile != "" {
		logPath := expandTilde(cfg.LogFile)
		logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open log file %s: %v\n", logPath, err)
		} else {
			logWriter = logF
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: lvl})))
}

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
