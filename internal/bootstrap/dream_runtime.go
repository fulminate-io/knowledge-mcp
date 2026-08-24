// SPDX-License-Identifier: Apache-2.0

// Client-side dream.Runner construction. Split from dream.go, which keeps the
// intercept chain and the dispatch seam that feeds this Runner; nothing moved
// changed shape.
//
// Post-Phase-F the server-side chokepoint that emitted tool-* events on
// every MCP tool call is gone — no tool-* events ever cross the wire to
// the client's local EventBus. The only events firing on the bus owned
// by THIS Runner are worker-* events emitted from the Runner's own
// runWorker (worker-started / worker-completed). Those are filtered by
// the self-trigger guard in dispatchLoop (OriginIsDreamWorker), so a
// worker subscribed to tool-completed can never re-fire on its own
// child invocations.
//
// Wiring the Runner installs NO triggers and reads NO worker rows. A worker
// runs only when this process creates or triggers it: worker(create) installs
// that worker's triggers through Runner.InstallWorker, and worker(trigger)
// dispatches through Runner.OnManualTrigger. Persisted worker rows stay
// first-class data — list / update / delete are unchanged — but a daemon
// restart re-arms nothing on their behalf.
//
// Worker invocation in the client topology happens through the manual
// path (worker.trigger MCP intercept, Phase H) which calls into
// Runner.OnManualTrigger directly — no event bus involvement. The bus
// remains live so worker-started / worker-completed status events can
// still be observed if the client ever subscribes to them, but no
// trigger-driven dispatch happens today.

package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/dream"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/workercrud"
)

// runtimeLister is the Execute-only seam buildRuntime takes so it can be
// handed the login-aware *graphclient.Router (cloud when logged in, local
// otherwise) rather than the bare local *graphclient.GraphClient. The
// argument flows ONLY to the dream Registry's worker-list lister
// (workercrud.New); the worker tool-dispatch path is wired separately via
// c.dispatchForRunner. Mirrors thought.Caller — both *graphclient.GraphClient
// and *graphclient.Router satisfy it structurally.
type runtimeLister interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// buildRuntime is the single-source construction path for the client-side
// dream.Runner. Both wireWorkerRuntime (the serve daemon path) and Phase I's
// runWorkerSubcommand (CLI `knowledge worker run` path) call through here
// so the construction order — config load → bus → registry(client) →
// runner(reg, bus, client, graphStorage) — stays in one place.
//
// gc is the login-aware Execute seam (runtimeLister). wireWorkerRuntime
// passes the client's c.router so the dream Registry's worker-list
// loopback routes per-call to cloud when logged in (no local server) and
// to the local graph otherwise. The CLI subcommand path runs BEFORE the
// MCP client is built and so constructs its own local *graphclient.GraphClient
// (which also satisfies runtimeLister) — the signature stays uniform across
// both callers.
//
// graphStorage is the absolute path to ~/.knowledge/ (already
// tilde-expanded by main()); the Runner writes per-worker logs under
// <graphStorage>/workers/<name>.log.
//
// The Registry takes the Execute seam directly and resolves workers via a
// wire-loopback query.
func buildRuntime(gc runtimeLister, port int, graphStorage string, dispatch dream.DispatchFunc) (*dream.Runner, error) {
	cfgPath, err := defaultConfigPath()
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	// Loopback bind addr — the client always talks to 127.0.0.1:<port>,
	// so config.LoadOrAutoDetect's local-precedence path runs (matches
	// the server's loadConfigForListener semantics for loopback bind).
	bindAddr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	cfg, wroteStarter, err := config.LoadOrAutoDetect(cfgPath, bindAddr)
	if err != nil {
		return nil, fmt.Errorf("load/auto-detect config: %w", err)
	}
	if wroteStarter {
		slog.Info("auto-detected provider, wrote starter config",
			"path", cfgPath,
			"provider", cfg.Default.Provider,
			"model", cfg.Default.Model,
		)
	}

	bus := dream.NewEventBus()
	// Wire the dream Registry through workercrud.Client so it reuses the
	// query-tool wire path. workercrud.Client.List itself is wire-loopback;
	// the indirection saves no round-trips but eliminates dream's bespoke
	// wire-row decoder (~50 lines).
	reg := dream.NewRegistry(workercrud.New(gc))
	// The MCP tool catalog is client-owned. The Runner carries it so
	// BuildAllowedTools can filter the worker allowlist locally (and without
	// dream importing tools — that would cycle, since tools imports dream).
	runner := dream.NewRunner(reg, bus, graphStorage, dispatch, tools.AllToolSchemas())
	return runner, nil
}

// wireWorkerRuntime constructs the client-side dream.Runner and wires it
// into the *client. It hands buildRuntime the login-aware c.router so the
// Registry's worker-list loopback routes to cloud when logged in (no local
// server) and local otherwise, and assigns the returned Runner to
// c.runtime.
//
// Construction is the whole of the wiring: it issues no graph read and
// installs no triggers, so a daemon boot re-arms nothing a previous session
// created. Triggers are installed only for a worker created in THIS process.
func wireWorkerRuntime(c *client, f Config) error {
	runner, err := buildRuntime(c.router, f.Port, f.GraphStorage, c.dispatchForRunner())
	if err != nil {
		return err
	}
	c.runtime = runner
	return nil
}

// defaultConfigPath returns the path to ~/.knowledge/config, mirroring
// cmd/knowledge-server/server.go::loadConfigForListener. Extracted so
// buildRuntime stays under the function-line cap.
func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".knowledge", "config"), nil
}
