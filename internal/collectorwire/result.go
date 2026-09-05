// SPDX-License-Identifier: Apache-2.0

// Package collectorwire holds CollectResult, the client-internal aggregate
// of a collection run's output. It is a client-only leaf: the collector tree
// builds a CollectResult, then the client serializes its pieces onto the wire
// (node chunks + proto edges + scalars). The server reassembles those pieces
// from the collect chunk/finalize wire — CollectResult itself no
// longer crosses to the server as a type.
package collectorwire

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// CollectResult holds the output of a collection run.
//
// PostPopulate used to live here as a per-result closure. It now lives in
// cmd/knowledge/internal/postpopulate keyed by collector name so it can cross the wire (the
// Sink's server-side receiver looks it up and invokes it against the
// local DB). The GraphCode overlay-diff equality predicate is hardcoded
// inline by the client sink — see
// cmd/knowledge/internal/collector/local.writeOverlay.
type CollectResult struct {
	GraphType kgtypes.GraphType
	GraphName string
	// Nodes is []*knowledgev1.Node — the typed wire node the client builds and
	// serializes onto the wire (the store.Node wrapper was dropped from the
	// client build path). Pointer elements: knowledgev1.Node carries a noCopy, so a
	// value slice would make collector appends + the wire-send range copylocks
	// violations.
	Nodes []*knowledgev1.Node
	Edges []kgwire.BatchEdge
	// CurrentBranch is the git branch the collector ran against. The
	// server (Sink.WriteResult) decides overlay-vs-full-replace by
	// comparing against the existing graph's recorded default.
	CurrentBranch string
	// Promote, when true (code-only), tells the server to land this collect in
	// the base graph regardless of the recorded default branch and to overwrite
	// the recorded default branch to CurrentBranch. Threaded onto
	// CollectChunkRequest + FinalizeRequest by the sink.
	Promote bool
	// SyncCommit is the git HEAD SHA the collector ran against; SyncTime is
	// the collection wall-clock as unix nanos. The server persists both onto
	// code-graph metadata (SyncCommitKey / SyncTimeKey) so a later catalog
	// read can compute commits-behind + last-collected-when. Empty/zero for
	// a non-git collection.
	SyncCommit string `json:"sync_commit,omitempty"`
	SyncTime   int64  `json:"sync_time,omitempty"`
	// ModulePath is the Go module path declared in <rootDir>/go.mod
	// Empty for non-Go repos. Persisted to graph
	// metadata under kgtypes.ModulePathKey so pkg/topology/dsm.go can read
	// it server-side without opening go.mod on the server pod.
	ModulePath string `json:"module_path,omitempty"`
	// LayerConfig is the raw YAML body of `.knowledge/topology_layers.yaml`
	// when the file exists. Empty otherwise. Persisted to graph metadata
	// under kgtypes.LayerConfigKey so ConfigFileProvider can parse it
	// server-side without filesystem access.
	LayerConfig string `json:"layer_config,omitempty"`
	// WalkComplete reports that discovery AND chunking read every file they set
	// out to read — no unreadable file, no truncated walk.
	//
	// IT IS ONE SIGNAL WITH TWO CONSUMERS, deliberately: it drives the client's
	// incomplete-walk fallback AND rides the wire as FinalizeRequest.walk_complete,
	// where it is the server's second deletion guard. Wiring them from one source
	// is what keeps a read-failed file from being named as a deletion; the deletion
	// set arithmetic does not do that alone.
	//
	// THE ZERO VALUE IS THE SAFE ONE. A collector that does not set it reports an
	// incomplete walk, which disables the diff and the deletion phase rather than
	// enabling them on an unverified premise.
	WalkComplete bool `json:"walk_complete,omitempty"`
	// DiscoveryFingerprint is a canonical digest of the discovery CONFIGURATION
	// this result was produced under. Empty for collectors that do no discovery.
	//
	// IT IS A FIELD RATHER THAN A LOCAL BECAUSE NOTHING DOWNSTREAM CAN DERIVE IT.
	// The test that keeps this from eroding the no-widening rule — apply it to any
	// future proposal — is: CAN THE SINK COMPUTE IT FROM WHAT IT ALREADY HOLDS?
	// The per-file contribution hash can, which is exactly why that one stays a
	// local in the sink's frame. This cannot: the configuration is decided three
	// packages upstream in the discovery pass, and a file scoped out of discovery
	// leaves NO trace in the result at all. There is nothing to derive from.
	//
	// WHAT IT PREVENTS: a collect scoped by package prefixes emits nothing for the
	// out-of-scope files, so those paths would be named as deletions with every
	// guard admitting them — the walk was complete, the ratio is ordinary, and
	// each named path really does have a live node. Comparing this value against
	// the previous collect's is what refuses that.
	//
	// CLIENT-INTERNAL: this struct is never marshaled and the server module never
	// imports this package, so the field cannot reach the proto wire unless
	// someone writes it into a request literal by hand.
	DiscoveryFingerprint string `json:"discovery_fingerprint,omitempty"`
	// CollectorOutputVersion is the identity of the collector build that produced
	// this result (parser.CollectorOutputVersion). It answers the one question no
	// per-file diff can: whether THIS collector would emit different rows for a
	// file whose content never changed. The emitted values outside the per-file
	// contribution hash — node Id, Summary, Keywords, metadata — move without
	// moving any hash, so a diff-mode collect is blind to them by construction.
	//
	// IT PASSES THE SAME NO-WIDENING TEST DiscoveryFingerprint DOES: the sink
	// cannot compute it from what it holds. The sink sees rows, and the whole
	// point is that the rows look identical either way; only the producer knows
	// which collector made them.
	//
	// ZERO MEANS UNSTAMPED, NOT UNCHANGED. A producer that leaves it zero is
	// refused loudly on the diff path rather than read as "same as last time",
	// which would silently disable the mechanism for that collector.
	//
	// CLIENT-INTERNAL, exactly as above: this struct is never marshaled and the
	// server module never imports this package.
	CollectorOutputVersion uint32 `json:"collector_output_version,omitempty"`
	// NonSubstantiveNodes is how many of Nodes carry a node TYPE that a
	// composition invariant counts as content while being, in fact, retained
	// chrome. The producing collector counts them; NewCollectComposition
	// copies the figure verbatim and the producing collector's own invariant
	// subtracts it.
	//
	// IT IS A COUNT RATHER THAN A PREDICATE BECAUSE THE KNOWLEDGE IS THE
	// COLLECTOR'S. The web collector retains a navigation strip as a node
	// carrying the `paragraph` type — the graph keeps its node vocabulary —
	// so a nav-only page would otherwise read as a substantive harvest and the
	// captured-only-chrome leg would stop firing. Only the web collector knows
	// which of its paragraph nodes are strips; carrying a count keeps that
	// knowledge on its own side of the seam instead of teaching
	// collector-generic code to read a web metadata key.
	//
	// ZERO IS THE CORRECT DEFAULT for every collector that has no such class:
	// subtracting nothing leaves the invariant exactly as it was.
	NonSubstantiveNodes int `json:"non_substantive_nodes,omitempty"`
	// Degraded is the per-class census of work this collection run DROPPED:
	// class name to count. A class with no occurrences is ABSENT rather than
	// zero, so an empty map means a clean run and renders nothing.
	//
	// CLASSES, NEVER URLS. A census keyed by the thing that failed grows with
	// the corpus and turns a collect response into a log; a fixed class
	// vocabulary answers "what did this harvest lose, and how much" in one
	// line whatever the crawl's size. The vocabulary belongs to the producing
	// collector, which is the only layer that knows what its lanes are.
	Degraded map[string]int `json:"degraded,omitempty"`
	// GithubFollowUps is how many DISTINCT units of github follow-up work —
	// a repository AT A REF, which is the unit the materializer itself works
	// in — this harvest met and did not materialize, and GithubFollowUpSample
	// is a bounded sample of them for the rendered response. Two refs of one
	// repository are two units; a URL naming no ref asks for the default
	// branch and is covered by any ref of that repository.
	//
	// THEY ARE NOT A DEGRADE. Under the user's ruling — "github links are
	// things the user would decide to follow up on, its not a failure at all"
	// — an unmaterialized repository is neither a failure nor dropped work; it
	// is a decision the caller gets to make. It therefore rides its own pair
	// of fields rather than a degrade class, and nothing about it makes a
	// harvest unsuccessful.
	//
	// The COUNT is exact and the SAMPLE is capped, so the response stays one
	// line on a crawl that met a thousand repositories while still naming
	// enough of them to act on.
	GithubFollowUps      int      `json:"github_follow_ups,omitempty"`
	GithubFollowUpSample []string `json:"github_follow_up_sample,omitempty"`
}
