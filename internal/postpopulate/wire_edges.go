// SPDX-License-Identifier: Apache-2.0

package postpopulate

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphsel"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// OutgoingEdges / IncomingEdges re-export the shared kgwire.EdgeDirection enum
// values BrowseEdges selects on. The former client-LOCAL 2-value EdgeDirection
// folded onto the canonical 3-value kgwire.EdgeDirection (kgwire/edgedirection.go);
// these aliases keep postpopulate.OutgoingEdges / postpopulate.IncomingEdges
// resolving for the collector PostPopulate callers (cloud/aws|gcp|azure) that
// reference them by package-qualified name.
const (
	// OutgoingEdges returns edges whose source is the queried node.
	OutgoingEdges = kgwire.OutgoingEdges
	// IncomingEdges returns edges whose target is the queried node.
	IncomingEdges = kgwire.IncomingEdges
)

// BrowseEdges reads the edges of a single node from a named per-account/per-repo
// graph via the Execute carrier seam: a RETURN_MODE_EDGES read keyed by the node
// id (the same shape engine.dispatchGraphWideEdges / graphWideEdgeUnion uses),
// decoded from the typed edges carrier. Unlike a traverse-walk, this returns the
// RAW edges — including edges whose endpoint is a bare string that is NOT itself a
// graph node (e.g. a dangling Route53 DNS hostname, an unresolved group: target)
// — which is exactly what an edge-rewrite resolver needs.
//
// dir selects outgoing (Forward=true) or incoming (Forward=false). edgeTypes
// filters to the listed edge types (empty = any). Cloud/cicd edge-type constants
// are already stored in their canonical UPPERCASE form (edge_types.go), so they
// ride AS-GIVEN. The (gt, graphName) selector routes the read to the right backing
// DB (cloud/cicd by Account, code by Repo) via the same translation selectorArgs
// performs for the query/mutate helpers.
//
// Returns wire edges ([]knowledgev1.Edge) straight from engine.DecodeEdges —
// the knowledgev1-typed read surface providers now consume.
func BrowseEdges(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, graphName, nodeID string, dir kgwire.EdgeDirection, edgeTypes []kgtypes.EdgeType) ([]knowledgev1.Edge, error) {
	if nodeID == "" {
		return nil, nil
	}
	fwd := dir == OutgoingEdges
	sel := &knowledgev1.Selection{}
	if len(edgeTypes) > 0 {
		types := make([]string, len(edgeTypes))
		for i, t := range edgeTypes {
			types[i] = string(t)
		}
		sel.EdgeTypes = types
	}
	plan := &knowledgev1.QueryPlan{
		Ids:        []string{nodeID},
		Selection:  sel,
		Forward:    &fwd,
		ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES,
	}
	req := &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: plan},
		Target: edgeSelector(gt, graphName),
	}
	resp, err := gc.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("postpopulate: browse edges %s/%s from %s: %w", gt, graphName, nodeID, err)
	}
	return engine.DecodeEdges(resp)
}

// UnlinkEdge removes a single directed edge (from→to of edgeType) from a named
// per-account/per-repo graph via EXACTLY ONE mutate(unlink) Execute call. Edge
// identity is (from, to, type) — no metadata. Used by edge-rewrite resolvers
// (Route53 DNS alias resolution) to retract a dangling edge before relinking it
// to the resolved target. The (gt, graphName) selector routes the write to the
// right backing DB so the unlink lands in the per-account graph.
func UnlinkEdge(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, graphName, fromID, toID string, edgeType kgtypes.EdgeType) error {
	args := selectorArgs(gt, graphName)
	args["operation"] = "unlink"
	args["from"] = fromID
	args["to"] = toID
	args["relationship"] = string(edgeType)
	body, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("postpopulate: marshal unlink args: %w", err)
	}
	req, ok := engine.Compile("mutate", body)
	if !ok {
		return fmt.Errorf("postpopulate: unlink args not reducible to a MutationPlan")
	}
	if _, err := gc.Execute(ctx, req); err != nil {
		return fmt.Errorf("postpopulate: unlink %s/%s (%s -%s-> %s): %w", gt, graphName, fromID, edgeType, toID, err)
	}
	return nil
}

// UnlinkEdgesBatch removes a set of directed edges from a named graph. The
// engine has no batch-unlink mutation (edge identity is from/to/type, not a
// predicate set), so this issues one mutate(unlink) per edge — the faithful
// wire equivalent of the prior bulk edge-removal. The batched WRITE
// requirement of the ticket applies to create_batch edge WRITES (LinkEdgesBatch),
// not to removals. Empty edges is a no-op. First error aborts the batch.
func UnlinkEdgesBatch(ctx context.Context, gc GraphCaller, gt kgtypes.GraphType, graphName string, edges []knowledgev1.Edge) error {
	for i := range edges {
		e := &edges[i]
		if err := UnlinkEdge(ctx, gc, gt, graphName, e.FromId, e.ToId, kgtypes.EdgeType(e.Type)); err != nil {
			return err
		}
	}
	return nil
}

// edgeSelector builds the proto GraphSelector for a RETURN_MODE_EDGES read,
// mirroring the selectorArgs (gt, graphName) → field routing in proto form:
// code routes by Repo, cloud/cicd by Account, everything else by Name. This is
// the proto-shaped twin of selectorArgs (which produces the JSON-arg form the
// query/mutate engine.Compile path consumes); the edge-read builds the proto
// directly because RETURN_MODE_EDGES is not an engine.Compile tool shape.
func edgeSelector(gt kgtypes.GraphType, graphName string) *knowledgev1.GraphSelector {
	return graphsel.GraphSelectorFor(gt, graphName, true)
}
