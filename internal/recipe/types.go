// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// TargetSpec identifies the graph a recipe run writes into. A recipe targets
// exactly one (GraphType, Name) pair, resolved from the recipe node's
// target_graph_type + target_name metadata. The client-side counterpart of the
// former server transformer.TargetSpec.
type TargetSpec struct {
	// GraphType is the target domain graph type (typically kgtypes.GraphPractice).
	// Never a source-only graph type (raw web / pdf / logs).
	GraphType kgtypes.GraphType
	// Name is the per-type graph name (e.g. "design-patterns" for
	// practice/design-patterns).
	Name string
}

// Options carries the per-invocation knobs RunRecipe and the interpreter honor.
// The client-side counterpart of the former server transformer.Options, narrowed
// to the fields the client path actually uses (SourceManifest, Force, DryRun) —
// the server's OnProgress was never consulted by the eval bodies and is dropped.
type Options struct {
	// SourceManifest is the opaque context blob the collect layer builds,
	// encoding the source slug + recipe name as `source=<slug>;recipe=<name>`
	// (see FormatSourceManifest / ParseSourceManifest). RunRecipe parses it to
	// obtain the source slug (used for translated-from Evidence + Force scoping)
	// and the recipe name to load.
	SourceManifest string
	// Force requests per-source overwrite: when true, RunRecipe deletes prior
	// nodes in the target graph that carry a translated-from edge whose
	// Evidence.source matches the current run's source slug BEFORE emitting new
	// ones. Force=false is idempotent (same inputs → same deterministic IDs →
	// no duplicates).
	Force bool
	// DryRun, when true, instructs the interpreter to compute its Result without
	// writing anything: RunRecipe builds the Result but skips the Sink write.
	DryRun bool
}

// Result carries the outputs of a recipe run. On the client EVERY run (DryRun or
// not) accumulates its emissions into Nodes / Edges / Lineage in memory; RunRecipe
// then ships them through the collector Sink on a non-DryRun run. This is the
// client-side counterpart of the former server transformer.Result, retyped onto
// the wire node (*knowledgev1.Node) and the client edge build-carrier
// (kgwire.BatchEdge).
type Result struct {
	// Nodes is the list of target-graph nodes the run emitted. Order is
	// emission order. Pointer elements: knowledgev1.Node carries a noCopy.
	Nodes []*knowledgev1.Node

	// Edges is the list of target-graph structural edges between the Nodes
	// (from `link` rules). Does NOT include the Lineage translated-from edges.
	Edges []kgwire.BatchEdge

	// Lineage is the list of translated-from edges pointing from the
	// newly-emitted nodes back to their source nodes in the source graph. Each
	// edge carries Evidence JSON with source=<slug>.
	Lineage []kgwire.BatchEdge

	// Stats holds the per-run counters surfaced to the MCP collect response.
	Stats Stats
}

// Stats is the counter block rendered into the MCP collect response. New counter
// fields are additive — consumers tolerate unknown fields. Client-side
// counterpart of the former server transformer.Stats.
type Stats struct {
	// NodesEmitted is the total target-graph node count emitted.
	NodesEmitted int
	// SkippedChunks counts rows skipped for lacking an identity signal.
	SkippedChunks int
	// ElapsedMillis is wall-clock duration of the RunRecipe call.
	ElapsedMillis int64
	// ForceDeleted is the number of prior target-graph nodes removed by
	// Force=true before any new emission wrote. Zero when Force=false.
	ForceDeleted int
	// LookupsResolved counts `lookup` rule invocations that found a matching
	// node already emitted earlier in THIS run.
	LookupsResolved int
	// LookupMisses counts `lookup` rule invocations whose computed StableID was
	// not emitted earlier in this run.
	LookupMisses int
	// LinkMisses counts `link` rule invocations skipped because either endpoint
	// was empty (unbound $var) or not emitted earlier in this run.
	LinkMisses int
}
