# collect

## Overview

`collect` pulls data from an external source into a graph. Each collector type
handles one kind of source — `code` for a repository, `aws`/`gcp` for a cloud
account, `logs` for a configured log backend, `web` for a crawl, `pdf` for a
document. The collector discovers, chunks, and writes typed nodes and edges into
the appropriate graph. It is dispatched by the `type` field rather than an
`operation` field.

Collection runs on the client side: the knowledge MCP client intercepts the call
and runs the collector locally, streaming chunks to the server. Summarization and
embedding then drain in the background.

## When & how to use

Reach for `collect` to index a source so it becomes searchable. The most common
case is re-collecting a code repo after changes — unchanged nodes carry their
summaries and vectors forward, so only changed files are re-summarized.

`type` is always required. `id` is required for every type except `logs`:

| `type` | Required | `id` is… |
| --- | --- | --- |
| `code` | `type`, `id` | An absolute path to the repo root (the repo name is derived from it). |
| `aws` / `gcp` | `type`, `id` | The account/region selector the collector parses. |
| `web` | `type`, `id` | A source slug; pair with `seed_urls` to start the crawl. |
| `pdf` | `type`, `id` | An absolute path to the `.pdf` file. |
| `logs` | `type` | Optional — supply `backend`/`provider` plus the logs-only filters instead. |

```jsonc
// Re-index a code repo (use an ABSOLUTE path)
collect({ "type": "code", "id": "/Users/me/code/myrepo" })

// Crawl a docs site
collect({ "type": "web", "id": "mydocs", "seed_urls": ["https://example.com/docs"] })
```

For `type: "code"`, always pass an absolute path — a relative path (`"."`,
`"./foo"`) is rejected, because the repo name is taken from the final path segment
and a relative path would key a fresh graph under the wrong name. A code
re-collection takes from tens of seconds to a couple of minutes; the chunk upload
returns quickly while summarization continues in the background.

## Registered custom collector types

Beyond the built-in types (`code`, `aws`, `gcp`, `logs`, `web`, `pdf`), a
**registered custom type** is also an accepted `type` for `collect`. Custom
types are defined with the [`custom_collector`](custom_collector.md) tool, which
pairs a type name with an external collector binary. Once registered, run a
collection against it exactly like a built-in: `collect({ "type": "<your-type>", ... })`.

Registered custom collectors carry their domain parameters inside the single
`params` object: the whole object is validated against the collector's
`param_schema` and forwarded to the binary before exec. The built-in types ignore
`params` and read their own typed fields instead.

For a registered type the collect `id` doubles as the **default `graph_name`** —
when the collector's envelope omits `graph_name`, the collection writes into the
named graph identified by the `id` (an envelope that sets its own `graph_name`
overrides this). See the [`custom_collector` guide](custom_collector.md) for the
full plugin/envelope contract, the settable node/edge field set, the param
transport and security model, and a worked register → collect example.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `auth_type` | string |  |  | Logs only: auth mechanism (bearer, basic, aws_profile, api_key, service_account, kubeconfig). |
| `backend` | string |  |  | Logs only: name of a configured log_backend node. |
| `credential` | string |  |  | Logs only: credential value when passing provider inline. |
| `dry_run` | boolean |  |  | REFUSED with transformer="recipe". It meant "compute the projection but skip the write"; a recipe run writes nothing, so there is no write to skip. Pass extract=true to read the rows back. |
| `end` | string |  |  | Logs only: RFC3339 end timestamp. |
| `extract` | boolean |  |  | Web/PDF only, transformer="recipe" only, and REQUIRED there: return the emitted rows for inspection. It is the only mode a recipe run has — every run writes nothing. Bounded by max_rows and max_bytes, with any truncation disclosed in the response. |
| `filters` | object |  |  | Logs only: exact-match label filters applied to log entries. |
| `follow_patterns` | array of string |  |  | Web only: regex allowlist for internal links. |
| `follow_patterns[]` | string |  |  |  |
| `force` | boolean |  |  | Skip the safety check for existing indexed graphs — the code collector's bypass, and shared by every collect type EXCEPT one. REFUSED with transformer="recipe": a recipe run returns rows and writes nothing, so there is nothing for force to bypass. |
| `id` | string |  |  | Opaque identifier parsed by the collector (path, account:region, web source slug, absolute path to a .pdf, etc.). A pdf graph is NAMED AFTER THE FILE — the sanitized basename with no suffix — so for type="pdf" the id is the absolute path to the document, not the graph name. Optional for type="logs", and optional for type="web" when seed_urls is supplied: the graph is then named after the first seed URL's host, with a leading www. stripped and dots mapped to hyphens (www.Go101.org becomes go101-org). A collect into an existing raw graph that was collected from a DIFFERENT source is refused, naming both sources, rather than merged into it. |
| `kube_context` | string |  |  | Logs only: kubeconfig context name. |
| `materialize_github` | boolean |  |  | Web only: OPT IN to materializing github repository seeds into the graph. Off by default — without it a github URL is fetched not at all and is reported in the collect response as a follow-up candidate for you to decide about. Refused when set with no github repository URL among the seeds. |
| `max_bytes` | integer |  |  | Web/PDF only, transformer="recipe" only, extract mode: cap on the rendered response size in bytes. 0 selects the default (65536). Truncation is stated in the response rather than applied silently. |
| `max_concurrency` | integer |  |  | Web only: number of crawl workers. 0 selects the default (8) and a value above 32 is REFUSED, naming the value and the cap, rather than clamped. Per-host politeness does NOT serialize same-host fetches — it enforces a minimum spacing between request STARTS to one host, so same-host parallelism is bounded by roughly ceil(request_latency / politeness_ms) and capped by max_concurrency, while cross-host parallelism is bounded by max_concurrency alone. |
| `max_depth` | integer |  |  | Web only: BFS depth bound from a seed URL. |
| `max_download_bytes` | integer |  |  | Web only: per-(owner,repo,ref) cap on github materialization downloads. 0=default (50 MiB), -1=unlimited, >0=explicit cap (uncompressed bytes). |
| `max_entries` | integer |  |  | Logs only: cap on entries pulled from the provider. |
| `max_pages` | integer |  |  | Web only: cap on total pages fetched across the crawl. |
| `max_pages_per_host` | integer |  |  | Web only: cap on pages fetched from any single host within the crawl, independent of max_pages. 0 = off (no per-host cap). When both fire, the crawl stops for a host once either cap hits first. |
| `max_path_segments` | integer |  |  | Web only: cap on the number of non-empty URL path segments a followed link may have; catches recursive-path traps like /a/b/a/b/.... 0 = off (unbounded), the default. |
| `max_rows` | integer |  |  | Web/PDF only, transformer="recipe" only, extract mode: cap on rows returned. 0 selects the default (200); the response reports rows matched alongside rows returned, so a truncated extract is never mistaken for a short one. |
| `offset` | integer |  |  | Web/PDF only, transformer="recipe" only, extract mode: zero-based index of the first MATCHED row to return, for paging a document larger than one response. Every matched row is still counted, so the header's matched total names the whole population behind the page; the truncation line names the next offset to resume from, and a page starting past the end says so rather than looking like an empty match. Negative values are refused. |
| `params` | object |  |  | Registered custom_collector types only: opaque param object forwarded to the external collector binary, validated against its param_schema before exec. Built-in types ignore it. |
| `politeness_ms` | integer |  |  | Web only: per-host request delay in milliseconds. |
| `promote` | boolean |  |  | Code only: promote this branch to the base graph — land in base regardless of the recorded default branch, overwrite the recorded default branch to the collected branch, and delete the now-redundant same-name overlay. No effect for non-code collectors. |
| `provider` | string |  |  | Logs only: provider identifier (e.g., cloudwatch, loki, stackdriver, k8s). |
| `raw_query` | string |  |  | Logs only: provider-native query overriding structured fields. |
| `recipe` | string |  |  | REFUSED. It named a SAVED recipe node, which is removed along with the transformers graph family — recipes are ephemeral inline bodies now. Pass the body as `recipe_body` with extract=true instead. The param is still declared so the refusal can name what you sent. |
| `recipe_body` | string |  |  | Web/PDF only, transformer="recipe" only, and REQUIRED there: the inline recipe body to run. Requires extract=true — a recipe run returns rows and writes nothing, so there is no other mode. See help("recipes") for worked bodies to copy. |
| `seed_urls` | array of string |  |  | Web only: starting URL(s) for the crawl. |
| `seed_urls[]` | string |  |  |  |
| `severity_min` | string |  |  | Logs only: minimum severity to include (DEBUG\|INFO\|WARN\|ERROR). |
| `source` | string |  |  | Logs only: provider-specific log source selector. |
| `start` | string |  |  | Logs only: RFC3339 start timestamp. |
| `text_filter` | string |  |  | Logs only: free-text substring or pattern applied to log messages. |
| `transformer` | string |  |  | Web/PDF only: optional transformer name. |
| `type` | string | yes |  | Collector name (e.g., "code", "aws", "gcp", "logs", "web", "pdf"). |
| `url` | string |  |  | Logs only: backend base URL. |
| `user_agent` | string |  |  | Web only: override for the HTTP User-Agent header. |
<!-- END GENERATED: params -->
