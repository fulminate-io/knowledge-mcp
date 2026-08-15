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

The first column is the `Language` constant's string value from `language.go` —
the value persisted on every node — not a display name. Extensions come from
`extMap`. Four languages also match by filename: `dockerfile` on `Dockerfile`
and on the `Dockerfile.<suffix>` form, `bash` on `Makefile`/`GNUmakefile`,
`ruby` on `Rakefile`/`Gemfile`, and `groovy` on `Jenkinsfile`. Extensions are
consulted BEFORE filenames, so a file named `Dockerfile.go` is Go.

Every extension listed here was parse-tested against the grammar it routes to.
An extension that is absent routes nowhere at all: detection returns unknown,
discovery declines the file, and it is never chunked — which is why `.less`,
`.sass`, `.heex` and `.xhtml` are deliberately absent rather than mapped to the
nearest grammar. See the comment under `extMap` for the measurement.

| Language     | Extensions                                                       | Query File                       |
| ------------ | ---------------------------------------------------------------- | -------------------------------- |
| go           | `.go`                                                            | `queries_go.go`                  |
| typescript   | `.ts` `.mts` `.cts`                                              | `queries_typescript.go`          |
| tsx          | `.tsx`                                                           | `queries_typescript.go` (shared) |
| javascript   | `.js` `.jsx` `.mjs` `.cjs`                                       | `queries_javascript.go`          |
| python       | `.py` `.pyi` `.pyw`                                              | `queries_python.go`              |
| java         | `.java`                                                          | `queries_java.go`                |
| rust         | `.rs`                                                            | `queries_rust.go`                |
| c            | `.c` `.h`                                                        | `queries_c.go`                   |
| cpp          | `.cpp` `.cc` `.cxx` `.hpp` `.hh` `.hxx` `.c++` `.h++` `.ipp` `.tpp` `.inl` | `queries_cpp.go`       |
| csharp       | `.cs` `.csx`                                                     | `queries_csharp.go`              |
| ruby         | `.rb` `.rake` `.gemspec` `.ru` (+ filename)                      | `queries_ruby.go`                |
| php          | `.php` `.phtml`                                                  | `queries_php.go`                 |
| swift        | `.swift`                                                         | `queries_swift.go`               |
| kotlin       | `.kt` `.kts`                                                     | `queries_kotlin.go`              |
| scala        | `.scala` `.sc` `.sbt`                                            | `queries_scala.go`               |
| elixir       | `.ex` `.exs`                                                     | `queries_elixir.go`              |
| lua          | `.lua`                                                           | `queries_lua.go`                 |
| bash         | `.sh` `.bash` `.zsh` `.bats` `.ksh` (+ filename)                 | `queries_bash.go`                |
| groovy       | `.groovy` `.gradle` `.gvy` `.gy` (+ filename)                    | `queries_groovy.go`              |
| elm          | `.elm`                                                           | `queries_elm.go`                 |
| ocaml        | `.ml` `.mli`                                                     | `queries_ocaml.go`               |
| hcl          | `.tf` `.hcl` `.tfvars`                                           | `queries_hcl.go`                 |
| protobuf     | `.proto`                                                         | `queries_protobuf.go`            |
| css          | `.css` `.scss`                                                   | `queries_css.go`                 |
| html         | `.html` `.htm`                                                   | `queries_html.go`                |
| sql          | `.sql` `.pgsql` `.mysql`                                         | `queries_sql.go`                 |
| dockerfile   | (filename)                                                       | `queries_dockerfile.go`          |
| svelte       | `.svelte`                                                        | `queries_svelte.go`              |
| toml         | `.toml`                                                          | `queries_toml.go`                |
| yaml         | `.yaml` `.yml`                                                   | `queries_yaml.go`                |
| markdown     | `.md` `.markdown` `.mdx`                                         | `queries_markdown.go`            |
| cue          | `.cue`                                                           | `queries_cue.go`                 |

`.tsx` rides a SEPARATE grammar (`typescript/tsx`) from `.ts`
(`typescript/typescript`) because tree-sitter splits JSX into a sibling
grammar — the plain `typescript` grammar lexes `<Component>` as type syntax
and derails JSX into ERROR nodes. The `tsx` grammar is a strict superset, so
`LangTSX` reuses the same `tsQueries`. This is the only language pair in the
registry where two `Language` constants share one query set across two
grammars. (`.jsx` needs no such split — the `javascript` grammar is
JSX-capable natively.)

`.h` is the one extension whose routing is confirmed by the parse rather than
trusted from the table. `extMap` sends every `.h` to C, but a `.h` may
legitimately be either language, so when the C parse produces an error tree the
same bytes are re-parsed under the `cpp` grammar and that tree is adopted if it
comes back clean. A C++ header named `.h` is therefore indexed under the cpp
grammar — its namespaces, classes and inline methods become chunks, and the
node's persisted `Language` is `cpp` — while a C header parses cleanly the
first time and never reaches the fallback. The second parse is paid only by a
header the C grammar has already failed on.

## Node IDs changed: existing non-Go graphs need a re-collect

Two changes to declaration naming mean an already-collected graph keeps its old
shape until the repo is collected again.

**Class members are now parent-qualified.** A member declared inside a class,
interface, trait, protocol, module or namespace takes that container as its
`ParentName`, so its node ID moves from `<file>:<member>` to
`<file>:<Class>.<member>`. This affects python, typescript, tsx, javascript,
ruby, java, csharp, php, scala, swift, cpp, kotlin, rust, ocaml and groovy. A
language qualifies when its queries chunk a member nested inside a container
whose name is resolvable AND capture that member's name.

Container names come from three sources, because grammars disagree about where
they put them: the `name:` field, the `type:` field when it binds a
`type_identifier`, and — for grammars that attach no field name to any node —
the first direct named child of an identifier-like kind. Kotlin needs the
third: it is why Kotlin members carry their class today, and why the scan must
not stop at the first named child, since `modifiers` occupies that slot on
`data class` and on any class with a visibility modifier. Rust needs the second:
`impl_item` carries `type:` and no `name:`, so a method takes its impl's type as
its parent, while a generic `impl<T> Gen<T>` binds a `generic_type` and is
deliberately declined, leaving its members unparented rather than parented to a
container chunk that does not exist.

Containment is SINGLE-ANCESTOR: a member takes its NEAREST named container, not
a dotted chain of every container above it. Where a grammar hands over a
qualified spelling as one name node — C#'s `namespace App.Models`, C++17's
`namespace a::b` — that full path is kept, because it arrives as the
container's own name.

Go is unaffected: its receiver-qualified method IDs are unchanged, and no Go
node kind participates in the container ascent.

**Declarations whose queries capture no name are now named by the chunker.**
groovy, lua, ocaml, elm, php namespaces and elixir recover a declaration's name
from the parsed node through a per-language resolver, instead of from a
tightened query. A tree-sitter pattern that names a field FILTERS on it as well
as capturing it, so requiring the name field would DELETE the declarations that
lack it — an OCaml `let () = ...`, a PHP global `namespace { }` — rather than
leave them unnamed. Resolving on the node leaves the chunk set byte-identical
and adds only the name. Groovy is the case worth naming: its members already
resolved their class through the ascent, and the missing class NAME was the only
thing keeping the parent-to-member edge inert on the from side.

**Elixir declarations are named after the entity, not the macro.** Elixir has no
declaration node kind — a definition is a `call` whose target is a macro
keyword, and so is every other expression in the language — so every definition
used to be a chunk named `def`, colliding with every other, while `use`,
`assert` and every in-body call became declarations of their own. The query now
admits only the language's definition macros and the name comes from the call's
arguments. Test invocations dropped from that set are not lost: the parallel
`TestBlocks` pass already chunks them, so what disappears is a duplicate.

**The ECMAScript family gains wholly new nodes.** typescript, tsx and
javascript (`.js`, `.jsx`, `.mjs`) now chunk class-body members —
constructors, methods, getters, setters, static and async members — which
their `TopLevel` queries never matched before. Those nodes do not exist in any
previously collected graph.

Structural edges no longer share one rule, and the symbol map they used to
resolve against is gone. Four things are true now:

- **File-to-symbol `CONTAINS` is addressed POSITIONALLY**, by the chunk slot the
  chunker records at emission, and consults no declaration index at all. It is
  exact by construction rather than by name agreement.
- **Orphan chunks receive containment** where they previously received none —
  every chunk comes from a file, so every chunk node is contained by its file.
- **A Go method's parent-to-member source is the one containment endpoint still
  resolved by NAME**: its container is the receiver type, a sibling declaration
  that may live in another file, so no slot can address it. It resolves through
  `parser`'s collision-safe declaration index at package scope.
- **A reference may now produce a GROUP of edges** rather than one or none. A
  reference matching several surviving declarations emits one edge per
  candidate at `Confidence` 1/N sharing one group key, tagged either
  `ambiguous-name` (closed: exactly one is the referent) or `dynamic` (open:
  one of these, or something static analysis cannot reach).

A graph collected before this change carries whatever its old endpoints
resolved to.

Nothing migrates in place: run a re-collect of the repo to pick up the new IDs
and nodes. Superseded nodes are cleaned up by the collect-epoch sweep — the
server's `Finalize` tombstones every node whose `CollectEpoch` differs from the
just-completed collect's epoch, so the old IDs disappear as part of the same
run rather than lingering beside the new ones.

### What changes per language, so an operator can decide what to re-collect

- **kotlin, rust, cpp, csharp, php (braced form), ocaml** — members gain a
  container prefix, moving from `<file>:<member>` to
  `<file>:<Container>.<member>`.
- **groovy, lua, ocaml, elm, php namespaces** — previously-unnamed declarations
  gain names, moving from a line-range identity to `<file>:<Name>`.
- **elixir** — definition nodes move from `<file>:def` (hash-suffixed on
  collision) to `<file>:<entity>`, and the plain-call chunks disappear.
- **php and csharp** — a file declaring its namespace in the sibling form
  (`namespace App\Models;`, or C#'s file-scoped `namespace App.Models;`) takes
  that namespace as its symbol namespace instead of its parent directory. No
  node ID changes, since the namespace is not part of a node ID; more edges
  resolve, because two files of one namespace in different directories now
  share a symbol-map prefix.
- **newly routed extensions** — files previously absent from the index
  entirely now appear. See the Supported Languages table.

### Duplicate containers: each edge names the right declaration

When two containers in one file share a name — a reopened C++ or C# namespace,
a Rust struct beside its `impl`, two blocks of one PHP namespace — each
container is disambiguated with a path-hash suffix appended to its own name.

Each member's parent-to-member `CONTAINS` edge names the container BLOCK that
lexically encloses it, so a Rust method attaches to its `impl` and each
reopened C++, C# or PHP block keeps its own members rather than all of them
piling onto one spelling.

A TYPE REFERENCE to the shared name resolves to the type declaration — the
struct or the class — when exactly one of the colliding declarations is a type.
When the name is ambiguous it is left unresolved: a function sharing a type's
name, or two declarations of the same kind, yield no candidate the reference
can be attributed to, and a wrong edge is worse than a missing one.

Member node IDs are unchanged by any of this. Only the edges carry the
disambiguated name; a member's own `ParentName` stays as the source wrote it.
`TestContainerCollision` pins the behavior.

### Naming compromises worth knowing about

- Lua names arrive pre-qualified from the grammar and are kept verbatim, so
  `M:method` retains its colon rather than being normalized to a dot. The colon
  is inert to edge resolution, which splits only on `.`.
- An OCaml `let a = 1 and b = 2` is one chunk spanning both bindings and takes
  the first binding's name.
- A Lua `local a, b = 1, 2` declares two variables in one node and is left
  unnamed rather than named after the first — a node claiming to be one variable
  while spanning two is a partial truth the graph is better without.

### One routing problem that cannot be fixed by mapping an extension

- `.mli` interface files are parsed by the OCaml IMPLEMENTATION grammar, so an
  interface file produces an error tree and its `val` signatures match nothing.
  The vendored binding exposes no OCaml interface grammar at all, so this
  cannot be fixed at the current pin.

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
  `chunker_identity.go` `firstStringArg` over the @decl node.
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
handles disambiguation transparently. `ChunkNodeID` at `indexer_chunk.go:130`
falls through to `file:Name` when `ParentName` is empty, and `DeduplicateChunks`
at `indexer_chunk.go:142-160` appends `#PathHash` to colliding IDs (the AST
path hash from `chunker_identity.go` `astPathHash` is unique per source
position). Bucket
B query authors do NOT need to synthesize a synthetic parent name to avoid
collisions — the dedup pass in the indexer already covers this case.

### Helper reuse

`walkTestBlocks` and `emitTestBlockChunk` reuse:

- `firstStringArg` (`chunker_identity.go`) — string-literal fallback when @name is absent.
- `qualifiedName` (`chunker_edges.go:183`) — same shape as declarations for the CONTAINS edge target: `<namespace>.<name>`, or `<namespace>.<parent>.<name>` when @parent_name bound, which is what the chunk's own symbol-map key uses.
- `astPathHash` (`chunker_identity.go`) — same dedup-breaker as declaration chunks.

Test_block chunks emit ONE edge: `file → chunk` CONTAINS. No CALLS, no USES_TYPE,
no EMBEDS. Test bodies often DO contain calls; tracking those edges is Bucket B's
problem, not the abstraction's.

## Important: CGO Required

This package uses CGO via smacker/go-tree-sitter. The tree-sitter `Parser` is NOT thread-safe — for parallel chunking, create one `Chunker` per goroutine.

Always use `defer .Close()` on Parser, Tree, Query, and QueryCursor objects (smacker issue #181).

**One `Chunker` per goroutine is necessary but not sufficient for Lua.** The
vendored Lua grammar's external scanner allocates no per-parser payload and
keeps its lexer state in file-scope C variables shared by the whole process, so
concurrent Lua parses corrupt each other and return structurally different
trees for identical input — silently, with no error, and invisibly to Go's race
detector. `Parser.Parse` therefore serializes Lua parses behind `luaParseMu`
(`parser.go`); every other language is unaffected and still parses in parallel.
This lock is permanent, not a stopgap. Upstream tree-sitter-lua fixed the
scanner at v0.3.0 (per-payload state, still ABI-compatible with the vendored
core), but no Go binding ships the fixed scanner — smacker/go-tree-sitter is
unmaintained at exactly our pin, and the maintained official binding cannot
co-link with it — so the lock stays until the grammars are re-vendored
wholesale. `scripts/lua_scanner_state_check.sh` is the watchdog that fires if
the vendored scanner ever stops holding file-scope state.
`parser_lua_concurrency_test.go` is the regression guard at both the parse and
the `ChunkFile` layer. Adding a new language needs no such treatment unless its
grammar's scanner likewise holds mutable state at file scope.

## Files

| File                    | Purpose                                                                        |
| ----------------------- | ------------------------------------------------------------------------------ |
| `language.go`           | Language detection, registry, `langEntry` with `sync.Once` caching             |
| `parser.go`             | Tree-sitter parser wrapper                                                     |
| `types.go`              | `Chunk`, `ChunkContext`, `QuerySet`, `Result` types                            |
| `options.go`            | Functional options: `WithMaxChunkTokens`, `WithOverlapLines`, `WithoutContext` |
| `chunker.go`            | Core chunking engine: `ChunkFile`, edge extraction, orphan collection          |
| `chunker_filecontext.go`| File imports and symbol namespace, incl. the PHP/C# declared-namespace override |
| `chunker_identity.go`   | Container/scope resolution: `containerName`, `findEnclosingScope`, `classLikeTypes` |
| `chunker_decl_name.go`  | `declNameResolvers` registry + shared field/child helpers                      |
| `chunker_go.go`         | Go-specific helpers: receiver extraction, embeds, signatures                   |
| `queries_go.go`         | Go tree-sitter query patterns                                                  |
| `queries_typescript.go` | TypeScript tree-sitter query patterns                                          |
