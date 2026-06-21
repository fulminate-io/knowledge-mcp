# Set up with Claude Code

This guide gets knowledge running and wired into Claude Code on macOS, via Homebrew.
By the end you will have the knowledge daemon running as a background service and
the knowledge MCP server registered with Claude.

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
Claude connects to. The daemon owns two loopback ports:

- **`15023`** — the streamable-HTTP MCP endpoint (`/mcp`) that Claude talks to
  (the daemon's `--http-port`).
- **`15022`** — the graph server that owns storage (the `--port`). The daemon
  starts and manages this for you; you do not launch it separately.

> Bare `knowledge` (with no subcommand) does not serve MCP — it just points you at
> `knowledge serve`. Always run the daemon via `brew services start knowledge` (or
> `knowledge serve` directly) and point Claude at its MCP endpoint.

## 3. Install the Claude assets and register the MCP server

```sh
knowledge install-claude-assets
```

This one command does four things:

1. Writes the bundled agent and skill catalog under `~/.claude`, so a
   brew-installed client has the same agents and skills a source build ships.
2. Primes the knowledge-managed block in `~/.claude/CLAUDE.md` (the tool-usage
   guidance).
3. Merges the promote-guard `PreToolUse` hook into `~/.claude/settings.json` (a
   deep JSON merge that leaves your other settings intact).
4. Unless you pass `--no-mcp`, registers the knowledge MCP server with Claude at
   user scope. The exact command it runs is:

   ```sh
   claude mcp add-json -s user knowledge '{"type":"http","url":"http://127.0.0.1:15023/mcp","timeout":180000}'
   ```

   That registers the daemon's loopback `/mcp` endpoint as a streamable-HTTP MCP
   server named `knowledge`, with a generous 180000 ms (180 s) per-call timeout so
   legitimately long operations (collect, large reads) are not cut off by Claude's
   default.

Useful flags:

- `--dry-run` — preview everything that would be written, touching nothing.
- `--diff` — read-only; print a unified diff of every file that differs from the
  bundled version.
- `--verbose` — print each file path as it is written.
- `--no-mcp` — skip the MCP registration (install only the assets).

The full flag table is in the
[binaries guide](binaries.md#knowledge-install-claude-assets).

> After a `brew upgrade knowledge`, re-run `knowledge install-claude-assets` to
> refresh any assets that drifted — the binary warns you when they are stale.

## 4. Verify

1. Confirm the daemon is listening on `15023` (the MCP endpoint).
2. Confirm Claude sees the server:

   ```sh
   claude mcp list
   ```

   `knowledge` should appear at `http://127.0.0.1:15023/mcp`.
3. Start a **new** Claude session so it loads the freshly-registered server, then
   try a knowledge tool (e.g. ask it to `search` the graph).

## 5. Configure the provider (optional)

On first run the daemon auto-detects a provider and writes `~/.knowledge/config`.
If you have the `claude` CLI on PATH, it picks `claude-cli` as the zero-config
default: it needs no API key, authenticating through your existing `claude` login.

You only need to touch the config to change a model, point a consumer at a
different provider, or add a credential (a Voyage key for vector search, a Linear
key for the ticket backend). See the [configuration guide](config.md) for the full
`~/.knowledge/config` shape, including `[default]` and `[credentials]`.

## See also

- [Configuration](config.md) — the `~/.knowledge/config` file.
- [Binaries & CLI flags](binaries.md) — every subcommand and flag.
- [Set up with Codex](setup-codex.md) — the Codex counterpart.
