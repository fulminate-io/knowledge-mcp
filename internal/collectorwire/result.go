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
}
