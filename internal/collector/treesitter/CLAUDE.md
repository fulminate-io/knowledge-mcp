# pkg/treesitter

AST-aware code chunking and graph edge extraction using [smacker/go-tree-sitter](https://github.com/smacker/go-tree-sitter).

## What It Does

Parses source files into semantic chunks (functions, types, imports) and extracts structural relationship edges (CALLS, IMPORTS, CONTAINS, EMBEDS, USES_TYPE) in a single AST pass. This is the foundation for codebase RAG — chunks feed vector embeddings, edges feed the code store.

## Key Types

- `Chunker` — main entry point. Create with `NewChunker(opts...)`, call `ChunkFile()`, defer `Close()`
- `Chunk` — semantic unit: content, file path, line range, chunk type, name, context
- `Result` — output of `ChunkFile()`: chunks + edges (edges use `codegraph.Edge` from `pkg/codegraph`)
- `QuerySet` — per-language tree-sitter S-expression queries for extracting declarations and relationships
- `Language` — enum: `LangGo`, `LangTypeScript`, `LangUnknown`

## Supported Languages

| Language   | Extensions | Grammar             | Query File              |
| ---------- | ---------- | ------------------- | ----------------------- |
| Go         | `.go`      | `golang`            | `queries_go.go`         |
| TypeScript | `.ts`      | `typescript/typescript` | `queries_typescript.go` |
| TSX        | `.tsx`     | `typescript/tsx`    | `queries_typescript.go` (shared) |

`.tsx` rides a SEPARATE grammar (`typescript/tsx`) from `.ts`
(`typescript/typescript`) because tree-sitter splits JSX into a sibling
grammar — the plain `typescript` grammar lexes `<Component>` as type syntax
and derails JSX into ERROR nodes. The `tsx` grammar is a strict superset, so
`LangTSX` reuses the same `tsQueries`. This is the only language pair in the
registry where two `Language` constants share one query set across two
grammars. (`.jsx` needs no such split — the `javascript` grammar is
JSX-capable natively.)

## Adding a New Language

1. Add grammar import in `language.go` (e.g., `"github.com/smacker/go-tree-sitter/python"`)
2. Create `queries_<lang>.go` with a `func <lang>Queries() *QuerySet` returning the query patterns
3. Add language constant and register in `registry` map in `language.go`
4. Add file extensions in `extMap`
5. Add language-specific helpers in `chunker_<lang>.go` if needed

### Dual-grammar dialect reuse (the `LangTSX` pattern)

When a new dialect rides a DIFFERENT tree-sitter grammar but shares the
query set of an existing language — the JSX/non-JSX TypeScript split is the
canonical case — **step 2 is skipped**: register the new `Language` const
against the new grammar while pointing `queries:` at the existing
`<lang>Queries` func (the superset grammar accepts every kind the shared
queries capture). The follow-on wiring is forced by the closed-allowlist
coverage gates, since the shared query set carries a non-empty `TestBlocks`:

- `testBlockClassifiers[<NewLang>]` in `chunker_<base>.go`'s `init`,
- `frameworkTables[<NewLang>]` in `framework_tables.go`,
- a `testCorpusMatrix[<NewLang>]` row + at least one fixture, in
  `chunker_corpus_test.go`,
- the `expectedTestBlock` allowlist in `chunker_test_block_dispatch_test.go`.

If the `ast` placeholder DSL must work on the dialect, also add a parallel
`<newLang>LangConfig` in `cmd/knowledge/internal/ast/` mirroring the base
language's config but bound to the new `Language` (registration alone gives
`explain`/`list_node_kinds`, not first-class `match`/`replace`/`where`).

The persisted node `Language` is the const string; if any behavior keys off
that literal (e.g. the topology per-language scope filter at
`cmd/knowledge/internal/topology/graph/god_object.go`), give it a family
alias so the dialect isn't silently excluded from the base language's scope.

## TestBlocks Query Convention

`QuerySet.TestBlocks` matches call-expression / macro / do-end style test invocations
(`it("...", () => {...})`, `describe("...") { ... }`, RSpec do-end blocks, GoogleTest
`TEST(Suite, Name) { ... }` macros). It runs as a parallel pass after `TopLevel` —
the two passes are strictly disjoint so a test invocation nested inside a function
declaration emits both a `function_declaration` chunk (from TopLevel) and a
`test_block` chunk (from TestBlocks). No dedup.

When a language's TestBlocks string is empty (the zero value), `walkTestBlocks`
returns immediately — identical to how `walkImports` skips when `Imports == ""`.
Bucket A languages that pre-date this query field need no edits.

Capture conventions Bucket B authors must observe:

- `@decl` — the call/macro/do-block node. Required. The chunk's `Content`,
  `StartByte`/`EndByte`, and `StartLine`/`EndLine` come from this node.
- `@name` — the string-literal label (e.g., `"rejects expired"`). Optional.
  When absent, `walkTestBlocks` falls back to `firstStringArg` from
  `chunker_identity.go:137` over the @decl node.
- `@parent_name` — the outer describe/context name when nested. Optional.
  Assigned verbatim to `chunk.ParentName`. NO automatic AST ascent — if the
  query doesn't bind `@parent_name`, `chunk.ParentName == ""` and the chunk
  is treated as top-level. This is by design: implicit ascent collides with
  language-specific scoping rules (e.g., RSpec contexts vs JS modules).
- `@params` — the closure parameter list text (e.g., `(done)` or `(t *testing.T)`).
  Optional. Assigned verbatim to `chunk.Context.Signature` (no normalization,
  no quote stripping, no whitespace collapse). When absent, `Signature == ""`.

### `@parent_name` collision discipline

When two test_block chunks in the same file share a `Name` and both lack
`@parent_name` (e.g., two top-level `it("foo", ...)` calls in the same spec
file because the author forgot the outer `describe`), the node ID layer
handles disambiguation transparently. `ChunkNodeID` at `indexer_chunk.go:104`
falls through to `file:Name` when `ParentName` is empty, and `DeduplicateChunks`
at `indexer_chunk.go:114-134` appends `#PathHash` to colliding IDs (the AST
path hash from `chunker_identity.go:160` is unique per source position). Bucket
B query authors do NOT need to synthesize a synthetic parent name to avoid
collisions — the dedup pass in the indexer already covers this case.

### Helper reuse

`walkTestBlocks` and `emitTestBlockChunk` reuse:

- `firstStringArg` (`chunker_identity.go:137`) — string-literal fallback when @name is absent.
- `qualifiedName` (`chunker_edges.go:183`) — same `<package>.<name>` shape as declarations for the CONTAINS edge target.
- `astPathHash` (`chunker_identity.go:160`) — same dedup-breaker as declaration chunks.

Test_block chunks emit ONE edge: `file → chunk` CONTAINS. No CALLS, no USES_TYPE,
no EMBEDS. Test bodies often DO contain calls; tracking those edges is Bucket B's
problem, not the abstraction's.

## Important: CGO Required

This package uses CGO via smacker/go-tree-sitter. The tree-sitter `Parser` is NOT thread-safe — for parallel chunking, create one `Chunker` per goroutine.

Always use `defer .Close()` on Parser, Tree, Query, and QueryCursor objects (smacker issue #181).

## Files

| File                    | Purpose                                                                        |
| ----------------------- | ------------------------------------------------------------------------------ |
| `language.go`           | Language detection, registry, `langEntry` with `sync.Once` caching             |
| `parser.go`             | Tree-sitter parser wrapper                                                     |
| `types.go`              | `Chunk`, `ChunkContext`, `QuerySet`, `Result` types                            |
| `options.go`            | Functional options: `WithMaxChunkTokens`, `WithOverlapLines`, `WithoutContext` |
| `chunker.go`            | Core chunking engine: `ChunkFile`, edge extraction, orphan collection          |
| `chunker_go.go`         | Go-specific helpers: receiver extraction, embeds, signatures                   |
| `queries_go.go`         | Go tree-sitter query patterns                                                  |
| `queries_typescript.go` | TypeScript tree-sitter query patterns                                          |
