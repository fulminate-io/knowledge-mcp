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
// to the fields the client path actually uses — the server's OnProgress was never
// consulted by the eval bodies and is dropped, and Force and DryRun are both
// retired: a recipe run writes nothing at all, so there is neither a destructive
// knob nor a write to skip.
type Options struct {
	// SourceManifest is the opaque context blob the collect layer builds,
	// encoding the source slug + recipe name as `source=<slug>;recipe=<name>`
	// (see FormatSourceManifest / ParseSourceManifest). RunRecipe parses it to
	// obtain the source slug (stamped into translated-from Evidence for lineage)
	// and the fixed inline recipe key.
	SourceManifest string

	// Extract turns a run into EXTRACT MODE: the emitted rows are captured onto
	// Result.Extract for the caller to read. It is the only mode an admitted run
	// has — nothing is ever written — and RunRecipe refuses a run without it.
	Extract bool

	// Body is an INLINE recipe body, used instead of loading a saved recipe by
	// name. Only meaningful in extract mode.
	Body string

	// MaxRows caps how many rows extract mode returns. Zero or negative selects
	// DefaultExtractMaxRows — never "no limit", because an unbounded extract is
	// exactly what the bounded-output rule forbids.
	//
	// There is deliberately NO MaxBytes here. The byte cap can only be applied
	// where rendered sizes are known, which is the renderer in the tools layer;
	// a MaxBytes field on this struct would be declared and never read, so a
	// direct caller setting it would be silently ignored while
	// Result.Extract.Truncated reported on the row cap alone.
	MaxRows int

	// Offset is the zero-based index of the first MATCHED row returned: rows
	// [Offset, Offset+MaxRows) are captured, and EVERY matched row is still
	// counted whether or not it is captured. That is what lets page three
	// report the full population behind it, so a caller can tell a cursor
	// overshoot from an empty match.
	//
	// A NEGATIVE VALUE IS AN ERROR, not a clamp — see effectiveOffset.
	Offset int
}

// Result carries the outputs of a recipe run. Every run accumulates its
// emissions into Nodes / Edges / Lineage in memory and ships them NOWHERE: the
// rows the caller reads come back on Extract. This is the
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

	// Extract carries the captured rows of an EXTRACT-mode run, and is nil on
	// every other run. Nodes/Edges/Lineage above accumulate exactly as they
	// always have, including in extract mode — only this caller-facing row list
	// is bounded.
	Extract *ExtractResult
}

// Stats is the counter block rendered into the MCP collect response. New counter
// fields are additive — consumers tolerate unknown fields. Client-side
// counterpart of the former server transformer.Stats.
type Stats struct {
	// NodesEmitted is the total target-graph node count emitted.
	NodesEmitted int
	// SkippedChunks counts rows skipped for lacking an identity signal.
	SkippedChunks int
	// SkippedExisting counts emitted nodes the write guard dropped because the
	// target already holds a byte-identical row under the same id. It is a
	// SILENT, SUCCESSFUL outcome — distinct from a collision, which refuses the
	// whole write — and it exists so a re-run that legitimately wrote nothing is
	// distinguishable in the response from one that emitted nothing at all.
	SkippedExisting int
	// ElapsedMillis is wall-clock duration of the RunRecipe call.
	ElapsedMillis int64
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
