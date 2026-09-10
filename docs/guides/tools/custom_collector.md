# custom_collector

## Overview

A **custom collector** is a user-defined graph family populated by proxying an
MCP provider with a specific tool-call target. Alongside the built-in families
(knowledge, code, practice, linkage, checks, web, pdf) you can
define your own.

**Registration is a config file, not a tool call.** A custom collector is an
entry in a `collectors.json` file shaped like Claude's MCP server config, with
one addition: a `tool` field naming the single MCP tool the daemon calls to
collect. `knowledge collector add | list | get | remove` writes and reads those
files, mirroring `claude mcp`. The `custom_collector` MCP tool is READ-ONLY: its
one operation, `list`, reports what the SERVER holds for each family — the
summary/embed/sync behavior its indexing pipeline reads.

The entry's **name is the graph family** the results land in; the `collect`
call's `id` is the instance inside that family. An entry named `tickets`
collected with `id: "acme"` writes the `acme` graph of the `tickets` family, and
a second collect under `id: "beta"` writes a second graph in the same family.

## Where the ready-made collectors live

A set of collectors is published and maintained rather than written from
scratch: AWS, Azure, CloudWatch, GCP, Kubernetes, Kubernetes logs, Loki and
Stackdriver. They live in the public **knowledge-contrib** repository, together
with the collector framework they are built on and the install script that
fetches them.

Each release there is cut on the same tag as the client release, so a client
version and a collector version pair by tag. It carries one archive per
collector per platform, named
`knowledge-collector-<collector>-<os>-<arch>.tar.gz` (`.zip` on Windows) and
holding a single binary called `knowledge-collector-<collector>`, plus a
`checksums.txt` covering every archive. The install script in that repository is
the way to get one: it picks the archive for the running platform, verifies it
against `checksums.txt`, and installs nothing at all if the entry is missing or
the checksum does not match.

Installing a published collector still leaves the registration to you, because
registration is a config file and nothing else: put the installed binary's path
in the entry's `command`, as the sections below describe. Writing your own
collector against the same framework is the other half of this guide, and
nothing about the published set is privileged — a collector you write is
registered exactly the same way.

That repository's front page lists what it carries and how to install it, and
`framework/README.md` there is the collector author's side of everything below:
the wire contract in a language-neutral form, what the Go framework adds over
it, a worked collector, and the registration entry that installs it.

## The config file

Two scopes, both named `collectors.json`:

| Scope | Path | For |
| --- | --- | --- |
| `user` | `~/.knowledge/collectors.json` | this machine's operator; the DEFAULT for a write |
| `project` | `<repo root>/.knowledge/collectors.json` | one repository, checked in beside it |

**Precedence, highest first: project, then user.** Where a name appears in both,
the project entry wins and **it is taken whole — no field is merged across
scopes**. A project entry with no `env` block resolves to an empty environment,
never to the user entry's. That is the rule Claude's own MCP config states for
its scopes.

The daemon finds the project file by walking UP from the calling session's own
working directory to the nearest ancestor holding `.knowledge/collectors.json`.
Two sessions standing in two different repositories see two different project
files; a session with no working directory of its own sees the user scope alone.

**Every read re-reads the file from disk.** A hand edit takes effect on the next
registration lookup — no daemon restart, no cache to go stale.

```jsonc
{
  "collectors": {
    "tickets": {
      "type": "stdio",
      "command": "/usr/local/bin/ticket-mcp",
      "args": ["--serve"],
      "env": { "TICKET_API_BASE": "https://tickets.example.com", "HOME": "/home/you" },
      "tool": "collect_tickets"
    },
    "loki-remote": {
      "type": "http",
      "url": "https://collectors.example.com/loki",
      "headers": { "Authorization": "Bearer ${LOKI_TOKEN:-}" },
      "tool": "collect_logs",
      "behavior": { "summarizable": true, "embeddable": true, "syncable": true }
    }
  }
}
```

**A 401 from a remote provider with nothing else wrong means the token is unset
where the daemon is running.** The header is sent exactly as written, so an unset
`LOKI_TOKEN` sends `Bearer ` with nothing after it; the daemon composes no
credential of its own and has nothing else to try.

### The command is resolved against the DAEMON's PATH, not your shell's

**Give `command` an absolute path unless you know the name is on the daemon's
own `PATH`.** Every worked entry on this page does.

The daemon resolves a stdio entry's `command` once, with `exec.LookPath`, in the
process serving the collect. `LookPath` reads the `PATH` of THAT process — the
daemon's — and a daemon started by a service manager does not inherit your login
shell's environment. Under launchd the daemon's `PATH` is
`/usr/bin:/bin:/usr/sbin:/sbin`, so a command installed under a user prefix
(`/opt/homebrew/bin`, `~/.local/bin`, a Node or Python tool shim) does not
resolve, and the collect fails with `stdio provider command "x" is not
executable`.

**`knowledge collector add` proves nothing about this.** Its dial runs in the
shell YOU typed the command in, with your `PATH`, so a bare name that dials
cleanly at registration can still fail at the first collect. The two processes
are not the same and do not share an environment.

**Putting `PATH` in the entry's `env` block does not help either**, and this is
the trap worth knowing: the env block is the CHILD's environment and is applied
after the lookup has already happened. It changes what the provider sees once it
is running; it cannot change how its own name was resolved.

An absolute path sidesteps all of it. `command -v <name>` in your shell prints
the path to put in the entry.
### The entry

| Field | Applies to | Meaning |
| --- | --- | --- |
| `type` | both | `"stdio"` or `"http"`. Required. |
| `command` | stdio | The provider executable. **An absolute path, or a name on the DAEMON's `PATH`** — see below. Required. |
| `args` | stdio | Arguments, each its own argv element. Never a shell string. |
| `env` | stdio | Name → value. **The child's whole environment.** |
| `url` | http | The provider's MCP endpoint (http or https). Required. |
| `headers` | http | Name → value, sent on every request to the provider. |
| `tool` | both | The single MCP tool the daemon calls to collect. Required. |
| `behavior` | both | Optional. `syncable`, `summarizable`, `embeddable`, `embed_fields`, `summarize_fields`, `bm25_fields`, `extra`. |
| `node_types` | both | Optional. Per-node-type overrides of the behavior cascade. |

An entry naming the other transport's fields is refused — a `stdio` entry
carrying `url` or `headers`, an `http` entry carrying `command`, `args` or `env`.

**Everything is refused loudly, nothing is skipped.** Invalid JSON, an unknown
key at any level, a missing or unknown `type`, a stdio entry with no `command`,
an http entry with no `url`, a name repeated inside one file, a non-string env or
header value, a missing `tool`, a non-boolean where a behavior boolean belongs —
each is an error naming the file, the entry and the field. A file the daemon
cannot read is an error too: an absent file is an empty scope, an unreadable one
is unknown registrations.

### `${VAR}` expansion

`${VAR}` and `${VAR:-default}` are expanded in `command`, every element of
`args`, every VALUE of `env`, `url`, and every VALUE of `headers`. The bare
`$VAR` form works too. **An unset variable with no default is an ERROR** naming
the file, the entry, the field and the variable — never an empty substitution.

Four positions stay LITERAL and are never expanded: the entry name, `tool`, the
KEYS of `env` and the KEYS of `headers`. A key computed from the environment
would make the file's own shape unreadable.

This is what lets a project-scope file live at a repository root without holding
a secret: name the variable, keep the value in your own environment.

**Whose environment, though, is the part that decides whether it works.** The
expansion runs when the FILE IS READ, in the process serving the collect. Started
by hand from your shell, that process has your environment and a reference
resolves. Started by a service manager, it has whatever the service definition
gives it, which is usually a bare PATH — and then a `${VAR}` with no default is
REFUSED, naming the file, the entry, the field and the variable. The refusal is
scoped to the entry that carries the reference: every other collector in that
file still collects. `${VAR:-}` does not fail at all; it quietly resolves to the
empty string.

**A stdio entry's `env` block carries configuration AND credentials, and the two
are written differently.** Configuration is a literal. A credential is a bare
`${NAME}` reference and never a value: the value stays in the environment the
serving process runs in, so the file at your repository root holds a variable
name and nothing anyone can use. The install script writes exactly that shape for
a credential name your installing shell holds.

**A credential is never written with the `${NAME:-}` default.** An empty value is
not the same as an absent name, and a provider that tells them apart refuses the
empty one or, worse, acts on it: the entry's env block is the child process's
WHOLE environment, so `NAME=` is a name the provider sees set. The bare form has
no default to resolve to, so a serving process that does not hold the name
refuses that entry by name instead — which is a message you can act on rather
than a credential that looks supplied.

**Whether `${VAR:-}` is safe in an env block is the collector's own answer, per
name.** A collector marks each name it tells present-and-empty apart from absent
with `empty_sensitive` on its describe declaration; for every other name the
defaulted form resolves to an empty value the collector treats exactly as it
treats an absent one, so a worked entry may show it. For a MARKED name, a worked
entry omits the key or gives a literal, because the reference delivers a state
that collector acts on. Ask a published collector which of its names are marked
with `install.sh --print-env-sensitive-table`, or your own binary with
`--describe-env-sensitive`.

An HTTP entry is the other case, and a reference is the right mechanism there
too: no child process is involved, the daemon sends the header itself, and the
value resolves in the same process that is doing the reading. A bare reference
whose variable that process does not hold refuses THAT ENTRY by name, naming the
header, and leaves every other collector in the scope collecting. The defaulted
form, `${LOKI_TOKEN:-}` as the example below does, sends the header with an empty
credential instead, which the provider reports as a permissions problem.

### The behavior block

The behavior an entry declares is forwarded to the server, where the indexing
pipeline reads it. **An entry with no `behavior` block gets `syncable` TRUE, and
`summarizable` and `embeddable` FALSE.** An explicit value is honored exactly as
written, per family or per node type.

The two LLM axes are opt-in because they are LLM spend, and whether a given
family is worth summarizing or embedding is a decision about that collector. What
an opted-out family gives up is summaries and vectors, which is semantic ranking.
It does not give up findability: a registered family is admitted to the KEYWORD
index whatever its embed setting says, so an entry with no block is searchable
from its first collect.

**Where these values come from now.** `collector add` fills the three field
lists, and `syncable`, from your collector's own `describe` declaration, so an
operator installing it writes none of them by hand; `summarizable` and
`embeddable` come from the operator's flags ALONE and never from the
declaration, and a flag always overrides a declared value for the same field.
The collector's suggestion is printed by the add rather than applied.

Opting an axis in with no field list composes its text from a default shape:
`symbol_name`, `summary`, `keywords`, `description` and `content`. Name
`embed_fields`, `summarize_fields` or `bm25_fields` to choose a different set. A
list you declare must name at least one field, must not carry a blank name, and
must not name the same field twice — each of those is refused when the file is
read, rather than quietly composing text you did not ask for.

## The CLI

```bash
knowledge collector add [-s user|project] [-t stdio|http] --tool <name> \
    [-e KEY=VALUE]... [-H 'Name: value']... \
    [--summarizable=true|false] [--embeddable=true|false] \
    [--embed-fields <field>]... [--summarize-fields <field>]... [--bm25-fields <field>]... \
    <name> -- <command> [args...]
knowledge collector add [-s ...] -t http --tool <name> [-H 'Name: value']... <name> <url>
knowledge collector list [-s user|project] [--port <n>]
knowledge collector get <name>
knowledge collector remove <name> [-s user|project]
```

**Options come before the name and the command follows a `--`**, exactly as
`claude mcp add` does. The `--` is what keeps the provider's own flags out of
this command's parser: `... tickets -- /usr/local/bin/ticket-mcp --region us-east-1` passes
`--region us-east-1` to the provider.

- `-e KEY=VALUE` and `-H 'Name: value'` repeat, and each splits on its FIRST
  separator only, so a value containing `=` or `:` survives intact.
- The default scope is `user`.
- `add` **dials the provider before writing**: it completes the MCP handshake,
  finds the tool and checks its schemas. A provider that fails the contract
  leaves NO entry written.
- `add`, `get` and `remove` touch files only and need no running daemon. `list`
  reads the server for its legacy column and says so when it cannot.

`list` shows every entry from both scopes with its scope, marks which one is in
effect where a name appears twice, and names any legacy family (below).

## The eight collectors that ship with this repository

Cloud inventory and log collection are custom collectors like any other. Eight
of them are built from `cmd/collectors` in this repository and register a family
each:

| Collector | Family it registers | Source |
| --- | --- | --- |
| `aws` | AWS resource inventory | `cmd/collectors/aws` |
| `gcp` | GCP resource inventory | `cmd/collectors/gcp` |
| `azure` | Azure resource inventory | `cmd/collectors/azure` |
| `k8s` | Kubernetes resource inventory | `cmd/collectors/k8s` |
| `cloudwatch` | CloudWatch log graphs | `cmd/collectors/cloudwatch` |
| `loki` | Loki log graphs | `cmd/collectors/loki` |
| `stackdriver` | Cloud Logging log graphs | `cmd/collectors/stackdriver` |
| `k8s-logs` | Kubernetes Events log graphs | `cmd/collectors/k8s-logs` |

Each has its own README naming the environment it reads and a worked
`collectors.json` entry. Build the binary you want, then register it with
`knowledge collector add` like any other provider; the per-collector
summarize and embed behavior is opt-in through the flags above.

**Released builds are published, and one command installs one.** Each of the
eight ships to the public `knowledge-contrib` repository on a knowledge release,
one archive per collector per platform with a `checksums.txt` covering them all,
and that repository's install script fetches the archive for your platform and
verifies it before installing anything:

```bash
curl -fsSL https://raw.githubusercontent.com/fulminate-io/knowledge-contrib/main/install.sh | sh -s -- aws
```

It places the binary in `~/.knowledge/bin`, records the tag in a
`<binary>.version` sidecar beside it, and ends by running the `knowledge
collector add` above for you: that collector's tool name, the absolute path to
the binary it just placed, and the non-secret configuration you have set. A
second run downloads nothing while the placed binary reports the target tag.
`uninstall.sh <collector>` removes the binary, the sidecar and the entry, and no
graph.

**What the script does not do is supply your credentials**, and that is the part
that stays yours. It writes no credential VALUE into the entry. For a credential
name your installing shell holds it writes a bare `${NAME}` reference, so the
value is read from the environment the serving process runs in when it starts the
collector; a name that shell does not hold is left out of the entry entirely. It
never writes the `${NAME:-}` form, for the reason the expansion section above
gives. Each collector's README says which names it reads and how its credential
reaches it. A private release is fetched with a token read from
`GH_TOKEN`, then `GITHUB_TOKEN`, which is written nowhere.

Registering a collector of your own needs none of this: it is the same
`knowledge collector add`, with a command or a URL and the tool it serves.

## Legacy families: nothing resolves from the server catalog

The config file IS the registration record. A family the server still holds a
record for, with no entry in either file, is **legacy**: nothing resolves from
it. `knowledge collector list` names it and prints the `knowledge collector add`
invocation that would write the entry, and a `collect` on that name FAILS,
naming the family, the absolute path of the file to write and that same
invocation. A record from a retired contract that names no tool cannot be
converted automatically; `list` says so, and the entry is written by hand from
the table above.

Once you have written the entry — or decided the family is dead — the stale
server record is removed with the generic delete, which addresses it by the
family name:

```jsonc
mutate({ "operation": "delete", "ids": ["tickets"] })
```

`custom_collector` has no write operations: the config file is the registration
record, and the catalog record is an ordinary graph node.

## When & how to use

Reach for a custom collector when the built-in graph families do not cover a
source you want in the graph and you have (or can write) an MCP provider that
emits nodes for it. Most developers never need this — the built-in types handle
code and knowledge already.

### Precedence over a built-in collector of the same name

Some built-in *collectors* are not built-in *graph types*: `aws`, `gcp`, `azure`,
`k8s`, `github`, `gitlab` and `bitbucket` are collector names, and you may
register a family under any of them. When you do, the entry wins:
a registered custom_collector family wins over a built-in collector of the same name.
An entry named `gcp` makes `collect({"type": "gcp"})` dial your provider and land
the result in your `gcp` family; the built-in GCP collector does not run, and no
built-in post-collect enrichment runs against anyone else's graphs. The built-in
collector still serves the name whenever no entry claims it, so removing your
entry restores it.

Built-in **graph type** names — knowledge, code, practice, linkage,
checks, web, pdf — cannot be registered at all, so they are never shadowed. The
three RETIRED names, `cloud`, `logs` and `cicd`, cannot be registered either:
they named built-in families until their collectors became contrib collectors,
and an
operator's leftover storage directory must not be adopted by a new family. The
refusal names the removal rather than reporting the name as unknown.
`knowledge collector add` refuses one before writing anything.

A registered family is also a first-class row on `manage({"operation":
"status"})`'s coverage table, beside the built-in families. That table is an
INVENTORY of what the machine holds, so no behavior declaration is required to
be on it: a family is listed from the moment its entry exists, whatever its
behavior block says, and a family declaring neither `syncable` nor `embeddable`
is listed with its columns structurally zero rather than omitted.

Before its first collect the family has no graph, so its row names the family
with no instance half, and its band reads `not collected`. The count cells read
`not collected (registered, no graph yet)` rather than zero. The segment cell is
read from the entry's own declaration: a family declaring `embeddable` anywhere
in the cascade could carry a segment pool once collected and says nobody has
read one, while a family declaring none renders the no-pool dash. Run a collect
and the row becomes an ordinary one.

```jsonc
// Read what the SERVER holds for each family: its behavior, and any legacy
// family with no config entry.
custom_collector({ "operation": "list" })
```

## Parameters

<!-- BEGIN GENERATED: params -->
| Parameter | Type | Required | Enum | Description |
| --- | --- | --- | --- | --- |
| `format` | string |  |  | Output format: 'text' (default) or 'json'. |
| `operation` | string | yes | list | Operation to perform. Registration is a config file: use `knowledge collector add\|get\|remove` to write one. |
<!-- END GENERATED: params -->

## The collector contract

Writing the entry is only half the story: the other half is the MCP provider you
point it at. This section is the contract that provider must honor. Once an entry
exists, you populate its family with [`collect`](collect.md) —
`collect({ "type": "<your-family>", "id": "<graph>", ... })` dials the provider,
completes the MCP handshake, lists its tools, verifies the entry's tool schemas,
calls it, and streams the nodes and edges it returns to the server.

**Your provider serves TWO tools.** The collect tool your entry names, and a
second one named `describe` that says what your collector *is*. Both are
required; a provider that serves only the first is refused by name and nothing
is written.

**The schemas are a hard requirement, checked twice.** The tool you name must
advertise *both* an `inputSchema` and an `outputSchema`, `describe` must
advertise an `outputSchema`, and each must satisfy the schemas below. They are
checked by `knowledge collector add` — which is why that command dials your
provider before writing anything — and again on every collect, before the call. A tool that advertises no output schema, or one that
does not declare what the contract requires, is refused with an error naming the
tool and the mismatch, and nothing is written. A hand-edited entry passes through
no write path, so it is dialed at its FIRST collect: editing the file by hand is
never a way past the contract check.

Because `outputSchema` and `structuredContent` exist only from MCP revision
2025-06-18 onward, a provider that speaks only 2024-11-05 cannot satisfy this
contract.

### The required `describe` tool

`describe` takes no arguments and returns ONE document: everything about your
collector that its registration entry records. `knowledge collector add` calls it
once during its dial and **writes the entry from it**, so an operator installing
your collector transcribes nothing.

```jsonc
{
  "behavior": {                      // REQUIRED
    "summarizable": true,            // a SUGGESTION — see below
    "embeddable": true,              // a SUGGESTION — see below
    "syncable": true,
    "embed_fields": ["summary", "symbol_name"],
    "summarize_fields": ["content"],
    "bm25_fields": ["symbol_name", "summary", "content"]
  },
  "node_type_overrides": {           // optional, keyed by your own node type
    "log-chunk": { "embeddable": false, "bm25_fields": ["symbol_name"] }
  },
  "node_types": ["log-template", "log-stream"],   // REQUIRED — see the vocabulary note
  "edge_types": ["CONTAINS"],                     // REQUIRED
  "environment": [                                // REQUIRED (an empty array if you read none)
    { "name": "HOME",        "class": "path" },
    { "name": "ACME_REGION", "class": "selector" },
    { "name": "ACME_TOKEN",  "class": "secret" },
    { "name": "HTTPS_PROXY", "class": "not-carried" }
  ],
  "context": {                        // optional — the same shape the entry's `context` key takes
    "aws": { "all_node_types": true, "node_fields": ["id"], "metadata_keys": ["region"] }
  }
}
```

The authority is the checked-in schema, `contract/collector_describe.schema.json`,
beside the two below; the refusal you get names the path that falls short.

**The two LLM axes are a SUGGESTION and never a setting.** Summarizing and
embedding are spend on the operator's account, so `collector add` takes them from
*their* flags (`--summarize`, `--embed`) and PRINTS what you suggested without
applying it. Everything else you declare is written as declared.

**The type vocabulary is CLOSED once it is registered.** `node_types` and
`edge_types` are every type your collector emits; they ride the registration
record to the server, and a collect carrying a type outside them is refused at
ingest, by name, with nothing written. Add a type to your declaration in the same
change that starts emitting it. A family registered before this tool existed
carries no vocabulary at all: it keeps accept-all and the collect says so once,
which is your signal to re-run the add.

**The environment declaration is of NAMES, never values.** The class is what an
installer does with each name: `path` writes the literal, `selector` writes the
literal when it is set and omits the key entirely when it is not, `secret` writes
nothing in any state — not the value and not a `${VAR}` reference — and
`not-carried` is a name you read that an installed entry deliberately does not
declare. Declare the last kind too: an operator whose variable is absent can then
tell a decision from an oversight. A declaration carrying a value is refused.

**Each name also carries an optional `empty_sensitive` flag.** Set it for a name
your collector tells PRESENT AND EMPTY apart from ABSENT — one it refuses when
empty, one whose presence it branches on, or one it hands to a dependency that
does either. Omitting it means false, so a collector that treats every name's
empty value as absent declares nothing new. It changes nothing an installer
writes into an entry; it decides whether your documentation may show a `${VAR:-}`
reference for the name, and it is legal on every class including `not-carried`,
because a name no entry carries can still appear in an example someone copies.

**Your binary answers three of these on argv, before anything serves.** An
installer is `#!/bin/sh` and cannot speak MCP, so `--describe-env-table` prints
one `class name` row per declared variable, `--describe-tool` prints the tool
name, and `--describe-env-sensitive` prints one bare name per marked variable and
nothing when none are marked. Each is matched in FIRST argv position only, so an
entry's own arguments reach your collector untouched. A collector built on the Go
framework gets all three from its `Describe()` method.

### The required input schema

The client calls your tool with the collect `id` and the collect `params` object:

```jsonc
{
  "type": "object",
  "required": ["id"],
  "properties": {
    "id":      { "type": "string" },  // the collect id — the graph INSTANCE to write
    "params":  { "type": "object" },  // the collect params, passed through verbatim
    "context": { "type": "object" }   // the declared foreign-graph context; see below
  }
}
```

Your tool's `inputSchema` must declare at least the **required** part of this,
which is the collect `id` alone. It may be **stricter** — declare the keys you
accept inside `params`, mark more of them required, add formats and enums — but
never looser. The arguments the client sends are validated against *your* schema
before the call, so a provider that needs a param the collect did not supply is
refused with a named mismatch rather than failing inside its own handler.

`params` and `context` are **optional** properties. A tool that declares neither
is admitted and simply receives neither, which is what keeps a provider written
against an earlier version of this contract working unchanged. Declare `context`
only if you mean to use it, and read the next section first.

### The declared foreign-graph context

A collector sometimes needs to know something about the operator's *other*
graphs: which resources another collector has inventoried, so it can emit its
own cross-graph references; which repositories are indexed; what a chart file in
one of them is called. Your walk cannot go and read those — it has no graph client, by design.
So it **declares** what it needs, once, in the config entry, and the client fills
the collect input's `context` block from its own graphs before the call.

The declaration is a `context` object beside `behavior` in your entry:

```jsonc
{
  "collectors": {
    "acme": {
      "type": "stdio",
      "command": "/usr/local/bin/acme-collector",
      "tool": "collect",
      "context": {
        // A REGISTERED GRAPH TYPE, by the name its own collector registered.
        // `code` is the one built-in family the client can supply; every other
        // key here is a family an operator installed.
        "aws": {
          "node_types":    ["aws-resource", "proxy"],
          "node_fields":   ["id", "type", "symbol_name"],
          "metadata_keys": ["resource_type"],
          "edge_fields":   ["from_id", "to_id"]
        },
        // EVERY node of a family, whatever its type, for a family whose type
        // vocabulary is open or too large to name. Declaring it beside a
        // node_types list is refused: the two say different things.
        "gcp": {
          "all_node_types": true,
          "node_fields":    ["id", "type"],
          "metadata_keys":  ["region"]
        },
        "code": {
          "node_types":     ["file"],
          "node_fields":    ["id", "file_path", "content"],
          "path_basenames": ["Chart.yaml", "Chart.yml"]
        }
      }
    }
  }
}
```

What arrives on the call is exactly that slice, in the contract's own spellings:

```jsonc
{
  "id": "prod",
  "context": {
    "aws": [
      { "graph_name": "prod-aws",
        "nodes": [ { "id": "...", "type": "aws-resource", "symbol_name": "api",
                     "metadata": { "resource_type": "ecs:service" } } ],
        "edges": [ { "from_id": "...", "to_id": "..." } ] }
    ],
    "code": [
      { "graph_name": "api",
        "nodes": [ { "id": "...", "file_path": "deploy/Chart.yaml", "content": "..." } ] }
    ]
  }
}
```

Four rules govern it, and each of them is something you can rely on.

**Declared-only, with no baseline.** You receive the families you named, the node
types you named, the fields you named and the metadata keys you named. Nothing
else is ever sent, and there is no default slice a collector gets for free. A
field you did not declare is absent from the document rather than present and
empty, so an absent field means "not declared" and never "not present in the
graph". A declared metadata key a node does not carry is omitted for that node
alone.

**Selecting every type is its own declaration.** `node_types` names the types
you want. An EMPTY list — or no `node_types` key at all — means the graph NAMES
and no nodes, which is the whole input a membership test over graph names needs.
`"all_node_types": true` is the third thing: every node of the family, whatever
its type, for a provider that emits one node type per resource kind and whose
list would rot the day it adds one. Declaring the flag AND a non-empty
`node_types` is refused by name when the file is read. The flag satisfies the
coherence rules below in the same way a type list does: a family draining every
type carries nodes, so the fields and metadata keys you declare have somewhere to
land, and the edge read has ids to pivot on.

**Nothing you cannot be given is silently dropped, and nothing inert is
admitted.** The families the client can supply are `code` plus **every graph
type registered on the daemon** — which is an operator-time fact, not a fixed
list, so the block is an object keyed by family name rather than a struct with a
field per family. Each of these is an error, naming the offending value:

- a family that is neither `code` nor a registered graph type. This one is
  checked at the COLLECT, where the registry is readable, and the refusal lists
  the families that would have been accepted;
- a RETIRED family name (`cloud`, `logs`), refused at load, at
  `knowledge collector add` and at every collect alike — that check needs no
  registry, so it fires at the moment the entry is written;
- a node or edge field the client cannot produce;
- `path_basenames` under any family but `code`, where there are no file paths to
  narrow;
- `node_fields` or `metadata_keys` with no `node_types` to carry them, which
  would ask for node facts with no node in the slice to put them on;
- a declaration your tool cannot receive: if your entry declares `context` and
  your tool's `inputSchema` does not declare the property, the entry is refused
  rather than the block being sent and dropped.

Declaring a family with **nothing at all** is not in that list and is legal — see
the graph-names rule below.

**A collector that declares nothing gets no block at all.** The `context` key is
absent from the arguments entirely, not present and empty, and the client
performs neither read. Everything written before this property existed keeps
working with no edit.

**The graph names always arrive, even for a family declared with nothing.** A
family whose object is empty yields one entry per graph carrying its name and no
nodes, which is the whole input a predicate that tests membership over repository
names needs. That is the legal way to ask for the names alone; asking for
`node_fields` with no `node_types` is the incoherent form refused above.

For the `code` family the names are the indexed repositories, and a **branch
overlay is not one of them**: a name of the form `repo@branch` is left out, so
what you receive is the set the built-in linker's own predicates work against.

`path_basenames` narrows the `code` family to files with those basenames. It is a
way to say what you need, not a limit imposed on you: declare none and you
receive every node of the declared types, whatever that costs.

### The required output schema

Your tool must return its graph as `structuredContent` matching at least:

```jsonc
{
  "type": "object",
  "required": ["nodes", "edges", "walk_complete"],
  "properties": {
    "nodes": { "type": "array",
               "items": { "type": "object", "required": ["id", "type"] } },
    "edges": { "type": "array",
               "items": { "type": "object", "required": ["from_id", "to_id", "type"],
                          "properties": { "source_graph": { "type": "string" },
                                          "target_graph": { "type": "string" } } } },
    "walk_complete": { "type": "boolean" }
  }
}
```

**`walk_complete` is the completeness assertion**, and it is required — you
cannot omit it. It is your statement that this walk enumerated the whole source:
`true` when nothing was skipped, unreadable or truncated, `false` otherwise. A
result asserting `false` is admitted with the assertion recorded, and it disables
the deletion phase for that collect, exactly as an incomplete walk does for a
code graph. Since the field is required, a provider cannot disable deletion by
silence — it has to say so.

**Your result carries no graph identity.** The output declares no graph type and
no graph name — neither is a field of it: the registration name is the family and
the collect `id` is the instance, both supplied by the client. A provider
therefore cannot write into a graph type it was not registered as.

The checked-in schemas are the authority; the JSON above is their shape. They
live beside the collector host in the client source as
`contract/collector_input.schema.json` and `contract/collector_output.schema.json`.

### Node and edge fields a collector may set

Each entry in `nodes[]` and `edges[]` is a flat JSON object. A collector may set
the fields below; everything else is server-owned bookkeeping (creation /
update / tombstone timestamps, collect epoch, internal versioning) that the
collect-write path stamps — a collector cannot set those. **A field not in these
tables is an error**, not a silent drop: a typo'd key is reported by name rather
than leaving you to debug an empty graph.

**Node fields** (every field except `id` and `type` is optional):

| Field | Type | Notes |
| --- | --- | --- |
| `id` | string | Stable identifier for the node within the graph. |
| `type` | string | Node type. **Required** — a node with an empty `type` fails the collect loud. |
| `symbol_name` | string | Display / symbol name. |
| `file_path` | string | Source path, when the node maps to a file. |
| `language` | string | Language tag. |
| `start_line` / `end_line` | int | Line span. |
| `content` | string | Full body text. |
| `signature` | string | Signature / one-line shape. |
| `summary` | string | Short summary (if the type is summarizable). |
| `description` | string | Human-facing description. |
| `source` | string | Provenance tag. |
| `status` | string | Open-string status. |
| `keywords` | string | BM25 keyword-token boost. |
| `is_exported` | bool | Visibility flag. |
| `metadata` | object (string→string) | Free-form domain data. **Any field not in this table rides here.** |

**Edge fields:**

| Field | Type | Notes |
| --- | --- | --- |
| `from_id` / `to_id` | string | Endpoint node IDs (the external contract references endpoints by ID, never by index). |
| `type` | string | Edge type. |
| `weight` | float | Optional. |
| `confidence` | float | Optional. |
| `method` | string | Optional — how the edge was derived. |
| `evidence` | string | Optional — backing evidence. |
| `source_graph` | string | Optional. The graph **family** `from_id` lives in, when the edge points OUT OF a node in ANOTHER graph. Omit it for an ordinary edge of your own graph. Set at most one of this and `target_graph`. See below. |
| `target_graph` | string | Optional. The graph **family** `to_id` lives in, when the edge points at a node in ANOTHER graph. Omit it for an ordinary edge of your own graph. Set at most one of this and `source_graph`. See below. |

### Edges that point into another graph

Most edges join two nodes of your own graph and need nothing extra. An edge one
of whose endpoints names a node in a *different* graph is a **cross-graph edge**,
and it names that graph's **family** — a graph type such as `code`, or another
registered family such as `acme-aws`. It is the family, not one graph instance:
the client enumerates that family's loaded graphs, finds the endpoint among them,
and derives the instance itself.

**Which field you set says which end is the foreign one.** Set `target_graph`
when `to_id` is the foreign node, and `source_graph` when `from_id` is. A Helm
chart that deploys a workload is the second shape: the relationship runs from the
chart file in a code graph to the object in yours, so reversing it to fit
`target_graph` would assert something different, and emitting it with no family
at all would land a dangling edge to an id nothing in your graph resolves.

Such an edge is **not written into your own graph**. The client materializes a
proxy for the foreign node and writes the edge into the **linkage graph**, which
is where every cross-graph relationship in the system lives. Writing a native
edge straight across a graph boundary would break per-graph isolation, which is
the reason the proxy exists at all.

Four conditions fail the whole collect, each naming the edge:

- no graph of the named family is loaded,
- the foreign endpoint is in none of that family's graphs,
- the family could not be enumerated,
- the edge names both `source_graph` and `target_graph`.

The last one is structural rather than a style rule: one resolution enumerates
one family and looks both endpoints up in it, so an edge foreign at both ends
names a relationship the client cannot resolve. All four are refused **during the
collect**, in the pass that resolves these edges — not at registration, which
never sees an edge.

None of the four is a warning. Your provider asserted a relationship to a node
in another graph; shipping the rest of the walk and dropping that assertion would
report success while losing the edge.

**A proxy you want in your OWN graph is a different shape and needs no field.**
Emit a node typed `proxy` carrying `foreign_graph`, `foreign_id` and
`foreign_type` in its `metadata`, plus an ordinary edge to it, and both land in
your graph exactly like any other node and edge. That is what the contrib log
collectors emit. Use `source_graph` or `target_graph` when the relationship
belongs in the linkage graph; emit an own-graph proxy node when you want the
reference to live beside your own data.

## The two transports

### stdio: a child process, with the entry's environment

A `stdio` provider is spawned as a child process speaking MCP over stdin/stdout.

- **`env` is the child's complete environment**, as name → value pairs written
  in the entry. A variable the block does not name is **absent** from your
  provider, whatever the daemon's own environment holds — no `HOME`, no `PATH`,
  no cloud SDK variables, no LLM keys. A variable the block DOES name arrives
  carrying the block's value, even when the daemon holds the same name with a
  different one: the daemon looks nothing up and passes nothing of its own.
- **A variable set to the empty string is PRESENT and empty**, which is a
  different input from one the block does not carry at all. Whether your provider
  acts on the difference is your provider's business, and you declare the answer
  per name with `empty_sensitive`; most readers treat the two states alike, and a
  worked entry may show `${VAR:-}` for those.
- **This is how a stdio provider gets its credentials.** Keep the value out of a
  checked-in file with `${VAR}`: the entry names the variable, your own
  environment holds the secret.
- **Nothing else arrives, including proxy and certificate variables.** The
  daemon adds no environment of its own — if your provider needs `HTTPS_PROXY`,
  `SSL_CERT_FILE` or any other platform variable, put it in the entry like any
  other. That keeps one rule with no exception, and it is the same rule whether
  your collector runs here over stdio or on another host over HTTP, which the
  daemon never spawns at all.
- **`command` is resolved on `PATH`** (or given as an absolute path) and each
  argument is a separate `args` element — nothing is passed through a shell.
- **The working directory is a temp dir**, so your provider does not auto-detect
  project config from the caller's cwd.
- **stdout is the protocol stream.** Write only JSON-RPC frames to it; send every
  log line to stderr, which is forwarded to the daemon's own stderr. A stray
  `print` to stdout corrupts the framing and surfaces as a handshake failure.

### http: streamable HTTP, with the entry's headers

An `http` provider is dialed over streamable HTTP at `url`, and **every request
carries the entry's `headers` verbatim** — the handshake, the tool listing and
the call alike. Nothing is spawned for it.

**The daemon composes no header of its own.** It attaches no credential, reads
none from its environment, and runs no OAuth flow: what your provider receives is
exactly what your entry says. An `Authorization` header is yours to write, with
`${VAR}` keeping the token out of the file.

## Limits and failure behavior

- **Collector traffic is uncapped, in both directions.** There is no size limit
  on the result your tool returns and none on the arguments the client sends,
  including the context block described above. This traffic runs between two MCP peers and
  reaches no model context, so the reason a size limit exists on a rendered
  surface does not apply to it. What sizes a collect is what your entry declares
  and what your walk reads, both of which are yours to choose. A collector served
  over HTTP to callers you do not control is yours to bound with your own
  middleware; the framework's own handler sets no limit.
- **The collect runs under the same rules a code collect does**: the same
  synchronous-wait cap before it detaches to finish in the background, the same
  in-flight gate (keyed on your registration name and the collect id), and the
  same pipeline wake, so the nodes you upload are summarized and embedded exactly
  as a built-in family's are. The post-collect enrichment tail is the one part
  that does not run for you — see below.
- **Every failure writes nothing.** A provider that will not start, a handshake
  that fails, a missing tool, a schema mismatch, a tool error result, a result
  that violates the schema your own tool advertised, a node with an empty type,
  an unknown field, a context declaration this client cannot satisfy — each
  returns an error and writes nothing. There is no partial collect.

### What the post-collect tail does not do for your family

After a collect of a registered family the client runs no post-collect enrichment
of its own: the cross-graph linker does not run, and
no built-in post-populate hook runs, whatever your family is named.
Those mechanisms are written against the built-in collectors' own node shapes,
so run against your graph they would either derive nothing or write into graphs
whose provider you are not.

Nothing else derives those edges for you, so it is worth being precise about what
your result can carry instead. A proxy node standing for a foreign resource is
expressible today, as is an ordinary edge from one of your nodes to it: both are
written with the node and edge fields you already use. An edge whose other
endpoint lives in another graph is expressible too: name the family in
`source_graph` or `target_graph`, and the client resolves that endpoint and
writes the edge into the linkage graph, as the edge-fields section above
describes.

What the tail does not do is DERIVE such an edge for you. Nothing infers one from
the ids you emit, so an edge that names neither field is an edge of your own
graph whatever its endpoints look like, and an id naming another graph's node
lands there as a dangling edge rather than a link out of it.

The pipeline wake still fires, so summarization and embedding are unaffected.

## A worked example, per transport

Install a stdio provider that exposes one tool, `collect_tickets`, declaring the
two schemas above:

```bash
knowledge collector add --tool collect_tickets \
  -e TICKET_API_BASE=https://tickets.example.com -e HOME=/home/you \
  tickets -- /usr/local/bin/ticket-mcp --serve
```

The command is written ABSOLUTE, for the reason at the top of this page: the dial
below runs in your shell and the collect runs in the daemon, and only one of them
has your `PATH`. `command -v ticket-mcp` prints the path to use.

That dials the provider, lists its tools, finds `collect_tickets` and checks its
schemas — so a typo in the tool name or a missing `outputSchema` is reported now
rather than at your first collect — and then writes:

```jsonc
// ~/.knowledge/collectors.json
{
  "collectors": {
    "tickets": {
      "type": "stdio",
      "command": "/usr/local/bin/ticket-mcp",
      "args": ["--serve"],
      "env": { "TICKET_API_BASE": "https://tickets.example.com", "HOME": "/home/you" },
      "tool": "collect_tickets"
    }
  }
}
```

A remote provider over HTTP, in the project scope so the repository carries it:

```bash
knowledge collector add -s project -t http --tool collect_logs \
  -H 'Authorization: Bearer ${LOKI_TOKEN:-}' \
  loki-remote https://collectors.example.com/loki
```

Then run a collection against either via [`collect`](collect.md):

```jsonc
collect({ "type": "tickets", "id": "acme", "params": { "repo": "acme/backend" } })
```

The provider is dialed, `collect_tickets` is called with
`{"id": "acme", "params": {"repo": "acme/backend"}}`, and the nodes and edges it
returns land in the `acme` graph of the `tickets` family — with your
`walk_complete` assertion recorded alongside them. A collect with no `id` is
refused: the id names the graph instance, so there is nothing to write into
without one.

## Reading the graph back

A registered custom graph is read with the same primitives every other family
uses — the registered name is the `graph` selector:

```jsonc
search({ "graph": "tickets", "name": "acme", "query": "login" })
query({ "graph": "tickets", "name": "acme", "id": "T-1" })
traverse({ "graph": "tickets", "name": "acme", "start": "T-1", "edge_types": ["blocks"] })
```

**Keyword search does not ride the behavior block.** `query` by id, `traverse`
and `search` all work on a collected custom graph with no block at all: a
registered family is admitted to the keyword index whatever its `embeddable`
setting says. What the block buys is the two LLM axes — summaries, and the
vectors that make search semantic rather than keyword-only:

```jsonc
"behavior": {
  "summarizable": true,
  "embeddable": true,
  "bm25_fields":      ["summary", "content"],
  "summarize_fields": ["summary", "content"],
  "embed_fields":     ["summary", "content"]
}
```

The three lists are optional even when an axis is on; leaving one out composes
from `symbol_name`, `summary`, `keywords`, `description` and `content`.
`bm25_fields` decides *which* text is indexed for keyword search, on the same
terms. Indexing is asynchronous — a search moments after a collect can still read
zero while the index catches up.

A node your collector already summarized is never sent to the summarizer, even
with `summarizable` on: opting in fills the nodes whose summary is empty and
leaves yours alone.
