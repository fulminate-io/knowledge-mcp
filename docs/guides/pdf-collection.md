# PDF collection

## Overview

The `collect` tool's `pdf` type reads a single PDF file and lands it in the graph
as a `pdf` graph. It opens the document, extracts the text, groups it into a
heading hierarchy, and emits a structured tree — document → sections →
paragraphs, lists, tables, code blocks — mirroring the shape a
[web crawl](web-collection.md) produces. The result is a queryable copy of the
document and the raw material a [recipe](recipes.md) can distill into structured
domain nodes.

Collection runs **client-side**: the MCP client intercepts the call, reads and
chunks the PDF locally, and streams the resulting nodes to the server, where
summarizing and embedding drain in the background.

The minimal invocation is the type plus an absolute path:

```jsonc
collect({
  "type": "pdf",
  "id": "/Users/you/books/designing-data-intensive-applications.pdf"
})
```

For the full parameter table, see [the collect tool guide](tools/collect.md) —
this guide narrates the behavior and the best practices.

## When & how to use

Reach for PDF collection when the content you want lives in a single PDF — a
book, a paper, a spec, an architecture guide. If the source is a website, use
[web collection](web-collection.md) instead.

### The id must be an absolute path

`id` is the absolute filesystem path to the `.pdf` file, and it **must** be
absolute — a relative or empty path is rejected before anything is read (the error
is `pdf collector: id "..." must be an absolute path`). The requirement keeps the
per-source graph name stable: the graph slug is derived from the path (a sanitized
basename plus a short hash of the full path), so the same file always lands in the
same graph regardless of which directory you ran the call from. Re-collecting the
same path overwrites its prior contents idempotently rather than duplicating them.

```jsonc
// Correct — absolute path
collect({ "type": "pdf", "id": "/Users/you/papers/raft.pdf" })

// Rejected — a relative path is refused before any read
collect({ "type": "pdf", "id": "./raft.pdf" })
```

### It is text extraction, not OCR

This is the single most important thing to know before collecting a PDF. The
extractor reads the document's **text layer** — it walks the PDF content stream
and decodes the text operators and embedded fonts. It is **not** OCR: it does not
look at the rendered pixels. A born-digital PDF (exported from a word processor,
LaTeX, or a publishing pipeline) has a real text layer and extracts cleanly. A
**scanned or image-only PDF has no text layer**, so collection yields little or
nothing — there is no text for the content-stream walker to find.

If your source is scanned, run it through an OCR pass first to add a text layer,
then collect the OCR'd file. Otherwise, expect sparse output and check what landed
before building a recipe on top of it.

### Section-mode chunking, and how to get paragraphs

The chunker defaults to **section mode**: it builds a heading hierarchy and nests
each section's body content as the `Children` of the section node. This default is
deliberate — section-level context is better signal than isolated paragraph
fragments for the recipe-driven extraction that is the primary downstream
consumer.

If you need **paragraph granularity**, you do not change a parameter — you walk a
section node's `Children`. The paragraph, list-item, table, and code-block nodes
are all there, nested one level under their enclosing section; a recipe reaches
them with a `traverse` over `contains` edges out of each section.

Chunks below a minimum size are dropped rather than merged, and there is **no page
cap** — the whole document is chunked, however long it is.

### Header and footer filtering is best-effort

Running headers and footers are filtered out by default so they do not pollute the
body text. This filtering is **best-effort**: when the underlying page-extraction
method for headers and footers has not been wired up for a document, the collector
silently treats the page as having none and continues. You never get an error from
it — at worst, a repeated running header survives into the body of a document
where header detection could not run.

### What lands in the graph

A collection emits a root `document` node carrying the PDF's Info-dictionary
metadata — title, author, subject, keywords, producer, creator, and creation and
modification dates — plus a flattened description so the document is searchable.
Under it, every chunk becomes a node: headings become `section` nodes, and body
content becomes `paragraph`, `list_item`, `table`, `code_block`, or (for anything
unclassified) `block` nodes, each connected to its parent by a `contains` edge
stamped with its document position. Node IDs are deterministic — derived from the
absolute path and each chunk's structural position — so re-collection is
idempotent.

This is the same node vocabulary the [web collector](web-collection.md) emits, by
design: a single [recipe](recipes.md) can translate from a `pdf` graph or a `web`
graph without caring which collector produced it.
