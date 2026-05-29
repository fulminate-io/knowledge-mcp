// SPDX-License-Identifier: Apache-2.0

// Package tools holds the client-side MCP tool intercepts for the knowledge
// stdio binary: the manage(status), collect, and backend-write-through
// paths that must run in-process. Collectors stream chunks to the graph
// server rather than running server-side; backend writes (Linear, Jira,
// GitHub) hit the third-party API inline from this package BEFORE the
// local mutate reaches the server.
//
// Every entry point takes a ClientDeps argument rather than a concrete
// *client type — the main package supplies accessors so this package has
// no import cycle back into cmd/knowledge.
package tools

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// BackendResolver routes between configured external project/ticket
// backends (Linear, Jira, GitHub Issues, ...). Production wires this to
// the cmd/knowledge/internal/backends/provider package; tests inject a
// fake to drive InterceptCreateProject / InterceptCreateTicket /
// InterceptMutate through a scripted Backend without setting
// LINEAR_API_KEY on the test process.
//
// Method semantics:
//   - Default returns the first configured backend (closed-switch order
//     in provider.Available), or nil when no backend is configured.
//     InterceptCreateProject calls this — empty result means "fall
//     through to the local-only path", populated means "write through".
//   - ByName returns the backend matching name, or nil when either the
//     name is unknown or that backend is not currently configured.
//     InterceptCreateTicket calls this with the parent project's
//     `backend` metadata value; InterceptMutate calls this with the
//     target node's `backend` metadata value.
type BackendResolver interface {
	Default() backends.Backend
	ByName(name string) backends.Backend
}

// GraphCaller is the narrow surface InterceptCreateProject /
// InterceptCreateTicket / InterceptMutate use to forward the local
// portion of a backend-backed call back into the MCP tool dispatch.
// Production wires this to a thin adapter over the same
// *graphclient.GraphClient the rest of the stdio client uses; tests inject
// a fake that records the (name, args) pair so assertions can verify
// the forward shape without an over-the-wire test.
//
// The interface stays this narrow on purpose — it is the base handle every
// intercept takes, then type-asserts UP to the carrier seams it needs
// (render.Executor / Indexer / Exporter / topologyFetcher). Execute is the base
// seam — every read/write rides the Execute carrier, and the production
// graphClientCaller satisfies it. Adding more would over-widen the test surface
// every intercept has to satisfy.
type GraphCaller interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// ClientDeps is the narrow surface the cmd/knowledge main package exposes
// to this internal/tools package. Keep it minimal — every new accessor
// widens the coupling. The accessors cover:
//
//   - GraphClient: liveness check + Status RPC for handleServerStatus.
//   - Sink: collector.Sink used by collect to stream chunks to the
//     server's IngestService.
//   - RootDir: project root directory the user passed via --root. The
//     client-side ast intercept walks this directory to find source
//     files (the server has no repo, especially in remote-server mode).
//   - WorkerRuntime: client-side dream runtime for the worker.trigger /
//     worker.status MCP intercepts. Returns nil when wireWorkerRuntime
//     degraded at boot — InterceptWorker nil-checks before dispatching
//     so the rest of the MCP loop keeps working.
//   - WorkerCRUD: client-side wire-loopback CRUD client for the
//     worker.list / worker.create / worker.update / worker.delete MCP
//     intercepts. Production wires a *workercrud.Client backed by the
//     same GraphClient; tests inject a fake. Returns nil only when the
//     client was constructed without a GraphClient — InterceptWorker
//     nil-checks before dispatching.
//   - Embedder: client-side BinaryEmbedder used by InterceptSearch /
//     InterceptQuery to embed query text on the client side so the
//     server-side compositor short-circuits its own embed call (Phase
//     4.5). Returns nil when no voyage_api_key is configured — search
//     falls back to BM25-only via the existing nil-embedder path.
//   - BackendResolver: resolves backend adapters (linear / jira / ...)
//     for the client-side InterceptCreateProject / InterceptCreateTicket
//     / InterceptMutate intercepts. Returns a resolver that always
//     returns nil from Default/ByName when no backend is configured —
//     intercepts fall through to local-only.
//   - GraphCaller: forwards intercept tail-calls (the local-mutate half
//     of a successful backend write-through) back into the MCP tool
//     dispatch. Returns nil only when the client was constructed without
//     a GraphClient (degraded headless mode) — intercepts fail fast in
//     that case rather than performing the backend write with no way to
//     persist the local-graph mirror. Post-FUL-323: returns the routed
//     *graphclient.Router that dispatches per-call to local or cloud
//     based on live auth state.
//   - LocalGraphCaller: returns a GraphCaller that ALWAYS targets the
//     local server (bypasses routing). The three callers that must read
//     and write the local graph regardless of login state — sync push,
//     post-collect linker, post-collect postpopulate — use this accessor.
//     Returns nil only when no local server is wired (cloud-first user);
//     those callers' existing nil-guards surface the degraded-mode error.
//   - RepoResolver: client-side cwd → code-graph-name resolver used by
//     the FUL-241 Phase 4 InjectRepoIfCodeGraph intercept. One resolver
//     per MCP session; sync.Once inside the resolver gates the
//     underlying code-graph catalog read so a 100-call burst still
//     produces exactly one wire read. Returns nil only in test
//     harnesses that don't exercise repo injection — InjectRepoIfCodeGraph
//     falls through (returns false) on nil, which is safe because the
//     server-side handlers already reject empty repo: with a typed
//     error after Phase 1.
type ClientDeps interface {
	GraphClient() *graphclient.GraphClient
	Sink() collector.Sink
	RootDir() string
	WorkerRuntime() WorkerRuntimeAPI
	WorkerCRUD() WorkerCRUDAPI
	Embedder() embed.BinaryEmbedder
	BackendResolver() BackendResolver
	GraphCaller() GraphCaller
	LocalGraphCaller() GraphCaller
	RepoResolver() *RepoResolver
}
