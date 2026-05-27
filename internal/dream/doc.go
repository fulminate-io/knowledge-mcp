// SPDX-License-Identifier: Apache-2.0

// Package dream is the LLM-driven worker substrate that runs alongside the
// knowledge graph server. A "worker" is a small autonomous agent: a
// system prompt, an allowlist of MCP tools, and a set of Triggers that
// determine when it runs (manual invocation, tool-call lifecycle,
// worker-call lifecycle, or — in a future ticket — cron).
//
// Design pillars
//
//  1. SINGLE in-Go ReAct runtime path. All workers, regardless of the
//     configured Provider (anthropic / openai / gemini / claude-cli /
//     codex-cli), execute through one runtime: cloudwego/eino's ReAct
//     agent driving domains/llm.Client and dispatching MCP tool calls
//     back through the local server's GraphClient. There is no CLI
//     subprocess runtime; the dream layer never branches on Provider.
//
//  2. CLI providers (claude-cli, codex-cli) cannot drive tool-use. When
//     a user configures Provider=claude-cli or Provider=codex-cli for a
//     worker, Worker.Validate accepts the value (it is a valid
//     config.Provider) and the runtime fails at the first MCP-tool
//     call with a typed *llm.LLMError surfaced from
//     domains/llm/claudecli/translate.go:64-70 (or the codex-cli
//     equivalent at domains/llm/codexcli/translate.go:101). The error
//     is structured (Reason="config" / "tools_not_supported") so the
//     Runner can record it as a worker-failed event without crashing.
//     Operators who want a working dream worker must configure an API
//     provider in [dream] of ~/.knowledge/config.
//
//  3. Cron triggers are parsed-not-dispatched in v1. Worker.Validate
//     accepts a well-formed cron expression on a Trigger with
//     Event="cron". No dispatcher fires cron triggers in v1; the
//     Runner logs an INFO line at boot for each cron-only worker so
//     operators are not surprised. A follow-up ticket adds the
//     dispatcher and removes that log line.
//
//  4. Workers live in the knowledge graph. A Registry loads NodeWorker
//     entries via the wire-loopback worker(operation:list) tool call
//     on demand — domains/dream is a pure data layer with no direct
//     graph-store coupling. There is no init-time package-level
//     Register/Unregister and no compile-time hardcoded slice — every
//     worker is authored via the worker MCP tool and persists as a
//     graph node, owned by cmd/knowledge/internal/workercrud.
//
//  5. Self-trigger guard. A worker's invocations are tagged with
//     Origin="worker:<name>" via the existing session_id wire
//     channel. The Runner installs a filter on every dream-worker
//     subscription that drops events whose Origin matches the
//     subscriber, preventing a worker from triggering itself in a
//     loop. The EventBus itself stays generic: it does not know
//     about worker identity. The filter is a Runner-side policy.
//
// Package layout
//
//   - worker.go    — Worker / Trigger types, event-name constants, Validate.
//   - eventbus.go  — in-memory typed fan-out bus with non-blocking emit.
//   - registry.go  — graph-loaded worker catalog backed by the wire-loopback worker:list tool call.
//
// Phase 1 contains only those three files plus tests. The Runner,
// MCP-tool wrappers, NodeWorker type, worker MCP tool, and event
// emission chokepoint at the server land in subsequent phases of the
// dream-foundation plan.
package dream
