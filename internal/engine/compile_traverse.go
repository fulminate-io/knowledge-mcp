// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// traverseArgs is the compile-local view of the `traverse` tool's wire shape.
// The published schema (tools/traverse_schema.go) is what it mirrors — there is
// no server-side twin to track: the server holds no traverse tool at all, only
// MergeBothTraversals for the engine's both-direction walk, and the client owns
// traverse rendering end to end.
type traverseArgs struct {
	Start               string   `json:"start"`
	Direction           string   `json:"direction"`
	Depth               int      `json:"depth"`
	Limit               int      `json:"limit"`
	EdgeTypes           []string `json:"edge_types"`
	Graph               string   `json:"graph"`
	Name                string   `json:"name"`
	Language            string   `json:"language"`
	Account             string   `json:"account"`
	Repo                string   `json:"repo"`
	Branch              string   `json:"branch"`
	IncludeEdgeMetadata bool     `json:"include_edge_metadata"`
	IncludeTombstones   bool     `json:"include_tombstones"`

	// Format is render-only (Compile ignores it); Render reads it for text/json.
	Format string `json:"format"`
}

// traverseDirections is the direction vocabulary the traverse tool PUBLISHES
// (traverse_schema.go's Enum{out,in,both}), and the SINGLE declaration of it on
// this side: both the membership test and the refusal message read this slice,
// so there is no second copy to drift from. Rendered in the schema's own order —
// out first, because it is the documented default — so the refusal message and
// the tool description name the values the same way round.
//
// A linear scan over three elements is the membership test: a set would be a
// second declaration of the same three tokens to keep in step, which is the
// trade the projection vocabulary makes for eighteen keys and this does not need.
var traverseDirections = []string{"out", "in", "both"}

// traverseDirectionList is the rendered accepted-values list, built ONCE at
// package scope so the refusal path allocates only its formatted message.
var traverseDirectionList = strings.Join(traverseDirections, ", ")

// ValidateTraverseDirection refuses a direction outside the published
// vocabulary, naming the offending value AND the accepted values — the
// bad-input rule: a rejected value is never coerced, defaulted or degraded, and
// the caller is told what would have worked.
//
// An EMPTY direction is accepted: the tool documents it as the "out" default,
// and compileTraverse's switch applies that default. The normalization here is
// the SAME lowercase-of-trimmed form that switch uses, and the two must stay
// that way — a value this accepts and that switch then fails to recognize would
// fall into its default arm and be denied generically, which is the exact defect
// this validator exists to remove.
//
// EXPORTED because the direction vocabulary is a tool-surface contract rather
// than a compile-local detail: a caller outside this package that wants to
// refuse a bad direction the same way should call this rather than restate the
// set.
func ValidateTraverseDirection(direction string) error {
	d := strings.ToLower(strings.TrimSpace(direction))
	if d == "" || slices.Contains(traverseDirections, d) {
		return nil
	}
	return validationError(fmt.Sprintf(
		"traverse: unknown direction %q. Accepted values: %s — an omitted or empty direction defaults to %q",
		direction, traverseDirectionList, "out"))
}

// precheckTraverse runs the traverse validation that must happen BEFORE any
// dispatch decision. It is invoked by Dispatch's traverse branch (a NAMED
// special-shape seam, the twin of precheckQuery): a non-nil error is rendered as
// an explicit validation-error result and NO Execute RPC is issued.
//
// It runs AHEAD of dispatchGraphWideEdges deliberately. That arm serves a
// start-less traverse without consulting direction at all and echoes the
// caller's value back in its JSON payload, so validating after it would leave
// the start-less shape accepting "sideways" and reporting it as the direction it
// walked.
//
// A payload that does not parse yields no gate here: Compile re-parses and
// returns ok=false, so the deny path surfaces the malformed call. Mirrors
// precheckQuery's disposition for the same reason.
func precheckTraverse(args json.RawMessage) error {
	var a traverseArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil //nolint:nilerr // malformed JSON is not a validation failure — let Compile (which re-parses) return ok=false so the deny path surfaces the parse error
	}
	return ValidateTraverseDirection(a.Direction)
}

// compileTraverse translates a reducible `traverse` call into a QueryPlan
// traversal. Returns ok=false — the explicit DENY, there is no legacy
// fallback — for:
//   - graph=logs (the client intercept owns formatted traversal output)
//   - a start-less graph-wide-edges traverse (dispatchGraphWideEdges claims it
//     in the Dispatch seam — a distinct two-step fast path, not a from_id walk)
//
// include_edge_metadata=true IS reducible: the engine re-walks the
// traversed edges and returns the per-edge metadata in
// ExecuteResponse.traversal_edges_json (the include_edge_metadata carrier); the
// client renders it.
//
// The carrier is requested for a SECOND, INDEPENDENT reason on the code graph:
// a code-graph walk needs the per-edge Method to tell a multi-candidate edge
// group from N bound edges, so the field is set there even when the caller did
// not ask for it. Do not "simplify" the condition back to the caller's
// parameter alone.
//
// Direction maps to the forward tri-state: out→true, in→false, both→nil (the
// engine computes the forward+backward union with min-distance dedup
// server-side — the client must NOT re-derive it). EdgeTypes are canonicalized
// per-graph CLIENT-SIDE (the engine uses them as-given). One
// plan per call.
func compileTraverse(args json.RawMessage) (*knowledgev1.ExecuteRequest, bool) {
	var a traverseArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, false
	}

	if a.Graph == "logs" {
		return nil, false // log traversal is rendered by the client intercept.
	}
	if a.Start == "" {
		return nil, false // graph-wide-edges fast path, not a from_id walk.
	}

	sel := &knowledgev1.Selection{FromId: []string{a.Start}}
	if len(a.EdgeTypes) > 0 {
		// Verbatim pass-through. The spellings arriving here were already
		// RESOLVED against this graph's own edge vocabulary in the Dispatch seam
		// (edge_type_resolve.go), and the engine uses edge_types AS-GIVEN, so any
		// further transformation here would undo that resolution.
		sel.EdgeTypes = a.EdgeTypes
	}

	plan := &knowledgev1.QueryPlan{
		Selection:  sel,
		ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL,
	}

	// include_edge_metadata rides the carrier — the engine re-walks the traversed
	// edges and returns the per-edge metadata for the client to render.
	//
	// A CODE-GRAPH walk sets it whether or not the caller asked, for the second
	// reason named on compileTraverse: without the per-edge Method the client
	// cannot tell a multi-candidate group from N bound edges. This is a
	// code-graph rule and must not be generalised to every graph to look
	// symmetric — groups are emitted only by the code collector, so widening it
	// would add a server-side edge re-walk to every knowledge, cloud and practice
	// traversal in exchange for a group that cannot exist there.
	if a.IncludeEdgeMetadata || isCodeGraph(a.Graph) {
		plan.IncludeEdgeMetadata = true
	}

	// forward tri-state from direction. Empty direction defaults to "out" (the
	// default ValidateTraverseDirection documents and admits); "both" leaves
	// Forward nil so the engine returns the both-union.
	switch strings.ToLower(strings.TrimSpace(a.Direction)) {
	case "", "out":
		t := true
		plan.Forward = &t
	case "in":
		f := false
		plan.Forward = &f
	case "both":
		// leave Forward nil → engine both-union.
	default:
		// Unreachable from the LLM surface: precheckTraverse refuses an unknown
		// direction in the Dispatch seam, before Compile is reached. It stays as
		// the compile-side default-deny for the OTHER Compile caller — thought/
		// wire.go's fetchTraversalPeerIDs, which builds the arg map itself — so a
		// programmatic caller that passes an unrecognized literal is denied rather
		// than silently walked in the "out" default direction.
		return nil, false
	}

	// MaxHops from depth only when supplied — the engine applies the store
	// default (1) when MaxHops==0 (no client-side default-injection).
	if a.Depth > 0 {
		plan.MaxHops = int32(a.Depth)
	}
	if a.Limit > 0 {
		plan.Limit = int32(a.Limit)
	}
	if a.IncludeTombstones {
		plan.IncludeTombstones = true
	}

	req := &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
		Target: buildTarget(a.Graph, a.Repo, a.Account, a.Name, a.Language, a.Branch),
	}
	return req, true
}
