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
| `dry_run` | boolean |  |  | Web/PDF only, transformer="recipe" only: compute emissions but write nothing. |
| `end` | string |  |  | Logs only: RFC3339 end timestamp. |
| `extract` | boolean |  |  | Web/PDF only, transformer="recipe" only: EXTRACT MODE — write nothing and return the emitted rows for inspection. Bounded by max_rows and max_bytes, with any truncation disclosed in the response. |
| `filters` | object |  |  | Logs only: exact-match label filters applied to log entries. |
| `follow_patterns` | array of string |  |  | Web only: regex allowlist for internal links. |
| `follow_patterns[]` | string |  |  |  |
| `force` | boolean |  |  | Skip safety check for existing indexed graphs. |
| `id` | string |  |  | Opaque identifier parsed by the collector (path, account:region, web source slug, absolute path to a .pdf, etc.). Optional for type="logs". |
| `kube_context` | string |  |  | Logs only: kubeconfig context name. |
| `max_bytes` | integer |  |  | Web/PDF only, transformer="recipe" only, extract mode: cap on the rendered response size in bytes. 0 selects the default (65536). Truncation is stated in the response rather than applied silently. |
| `max_depth` | integer |  |  | Web only: BFS depth bound from a seed URL. |
| `max_download_bytes` | integer |  |  | Web only: per-(owner,repo,ref) cap on github materialization downloads. 0=default (50 MiB), -1=unlimited, >0=explicit cap (uncompressed bytes). |
| `max_entries` | integer |  |  | Logs only: cap on entries pulled from the provider. |
| `max_pages` | integer |  |  | Web only: cap on total pages fetched across the crawl. |
| `max_pages_per_host` | integer |  |  | Web only: cap on pages fetched from any single host within the crawl, independent of max_pages. 0 = off (no per-host cap). When both fire, the crawl stops for a host once either cap hits first. |
| `max_path_segments` | integer |  |  | Web only: cap on the number of non-empty URL path segments a followed link may have; catches recursive-path traps like /a/b/a/b/.... 0 = off (unbounded), the default. |
| `max_rows` | integer |  |  | Web/PDF only, transformer="recipe" only, extract mode: cap on rows returned. 0 selects the default (200); the response reports rows matched alongside rows returned, so a truncated extract is never mistaken for a short one. |
| `params` | object |  |  | Registered custom_collector types only: opaque param object forwarded to the external collector binary, validated against its param_schema before exec. Built-in types ignore it. |
| `politeness_ms` | integer |  |  | Web only: per-host request delay in milliseconds. |
| `promote` | boolean |  |  | Code only: promote this branch to the base graph — land in base regardless of the recorded default branch, overwrite the recorded default branch to the collected branch, and delete the now-redundant same-name overlay. No effect for non-code collectors. |
| `provider` | string |  |  | Logs only: provider identifier (e.g., cloudwatch, loki, stackdriver, k8s). |
| `raw_query` | string |  |  | Logs only: provider-native query overriding structured fields. |
| `recipe` | string |  |  | Web/PDF only, transformer="recipe" only: name of a recipe node. The recipe's source_graph_type metadata must match `type`. |
| `recipe_body` | string |  |  | Web/PDF only, transformer="recipe" only: an INLINE recipe body to run instead of a saved recipe named by `recipe`. Requires extract=true — a write target comes from a saved recipe node, so to freeze an extraction save the same body as a recipe node and run it by name. Mutually exclusive with `recipe`. |
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
