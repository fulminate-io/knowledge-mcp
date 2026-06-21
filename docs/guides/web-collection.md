# Web collection

## Overview

The `collect` tool's `web` type crawls a public website and lands it in the graph
as a `web` graph keyed by a slug you choose. It walks from one or more seed URLs,
follows internal links, parses each page's HTML into a structured tree (page →
sections → paragraphs, lists, tables, code blocks), and stamps the internal and
external links it finds as edges. The result is a queryable, searchable copy of a
documentation site or reference corpus — the raw material a [recipe](recipes.md)
later distills into structured domain nodes.

Collection runs **client-side**: the MCP client intercepts the `collect` call,
runs the crawler locally, and streams the parsed nodes to the server. Summarizing
and embedding then drain in the background, so the call returns as soon as the
crawl finishes uploading.

The minimal invocation is a slug plus a seed URL:

```jsonc
collect({
  "type": "web",
  "id": "go101",
  "seed_urls": ["https://go101.org/article/101.html"]
})
```

For the full parameter table, see [the collect tool guide](tools/collect.md) —
this guide narrates the behavior and the best practices, not the parameter list.

## When & how to use

Reach for web collection when you want a reference site — API docs, a pattern
catalog, a book published as a website, an architecture-guidance corpus — inside
the graph so you can search it, traverse its link structure, and feed it to a
recipe. If the content lives in a single PDF instead, use
[PDF collection](pdf-collection.md). If you already have a collector binary for a
bespoke source, register it as a [custom collector](tools/custom_collector.md).

### The id is the graph slug

`id` is the per-graph source slug, and it becomes the name of the `web` graph the
crawl lands in. Pick a stable, descriptive slug (`hohpe-eip`, `go101`) and reuse
it: page node IDs are deterministic — derived by hashing the page URL together
with each record's structural position — so re-running the same crawl under the
same slug produces identical IDs and overwrites the prior contents rather than
duplicating them. Re-runs are idempotent by construction.

### seed_urls is required

A crawl must start somewhere: `seed_urls` is required and must contain at least
one URL. An empty `seed_urls` (or an empty `id`) is rejected before any fetch
happens. List several seeds when the content you want has no single entry point
that links to all of it.

### Bound your crawl explicitly

This is the most important thing to understand about web collection: **`max_depth`
and `max_pages` default to 0, and 0 means unbounded.** This is deliberate. The
collector does not fill in a default cap, because silent truncation at an
arbitrary page or depth limit hides catalog-size bugs — capping at, say, 200 pages
once turned a 500-page crawl into a 200-page one with no warning that anything was
missing. The crawl always terminates when its queue drains, so an unbounded crawl
of a well-behaved site is safe; but on a large or densely cross-linked site, an
unscoped crawl walks the entire reachable graph.

So scope the crawl on purpose. The `collect` tool exposes these scoping knobs:

- **`follow_patterns`** — a regex allowlist for internal links. Each entry is a Go
  regular expression; a link is followed only if it matches at least one pattern.
  An empty `follow_patterns` means "follow any internal link." This is the
  sharpest tool: confine the crawl to one section of a site by allowlisting its
  URL prefix.
- **`max_depth`** — the maximum link distance from a seed. Depth 1 fetches only the
  seeds; depth 2 adds the pages they link to; and so on.
- **`max_pages`** — a hard ceiling on the total number of pages fetched.
- **`max_path_segments`** — caps the number of non-empty URL path segments a
  followed link may have, catching recursive-path traps like `/a/b/a/b/...` that
  would otherwise inflate the queue without bound. `0` = off (unbounded), the
  default.
- **`max_pages_per_host`** — bounds pages fetched from any single host within the
  crawl, independent of `max_pages`. Useful when crawling across multiple hosts to
  stop one host from starving the others. `0` = off (no per-host cap); when both
  this and `max_pages` are set, the crawl stops for a host once either fires first.

A scoped crawl that confines itself to one documentation section and caps its page
budget:

```jsonc
collect({
  "type": "web",
  "id": "azure-patterns",
  "seed_urls": ["https://learn.microsoft.com/en-us/azure/architecture/patterns/"],
  "follow_patterns": ["^https://learn\\.microsoft\\.com/en-us/azure/architecture/patterns/"],
  "max_depth": 2,
  "max_pages": 100
})
```

### Politeness and fetch limits

The crawler is polite by default. `politeness_ms` is the minimum delay between
requests to the same host, defaulting to 50ms. Per-host fetches serialize through
a per-host lock, so concurrency across hosts never lets requests to a single host
breach the floor — raise `politeness_ms` for a site you want to tread more lightly
on. Each fetch times out at 30 seconds and follows at most 5 redirects.

Each page body is read up to a **10 MiB cap**; a larger page is truncated to that
limit (the page node is still emitted). The default request `User-Agent` is
`knowledge-web-collector/0.1 (+github.com/fulminate-io/knowledge)`; override it
with `user_agent` if a site needs a specific agent string.

### GitHub URLs are materialized

When the crawl encounters a `github.com` URL, it materializes the referenced
repository content rather than just scraping the rendered page. `max_download_bytes`
caps the bytes downloaded per `(owner, repo, ref)` during this materialization,
measured against the uncompressed on-disk footprint: `0` selects the default cap
of 50 MiB, `-1` means unlimited, and any positive value is an explicit byte cap.

### What lands in the graph

A crawl emits a `page` node per fetched page (carrying its URL, title, and
flattened body), `section` nodes nested under it via `contains` edges (each
stamped with its document position), and content nodes — paragraphs, list items,
tables, code blocks — under their sections. Internal links become `references`
edges marked `internal`; external links become `references` edges marked
`external`. This node vocabulary deliberately mirrors the PDF emitter so a single
[recipe](recipes.md) can translate from either source without caring which
collector produced the graph.
