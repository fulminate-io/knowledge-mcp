# collect

## Overview

`collect` pulls data from an external source into a graph. Each collector type
handles one kind of source — `code` for a repository, `web` for a crawl, `pdf`
for a document. The collector
discovers, chunks, and writes typed nodes and edges into the appropriate graph.
It is dispatched by the `type` field rather than an `operation` field.

**Cloud inventory and log collection are contrib collectors.** Eight of them:
`aws`, `gcp`, `azure` and `k8s` for cloud inventory, and `cloudwatch`, `loki`,
`stackdriver` and `k8s-logs` for logs. The cloud and log families were built
into this binary until this release and are separate collector binaries now,
each registering its own graph type. They are installed and collected exactly like
any other registered type — see the [`custom_collector` guide](custom_collector.md)
and each collector's own README under `cmd/collectors/`. Collecting one by its
old built-in name with nothing registered under it is refused, naming the
route.

Collection runs on the client side: the knowledge MCP client intercepts the call
and runs the collector locally, streaming chunks to the server. Summarization and
embedding then drain in the background.

## When & how to use

Reach for `collect` to index a source so it becomes searchable. The most common
case is re-collecting a code repo after changes — unchanged nodes carry their
summaries and vectors forward, so only changed files are re-summarized.

`type` and `id` are both always required:

| `type` | Required | `id` is… |
| --- | --- | --- |
| `code` | `type`, `id` | An absolute path to the repo root (the repo name is derived from it). |
| `web` | `type`, `id` | A source slug; pair with `seed_urls` to start the crawl. |
| `pdf` | `type`, `id` | An absolute path to the `.pdf` file. |
| `github` / `gitlab` / `bitbucket` | `type`, `id` | The org or workspace slug. |
| a registered type | `type`, `id` | The graph instance the result lands in; the registration name is the family. |

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

Beyond the built-in types (`code`, `web`, `pdf`, `github`, `gitlab`,
`bitbucket`), a
**registered custom type** is also an accepted `type` for `collect`. A custom
type is an entry in a `collectors.json` config file — see the
[`custom_collector` guide](custom_collector.md) — pairing a family name with an
MCP provider and the name of one tool to call on it, written with
`knowledge collector add`. Once the entry exists, run a collection against it
exactly like a built-in: `collect({ "type": "<your-family>", "id": "<graph>", ... })`.

A hand edit to the config file takes effect on the next collect, with no daemon
restart. A family the server still holds a record for with no config entry is
NOT collectable: the file is the registration record, and the refusal names the
entry to write.

Registered custom collectors carry their domain parameters inside the single
`params` object. It rides the MCP tool call as the `params` argument, beside the
collect `id`, and is validated against the schema the **provider** advertised for
that tool before the call is made — so a provider that needs a parameter you did
not supply refuses with a named mismatch rather than failing inside its own
handler. What may go inside `params` is that tool's own `inputSchema`. The
built-in types ignore `params` and read their own typed fields instead.

The entry's **name is the graph family** and the collect **`id` is the
instance** inside it: `collect({ "type": "tickets", "id": "acme" })` writes the
`acme` graph of the `tickets` family, and a second collect under `id: "beta"`
writes a second graph in the same family. The `id` is required for a custom
collect — it names the graph, so there is nothing to write into without one. The
provider's result carries no graph identity of its own: there is nothing for it
to override, and a provider cannot write into a family its entry did not name.

See the [`custom_collector` guide](custom_collector.md) for the config file's
shape and both scopes, the CLI, and the provider contract — the two required
schemas including the `walk_complete` completeness assertion, the settable node
and edge field set, the stdio environment block, and a worked add → collect
example per transport.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `dry_run` | boolean |  |  | REFUSED with transformer="recipe". It meant "compute the projection but skip the write", which is what an extract run already is: pass extract=true (without land) to see exactly the rows a landing would write, then land=true to write them. |
| `extract` | boolean |  |  | Web/PDF only, transformer="recipe" only: return the emitted rows for inspection and write nothing. One of the two recipe modes — a run must pass extract, land, or both. Bounded by max_rows and max_bytes, with any truncation disclosed in the response. An extract run is also the PREVIEW of what a landing would write. |
| `follow_patterns` | array of string |  |  | Web only: regex allowlist for internal links. |
| `follow_patterns[]` | string |  |  |  |
| `force` | boolean |  |  | Skip the safety check for existing indexed graphs — the code collector's bypass, and shared by every collect type EXCEPT one. REFUSED with transformer="recipe": force meant overwriting a colliding row, and a landing never overwrites — a resident id lands a versioned twin beside it and both are kept — so there is nothing for force to bypass. |
| `id` | string |  |  | Opaque identifier parsed by the collector (path, account:region, web source slug, absolute path to a .pdf, etc.). A pdf graph is NAMED AFTER THE FILE — the sanitized basename with no suffix — so for type="pdf" the id is the absolute path to the document, not the graph name. Optional for type="web" when seed_urls is supplied: the graph is then named after the first seed URL's host, with a leading www. stripped and dots mapped to hyphens (www.Go101.org becomes go101-org). A collect into an existing raw graph that was collected from a DIFFERENT source is refused, naming both sources, rather than merged into it. |
| `land` | boolean |  |  | Web/PDF only, transformer="recipe" only: WRITE the emitted nodes into the combined practice graph, grouped under a `source` hub named from the raw graph's slug. Each landed node carries the hub id in its `source_hub` metadata and one `sourced-from` edge to the hub; its own `source` field keeps the emitter's `recipe:<slug>` stamp. An emitted id that already exists lands a VERSIONED TWIN under a new id with a `next-version` edge from the old row to it, and the existing row is never modified. Refused when the combined practice graph does not exist yet, or when the raw graph records no source on its root. max_rows and offset are refused alongside it. Combine with extract=true to get the rows back as well. |
| `materialize_github` | boolean |  |  | Web only: OPT IN to materializing github repository seeds into the graph. Off by default — without it a github URL is fetched not at all and is reported in the collect response as a follow-up candidate for you to decide about. Refused when set with no github repository URL among the seeds. |
| `max_bytes` | integer |  |  | Web/PDF only, transformer="recipe" only, extract mode: cap on the rendered response size in bytes. 0 selects the default (65536). Truncation is stated in the response rather than applied silently. |
| `max_concurrency` | integer |  |  | Web only: number of crawl workers. 0 selects the default (8) and a value above 32 is REFUSED, naming the value and the cap, rather than clamped. Per-host politeness does NOT serialize same-host fetches — it enforces a minimum spacing between request STARTS to one host, so same-host parallelism is bounded by roughly ceil(request_latency / politeness_ms) and capped by max_concurrency, while cross-host parallelism is bounded by max_concurrency alone. |
| `max_depth` | integer |  |  | Web only: BFS depth bound from a seed URL. |
| `max_download_bytes` | integer |  |  | Web only: per-(owner,repo,ref) cap on github materialization downloads. 0=default (50 MiB), -1=unlimited, >0=explicit cap (uncompressed bytes). |
| `max_pages` | integer |  |  | Web only: cap on total pages fetched across the crawl. |
| `max_pages_per_host` | integer |  |  | Web only: cap on pages fetched from any single host within the crawl, independent of max_pages. 0 = off (no per-host cap). When both fire, the crawl stops for a host once either cap hits first. |
| `max_path_segments` | integer |  |  | Web only: cap on the number of non-empty URL path segments a followed link may have; catches recursive-path traps like /a/b/a/b/.... 0 = off (unbounded), the default. |
| `max_rows` | integer |  |  | Web/PDF only, transformer="recipe" only, EXTRACT mode: cap on rows returned. 0 selects the default (200); the response reports rows matched alongside rows returned, so a truncated extract is never mistaken for a short one. REFUSED on a landing run: it is a render parameter, and a landing writes the whole emitted set, so a row window would bound what you see and not what is written. |
| `offset` | integer |  |  | Web/PDF only, transformer="recipe" only, EXTRACT mode: zero-based index of the first MATCHED row to return, for paging a document larger than one response. Every matched row is still counted, so the header's matched total names the whole population behind the page; the truncation line names the next offset to resume from, and a page starting past the end says so rather than looking like an empty match. Negative values are refused, and so is any value on a landing run — see max_rows. |
| `params` | object |  |  | Custom collector families only (a `collectors.json` entry): the param object passed to the provider's tool. It rides the MCP tool call as the `params` argument beside the collect id, and is validated against the schema the PROVIDER advertised for that tool before the call — so a provider needing a param you did not supply refuses with a named mismatch rather than failing inside its own handler. What each provider accepts inside is its tool's own inputSchema. IT IS READ WHENEVER THE COLLECT DISPATCHES TO A CONFIG ENTRY, which is not the same as "whenever the type is not a built-in name": a registered custom_collector family wins over a built-in collector of the same name, so a collect(type:"gcp") whose family has a config entry reaches the provider and reads params. A collect that dispatches to a built-in collector ignores it. |
| `politeness_ms` | integer |  |  | Web only: per-host request delay in milliseconds. |
| `promote` | boolean |  |  | Code only: promote this branch to the base graph — land in base regardless of the recorded default branch, overwrite the recorded default branch to the collected branch, and delete the now-redundant same-name overlay. No effect for non-code collectors. |
| `recipe` | string |  |  | REFUSED. It named a SAVED recipe node, which is removed along with the transformers graph family — recipes are ephemeral inline bodies now. Pass the body as `recipe_body` with extract=true or land=true instead. The param is still declared so the refusal can name what you sent. |
| `recipe_body` | string |  |  | Web/PDF only, transformer="recipe" only, and REQUIRED there: the inline recipe body to run. Needs extract=true or land=true — a run that asks for neither is refused, because it would emit into a buffer nobody reads. See help("recipes") for worked bodies to copy. |
| `seed_urls` | array of string |  |  | Web only: starting URL(s) for the crawl. |
| `seed_urls[]` | string |  |  |  |
| `transformer` | string |  |  | Web/PDF only: optional transformer name. |
| `type` | string | yes |  | Collector name (e.g., "code", "web", "pdf", "github"), or a registered custom_collector family name. PRECEDENCE: a registered custom_collector family wins over a built-in collector of the same name. The built-in collector serves the names no config entry claims. Built-in GRAPH TYPE names (knowledge, code, practice, linkage, checks, web, pdf) cannot be registered at all, so they are never shadowed, and the RETIRED names (cloud, logs, cicd) cannot be registered either — the refusal names the removal. aws, gcp, azure, k8s, cloudwatch, loki, stackdriver, github, gitlab and bitbucket were built in until this release and are contrib collectors now. |
| `user_agent` | string |  |  | Web only: override for the HTTP User-Agent header. |
<!-- END GENERATED: params -->
