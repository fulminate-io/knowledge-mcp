# Configuration

The `knowledge serve` daemon reads a single TOML config file at `~/.knowledge/config`. It tells
knowledge which LLM provider and model to use for its background work
(summarization, the supervisor, topic summaries), which provider
embeds and reranks, and — optionally — holds the API keys those providers and the
Linear backend need.

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
- **`[supervisor]`** — the strong-model LLM the thought-topic synthesis pass
  resolves for its per-topic summaries.
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

# Optional: give the topic-summary model a deeper model while the summarizer
# stays cheap.
# [topics]
# provider = "anthropic"
# model    = "claude-opus-4-7"
```

The auto-detected default `provider = "claude-cli"` needs none of the LLM API keys
below — it authenticates through your local `claude` login.

## Embedding and rerank sections

Two further sections configure the two search-side axes. `[embedder]` decides
what turns text into vectors, at what width and in what representation.
`[reranker]` decides what re-scores a candidate set at search time. Both are
optional.

They are configured *separately* from the LLM consumers above, and they differ
from them in two ways you can see in the file:

- **Their own provider vocabulary.** `provider` here is one of `voyage`,
  `cohere`, `gemini`, `openai-compatible`, `fake` — not the LLM sections'
  `anthropic`/`openai`/`gemini`/`claude-cli`/`codex-cli`. The vocabularies are
  deliberately separate because three of the LLM providers publish no embeddings
  API at all; a shared list would let you name a provider that cannot embed.
- **No `[default]` inheritance.** `[default]` carries an LLM provider, which is
  not an embedding provider. An absent `[embedder]` or `[reranker]` falls back to
  its own axis default, never to `[default]`.

With both sections absent — the common case — each axis resolves to the `voyage`
provider with an empty `model`, and an empty `model` means *the arm's own
default*. The config layer carries no provider model name, so read an omitted
model as "whatever that arm defaults to", not as a missing value.

Both sections are machine-wide today, one embedder and one reranker serving every
graph; per-graph configuration is planned.

### `[embedder]` keys

| Key | Required? | Meaning |
| --- | --- | --- |
| `provider` | optional (absent = `voyage`) | One of `voyage`, `cohere`, `gemini`, `openai-compatible`, `fake`. See [what this build can select](#what-this-build-can-select). |
| `model` | optional | The provider-specific model name. Empty = the arm's own default. |
| `base_url` | optional | Overrides the arm's endpoint. |
| `key` | optional | A per-section credential that overrides the provider-resolved one. |
| `dimension` | optional (absent = `256`) | Vector width in **bits**. This build accepts `256` only. |
| `dtype` | optional (absent = `"ubinary"`) | Output representation. This build accepts `"ubinary"` only. |

### `[reranker]` keys

`[reranker]` takes `provider`, `model`, `base_url` and `key`, with the same
meanings. It has no `dimension` and no `dtype`: a reranker returns scores, not
vectors, so there is nothing to quantize.

### What this build can select

The two axes share a provider vocabulary, not a set of arms. Naming a provider
the vocabulary accepts is therefore not the same as getting one, and the
difference surfaces as an error when the arm is constructed rather than when the
file is parsed.

| `provider` | `[embedder]` | `[reranker]` |
| --- | --- | --- |
| `voyage` | yes | yes |
| `cohere` | yes | no arm registered |
| `gemini` | yes, `dtype = "float32"` only (see below) | no arm registered |
| `openai-compatible` | yes, `dtype = "float32"` only (see below) | no arm registered |
| `fake` | yes | no arm registered |

Two consequences worth stating outright, because a config naming either case
parses cleanly and fails later:

- **The `gemini` and `openai-compatible` embedding arms serve `dtype =
  "float32"` only.** Both refuse `dtype = "ubinary"`, each naming its own
  reason: Gemini publishes no quantized embedding output for its embedding
  models, and the OpenAI embeddings request envelope carries no quantization
  knob, so there is no way to ask a compatible server for a quantized vector.
  The refusal names both the value you supplied and the value the arm serves.
  Because `dtype` defaults to `"ubinary"`, naming either provider **without**
  also setting `dtype = "float32"` is refused when the embedder is built.
- **One rerank arm is registered: `voyage`.** A provider that embeds does not
  necessarily publish a rerank API, so the rerank registry is allowed to be
  narrower than the embed one; an absent entry is how the reranker reports "this
  provider publishes no rerank API". A `[reranker]` naming anything else parses,
  then fails when the reranker is constructed, with an error naming the provider
  as not registered.

### Credentials for these sections

Each axis resolves its **own** key from its **own** resolved provider:

| `provider` | Resolved credential |
| --- | --- |
| `voyage` | `[credentials].voyage_api_key`, else `VOYAGE_API_KEY` |
| `cohere` | `[credentials].cohere_api_key`, else `COHERE_API_KEY` |
| `gemini` | `[credentials].gemini_api_key`, else `GEMINI_API_KEY` |
| `openai-compatible` | `[credentials].openai_api_key`, else `OPENAI_API_KEY` |
| `fake` | none — it authenticates nothing |

Because resolution runs per axis from that axis's own provider, an embedder on
one provider and a reranker on another read different credentials, and neither
reads the other's.

**The per-section `key` overrides that resolution**, and it is the reason the key
exists at all: a provider name cannot identify the credential for
`openai-compatible`, because that one arm serves a whole population of
third-party endpoints. Resolving its key from the provider name alone hands every
one of them your `OPENAI_API_KEY` — so if you point `base_url` at a non-OpenAI
compatible endpoint, set `key` as well. The endpoint is a per-section fact, which
makes the credential a per-section fact too. Set `key` and it wins; leave it
unset and the table above applies.

This override exists on `[embedder]` and `[reranker]` only. The LLM sections
above still resolve their credential from their provider.

### Key or `base_url`

An API provider — `voyage`, `cohere`, `gemini`, `openai-compatible` — needs a
resolved key **or** a `base_url`. Building an arm for one with neither is
refused, naming the provider. The rule is "key or `base_url`, never neither": it
is not "`base_url` implies keyless", and not "`base_url` is required".

A `base_url` with no key is the keyless-endpoint case — the endpoint is a
compatible server you run yourself, such as vLLM, Ollama or LM Studio, which
handles auth out of band.

`fake` is exempt from the rule: it makes no network call, so it needs neither a
credential nor an endpoint.

Having no embedding credential *at all* is a different thing and is not an
error — it is the BM25-only degrade described under
[Embedding credentials](#embedding-credentials-and-the-bm25-only-degrade).

### Vector width and representation

`dimension` and `dtype` are the quantization knobs. **This build accepts
`dimension` of `256`, `512`, `1024` or `2048`, and `dtype` of `"ubinary"` or
`"float32"`.** Any other value is refused with an error naming the value you
supplied and the accepted set.

**The defaults do not move**: an absent `[embedder]` section, or one that leaves
these keys unset, still resolves to `dimension = 256` with `dtype = "ubinary"`.
Widening what a build accepts is not the same as changing what an existing
deployment runs — a graph's embed identity is sticky, and no config change may
trigger a corpus-scale re-embed spend.

Not every arm serves every accepted value. The accepted set is what this build
can *carry*; what a given provider can *produce* is the arm's own statement, and
an arm refuses a dtype it cannot serve at construction time, naming both the
value you supplied and the ones it serves. See the arm table above.

The refusal is enforced both when the config file is parsed and when an embedding
configuration is built in-process, with the same message, so which layer refused
you does not change what you read.

### The `fake` provider

`fake` is a deterministic arm that derives each vector from a hash of the text.
It ships in the binary rather than living in a test file because an end-to-end
run needs a provider whose output a test can predict exactly — across processes
and machines, with no network and no key.

**Its vectors carry no semantic meaning.** Two texts that mean the same thing
hash to unrelated bytes, so search results from a graph embedded this way are
arbitrary. That is the point: determinism, not quality. It is a test and
end-to-end arm, not a production embedder, and constructing it logs a warning
saying so.

### Startup checks

At startup the daemon pings each configured axis once: a single one-token embed
on the embedding axis, and a single one-document rerank on the rerank axis. Each
ping runs against the model you configured rather than a cheaper stand-in — a
ping against a model you do not use proves less. The rerank ping is billed spend
that a config without a `[reranker]` credential does not incur.

An axis with no resolved credential and no `base_url` returns without calling
anything, which is the lever if you want no startup spend on that axis. A failed
ping names a class you can act on: a rejected key (401/403, which covers invalid,
revoked and out-of-credits), rate limiting (429), another status, or an endpoint
that could not be reached — a firewall, a captive portal, or a local compatible
server that is not running.

A misconfigured section fails loudly rather than being coerced into something
workable. An unrecognized `provider` is refused with an error naming both the
value you wrote and the accepted vocabulary.

### Changing the embedding model on a graph that already has vectors

Setting `[embedder].model` is supported, and on a graph that already holds
vectors it carries a hazard worth stating before you do it: **changing the model
does not re-embed anything.** The embed cache is keyed on content, so unchanged
content hits the cache and no gap is reported. The corpus keeps whatever model
produced its stored vectors while every new query vector comes from the new
model, and the two are compared by hamming distance. Both are the same size, so
no length guard notices. The failure mode is degraded search quality with no
error, no crash and no log line — until the corpus is rebuilt.

One config drives both the indexing embedder and the search-time query embedder,
which is why the two halves can diverge this way.

The daemon warns when the resolved embedding model is not the arm's own default.
Read that warning for exactly what it says: there is no stored record of which
model produced a graph's existing vectors, so it is **not** a comparison against
your corpus — only a note that the configured model differs from the default. An
empty `model` is the ordinary no-config case and is not warned about.

The rerank model carries no equivalent hazard: reranking re-scores results at
query time and stores nothing, so changing `[reranker].model` takes effect
immediately with nothing to rebuild.

### Examples

Cohere embeddings with Voyage rerank. Each axis resolves its own key, so this
config reads `COHERE_API_KEY` for the embedder and `VOYAGE_API_KEY` for the
reranker:

```toml
schema_version = 1

[default]
provider = "claude-cli"
model    = "claude-haiku-4-5"
cli_bin  = "/opt/homebrew/bin/claude"

[embedder]
provider  = "cohere"
dimension = 256
dtype     = "ubinary"

[reranker]
provider = "voyage"
```

Keeping the default embedder and naming a cheaper, faster rerank model. Only the
rerank model is pinned here — see the hazard above before pinning an embedding
model on a graph that already has vectors:

```toml
schema_version = 1

[reranker]
provider = "voyage"
model    = "rerank-2.5-lite"
```

A third-party endpoint behind the `openai-compatible` arm, with its own key so
that endpoint never receives your `OPENAI_API_KEY`. This is the shape the
per-section `key` exists for. `dtype` is set explicitly because that arm serves
`"float32"` only and the default is `"ubinary"` — omit it and the embedder is
refused at construction:

```toml
schema_version = 1

[embedder]
provider = "openai-compatible"
model    = "text-embedding-3-small"
base_url = "https://embeddings.example.com/v1"
key      = "the-endpoint-s-own-key"
dtype    = "float32"
```

The deterministic arm, for an end-to-end run that must not call out or hold a
key:

```toml
schema_version = 1

[embedder]
provider = "fake"
```

## Credentials

The optional `[credentials]` table holds backend and LLM API keys. Every key is
optional, and every key falls back to a matching environment variable when unset in
the file. **The file wins over the environment** when both are set — the file is
the deliberate, persistent choice. An empty result simply disables the
corresponding feature; it is never an error.

Storing keys in the file (rather than only in the environment) makes the daemon
launch-method-agnostic: brew services, systemd, and k8s all read the same file. If
you put secrets here, **`chmod 600 ~/.knowledge/config`** so only you can read it.

The six keys:

| `[credentials]` key | Env-var fallback | Enables |
| --- | --- | --- |
| `voyage_api_key` | `VOYAGE_API_KEY` | The `voyage` embedding and rerank provider (see below). |
| `linear_api_key` | `LINEAR_API_KEY` | The Linear ticket/project backend (see below). |
| `anthropic_api_key` | `ANTHROPIC_API_KEY` | The `anthropic` LLM provider. |
| `openai_api_key` | `OPENAI_API_KEY` | The `openai` LLM provider, and the `openai-compatible` embedding provider. |
| `gemini_api_key` | `GEMINI_API_KEY` | The `gemini` LLM provider, and the `gemini` embedding provider. |
| `cohere_api_key` | `COHERE_API_KEY` | The `cohere` embedding provider. |

```toml
[credentials]
voyage_api_key    = "..."
linear_api_key    = "..."
anthropic_api_key = "..."
openai_api_key    = "..."
gemini_api_key    = "..."
cohere_api_key    = "..."
```

Three of these keys are read by both axes' vocabularies — the LLM sections and
the `[embedder]`/`[reranker]` sections resolve from the same `[credentials]`
table. Where that sharing is a problem, the per-section `key` is the escape:
see [Credentials for these sections](#credentials-for-these-sections).

> The Fulminate Cloud login token is **not** one of these keys. It is not stored in
> this file at all — it lives in your operating system keychain, or in a 0600
> file under `~/.knowledge` when no keychain is reachable (both managed by
> `knowledge login` / `knowledge logout`). `[credentials]` is only the six
> backend/LLM/embedding keys above.

### Embedding credentials and the BM25-only degrade

An embedding credential is what enables vector and hybrid search: with one
resolved, knowledge stores binary vectors and runs vector/hybrid search plus
cross-encoder rerank. **Without one, search degrades to BM25-only** — a
documented, non-error opt-out, not a failure.

Which key that is depends on the `[embedder]` provider, per
[the resolution table above](#credentials-for-these-sections); with no
`[embedder]` section the provider is `voyage` and the key is `voyage_api_key` /
`VOYAGE_API_KEY`. At startup, an axis with no resolved credential and no
`base_url` logs the degrade and skips its ping. The embedding axis logs:

```
precheck: no embed credential — vector search disabled (BM25 only)
```

and the rerank axis logs its own:

```
precheck: no rerank credential — cross-encoder rerank disabled (RRF ordering only)
```

Each line carries a `provider` attribute naming the axis's resolved provider, so
the two axes stay distinguishable when they are configured differently.

The default `[embedder]` provider is `voyage`. To enable Voyage search:

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
