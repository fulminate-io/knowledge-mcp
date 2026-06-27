# file_symbols

## Overview

`file_symbols` lists every code symbol defined in a file — its functions, types,
methods, and their signatures, summaries, and source — in one call. It is the fast
way to understand a file's shape before you edit it, without reading the whole
thing.

## When & how to use

Reach for `file_symbols` when you are about to work in a file and want its symbol
inventory: what is defined, where, and with what signature. It pairs naturally
with `search` (find the file) and `traverse` (walk the call graph from a symbol).

`file_path` is the only required parameter, and partial paths work. Set
`include_source: false` for a compact overview when you only need names and
signatures.

```jsonc
// Full symbol listing with source
file_symbols({ "file_path": "tools/tools_dispatch.go" })

// Compact overview — names and signatures only
file_symbols({ "file_path": "tools_help.go", "include_source": false })
```

Pass `repo` to disambiguate when the same path exists in more than one indexed
repo. For the full parameter reference, run `help("file_symbols")`.

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `file_path` | string | yes |  | File path (partial paths work). |
| `file_paths` | array of string |  |  | Multiple file paths in one call (combined with file_path). |
| `file_paths[]` | string |  |  |  |
| `format` | string |  |  | Output format: 'text' (default, markdown) or 'json' (structured rows: {id, symbol_name, type, file_path, start_line, end_line, signature, summary}). |
| `include_source` | boolean |  |  | Include source code (default: true). |
| `include_tombstones` | boolean |  |  | Include tombstoned (deleted) symbols in results. Default false. |
| `repo` | string |  |  | Repository (code graph) name — REQUIRED for graph=code; it is never inferred from cwd. search accepts 'all' to span every code repo. Not used by the knowledge graph. |
<!-- END GENERATED: params -->
