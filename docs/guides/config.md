# Configuration

The `knowledge serve` daemon reads a single TOML config file at `~/.knowledge/config`. It tells
knowledge which LLM provider and model to use for its background work
(summarization, dream, the hive supervisor, topic summaries) and — optionally —
holds the API keys for the embedder and the Linear backend.

You usually do not write this file by hand. On first run, if no config exists, the
daemon auto-detects a usable provider (preferring a CLI you already have on PATH,
such as `claude` or `codex`) and writes a starter config for you. This page
documents the shape so you can edit it confidently when you want to change a model,
add a credential, or point a single consumer at a different provider.

The daemon reads the file only at startup. After editing it, restart the daemon
(`brew services restart knowledge`, or stop and re-run `knowledge serve`) for the
change to take effect.

## File location

`~/.knowledge/config` (no extension). The daemon always reads this path. A
different path can be passed to `knowledge doctor --config-file` for diagnostics;
empty resolves to the default above.

## Top-level keys

Two keys live at the top level, outside any `[section]`:

- **`schema_version`** — the on-disk format version. The current value is `1`.
  Absent is treated as `1` (pre-versioning configs are accepted). The server
  rejects a config whose `schema_version` is *higher* than the binary supports,
  with an "upgrade knowledge" message — that is the only thing this key gates.
- **`health_probe_interval`** — optional. A Go duration string (e.g. `"10m"`). It
  sets how often the background health-prober re-checks a fallback chain entry that
  was marked limited, shifting traffic back once it recovers. Absent (the common
  case) means "use the built-in default" — it is not an instant re-probe. A
  malformed duration is a hard startup error that names the key.

## Provider/model sections

The config resolves an LLM `(provider, model)` for each *consumer* — the substrate
components that actually call an LLM. The sections are:

- **`[default]`** — applies to any consumer that does not set its own value.
- **`[summarizer]`** — the pipeline summarizer (high-volume, text-extraction work;
  a haiku-class model is plenty).
- **`[dream]`** — the dream phases (deeper, multi-step reasoning; an opus-class
  model benefits).
- **`[supervisor]`** — the hive supervisor (the strong-model judge for ambiguous
  worker transcripts).
- **`[topics]`** — the topic-summary model the similarity lever uses over thought
  clusters.

Each per-consumer section is optional. When a section is absent, that consumer
inherits everything from `[default]`. When a section is present, it overrides
`[default]` **per field** — anything you set wins, anything you leave out inherits.

### Section keys

Every section takes the same four keys:

| Key | Required? | Meaning |
| --- | --- | --- |
| `provider` | **required** (via the section or `[default]`) | One of `anthropic`, `openai`, `gemini`, `claude-cli`, `codex-cli`. |
| `model` | **required** (via the section or `[default]`) | The provider-specific model name. |
| `cli_bin` | **required for CLI providers** (`claude-cli`, `codex-cli`); ignored for API providers | Absolute path to the CLI binary. |
| `base_url` | optional, API providers only | Overrides the provider's default endpoint. |

The required-vs-optional contract:

- **`provider` and `model` are always required.** If neither the consumer's section
  nor `[default]` supplies one, startup fails naming the missing field.
- **`cli_bin` is required for the CLI providers** `claude-cli` and `codex-cli`.
  There is no PATH fallback — the path must be set, exist, and be executable. This
  is deliberate: it makes a launchd/systemd/k8s-managed server (which runs with a
  sanitized PATH) behave exactly like an interactive shell. First-run auto-detect
  resolves the absolute path for you; edit it if you ever move the binary. For API
  providers (`anthropic`/`openai`/`gemini`) `cli_bin` is ignored.
- **`base_url` is optional** and applies only to API providers. Setting it points
  that provider at a different endpoint — for example a local OpenAI-/Anthropic-/
  Gemini-compatible server. A set `base_url` is also the *keyless* alternative: an
  API-provider consumer with `base_url` set passes startup validation without a
  key, since the endpoint handles auth out of band.

### Fallback chains

A consumer section may carry an ordered fallback chain via nested
`[[<consumer>.fallback]]` array-of-tables. Each fallback entry is resolved through
the same per-field `[default]` inheritance and the same `provider`/`model`
required-field gate as the primary. The consumer shifts to the next entry when the
primary fails on a transient condition (quota, rate-limit, overload, timeout). The
common case is no fallback at all.

### Example

```toml
schema_version = 1

[default]
provider = "claude-cli"
model    = "claude-haiku-4-5"
cli_bin  = "/opt/homebrew/bin/claude"

# Optional: give dream a deeper model while the summarizer stays cheap.
# [dream]
# provider = "anthropic"
# model    = "claude-opus-4-7"
```

The auto-detected default `provider = "claude-cli"` needs none of the LLM API keys
below — it authenticates through your local `claude` login.

## Credentials

The optional `[credentials]` table holds backend and LLM API keys. Every key is
optional, and every key falls back to a matching environment variable when unset in
the file. **The file wins over the environment** when both are set — the file is
the deliberate, persistent choice. An empty result simply disables the
corresponding feature; it is never an error.

Storing keys in the file (rather than only in the environment) makes the daemon
launch-method-agnostic: brew services, systemd, and k8s all read the same file. If
you put secrets here, **`chmod 600 ~/.knowledge/config`** so only you can read it.

The five keys:

| `[credentials]` key | Env-var fallback | Enables |
| --- | --- | --- |
| `voyage_api_key` | `VOYAGE_API_KEY` | Vector/hybrid search + rerank (see below). |
| `linear_api_key` | `LINEAR_API_KEY` | The Linear ticket/project backend (see below). |
| `anthropic_api_key` | `ANTHROPIC_API_KEY` | The `anthropic` API provider. |
| `openai_api_key` | `OPENAI_API_KEY` | The `openai` API provider. |
| `gemini_api_key` | `GEMINI_API_KEY` | The `gemini` API provider. |

```toml
[credentials]
voyage_api_key    = "..."
linear_api_key    = "..."
anthropic_api_key = "..."
openai_api_key    = "..."
gemini_api_key    = "..."
```

> The Fulminate Cloud login token is **not** one of these keys. It is not stored in
> this file at all — it lives in your operating system keychain (managed by
> `knowledge login` / `knowledge logout`). `[credentials]` is only the five
> backend/LLM keys above.

### Voyage (embeddings + search quality)

The `voyage_api_key` key (env: `VOYAGE_API_KEY`) enables Voyage AI embeddings. With
it set, knowledge stores binary vectors and runs vector/hybrid search plus
cross-encoder rerank. **Without it, search degrades gracefully to BM25-only** — this
is a documented, non-error opt-out, not a failure. At startup the daemon logs
exactly:

```
precheck: VOYAGE_API_KEY unset — vector search disabled (BM25 only)
```

To enable Voyage search:

1. Create a Voyage API key. The how-to is in the official Voyage docs:
   [docs.voyageai.com — API key and installation](https://docs.voyageai.com/docs/api-key-and-installation).
   You can create one directly from the Voyage dashboard (requires login):
   [dashboard.voyageai.com — API keys](https://dashboard.voyageai.com/organization/api-keys).
2. Set it: add `voyage_api_key = "..."` under `[credentials]`, or export
   `VOYAGE_API_KEY`. If you put it in the file, `chmod 600 ~/.knowledge/config`.
3. Restart the daemon. The BM25-only degrade line should no longer appear.

### Linear (ticket/project backend)

The `linear_api_key` key (env: `LINEAR_API_KEY`) enables the Linear backend, which
syncs tickets and projects to and from Linear. This is part of the open-source
build — it is gated **only on the key being present**, not on any paid or cloud
tier. With the key set the Linear backend is enabled; with it unset the backend is
simply disabled.

You need a Linear **Personal API Key**. To enable it:

1. Create a Linear Personal API Key. The how-to is in the official Linear docs:
   [linear.app/docs — API and webhooks](https://linear.app/docs/api-and-webhooks).
   You create the key under Settings → Account → Security & Access (requires login):
   [linear.app — account security settings](https://linear.app/settings/account/security).
2. Set it: add `linear_api_key = "..."` under `[credentials]`, or export
   `LINEAR_API_KEY`. If you put it in the file, `chmod 600 ~/.knowledge/config`.
3. Restart the daemon.

## See also

- [Binaries & CLI flags](binaries.md) — the server and client flags, including
  `--config-file`, `--port`, and `--http-port`.
- [Set up with Claude Code](setup-claude.md) — first-run setup for Claude.
- [Set up with Codex](setup-codex.md) — first-run setup for Codex.
