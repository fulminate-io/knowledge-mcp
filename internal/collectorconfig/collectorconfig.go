// SPDX-License-Identifier: Apache-2.0

// Package collectorconfig owns the custom-collector CONFIG FILE: the two scoped
// `collectors.json` files that ARE the registration record for a custom
// collector, their strict loader, and the runtime and persisted records built
// from one entry.
//
// THE FILE IS THE REGISTRATION RECORD. There is no registration tool call and no
// server-side connection record: an entry present in a file is a registered
// graph family, and an entry removed from a file is gone at the next lookup. The
// shape mirrors Claude's MCP server config (a `collectors` object of named
// entries, each `stdio` with command/args/env or `http` with url/headers) with
// one deliberate addition, `tool`, naming the single MCP tool the daemon calls
// to collect.
//
// TWO RECORDS COME OUT OF ONE ENTRY, AND THEY ARE NOT THE SAME OBJECT.
//
//   - The RUNTIME record (Runtime) is built in memory for one collect and never
//     crosses the wire: the provider to dial, the tool to call, the child's whole
//     environment, and the http headers. It carries the operator's secrets.
//   - The PERSISTED record (Persisted) is upserted to the server and carries the
//     family NAME, its BEHAVIOR and its declared TYPE VOCABULARY — never a
//     CollectorSpec, so no env value and no header value is ever stored in a
//     graph-resident node. The server reads two things from it: the behavior
//     cascade, which its routing gate and its whole summarize / embed / sync
//     pipeline key on; and the vocabulary, which its ingest handler refuses an
//     undeclared node or edge type against. The env DECLARATION stays on the
//     entry: no server code reads what a collector reads.
//
// EVERY REFUSAL NAMES THE FILE, THE ENTRY AND THE FIELD, in that order, and
// nothing is skipped, defaulted or coerced — an entry this package cannot read
// is an error, never an entry it quietly leaves out. The ONE default is the
// absent behavior block (see Behavior), which is a recorded decision rather than
// a coercion. A DECLARED block is held to the same standard as every other
// field: a field list that is empty, blank or duplicated is refused at load, not
// quietly read as "nobody declared one".
package collectorconfig

import "github.com/fulminate-io/knowledge-mcp/internal/externalcollector"

// The two transports an entry may name. They are the JSON `type` values, and an
// entry naming anything else is refused rather than defaulted.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// Scope names which of the two files an entry came from. PROJECT beats USER, and
// the winning entry is taken WHOLE — no field is merged across scopes, which is
// the rule Claude's own MCP config states for its scopes.
type Scope string

const (
	// ScopeUser is ~/.knowledge/collectors.json — the machine's operator.
	ScopeUser Scope = "user"
	// ScopeProject is <repo root>/.knowledge/collectors.json.
	ScopeProject Scope = "project"
)

// FileName is the basename of both scoped files. The directory differs; the name
// does not, so an operator moving an entry between scopes moves the same text.
const FileName = "collectors.json"

// ConfigDirName is the per-repository config directory the project-scope file
// lives in, matching <root>/.knowledge/topology_layers.yaml.
const ConfigDirName = ".knowledge"

// File is the whole config file: one object keyed `collectors`, exactly as
// Claude's config is one object keyed `mcpServers`.
type File struct {
	Collectors map[string]Entry `json:"collectors"`
}

// Entry is one registered collector. The field set mirrors Claude's MCP server
// entry plus `tool`; `behavior` and `node_types` mirror the graph-type record's
// own two behavior messages one for one, so `node_types` is a SIBLING of
// `behavior` here exactly as it is a sibling there.
type Entry struct {
	Type      string                      `json:"type"`
	Command   string                      `json:"command,omitempty"`
	Args      []string                    `json:"args,omitempty"`
	Env       map[string]string           `json:"env,omitempty"`
	URL       string                      `json:"url,omitempty"`
	Headers   map[string]string           `json:"headers,omitempty"`
	Tool      string                      `json:"tool"`
	Behavior  *Behavior                   `json:"behavior,omitempty"`
	NodeTypes map[string]NodeTypeOverride `json:"node_types,omitempty"`
	// Context declares the FOREIGN-GRAPH CONTEXT this collector needs — the
	// cloud resources or code-graph nodes it computes its own cross-graph
	// answers from. It is a SIBLING of behavior and node_types here because it
	// is a third declared-only fact about the family, and it is the one of the
	// three that never reaches the server: the client fills the collect input's
	// context block from it and Persisted carries no trace of it.
	//
	// The type is the CONTRACT's, not this package's, so the family names and
	// the field names have one source. Do not confuse its nested node_types with
	// the NodeTypes field above: they are different objects two levels apart and
	// the JSON does not collide.
	Context externalcollector.ContextDeclaration `json:"context,omitempty"`
	// Vocabulary is the CLOSED node and edge type vocabulary this collector's
	// describe tool declared. It is the one half of the entry the SERVER reads
	// for something other than the behavior cascade: it rides the registration
	// record, and a collect carrying a type outside it is refused at ingest by
	// name.
	//
	// IT IS A POINTER, AND THE PRESENCE IS THE WHOLE POINT. An absent vocabulary
	// is a family registered before describe existed: it keeps today's accept-all
	// behavior and the collect says so once. A PRESENT vocabulary declaring empty
	// lists is a different statement — this collector emits nothing — and every
	// node it then sends is refused. A non-pointer slice pair would collapse the
	// two into one shape and silently widen every old registration.
	//
	// Its nested node_types is the VOCABULARY and not the override map above:
	// two objects two levels apart, as with Context.
	Vocabulary *Vocabulary `json:"vocabulary,omitempty"`
	// EnvDeclaration is the environment variables the collector READS, each with
	// its class, as its describe tool declared them. It is a declaration of
	// NAMES and it is ENTRY-ONLY: the persisted record carries what the server's
	// cascade consumes, and no server code reads what a collector reads.
	//
	// DO NOT CONFUSE IT WITH Env ABOVE, which is the child's whole environment
	// and carries VALUES. This one is what the names MEAN — which of them is a
	// path, which selects a thing, and which is a credential that must never be
	// written into a config file in any state.
	EnvDeclaration []externalcollector.DeclaredEnv `json:"env_declaration,omitempty"`
}

// Vocabulary is one collector's closed type vocabulary.
//
// BOTH HALVES TRAVEL TOGETHER because the describe contract requires a collector
// to declare both: a collector that emits no edges declares an empty edge list,
// which is a statement, while a missing one would be a silence the ingest
// refusal cannot act on.
type Vocabulary struct {
	// NodeTypes is every node type the collector emits.
	NodeTypes []string `json:"node_types"`
	// EdgeTypes is every edge type it emits.
	EdgeTypes []string `json:"edge_types"`
}

// Behavior is the graph-level behavior declaration the loader forwards to the
// server. The three booleans are POINTERS so an omitted key stays distinguishable
// from an explicit false.
//
// THE LOADER WRITES ALL THREE EXPLICITLY, AND THE THREE DO NOT SHARE A DEFAULT.
// syncable defaults TRUE; summarizable and embeddable default FALSE. An explicit
// value in the entry is honored exactly as written in every case.
//
// THE TWO LLM AXES ARE OPT-IN PER COLLECTOR, which is a decision recorded here
// rather than an inference. Summarizing and embedding a family's nodes is LLM
// spend on data this client did not produce, and whether it is worth paying is a
// property of the particular collector, not something a loader can settle for
// every collector anyone installs. So a family is summarized or embedded because
// its entry asked to be.
//
// WHAT AN OPTED-OUT FAMILY LOSES, stated because an earlier version of this
// comment argued the opposite. It was once true that a family reaching the server
// with these unset was collected, readable by id and walkable, and never entered
// the text index — a graph nobody could search, which is the outcome nobody
// installs a collector for. That is no longer so: a REGISTERED family is admitted
// to the keyword corpus regardless of its embed setting, so a collector installed
// with no flags is text-searchable from its first collect. What it gives up is
// summaries and vectors, which is semantic ranking rather than findability, and
// an operator who wants those declares the axis.
//
// WHY THEY ARE STILL WRITTEN RATHER THAN LEFT UNSET. The server's cascade
// coalesces an unset boolean to false, so leaving them out would reach the same
// behavior — but an operator reading `collector get` or the graph-type catalog
// would see a blank where a decision belongs, and could not tell "this family
// opted out" from "nobody said". Presence is what carries that distinction, which
// is why these are pointers.
type Behavior struct {
	Syncable        *bool             `json:"syncable,omitempty"`
	Summarizable    *bool             `json:"summarizable,omitempty"`
	Embeddable      *bool             `json:"embeddable,omitempty"`
	EmbedFields     []string          `json:"embed_fields,omitempty"`
	SummarizeFields []string          `json:"summarize_fields,omitempty"`
	Bm25Fields      []string          `json:"bm25_fields,omitempty"`
	Extra           map[string]string `json:"extra,omitempty"`
}

// NodeTypeOverride is the per-node-type half of the cascade. Unset means inherit
// the graph default, which is the server's own rule for these two.
type NodeTypeOverride struct {
	Summarizable    *bool    `json:"summarizable,omitempty"`
	Embeddable      *bool    `json:"embeddable,omitempty"`
	EmbedFields     []string `json:"embed_fields,omitempty"`
	SummarizeFields []string `json:"summarize_fields,omitempty"`
	Bm25Fields      []string `json:"bm25_fields,omitempty"`
}

// ScopedEntry is one entry with the scope and file it was read from, which is
// what `knowledge collector list` renders and what a refusal names.
type ScopedEntry struct {
	Name  string
	Scope Scope
	Path  string
	Entry Entry
	// Unresolved is THIS entry's own `${VAR}` refusal, carried rather than
	// returned as the file's error, and nil for an entry that resolved. The
	// message names the file, the entry, the field and the variable.
	//
	// A CALLER THAT USES THE ENTRY MUST READ IT FIRST. Entry holds the
	// UNEXPANDED text when this is set, so spawning from it would hand a child
	// the literal `${TOKEN}` as its credential. The collect path refuses by name
	// on it; `collector list` and `collector get` render the entry and SAY that
	// its environment does not resolve, because a read-only report of what the
	// file says is exactly what an operator diagnosing the refusal is after.
	Unresolved error
}
