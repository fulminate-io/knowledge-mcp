// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/prune"
)

// wireAutoPrune spawns the client-side auto-prune runner as an async
// goroutine. Non-fatal — degrade-on-failure shape mirrors
// wireWorkerRuntime (cmd/knowledge/dream.go:198): a failed prune logs
// slog.Warn on stderr (visible in the MCP host's debug pane) but never
// blocks the MCP handshake or the dispatch loop.
//
// Opt-in: there is NO --no-prune / --auto-prune / --retention CLI flag
// (per locked feedback: daemon credentials + policy belong in the
// config file, not flag surface). Operators disable auto-prune by
// omitting the [retention] section; prune.Run short-circuits inside
// without firing an RPC when sessions is empty.
//
// T3-1 absorption: explicitly load the config inside the goroutine
// rather than relying on wireWorkerRuntime to have done so. The
// --no-worker-runtime path skips that wire entirely, and even when
// wireWorkerRuntime runs we cannot count on its load completing before
// this goroutine reads config.RetentionSessions. Load is idempotent
// (sets the same singleton); a redundant load against a config already
// in memory costs ~one os.ReadFile + TOML parse.
//
// T3-2 absorption: short-circuit on an unreachable server BEFORE firing
// the RPC. Without this, the goroutine would block for the full 60s
// perCallTimeout against a dead server and emit a noisy slog.Warn line.
// HealthyCtx with a 1s timeout is the same pattern ensureServerReachable
// uses (cmd/knowledge/lifecycle.go:288-294).
func wireAutoPrune(c *client) {
	go func() {
		ctx := context.Background()
		if !loadConfigForAutoPrune(c.port) {
			return
		}
		if !serverHealthyForAutoPrune(ctx, c) {
			return
		}
		if err := prune.Run(ctx, c.client); err != nil {
			slog.Warn("auto-prune partially or fully failed; other tools work", "error", err)
		}
	}()
}

// loadConfigForAutoPrune resolves the standard ~/.knowledge/config path
// and calls config.LoadOrAutoDetect under the same loopback bindAddr
// shape as buildRuntime. Returns false on failure (already slog.Warn'd)
// so the caller can return without proceeding to the RPCs.
//
// Idempotent against a prior load — config.setActive replaces the
// singleton with an equivalent value, and the per-process file read
// cost is well under a millisecond.
func loadConfigForAutoPrune(port int) bool {
	cfgPath, err := defaultConfigPath()
	if err != nil {
		slog.Warn("auto-prune: resolve config path failed; skipping", "error", err)
		return false
	}
	bindAddr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	if _, _, err := config.LoadOrAutoDetect(cfgPath, bindAddr); err != nil {
		slog.Warn("auto-prune: load config failed; skipping", "error", err)
		return false
	}
	return true
}

// serverHealthyForAutoPrune pings the local knowledge-server with a 1s
// timeout. Returns false (with a debug log) when the server is
// unreachable — auto-prune is a background convenience, not a feature
// users expect to see error noise from when the server happens to be
// down.
func serverHealthyForAutoPrune(parent context.Context, c *client) bool {
	healthCtx, cancel := context.WithTimeout(parent, 1*time.Second)
	defer cancel()
	if !c.client.HealthyCtx(healthCtx) {
		slog.Debug("auto-prune: server unreachable; skipping")
		return false
	}
	return true
}
