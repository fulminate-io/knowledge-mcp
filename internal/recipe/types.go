// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// TargetSpec identifies the graph a run's emitted ids are hashed under, and — on
// a LANDING run — the graph its nodes are written into. A run targets exactly one
// (GraphType, Name) pair, supplied by the caller: an extract run gets the extract
// sentinel, because its ids never reach storage, and a landing run gets the real
// combined-practice key, because the collision read that decides base-versus-twin
// looks those ids up in that graph and the two must be the same key.
type TargetSpec struct {
	// GraphType is the target domain graph type (typically kgtypes.GraphPractice).
	// Never a source-only graph type (raw web / pdf / logs).
	GraphType kgtypes.GraphType
	// Name is the per-type graph name (e.g. "design-patterns" for
	// practice/design-patterns).
	Name string
}

// Options carries the per-invocation knobs RunRecipe and the interpreter honor.
//
// Force and DryRun are both retired and are NOT coming back under the landing:
// force meant "overwrite a colliding resident", and a landing never overwrites —
// a collision lands a versioned twin and both rows are retained — while dry_run
// meant "compute the projection but skip the write", which is what an EXTRACT run
// already is. Neither names a distinction this surface still has.
type Options struct {
	// SourceManifest is the opaque context blob the collect layer builds,
	// encoding the source slug + recipe name as `source=<slug>;recipe=<name>`
	// (see FormatSourceManifest / ParseSourceManifest). RunRecipe parses it to
	// obtain the source slug — the second component of every emitted StableID,
	// and the default value of each emitted node's `source` field — plus the
	// fixed inline recipe key.
	SourceManifest string

	// Extract turns a run into EXTRACT MODE: the emitted rows are captured onto
	// Result.Extract for the caller to read and nothing is written.
	Extract bool

	// LandTarget names the graph a LANDING run writes into, and its emptiness IS
	// the mode discriminant: a zero TargetSpec is an extract-only run and a
	// non-zero one is a landing.
	//
	// ONE FIELD RATHER THAN A BOOL PLUS A TARGET, because two fields can disagree.
	// A `Land bool` set with no target, or a target set with the bool clear, is a
	// state the caller can reach and neither the interpreter nor the landing could
	// act on; there is no such state here.
	//
	// IT IS THE CALLER'S TO SUPPLY. The recipe package knows nothing about which
	// graph a practice landing belongs in, and hard-coding one here would put the
	// combined graph's name in a second place that could drift from the first. The
	// collect layer resolves it once and passes it.
	//
	// An admitted run has at least one of Extract and LandTarget set; RunRecipe
	// refuses one with neither, naming both.
	LandTarget TargetSpec

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
// emissions into Nodes / Edges in memory and writes nothing itself; the rows an
// extract reads come back on Extract, and a landing composes its create_batch
// from Nodes and Edges. Retyped onto the wire node (*knowledgev1.Node) and the
// client edge build-carrier (kgwire.BatchEdge).
type Result struct {
	// Nodes is the list of target-graph nodes the run emitted. Order is
	// emission order. Pointer elements: knowledgev1.Node carries a noCopy.
	Nodes []*knowledgev1.Node

	// Edges is the list of target-graph structural edges between the Nodes, from
	// the body's `link` rules. It is the ONLY edge list a run produces: the
	// translated-from edges back into the raw source graph are retired, so there
	// is no second carrier and nothing for a ship-side filter to strip.
	Edges []kgwire.BatchEdge

	// Stats holds the per-run counters surfaced to the MCP collect response.
	Stats Stats

	// Extract carries the captured rows of an EXTRACT-mode run, and is nil on
	// every other run. Nodes and Edges above accumulate exactly as they always
	// have, including in extract mode — only this caller-facing row list is
	// bounded, which is why a landing refuses the row-window params rather than
	// writing more than it shows.
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

// Landing reports whether these options describe a LANDING run — one whose
// emitted nodes are written into a target graph — as opposed to an extract-only
// run that returns rows and writes nothing.
//
// It reads the target rather than a separate flag so there is exactly one place
// the mode is decided and no pair of fields that can disagree about it.
func (o Options) Landing() bool {
	return o.LandTarget.GraphType != ""
}
