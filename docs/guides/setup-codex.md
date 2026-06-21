# Set up with Codex

This guide gets knowledge running and wired into Codex on macOS, via Homebrew. By
the end you will have the knowledge daemon running as a background service and the
knowledge MCP server registered with Codex.

## 1. Install

Install the Homebrew tap and the `knowledge` formula:

```sh
brew tap fulminate-io/knowledge
brew install knowledge
```

The formula installs the `knowledge` client and pulls in the `knowledge-server`
graph-server binary it depends on.

## 2. Start the daemon

Start the knowledge background service as **your user** (not `sudo` — the daemon
reads your home-directory config and credentials):

```sh
brew services start knowledge
```

This runs `knowledge serve`, the long-lived daemon that exposes the MCP endpoint
Codex connects to. The daemon owns two loopback ports:

- **`15023`** — the streamable-HTTP MCP endpoint (`/mcp`) that Codex talks to (the
  daemon's `--http-port`).
- **`15022`** — the graph server that owns storage (the `--port`). The daemon
  starts and manages this for you; you do not launch it separately.

> Bare `knowledge` (with no subcommand) does not serve MCP — it just points you at
> `knowledge serve`. Always run the daemon via `brew services start knowledge` (or
> `knowledge serve` directly) and point Codex at its MCP endpoint.

## 3. Install the Codex assets and register the MCP server

```sh
knowledge install-codex-assets
```

This command installs the agent and skill catalog into Codex's split layout and
registers the MCP server:

1. Writes the bundled **skills verbatim** to `~/.agents/skills`.
2. Writes the bundled **agents, translated to Codex's TOML format**, to
   `~/.codex/agents/<name>.toml` (each `agents/<name>.md` becomes
   `<name>.toml`).
3. Primes the knowledge-managed block in `~/.codex/AGENTS.md` (the tool-usage
   guidance).
4. Unless you pass `--no-mcp`, registers the knowledge MCP server with Codex. It
   runs:

   ```sh
   codex mcp add knowledge --url http://127.0.0.1:15023/mcp
   ```

   `codex mcp add` has no timeout flag, so the installer then patches
   `~/.codex/config.toml` to set the per-server tool-call timeout, preserving every
   other entry in the file:

   ```toml
   [mcp_servers.knowledge]
   tool_timeout_sec = 180
   ```

   That 180 s timeout keeps legitimately long operations (collect, large reads)
   from being cut off by Codex's default.

Two deliberate differences from the Claude setup:

- **No promote-guard hook.** The Claude installer merges a `PreToolUse` hook into
  `settings.json`; the Codex installer installs no equivalent (it writes no
  `settings.json` and no hook).
- **No `-s user` scope flag.** Codex's `mcp add` has no scope flag, so the
  registration omits it.

Useful flags:

- `--dry-run` — preview everything that would be written, touching nothing.
- `--diff` — read-only; print a unified diff of every file that differs from the
  bundled version.
- `--verbose` — print each file path as it is written.
- `--no-mcp` — skip the MCP registration (install only the assets).

The full flag table is in the
[binaries guide](binaries.md#knowledge-install-codex-assets).

> After a `brew upgrade knowledge`, re-run `knowledge install-codex-assets` to
> refresh any assets that drifted.

## 4. Verify

1. Confirm Codex sees the server:

   ```sh
   codex mcp list
   ```

   `knowledge` should appear at `http://127.0.0.1:15023/mcp`.
2. Restart Codex (or start a new thread) so it reloads the registered MCP server
   and the freshly installed skills, then try a knowledge tool.

## 5. Configure the provider (optional)

On first run the daemon auto-detects a provider and writes `~/.knowledge/config`.
If you have the `codex` CLI on PATH, it can pick `codex-cli`. The `codex-cli`
provider needs two things in the config:

- **`cli_bin`** — the absolute path to your `codex` binary (CLI providers have no
  PATH fallback; the auto-detector resolves this for you on first run).
- **`model`** — the default codex model is **`gpt-5.3-codex-spark`**.

Codex works out of the box with these defaults — no extra setup and no manual
argument tweaking. Point your editor at the daemon, install the assets, and the
`codex-cli` provider runs as-is.

See the [configuration guide](config.md) for the full `~/.knowledge/config` shape,
including `[default]` and `[credentials]` (a Voyage key for vector search, a Linear
key for the ticket backend).

## See also

- [Configuration](config.md) — the `~/.knowledge/config` file.
- [Binaries & CLI flags](binaries.md) — every subcommand and flag.
- [Set up with Claude Code](setup-claude.md) — the Claude counterpart.
