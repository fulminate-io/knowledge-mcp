// SPDX-License-Identifier: Apache-2.0

// client_deps_accessors.go — the *client methods that satisfy tools.ClientDeps,
// plus the tiny backend-resolver adapter one of them returns. Relocated verbatim
// from client.go, which keeps the struct itself, its schema loader and the no-op
// auth fallback the construction path degrades to.
//
// They are one SURFACE rather than one topic: every method here exists only because
// the tools package takes an INTERFACE instead of the concrete *client, which is
// what keeps tools from importing back into cmd/knowledge. Read together they are
// that contract; scattered through the struct they read as incidental getters.

package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/backends/provider"
	"github.com/fulminate-io/knowledge-mcp/internal/cli"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
	"github.com/fulminate-io/knowledge-mcp/internal/transcriptanalytics"
)

// LocalLiveness returns the LOCAL graph client as a liveness-only view
// (Healthy + Status, no Execute carrier). Satisfies tools.ClientDeps so the
// internal/tools package can reach the local daemon's liveness + Status RPCs
// for manage(status) without being able to pull a graph-write off the bare
// local client — graph reads/writes route via GraphCaller() (the Router).
// *graphclient.GraphClient satisfies tools.LocalLiveness structurally.
func (c *client) LocalLiveness() tools.LocalLiveness { return c.local }

// PipelineMetrics returns a snapshot of the client-side LLM pipeline
// counters (summary + embed queue depth, running workers, cumulative
// successes / terminal failures). Satisfies the optional
// tools.pipelineMetricser interface read by handleServerStatus —
// without this overlay, manage(status) would print zeros for the
// pipeline counters because the server-side StatusResponse leaves
// those proto fields unset (the pipeline moved client-side
// but the status overlay was never wired through).
//
// ok=false when pipeline is nil (--no-llm-pipeline, or neither
// summarizer nor embedder configured at boot); callers render that
// case as "(pipeline disabled)" so an operator can distinguish
// "queue empty" from "pipeline never wired."
func (c *client) PipelineMetrics() (pipeline.Metrics, bool) {
	if c.pipeline == nil {
		return pipeline.Metrics{}, false
	}
	return c.pipeline.Metrics(), true
}

// CloudStatusInfo reports whether this client routes to Fulminate Cloud, and
// the cloud host to surface in manage(status). Satisfies the optional
// tools.cloudStatusInfo interface read by handleServerStatus: when it reports
// true, status reports the CLOUD graph via the routed Stats RPC instead of the
// local daemon.
//
// It asks the ROUTER, deliberately, because the router is what actually decides
// where a call goes. Router.LoggedIn is `machineAuth || authState.IsLoggedIn`
// (router.go) — the same predicate Router.pick applies per RPC. Consulting
// c.authState directly instead would answer a narrower question, "is there a
// keychain login", and get the machine-token case wrong: a client authenticated
// by --auth-token / KNOWLEDGE_AUTH_TOKEN routes every op to cloud while holding
// no keychain session at all, so status rendered the LOCAL daemon while every
// other operation went to cloud. The nil-guard matches the degraded-test-fixture
// tolerance the other accessors carry (e.g. GraphCaller returns nil when
// router==nil).
func (c *client) CloudStatusInfo() (bool, string) {
	if c.router == nil {
		return false, cli.CloudEndpoint
	}
	return c.router.LoggedIn(context.Background()), cli.CloudEndpoint
}

// ClientVersion returns the in-process client binary version — the
// ldflags-injected bootstrap.Version, already the CLIENT version because
// handleServerStatus always runs client-side. Satisfies the optional
// tools.versionInfo interface read by manage(status); always known (no probe),
// never empty ("dev" when unstamped).
func (c *client) ClientVersion() string { return Version }

// DaemonVersion best-effort probes the running local `knowledge serve` daemon
// for its version, REUSING the existing probeDaemonVersion MCP-initialize
// round-trip (version_subcommand.go) against graphclient.DefaultMCPHTTPPort.
// Satisfies the optional tools.versionInfo interface; returns ("", false) on
// ANY failure (no daemon, timeout, malformed reply) so manage(status) degrades
// to a client-version-only render with no error.
func (c *client) DaemonVersion() (string, bool) {
	return probeDaemonVersion(graphclient.DefaultMCPHTTPPort)
}

// serverBinaryVersionBudget bounds the locate-and-exec of the installed
// knowledge-server on a status render. Generous for a process start, tight
// enough that a corrupt or wrong-architecture binary hanging on startup cannot
// wedge a status call.
const serverBinaryVersionBudget = 2 * time.Second

// ServerBinaryVersion reads the version of the INSTALLED knowledge-server
// binary on disk. Satisfies the optional tools.versionInfo interface.
//
// It answers a DIFFERENT question from DaemonVersion: that one reports the
// version of the daemon PROCESS currently running, while this reports what is
// on disk. The two diverge in different ways and have different remedies — a
// stale process is fixed by a restart, a stale binary only by re-installing —
// which is why they are separate stamps with separate skew lines rather than
// one comparison.
//
// Returns ("", false) on ANY failure — no binary found, an exec error, an empty
// reply — so both render surfaces degrade to no server-binary line and skew on
// nothing.
func (c *client) ServerBinaryVersion() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), serverBinaryVersionBudget)
	defer cancel()
	return serverBinaryVersion(ctx)
}

// ResetPipelineFailedCounters zeroes the session-lifetime failed counters.
// Satisfies the optional tools.pipelineResetter interface called by
// clear_llm_failures after removing on-disk markers.
func (c *client) ResetPipelineFailedCounters() {
	if c.pipeline != nil {
		c.pipeline.ResetFailedCounters()
	}
}

// WakePipeline nudges every LLM-pipeline collector to re-scan promptly.
// Two callers: the collect intercept (via the optional tools.pipelineWaker
// interface) after a successful collect, so a freshly-collected graph that had
// idle-backed-off its scan cadence discovers the new nodes within one base tick
// instead of waiting out the hour-long idle ceiling; and the activity hook
// (client_freshness.go) when the account watermark moved.
func (c *client) WakePipeline() {
	if c.pipeline != nil {
		c.pipeline.WakeAll()
	}
}

// CollectRuntime returns the standing collect runtime so the collect intercept
// (via the optional tools.collectRuntimeProvider interface) can launch a
// detached run and race it against the 60s detach threshold. Nil-guarded for
// direct test fixtures that build *client without constructClient.
func (c *client) CollectRuntime() *tools.CollectRuntime { return c.collectRuntime }

// CollectRunSnapshot returns the per-target collect-run snapshot manage(status)
// renders (satisfies the optional tools.collectRunReporter interface). Returns
// nil when the runtime was not constructed (direct test fixture), so the status
// section degrades to nothing exactly like the pipeline/transcript overlays.
func (c *client) CollectRunSnapshot() []tools.CollectRunStatus {
	if c.collectRuntime == nil {
		return nil
	}
	return c.collectRuntime.Snapshot()
}

// Sink returns the remote upload sink. Satisfies tools.ClientDeps — the
// collect intercept path streams chunks to the server via this sink rather
// than opening knowledge.bin directly.
func (c *client) Sink() collector.Sink { return c.sink }

// SubgraphFetcher returns the INNER ingest sink c.Sink()'s admitting wrapper
// delegates to — the same *remote.UploadSink instance, so the logs collector's
// cloud-subgraph read rides the same per-call login-routed picker as its writes
// while the admission wrapper stays in front of every WriteResult.
//
// The explicit nil branch is load-bearing: returning a typed-nil
// *remote.UploadSink directly would reach the caller as a NON-nil interface
// value and defeat the consumer's nil guard. Nil means the client was built
// without an ingest sink (router-less / headless fixture); the logs collector
// surfaces a loud error rather than collecting without cloud enrichment.
func (c *client) SubgraphFetcher() tools.CloudSubgraphFetcher {
	if c.subgraphFetcher == nil {
		return nil
	}
	return c.subgraphFetcher
}

// RootDir returns the project root directory passed via --root (defaults to
// ".") so the ast intercept can walk source files locally. Satisfies
// tools.ClientDeps; the server has no repo (remote-server mode) so AST
// parsing must happen on the client where the files live.
func (c *client) RootDir() string { return c.rootDir }

// RootDirSet reports whether --root was explicitly passed (vs the "." default).
// Consumed via the rootDirSourcer optional interface by the ast walk-root guard:
// a defaulted root plus no session cwd makes an omitted repo fail loud rather
// than silently walking the process cwd.
func (c *client) RootDirSet() bool { return c.rootDirSet }

// UsageAnalyzer returns the client-side agent-flow analyzer, lazily constructing it on
// first use. It is built over the default ~/.knowledge/transcripts-cache root (no
// router/network dependency). Returns a nil interface when the cache root cannot be
// resolved, so InterceptAnalyzeUsage's nil-check fires and renders the cold-cache hint
// rather than carrying a typed-nil.
func (c *client) UsageAnalyzer() tools.UsageAnalyzerAPI {
	c.usageAnalyzerMu.Lock()
	defer c.usageAnalyzerMu.Unlock()
	if !c.usageAnalyzerDone {
		svc, err := transcriptanalytics.NewService("")
		if err != nil {
			slog.Warn("bootstrap: usage analyzer unavailable (cache root unresolvable)", "err", err)
		}
		c.usageAnalyzer = svc
		c.usageAnalyzerDone = true
	}
	if c.usageAnalyzer == nil {
		return nil
	}
	return c.usageAnalyzer
}

// GraphTypeCRUD returns the client-side wire-loopback CRUD client for the
// graph_type intercepts (nil-tolerant; see the graphTypeCRUD field comment).
func (c *client) GraphTypeCRUD() tools.GraphTypeCRUDAPI {
	if c.graphTypeCRUD == nil {
		return nil
	}
	return c.graphTypeCRUD
}

// Embedder returns the client-side binary embedder so InterceptSearch /
// InterceptQuery can embed query text on the client side. Returns nil
// (the interface zero) when no voyage_api_key is configured — the search
// intercept falls through to forwarding the original args unchanged,
// and the server's compositor either uses its own embedder (if any)
// or returns BM25-only results.
func (c *client) Embedder() embed.BinaryEmbedder {
	return c.embedder
}

// BackendResolver returns the production backend resolver — delegates to
// cmd/knowledge/internal/backends/provider. Closed-switch order is set
// there; Default returns nil when no backend is configured (intercepts
// fall through to local-only).
func (c *client) BackendResolver() tools.BackendResolver { return providerBackendResolver{} }

// GraphCaller returns the production graph caller — the
// routing layer. The returned *Router dispatches per-call to either the
// local *GraphClient (default / logged out) or a lazily-built cloud
// *GraphClient (when AuthState reports IsLoggedIn=true), surfacing
// ErrNoBackend when neither is reachable. Intercepts forward the local
// portion of a backend-backed call through this caller. Returns nil
// only when the *client was constructed without a router (zero-value
// test fixture); intercepts fail fast in that case rather than
// performing the backend write with no way to persist the local-graph
// mirror.
func (c *client) GraphCaller() tools.GraphCaller {
	if c.router == nil {
		return nil
	}
	return c.router
}

// LocalGraphCaller returns a GraphCaller that ALWAYS targets the local server,
// bypassing routing. Only the three local-only callers use it: sync push (source
// bytes come from the LOCAL graph; destination is cloud), sync list, and sync
// pull (the OverwriteGraph apply target is the LOCAL .bin). The post-collect
// linker and postpopulate are NOT local-only (cloud-routed GraphCaller). Returns
// nil when the *client has no local GraphClient (cloud-first user, no install);
// callers' nil-guards surface the degraded-mode error. The wrapper satisfies
// Indexer / Exporter / Overwriter / metadataStatsCaller / statsRPC.
func (c *client) LocalGraphCaller() tools.GraphCaller {
	if c.local == nil {
		return nil
	}
	return graphClientCaller{gc: c.local}
}

// providerBackendResolver delegates to the production
// cmd/knowledge/internal/backends/provider package. Kept tiny so the
// *client struct can satisfy ClientDeps.BackendResolver() without
// holding a per-instance field.
type providerBackendResolver struct{}

func (providerBackendResolver) Default() backends.Backend {
	return provider.Default()
}
func (providerBackendResolver) ByName(name string) backends.Backend {
	return provider.ByName(name)
}
