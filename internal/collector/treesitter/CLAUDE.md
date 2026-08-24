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
legitimately be either language, so the same bytes are re-parsed under the
`cpp` grammar and that tree adopted when cpp is the better reading. A C++
header named `.h` is therefore indexed under the cpp grammar — its namespaces,
classes and inline methods become chunks, and the node's persisted `Language`
is `cpp`.

**A CLEAN C PARSE IS NOT EVIDENCE OF A C HEADER.** The rule used to be "re-parse
only when the C parse errored", and that silently mis-routed exactly the headers
that matter most: the C grammar is permissive enough to parse a C++ class header
without an error node. Measured on leveldb, whose public API lives in
`include/leveldb/*.h` — every one parsed clean as C, so its abstract bases were
chunked by a query set with no class row and never captured at all. Two rules
now decide:

- a header whose C parse **errored** is adopted on a clean cpp parse alone, as
  before — there is no C reading to prefer;
- a header whose C parse was **clean** is adopted only on POSITIVE evidence: its
  cpp parse must also be clean AND contain a node kind C has no syntax for
  (`class_specifier`, `namespace_definition`, `template_declaration`,
  `access_specifier`).

A pure C header cannot flip, and that direction is the one asserted: C is nearly
a subset of C++, so a C header parses clean under cpp and produces none of those
kinds. A C header using a C++ keyword as an identifier is declined by one of two
paths, and only one of them involves an error: `int template;` and
`int operator;` make the cpp parse error, and an erroring alternate is rejected;
`int class;` parses clean under cpp and yields none of the kinds, so the
node-kind check declines it. That check is the sole authority — a clean
alternate parse is never on its own a reason to adopt. Measured across 563 C
headers in
redis, curl and libuv: **zero** re-routed by this rule.
`TestCppHeaderFallbackDiscriminator` pins both directions, including a pure-C
header carrying `::` and the word `class` inside a comment and a string literal.

The second parse is rationed: a clean-C header pays a byte scan for a C++ marker
first and is re-parsed only if one is present. The scan may false-positive on a
comment, which costs one parse and changes no outcome — the node-kind check is
the authority.

**A LIMIT THAT NO ROUTING RULE REACHES.** `class MACRO Name { ... }` — the
export-macro idiom — is not parseable by the vendored tree-sitter-cpp: the
grammar reads the macro as the class name and derails into ERROR nodes. Such a
header errors under BOTH grammars, so it is never adopted and its classes are
never captured by either query set. Measured on leveldb: 20 of 56 headers, and
every public API header among them.

## Go interfaces: method-spec nodes, two-hop IMPLEMENTS, and interface-decl call targeting

A Go interface's method specs are now **nodes in their own right**, with the
receiver-qualified ID `<file>:<Interface>.<Method>` — the same shape a Go method
takes. Nothing like them existed before: the `TopLevel` query captured function,
method and type declarations only, so an interface's methods were invisible.

**A call through an interface-typed value targets the interface's method
declaration**, not the concrete implementations. The implementers are one hop
away over `IMPLEMENTS`:

```jsonc
traverse({ "graph": "code", "repo": "<repo>",
           "start": "<file>:<Interface>.<Method>",
           "edge_types": ["IMPLEMENTS"], "direction": "out" })
```

**DIRECTION IS INTERFACE → IMPLEMENTER**, and it is worth stating plainly because
this repository contains fixtures using the opposite convention:
`cmd/knowledge-server/internal/store/graph_traversal_test.go` builds
`{FromId: "StructX", ToId: "IFace", Type: "IMPLEMENTS"}` — implementer to
interface. Those are hand-built traversal test data with no producer behind them.
For a **collected** graph, the collector's direction above is authoritative.

`Edge.Method` on an IMPLEMENTS edge carries `method-set:<N>`, the interface's
**expanded** method-set size (embedded interfaces promoted in). Weight is 0. Use
N to weight the edge: a `method-set:1` edge is LOW-information, because
structurally identical single-method interfaces legitimately share satisfiers —
that is correct Go, not a defect. Measured on this repository: 99 of 184
interfaces are single-method and account for 2,954 of 3,529 derived pairs.

## IMPLEMENTS has TWO derivations, and Edge.Method tells them apart

**Go method-set matching** is the one described above: it INFERS satisfaction
the language leaves implicit, by comparing resolved signatures, and stamps
`method-set:<N>`. It is unchanged, and it is now gated to Go records
explicitly rather than by the accident that Go is the only registered
type-facts arm.

**Declared conformance** reads a supertype clause the source actually WROTE —
an implements, an extends, a mixin, a behaviour, a trait — and stamps
`declared-conformance:` followed by that clause's kind (for example
`declared-conformance:mixin`). Both levels and the direction are identical to
the method-set derivation: SUPERTYPE → SUBTYPE at the type level, and
supertype member → the subtype member of the same name below it. Weight is 0.
The member-level edge carries its type-level parent's Method byte-for-byte.

Two rules are worth knowing before reading such a graph:

- **A supertype that resolves to a NON-CONTRACT emits nothing.** A concrete
  base class's method IS the callable implementation, so fanning a call
  resolved to it across every subclass would state a fact this edge type does
  not mean. The outcome is counted on the derivation's log line instead.
- **A module-level-only result is correct, not truncated.** Where a language's
  container and its members are the same node kind, members carry no container
  name, so no member pairing can exist and the type-level edge stands alone.

**Which languages produce these edges is the census's answer, not this file's.**
`testTypedQualifierCensus` (`chunker_qualtypes_census_test.go`) carries one
enforced row per registered language; a list repeated here would go stale, and
the table cannot. Note that an armed row still does not mean edges: a language
whose grammar has no contract construct captures its heritage and emits nothing,
by the non-contract rule above. Picking up these edges on an existing graph
needs a re-collect.

The carrier, resolution and emission path are shared; each language contributes
an arm reading its own grammar's clause.

### What the nominal-static group's grammars expose

SCOPED TO SIX LANGUAGES ON PURPOSE, and not a list of every armed language —
the census above is that. These six arrived as one group and their clauses are
documented together because they are the group whose conformance is written as a
named heritage clause; the systems group and the dynamic group are covered by
their own rows in the census and by the recorded limitations further down.

Direction, the two derivations and the Method vocabulary are documented above;
this section is only which clause each grammar carries, what its arm reads, and
which conformance KIND that clause yields.

- **java** — three clauses, the third on the interface side. A class's
  `superclass` yields **extends**, its `interfaces` clause **implements**, and
  an interface's own extends clause — binding no field, hanging its types off a
  differently-kinded child — yields **extends**. A qualified supertype keeps its
  qualifier; a generic one keeps its head.
- **kotlin** — one clause, three shapes, because kotlin classifies none of its
  supertypes. A constructor invocation yields **extends** (only a class can be
  constructed); a `by` delegation yields **implements** (a language rule);
  a BARE supertype yields **undeclared**, because an interface and a
  constructor-less class supertype produce the same shape. There is no interface
  declaration kind — an interface is a class declaration carrying an anonymous
  `interface` child.
- **scala** — the extends clause is read IN ORDER: the first target yields
  **extends**, every target after a `with` yields **mixin**. A trait is scala's
  contract. Linearization is NOT modeled — effective method sets depend on mixin
  order — so this is first-level declared conformance only.
- **csharp** — one base list, EVERY entry **undeclared**: its children carry no
  class-versus-interface marker and the only token is the shared colon. The
  I-prefix is a convention, not a grammar fact.
- **php** — three clauses, each its own node kind: base clause **extends**,
  interface clause **implements**, in-body `use` **trait**. A trait and an
  interface are both contracts here, since a trait supplies members to its
  users. A `use`'s conflict-resolution braces hold ADAPTATIONS, not supertypes.
- **groovy** — SINGLE-SUPERTYPE `extends` ONLY, yielding **extends**. The
  boundary below is the whole of what the vendored grammar can express.

**A declared supertype resolving to a concrete class produces NO edge** — the
non-contract rule above, repeated here because a reader of these rows will
otherwise expect a java `extends Base` edge and read its absence as breakage.

### The groovy boundary

The vendored groovy grammar declares no `implements` token at all. A combined
`extends X implements Y` clause, a bare `implements` clause and a
multi-supertype `extends I, K` clause each emit an ERROR node, and the arm
DECLINES the whole declaration rather than read a recovered spelling: a
recovered parse cannot say which spelling was the superclass. Measured:
`class Server extends Base implements Greeter` recovers onto Greeter, so an arm
reading the `superclass` field would record the INTERFACE as the superclass.
Groovy registers no import-binding arm and resolves file-scoped, so a supertype
in another file cannot resolve.

So the only groovy declaration yielding an edge is a single-supertype interface
extending an interface. **A groovy graph with few or zero declared-conformance
edges is the expected outcome, not a collection failure.**

### scala and groovy gain member nodes

Both now chunk a contract's ABSTRACT members — a different node kind from a
concrete method, captured by neither query set before. A scala trait's
`def go(): Unit` and a groovy interface's `void go()` are now nodes parented to
their contract. No previously collected graph carries them, so those two
languages need a re-collect; nothing migrates in place.

**Interfaces with type parameters are reported UNDECIDED, not implementer-free.**
Type parameters do not unify under a syntax-level signature comparison, so a
generic interface derives nothing — and the derivation's log line carries a
`generic_undecided` count so a reader can tell "could not decide" from "no
implementers". A graph shows the same nothing for both.

**Interface-decl call targeting is visible for every Go file.** Go CALLS edges
are the tree-sitter derivation on every repository and every machine — nothing
replaces or post-processes them — so a call through an interface-typed value
targets the interface's method declaration wherever it appears, alongside the
interface method-spec nodes and the IMPLEMENTS edges.

**EMBEDS changed in both directions**, so a re-collected graph will differ:

- **Gained** — an interface's embedded elements now emit EMBEDS edges. They never
  did before, because the extractor looked for a struct body and found none.
- **Lost** — both embed extractors now bind their body from the enclosing
  `type_spec`'s `type` field instead of searching descendants, so the false edges
  a nested anonymous struct or interface used to produce are gone. On this
  repository that removal is zero edges (no such declaration exists here); the
  addition is 5.

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
and nodes.

**What actually reclaims a superseded ID is the server's file-scoped node
reclaim.** For every file a collect uploads and the server does not decline, it
tombstones each live collector-owned row that the collect did not carry. Old IDs
therefore disappear on the same collect that delivers the new ones — for the
files that actually re-land.

**`collect_epoch` is not a liveness or freshness signal.** The collect upsert
writes it only where a row actually changes; an unchanged row is skipped and
keeps whatever epoch it already had. So a live row at an older epoch means
"unchanged since that collect" and nothing more, and the server's deletion
basis does not read the column. A mixed-epoch file is the designed steady
state, never evidence of stale chunker output.

### What changes per language, so an operator can decide what to re-collect

- **kotlin, rust, cpp, csharp, php (braced form), ocaml** — members gain a
  container prefix, moving from `<file>:<member>` to
  `<file>:<Container>.<member>`.
- **groovy, lua, ocaml, elm, php namespaces** — previously-unnamed declarations
  gain names, moving from a line-range identity to `<file>:<Name>`.
- **c** — VARIABLE declarations gain names the same way, moving from
  `<file>:c<digest>` to `<file>:<name>`. This is the largest single migration in
  the list: 20,279 declarations on redis 7.4.0 and 14,152 on curl 8.9.1. Function
  PROTOTYPES and multi-declarator declarations stay unnamed deliberately — a
  prototype shares its name with its definition, and naming it would make every
  call to that function ambiguous. Measured across both corpora: zero names are
  carried by both a function definition and a named declaration in one file, so
  the naming introduces no new ambiguity.
  C also **loses** nodes: the struct row now requires a body, so a `struct X`
  MENTION inside a variable declaration and a forward declaration `struct foo;`
  no longer chunk. That removal is the point — two declarations under one name
  made the struct ambiguous in its own file and every lookup that had to name it
  declined.
- **c headers that are really C++** — see the `.h` routing section above; an
  affected header's persisted `Language` moves from `c` to `cpp` and every node
  under it is re-chunked by the cpp query set.
- **elixir** — definition nodes move from `<file>:def` (hash-suffixed on
  collision) to `<file>:<entity>`, and the plain-call chunks disappear.
- **php and csharp** — a file declaring its namespace in the sibling form
  (`namespace App\Models;`, or C#'s file-scoped `namespace App.Models;`) takes
  that namespace as its symbol namespace instead of its parent directory. No
  node ID changes, since the namespace is not part of a node ID; more edges
  resolve, because two files of one namespace in different directories now
  share a symbol-map prefix.
- **typescript and tsx** — two new declaration surfaces. TypeScript interface members are declarations of their own, so a `method_signature` or `property_signature` inside an interface body becomes `<file>:<Interface>.<Member>` where no node existed before; and an `abstract class` becomes a NAMED declaration, so its members move from `<file>:<member>` to `<file>:<Class>.<member>`. The abstract-class half is a defect fix as well as a change: an abstract class previously chunked UNNAMED, which the declaration index drops outright, and every one of its members carried an empty parent and collided with every other unparented member of the same name in the file.
- **newly routed extensions** — files previously absent from the index
  entirely now appear. See the Supported Languages table.

#### The legacy `module X { }` block: typescript and tsx only

The container admission is now per (language, kind) rather than per bare node
kind — `classLikeByLang` in `chunker_class_like.go`. Tree-sitter grammars reuse
kind spellings, so the old bare-spelling table admitted `module` for Ruby and
thereby admitted it for every grammar declaring that spelling. A TypeScript
`module Sink { export function write(): void {} }` block was consequently
treated as a class-like container and parented its members.

**IDs move for typescript and tsx ONLY**, and only for a declaration lexically
inside a legacy `module X { }` block whose members the TopLevel query chunks.
Such a member's node ID moves from `<file>:<Module>.<member>` to
`<file>:<member>`, and the parent-to-member CONTAINS edge from the module chunk
to that member DISAPPEARS. Both were measured, with TypeScript's
`namespace X { }` form as the oracle: the module form previously emitted
CONTAINS `<ns>.Sink` → `<ns>.Sink.write`, while the namespace form emits only
the file-to-declaration edge. The two forms now behave identically — a
TypeScript module block is a namespace rather than a class-like container, and
its members belong to the file.

**No other language's IDs move.** Every other per-language row is a faithful
transcription of the admission already in effect. python and elm lose their
accidental `module` admission with no observable effect, for the structural
reason recorded in `chunker_class_like.go`: python's `module` is the file ROOT
node and can never resolve a name through `containerName`, and elm's is a
keyword leaf that is never an ancestor of a declaration.

**ORDERING AGAINST THE TOMBSTONE LATCH.** blockedFilesSQL clause (b) blocks
any file carrying a tombstoned node row, and re-uploading the file does not
remove tombstoned rows, so the clause latches permanently. A moved node ID is
tombstoned on the next collect, so every affected file would enter the blocked
set and STAY there — a permanent per-collect upload floor rather than a
decline. The tombstone-latch convergence fix must therefore be landed BEFORE
OR CONCURRENTLY WITH the first re-collect that picks this change up.

The local exposure is measured at zero: a scan of both local corpora for
`module X {` blocks in `.ts`/`.tsx` found ZERO files in either, with a synthetic
control file matching so the scan is known not to have been silently broken. The
ordering constraint exists for user repositories where the form is present, not
for this one.

### Typed-qualifier and declared-conformance arms: which languages carry them

Two per-language registries decide what a language contributes beyond its
chunks: a **qualifier-type arm**, binding a local name to its declared type so a
call through that name resolves exactly, and a **type-facts arm**, recording
declared result types, field types, the contract predicate and any supertype
clause the source wrote.

**`testTypedQualifierCensus` (`chunker_qualtypes_census_test.go`) is the
authority on which languages carry which arm, and it is the thing to read.** It
holds one enforced row per registered language, each with a required reason. A
list copied into this file would go stale; the table cannot.

**An armed row does NOT mean the language produces conformance edges.** A
type-facts arm may serve method-set derivation, slot binds, or conformance
capture, and even a capturing arm emits nothing where the language has no
contract construct. The reason text on each row says which case it is.

**Arming a language REMOVES edges as well as adding them, so a re-collected
graph shrinks in places.** Before an arm exists, a call through an
undeclared-type receiver falls to the dynamic rung, which emits one open-set
edge per candidate at `Confidence` 1/N. Once the arm reads the annotation the
same call resolves to ONE target and the others disappear — the improvement, not
a loss: the source named a type, and a set containing the alternatives only said
nobody had read it. The surviving edge carries `Confidence` **0**, not 1, since
an exactly-bound edge emits no group at all — so a consumer filtering on
`Confidence >= 0.5` keeps the old ambiguous pair and drops the new exact answer.

#### Recorded limitations

Measured facts about what the collector does NOT read, recorded so an absence is
not mistaken for an oversight:

- **JSDoc and YARD are not parsed.** Both arrive as comment tokens, and comment
  chunks are dropped entirely, so a documented parameter type binds nothing.
- **Python structural Protocols are not matched.** A class satisfying a Protocol
  without naming it declares nothing. Naming `Protocol` as a base is an ordinary
  nominal base, and separately marks the declaring class a contract.
- **Elixir conformance is module-level only.** Its container and its members are
  both the `call` kind, so a function carries no container name and no member
  pairing can exist. The type-level edge stands alone and is the complete answer.
- **A member-level edge can cross to a same-base-name sibling.** Member pairing
  keys on `{scope, parent base name, name}`, and the suffix disambiguating two
  same-named CONTAINERS is not in that key — so where one file declares two
  containers of one name (python re-declaration, TypeScript declaration merging,
  a `module X` beside a `class X`), the pairing can attach to the sibling that
  declared no conformance. The type-level edge is unaffected, and the counters
  cannot see it: with only the sibling declaring it, the lookup finds one.
- **JavaScript captures class heritage but can emit no IMPLEMENTS edge**, having
  no contract construct — so every JavaScript capture is counted, not emitted.
- **Ruby and Elixir have no import binds**, so both their typed-qualifier
  resolution and their conformance resolution are same-file by construction.
- **Cross-file reach differs by route.** An imported type spelling resolves for
  the DIRECT-TYPE route; the call-return route and the field hop are same-file
  only — a property of where the index-aware resolver is wired in, not of any arm.

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

A DECLARED SUPERTYPE naming the shared name is a different question with a
different answer. It resolves to the CONTRACT among the colliding declarations
when exactly one of them is a contract and every other collider is a reopening
of it — another body of the same nominal type, carrying the language's own
reopening flag. Swift's protocol-plus-extension is the shape this serves: a
protocol that has an extension anywhere in its module shares one declaration
key with that extension, and before this rule every conformance naming such a
protocol was declined. Two same-named contracts, or a collider that is not a
reopening at all, still decline as ambiguous — the referent is genuinely
unknown there. The derivation's log line carries a `reopened_supertype` count
so a reader can tell a narrowed resolution from one that never collided.

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
- `qualifiedName` (`chunker_edges.go:411`) — same shape as declarations for the CONTAINS edge target: `<namespace>.<name>`, or `<namespace>.<parent>.<name>` when @parent_name bound, which is what the chunk's own symbol-map key uses.
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
| `chunker_identity.go`   | Container/scope resolution: `containerName`, `findEnclosingScope`              |
| `chunker_class_like.go`  | Per-language container admission: `classLikeByLang` |
| `chunker_decl_name.go`  | `declNameResolvers` registry + shared field/child helpers                      |
| `chunker_go.go`         | Go-specific helpers: receiver extraction, embeds, signatures                   |
| `queries_go.go`         | Go tree-sitter query patterns                                                  |
| `queries_typescript.go` | TypeScript tree-sitter query patterns                                          |
