// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// traverseArgs is the compile-local view of the `traverse` tool's wire shape,
// mirroring the server-side traverseArgs (tools_traverse.go:68).
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

// compileTraverse translates a reducible `traverse` call into a QueryPlan
// traversal. Returns ok=false (default-deny → legacy) for:
//   - graph=logs (the client intercept owns formatted traversal output)
//   - a start-less graph-wide-edges traverse (handleTraverseGraphWideEdges —
//     a distinct fast path, not a from_id walk)
//
// include_edge_metadata=true IS reducible: the engine re-walks the
// traversed edges and returns the per-edge metadata in
// ExecuteResponse.traversal_edges_json (the include_edge_metadata carrier); the
// client renders it.
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
		// Client canonicalizes the per-graph edge-type casing:
		// the engine now uses edge_types AS-GIVEN, so the client produces the
		// canonical casing (code/cloud/cicd/linkage/logs uppercase, else lowercase)
		// before it rides the wire.
		sel.EdgeTypes = canonicalEdgeCasings(a.Graph, a.EdgeTypes)
	}

	plan := &knowledgev1.QueryPlan{
		Selection:  sel,
		ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_TRAVERSAL,
	}

	// include_edge_metadata rides the carrier — the engine re-walks the traversed
	// edges and returns the per-edge metadata for the client to render.
	if a.IncludeEdgeMetadata {
		plan.IncludeEdgeMetadata = true
	}

	// forward tri-state from direction. Empty direction defaults to "out"
	// (validateDirection); "both" leaves Forward nil so the engine returns the
	// both-union.
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
		return nil, false // invalid direction → let legacy surface the error.
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
