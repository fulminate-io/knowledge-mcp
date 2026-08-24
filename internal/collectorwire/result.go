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
}
