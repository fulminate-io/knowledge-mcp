// SPDX-License-Identifier: Apache-2.0

// boot_config.go — the single home for the daemon's boot-time load of
// ~/.knowledge/config. loadBootConfig runs on EVERY serve, which is why it does
// not live in headless.go: that file declares itself the single home for the
// --headless umbrella flag's behavior, and a loader that runs unconditionally is
// not --headless behavior. defaultConfigPath lives here too, so the loader and
// the path it resolves stay in one place.

package bootstrap

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// loadBootConfig loads ~/.knowledge/config into the config singleton at daemon
// boot, so the [credentials] section resolves config-first (with env fallback)
// for every consumer wired afterwards. It is called from wireRuntimesBackground
// BEFORE llmproviders.BuildEmbedder, so the query embedder + rerank resolve the
// config voyage_api_key rather than falling straight to VOYAGE_API_KEY.
//
// TWO ARMS, because the two postures differ on ONE axis: whether the loader may
// WRITE.
//   - --headless (embedded/supervisor-managed): config.Load, which NEVER writes.
//   - a normal serve: config.LoadOrAutoDetect, which auto-detects a provider and
//     writes a starter file when the path is absent — the behavior a normal
//     serve has always had.
//
// Guarded by config.Loaded() so an already-loaded config (or a test's
// SetForTest) is left untouched — no double load, no clobber.
//
// Degrade-not-die on every error path, matching both of the predecessors this
// function replaces: on a defaultConfigPath error OR a load error (missing /
// unparseable file) it slog.Warn's and returns, leaving config unloaded so
// credentials fall back to the *_API_KEY env vars via credOrEnv — the documented
// degrade posture (missing voyage → BM25-only, missing linear → backend
// disabled).
func loadBootConfig(f Config) {
	if config.Loaded() {
		return
	}
	cfgPath, err := defaultConfigPath()
	if err != nil {
		slog.Warn("boot: could not resolve config path; credentials will resolve from env only", "error", err)
		return
	}
	if f.Headless {
		// config.Load, NOT LoadOrAutoDetect: LoadOrAutoDetect WRITES a starter
		// file when the path is absent, and the supervisor owns config
		// placement, so this arm must never write one.
		if _, err := config.Load(cfgPath); err != nil {
			slog.Warn("boot: could not load ~/.knowledge/config; credentials will resolve from env only",
				"path", cfgPath, "error", err)
			return
		}
		slog.Debug("boot: loaded config for credential resolution", "path", cfgPath)
		return
	}
	// Loopback bind addr — the client always talks to 127.0.0.1:<port>, so
	// config.LoadOrAutoDetect's local-precedence path runs (matches the
	// server's loadConfigForListener semantics for loopback bind).
	bindAddr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: f.Port}
	cfg, wroteStarter, err := config.LoadOrAutoDetect(cfgPath, bindAddr)
	if err != nil {
		slog.Warn("boot: could not load/auto-detect ~/.knowledge/config; credentials will resolve from env only",
			"path", cfgPath, "error", err)
		return
	}
	if wroteStarter {
		slog.Info("auto-detected provider, wrote starter config",
			"path", cfgPath,
			"provider", cfg.Default.Provider,
			"model", cfg.Default.Model,
		)
	}
}

// defaultConfigPath returns the path to ~/.knowledge/config, mirroring
// cmd/knowledge-server/server.go::loadConfigForListener.
func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".knowledge", "config"), nil
}
