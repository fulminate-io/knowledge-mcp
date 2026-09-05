# Binaries & CLI flags

## Overview

The project ships two binaries. `knowledge` is the client: a CLI and a local MCP
daemon that your coding assistant (Claude, Codex, and other MCP clients) talks to.
`knowledge-server` is the graph server that owns storage and serves the graph over
a local TCP port. The client manages the server for you — it can download it
(`knowledge install`), start and stop it (`knowledge start` / `knowledge stop`),
and run it inline (`knowledge serve`).

The client is also where the LLM pipeline lives: summarization and embedding run
client-side and write results back to the server, which keeps the server itself
free of any model API keys. Most flags you will set are on the client; the server
takes a small, stable set of its own, documented at the end of this page.

The sections below walk each subcommand. Flag tables are generated from the source
and kept in sync automatically — the narrative around them is what explains when
to reach for each one.

## `knowledge` (client)

`knowledge` is a multi-command CLI: it dispatches the subcommands documented on
this page (`serve`, `start`, `stop`, `status`, `login`, `logout`, `doctor`,
`install-claude-assets`, `install-codex-assets`, and the rest). Run bare with no
subcommand it does **not** serve MCP — it prints a hint pointing you at
`knowledge serve`, the daemon that exposes the MCP endpoint your assistant
connects to (see the next section). The client speaks to the graph server on
`--port` (default `15022`), starting one if needed, and runs the background work
that keeps the graph fresh: the client-side LLM pipeline that summarizes and
embeds nodes, and the propagation runtime that maintains the thought graph. The
flags below tune those background systems — batch sizes, pipeline worker counts,
and the `--no-*-runtime` switches that turn an individual subsystem off for
offline or low-noise development.

<!-- BEGIN GENERATED: flags-client -->
| Flag | Default | Description |
| --- | --- | --- |
| `--auth-token` |  | Opaque machine bearer token presented on every request, bypassing the interactive browser login and the platform keychain. Defaults to the KNOWLEDGE_AUTH_TOKEN environment variable; an explicit flag value wins. Empty leaves the interactive login path intact. |
| `--embed-batch-size` | `100` | Client-side LLM pipeline: items per embed worker batch (under voyageEmbedder's 128 internal cap) |
| `--embed-channel-size` | `10000` | Client-side LLM pipeline: EmbedWork channel buffer size (full = collector blocks) |
| `--embed-rpm` | `0` | Client-side LLM pipeline: max embed (Voyage) API requests per MINUTE across all embed workers; 0 = unlimited (default, preserves current 20-worker behavior). Proactive throttle for low-tier Voyage accounts — paces the opening burst so it respects the account RPM before the first 429. Companion to the reactive Retry-After backoff. |
| `--embed-workers` | `20` | Client-side LLM pipeline: count of embed worker goroutines |
| `--graph-storage` | `~/.knowledge/` | Directory for graph storage: the server writes its .bin here, and the client roots its segment cache under it (default ~/.knowledge/) |
| `--headless` | `false` | Run as an embedded/supervisor-managed daemon: serve the loopback /mcp endpoint and resolve query embeddings, but skip every background content + coordination loop. Implies --no-propagation-runtime, --skip-llm-precheck and --no-llm-pipeline, and additionally disables the transcript upload loops. Still loads ~/.knowledge/config (so [credentials] resolve config-first). Does not change auth. |
| `--log-file` |  | Log file path (logs to both stderr and file when set) |
| `--log-level` | `info` | Log level: debug, info, warn, error |
| `--log-rotate-compress` | `true` | Gzip rotated log files. Default true — disk savings outweigh the ~10ms compression cost on rotation. |
| `--log-rotate-max-age-days` | `30` | Max age (days) of rotated files to retain. 0 disables age-based pruning. |
| `--log-rotate-max-files` | `3` | Max rotated log files to retain (in addition to the active one). Older files are deleted. |
| `--log-rotate-max-size-mb` | `50` | Log file size (MB) at which to rotate. Set to 0 to disable rotation entirely (write append-only forever). |
| `--no-auth` | `false` | Force the client local-only: suppress BOTH cloud-selection triggers at the Router.pick chokepoint (machineAuth forced false WITHOUT consulting --auth-token/KNOWLEDGE_AUTH_TOKEN, and the keychain replaced with a no-op store so a live `knowledge login` refresh token reports IsLoggedIn==false). Fail-closed: no routed op can reach a fulminate.io host regardless of credentials present. Capability reduction only — the cloud endpoint is never overridden. Use for offline/OSS mode and as the safety floor for the bug-hunt harness. |
| `--no-auto-update` | `false` | Disable the background update-check loop, so this daemon never upgrades itself. Defaults to the KNOWLEDGE_NO_AUTO_UPDATE environment variable (accepted values: 1, t, T, true, TRUE, True, 0, f, F, false, FALSE, False; any other non-empty value is a startup error); an explicit flag value wins. The ~/.knowledge/config key auto_update = false is the third surface, and a disable from ANY of the three wins. |
| `--no-llm-pipeline` | `false` | Skip client-side LLM pipeline (summarize + embed) wiring. The MCP daemon and other tools continue to work; only background summarization/embedding stops. |
| `--no-propagation-runtime` | `false` | Skip client-side PropagationLoop wiring. The MCP daemon continues to serve and reflective tools still run on demand, but the hourly background cluster detection + valence propagation stops. Use for offline development or to silence background log noise. |
| `--no-transcript-upload` | `false` | Skip the background transcript-upload loops, including their HOME-side transcript cache writes. Individually addressable form of one of the gates --headless implies — for daemons that need the LLM pipeline (which --headless disables) but must not run coordination loops (e.g. the bench harness's corpus-pull daemon). |
| `--pipeline-tick` | `250ms` | Client-side LLM pipeline: per-graph collector poll interval |
| `--port` | `15022` | TCP port the graph server listens on |
| `--pprof` | `false` | Start the pprof profiling HTTP endpoint on 127.0.0.1:15021 (/debug/pprof/) at boot, AND pass --pprof to the knowledge-server this daemon spawns so its own /debug/pprof/ mounts too. Both endpoints bind loopback only. Also reachable on demand for this process via manage(pprof_start). Use to profile client-side work such as collect. |
| `--pprof-port` | `15021` | TCP port for this process's pprof profiling HTTP endpoint (loopback only). Applied when --pprof is set; the spawned knowledge-server serves its own /debug/pprof/ on --port instead. |
| `--reflect-backstop-interval` | `24h0m0s` | Client-side reflection: cadence of the full-corpus reflection backstop pass that resets DF-Leiden incremental drift. The hourly loop runs incrementally; once this interval elapses since the last full pass, the next tick forces a full Leiden recompute. Default 24h (nightly). |
| `--root` | `.` | Project root the client walks for ast + topology, and the current-tree fallback for resolving a bare repo name (default ".") |
| `--segment-residency-budget-bytes` | `1073741824` | Client-side segment residency ceiling, in RESIDENT HEAP BYTES summed across every per-graph segment pool: once the total crosses it, the coldest pools are unloaded from memory and reload from the local L2 disk cache on their next search. 0 disables eviction entirely. This counts modeled Go-heap bytes — the per-segment membership index, the liveness bitset, and whatever each payload declares it holds. A mapped segment's blob is page cache and is NOT counted, so the budget is not a bound on a pool's on-disk size. Defaults to the KNOWLEDGE_SEGMENT_RESIDENCY_BUDGET_BYTES environment variable and otherwise to 1073741824; an explicit flag value wins. |
| `--skip-llm-precheck` | `false` | Skip the live-ping check that runs against every configured (provider, model) tuple at client startup. Use for offline development or CI sandboxes; default is to fail-fast at boot rather than at first tool call. |
| `--summary-batch-size` | `20` | Client-side LLM pipeline: items per summary worker batch |
| `--summary-channel-size` | `10000` | Client-side LLM pipeline: SummaryWork channel buffer size (full = collector blocks) |
| `--summary-workers` | `25` | Client-side LLM pipeline: count of summary worker goroutines |
| `--web-origin` | `https://fulminate.io,https://dev.fulminate.io` | Comma-separated allow-list of browser web origins permitted to make cross-origin (CORS) requests to the daemon's loopback streamable-HTTP MCP endpoint. The Origin is reflected back only when it matches an entry; the list is never widened to '*'. An explicit value replaces the default (https://fulminate.io,https://dev.fulminate.io). Repeatable. |
<!-- END GENERATED: flags-client -->

## `knowledge serve`

`knowledge serve` runs the client as a long-lived daemon that also exposes a
streamable-HTTP MCP endpoint (`/mcp`) on `--http-port` (default `15023`), distinct
from the graph server's `--port`. Use it when you want a persistent daemon serving
MCP over HTTP — for example a shared local instance multiple tools connect to —
rather than the per-session stdio client. It carries the same pipeline and runtime
flags as the bare client, plus `--http-port`. Run the daemon detached so it
outlives any single session.

<!-- BEGIN GENERATED: flags-serve -->
| Flag | Default | Description |
| --- | --- | --- |
| `--auth-token` |  | Opaque machine bearer token presented on every request, bypassing the interactive browser login and the platform keychain. Defaults to the KNOWLEDGE_AUTH_TOKEN environment variable; an explicit flag value wins. Empty leaves the interactive login path intact. |
| `--embed-batch-size` | `100` | Client-side LLM pipeline: items per embed worker batch (under voyageEmbedder's 128 internal cap) |
| `--embed-channel-size` | `10000` | Client-side LLM pipeline: EmbedWork channel buffer size (full = collector blocks) |
| `--embed-rpm` | `0` | Client-side LLM pipeline: max embed (Voyage) API requests per MINUTE across all embed workers; 0 = unlimited (default, preserves current 20-worker behavior). Proactive throttle for low-tier Voyage accounts — paces the opening burst so it respects the account RPM before the first 429. Companion to the reactive Retry-After backoff. |
| `--embed-workers` | `20` | Client-side LLM pipeline: count of embed worker goroutines |
| `--graph-storage` | `~/.knowledge/` | Directory for graph storage: the server writes its .bin here, and the client roots its segment cache under it (default ~/.knowledge/) |
| `--headless` | `false` | Run as an embedded/supervisor-managed daemon: serve the loopback /mcp endpoint and resolve query embeddings, but skip every background content + coordination loop. Implies --no-propagation-runtime, --skip-llm-precheck and --no-llm-pipeline, and additionally disables the transcript upload loops. Still loads ~/.knowledge/config (so [credentials] resolve config-first). Does not change auth. |
| `--http-port` | `15023` | Loopback TCP port for the streamable-HTTP MCP endpoint (/mcp). Distinct from --port (the graph server). |
| `--log-file` |  | Log file path (logs to both stderr and file when set) |
| `--log-level` | `info` | Log level: debug, info, warn, error |
| `--log-rotate-compress` | `true` | Gzip rotated log files. Default true — disk savings outweigh the ~10ms compression cost on rotation. |
| `--log-rotate-max-age-days` | `30` | Max age (days) of rotated files to retain. 0 disables age-based pruning. |
| `--log-rotate-max-files` | `3` | Max rotated log files to retain (in addition to the active one). Older files are deleted. |
| `--log-rotate-max-size-mb` | `50` | Log file size (MB) at which to rotate. Set to 0 to disable rotation entirely (write append-only forever). |
| `--no-auth` | `false` | Force the client local-only: suppress BOTH cloud-selection triggers at the Router.pick chokepoint (machineAuth forced false WITHOUT consulting --auth-token/KNOWLEDGE_AUTH_TOKEN, and the keychain replaced with a no-op store so a live `knowledge login` refresh token reports IsLoggedIn==false). Fail-closed: no routed op can reach a fulminate.io host regardless of credentials present. Capability reduction only — the cloud endpoint is never overridden. Use for offline/OSS mode and as the safety floor for the bug-hunt harness. |
| `--no-auto-update` | `false` | Disable the background update-check loop, so this daemon never upgrades itself. Defaults to the KNOWLEDGE_NO_AUTO_UPDATE environment variable (accepted values: 1, t, T, true, TRUE, True, 0, f, F, false, FALSE, False; any other non-empty value is a startup error); an explicit flag value wins. The ~/.knowledge/config key auto_update = false is the third surface, and a disable from ANY of the three wins. |
| `--no-llm-pipeline` | `false` | Skip client-side LLM pipeline (summarize + embed) wiring. The MCP daemon and other tools continue to work; only background summarization/embedding stops. |
| `--no-propagation-runtime` | `false` | Skip client-side PropagationLoop wiring. The MCP daemon continues to serve and reflective tools still run on demand, but the hourly background cluster detection + valence propagation stops. Use for offline development or to silence background log noise. |
| `--no-transcript-upload` | `false` | Skip the background transcript-upload loops, including their HOME-side transcript cache writes. Individually addressable form of one of the gates --headless implies — for daemons that need the LLM pipeline (which --headless disables) but must not run coordination loops (e.g. the bench harness's corpus-pull daemon). |
| `--pipeline-tick` | `250ms` | Client-side LLM pipeline: per-graph collector poll interval |
| `--port` | `15022` | TCP port the graph server listens on |
| `--pprof` | `false` | Start the pprof profiling HTTP endpoint on 127.0.0.1:15021 (/debug/pprof/) at boot, AND pass --pprof to the knowledge-server this daemon spawns so its own /debug/pprof/ mounts too. Both endpoints bind loopback only. Also reachable on demand for this process via manage(pprof_start). Use to profile client-side work such as collect. |
| `--pprof-port` | `15021` | TCP port for this process's pprof profiling HTTP endpoint (loopback only). Applied when --pprof is set; the spawned knowledge-server serves its own /debug/pprof/ on --port instead. |
| `--reflect-backstop-interval` | `24h0m0s` | Client-side reflection: cadence of the full-corpus reflection backstop pass that resets DF-Leiden incremental drift. The hourly loop runs incrementally; once this interval elapses since the last full pass, the next tick forces a full Leiden recompute. Default 24h (nightly). |
| `--root` | `.` | Project root the client walks for ast + topology, and the current-tree fallback for resolving a bare repo name (default ".") |
| `--segment-residency-budget-bytes` | `1073741824` | Client-side segment residency ceiling, in RESIDENT HEAP BYTES summed across every per-graph segment pool: once the total crosses it, the coldest pools are unloaded from memory and reload from the local L2 disk cache on their next search. 0 disables eviction entirely. This counts modeled Go-heap bytes — the per-segment membership index, the liveness bitset, and whatever each payload declares it holds. A mapped segment's blob is page cache and is NOT counted, so the budget is not a bound on a pool's on-disk size. Defaults to the KNOWLEDGE_SEGMENT_RESIDENCY_BUDGET_BYTES environment variable and otherwise to 1073741824; an explicit flag value wins. |
| `--skip-llm-precheck` | `false` | Skip the live-ping check that runs against every configured (provider, model) tuple at client startup. Use for offline development or CI sandboxes; default is to fail-fast at boot rather than at first tool call. |
| `--summary-batch-size` | `20` | Client-side LLM pipeline: items per summary worker batch |
| `--summary-channel-size` | `10000` | Client-side LLM pipeline: SummaryWork channel buffer size (full = collector blocks) |
| `--summary-workers` | `25` | Client-side LLM pipeline: count of summary worker goroutines |
| `--web-origin` | `https://fulminate.io,https://dev.fulminate.io` | Comma-separated allow-list of browser web origins permitted to make cross-origin (CORS) requests to the daemon's loopback streamable-HTTP MCP endpoint. The Origin is reflected back only when it matches an entry; the list is never widened to '*'. An explicit value replaces the default (https://fulminate.io,https://dev.fulminate.io). Repeatable. |
<!-- END GENERATED: flags-serve -->

## `knowledge start`

`knowledge start` launches the graph server in the background and returns. It is
the explicit "bring the server up" command — handy when you want the server
running independently of any client session. It takes only the locating flags it
needs to find and bind the graph: `--graph-storage`, `--port`, and `--root`.

<!-- BEGIN GENERATED: flags-start -->
| Flag | Default | Description |
| --- | --- | --- |
| `--graph-storage` | `~/.knowledge/` | Directory for graph storage |
| `--port` | `15022` | TCP port the graph server listens on |
| `--pprof` | `false` | Start the server with its /debug/pprof/ handlers mounted on the --port listener (loopback only) |
| `--root` | `.` | Project root directory |
<!-- END GENERATED: flags-start -->

## `knowledge stop`

`knowledge stop` shuts down a running graph server gracefully, waiting up to
`--timeout` (default `30s`) for it to drain before giving up. Pair it with
`knowledge start` to manage the server's lifecycle by hand.

<!-- BEGIN GENERATED: flags-stop -->
| Flag | Default | Description |
| --- | --- | --- |
| `--graph-storage` | `~/.knowledge/` | Directory for graph storage |
| `--port` | `15022` | TCP port the graph server listens on |
| `--root` | `.` | Project root directory |
| `--timeout` | `30s` | Max wait for graceful shutdown |
<!-- END GENERATED: flags-stop -->

## `knowledge status`

`knowledge status` reports whether a graph server is up on `--port` and, when it
is, prints its PID, the node/edge/vector counts, and the graph path. When no
server is reachable it prints "not running" and exits non-zero, so scripts can
branch on the exit code. It shares the same locating flags as `knowledge start`
and `knowledge stop`.

<!-- BEGIN GENERATED: flags-status -->
| Flag | Default | Description |
| --- | --- | --- |
| `--graph-storage` | `~/.knowledge/` | Directory for graph storage |
| `--port` | `15022` | TCP port the graph server listens on |
| `--root` | `.` | Project root directory |
<!-- END GENERATED: flags-status -->

## `knowledge login`

`knowledge login` is the optional sign-in for Fulminate Cloud. It prints an
authorize URL to complete an OAuth flow, additionally opening your browser when
one is available, and stores the resulting credential in your operating system
keychain when one is available and in a 0600 file under `~/.knowledge`
otherwise; from then on the client routes to the cloud account instead of a
local graph. It is purely opt-in — the client and server work fully offline
against a local graph without ever logging in. There are no flags beyond
`--help`; headless and container environments are supported, since the printed
URL can be opened from any browser that can reach the callback address.

## `knowledge logout`

`knowledge logout` signs you back out: it deletes the stored credential from
whichever store holds it and best-effort revokes it server-side. It is idempotent — safe to run
when you are already logged out — and takes no flags beyond `--help`. After
logout the client returns to operating against your local graph.

## `knowledge install`

`knowledge install` downloads the matching `knowledge-server` binary so the client
has a server to run. By default it places the server next to the running client
binary; pass `--dest` to choose a different directory. Use `--check` to compare
the installed server version against the latest release without writing anything —
a quick way to see whether an update is available.

<!-- BEGIN GENERATED: flags-install -->
| Flag | Default | Description |
| --- | --- | --- |
| `--allow-downgrade` | `false` | Permit installing a release OLDER than the currently-installed version (default: refuse) |
| `--check` | `false` | Compare installed server version against latest release without writing |
| `--dest` |  | Destination directory for knowledge-server (default: sibling of the running knowledge binary) |
<!-- END GENERATED: flags-install -->

## `knowledge doctor`

`knowledge doctor` runs a one-shot diagnostic sweep and prints a checklist of how
your install is doing: whether the graph server is reachable, whether the code
index is current, whether the config and any consumer CLIs resolve, whether an
embedding key is present, and whether the editor assets are in place. It exits
non-zero when any check is an error (warnings and informational lines do not fail
it), which makes it a good first stop when something is not behaving. Pass
`--deep` to additionally exercise each configured provider's reachability — slower,
because it makes live network calls.

<!-- BEGIN GENERATED: flags-doctor -->
| Flag | Default | Description |
| --- | --- | --- |
| `--config-file` |  | Path to the TOML config file (default ~/.knowledge/config) |
| `--deep` | `false` | Exercise each configured provider's reachability/login (slower, makes network calls) |
| `--port` | `15022` | TCP port the graph server should be listening on |
<!-- END GENERATED: flags-doctor -->

## `knowledge install-claude-assets`

`knowledge install-claude-assets` sets up the editor side of the integration for
Claude. It registers the knowledge MCP server with the client (skip with
`--no-mcp`) and writes the bundled agent and skill catalog under `~/.claude`, so a
brew- or tarball-installed client gets the same agents and skills a source build
ships. Use `--dry-run` to preview what would be written without touching disk, or
`--diff` to print a unified diff of every file that differs from the bundled
version. `--dest` / `--claude-md-dest` override the destinations (mainly for
testing).

<!-- BEGIN GENERATED: flags-install-claude-assets -->
| Flag | Default | Description |
| --- | --- | --- |
| `--claude-md-dest` |  | CLAUDE.md destination path (default ~/.claude/CLAUDE.md) |
| `--claude-settings-dest` |  | settings.json destination path (default ~/.claude/settings.json) |
| `--dest` |  | Destination directory (default ~/.claude) |
| `--diff` | `false` | Print a unified diff of every file that differs (read-only; implies --dry-run) |
| `--dry-run` | `false` | Print what would be written without touching disk |
| `--no-mcp` | `false` | Skip registering the knowledge MCP server with the client (default: register at user scope) |
| `--verbose` | `false` | Print each file path written (default: summary only) |
<!-- END GENERATED: flags-install-claude-assets -->

## `knowledge install-codex-assets`

`knowledge install-codex-assets` is the Codex counterpart. It registers the
knowledge MCP server (skip with `--no-mcp`) and installs the same agent and skill
catalog into Codex's split layout — skills under `~/.agents/skills` and agents,
translated to Codex's format, under `~/.codex/agents`. As with the Claude
installer, `--dry-run` previews and `--diff` shows what differs; `--skills-dest` /
`--agents-dest` / `--agents-md-dest` override the destination roots (mainly for
testing).

<!-- BEGIN GENERATED: flags-install-codex-assets -->
| Flag | Default | Description |
| --- | --- | --- |
| `--agents-dest` |  | Agents destination root (default ~/.codex/agents) |
| `--agents-md-dest` |  | AGENTS.md destination path (default ~/.codex/AGENTS.md) |
| `--diff` | `false` | Print a unified diff of every file that differs (read-only; implies --dry-run) |
| `--dry-run` | `false` | Print what would be written without touching disk |
| `--no-mcp` | `false` | Skip registering the knowledge MCP server with the client (default: register) |
| `--skills-dest` |  | Skills destination root (default ~/.agents/skills) |
| `--verbose` | `false` | Print each file path written (default: summary only) |
<!-- END GENERATED: flags-install-codex-assets -->

## `knowledge-server`

`knowledge-server` is the graph server. It owns the on-disk graph and serves it
over a local TCP port; the client process runs the LLM pipeline and writes results
back, so the server itself needs no model API keys. You rarely launch it by hand —
the client downloads, starts, and stops it for you — but its flags are listed here
for when you run it directly.

The flags below are hand-maintained from the server's flag parser (they are not
auto-generated like the client tables above):

| Flag | Default | Description |
| --- | --- | --- |
| `--graph-storage` | `~/.knowledge/` | Directory for graph storage (`knowledge.bin` + `code/*.bin`). |
| `--root` | `.` | Project root directory for collectors and the default active repo. |
| `--port` | `15022` | TCP port for server mode. |
| `--drain-timeout` | `0` | Max wait for in-flight requests on shutdown (`0` = default of 5 minutes). |
| `--max-loaded-repos` | `5` | Max lazily-loaded graphs kept in memory at once; past the bound the least-recently-used one is unloaded and re-read from disk on its next access. `0` = unbounded. |
| `--graph-idle-ttl` | `15m` | Unload a lazily-loaded graph that has gone this long without a read or a write, whatever the resident count. `0` disables the idle sweep. |
| `--log-level` | `info` | Log level: `debug`, `info`, `warn`, `error`. |
| `--log-file` |  | Log file path (logs to both stderr and the file when set). |
| `--pprof` | `false` | Enable the pprof profiling endpoint at `/debug/pprof/`. |
| `--log-rotate-max-size-mb` | `50` | Log size (MB) at which to rotate. `0` disables rotation (append forever). |
| `--log-rotate-max-files` | `3` | Max rotated log files to retain (besides the active one). |
| `--log-rotate-max-age-days` | `30` | Max age (days) of rotated files to retain. `0` disables age-based pruning. |
| `--log-rotate-compress` | `true` | Gzip rotated log files. |
| `--situation-ttl` | `1d` | Merge aged session overlays into base and load recent ones on startup (e.g. `1d`, `24h`, `0` to disable). |

### Graph residency

Every graph other than the knowledge base is loaded lazily, on the first request
that needs it, and the two flags above decide when it leaves memory again. An
unloaded graph is not lost: its `.bin` is untouched and the next access re-reads
it, paying one cold load.

Three kinds of graph are never unloaded. The knowledge graph is resident from
boot and is not part of this bookkeeping at all. The linkage graph is loaded
eagerly at startup so it is always available. Overlays — session overlays and
code branch overlays, the names carrying an `@` — are composed into live read
views and stay with their base.

A graph is only unloaded once it has gone a grace period with no read and no
write, so a burst of activity across more graphs than `--max-loaded-repos` allows
will exceed the bound for as long as those graphs stay busy, and settle back to
it once they go quiet. That is the intended behaviour: the bound yields to work
in progress rather than unloading a graph a request is still using.
