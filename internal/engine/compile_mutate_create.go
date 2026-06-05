// SPDX-License-Identifier: Apache-2.0

package engine

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// createPayload builds the NodeBody + BatchEdgeSpec payload for compileMutateCreate,
// dispatching on the create vs create_batch shape. Returns ok=false for the
// reducible-deny cases (empty batch, missing single-create type, unparseable
// edge last_validated) so the caller falls through to legacy.
func createPayload(a mutateArgs) ([]*knowledgev1.NodeBody, []*knowledgev1.BatchEdgeSpec, bool) {
	if a.Operation == "create_batch" {
		// Edges-only create_batch is a first-class shape: PostPopulate hooks
		// write structural edges referencing nodes the collector
		// already uploaded, with no new node bodies. The store's CreateBatch
		// supports it natively (edges referencing pre-existing nodes by string
		// ID). Only the truly-empty batch (0 nodes AND 0 edges) is non-reducible
		// — there is nothing to create.
		if len(a.Nodes) == 0 && len(a.Edges) == 0 {
			return nil, nil, false // nothing to create.
		}
		bodies := make([]*knowledgev1.NodeBody, 0, len(a.Nodes))
		for _, n := range a.Nodes {
			bodies = append(bodies, nodeBodyToProto(n))
		}
		edges, ok := batchEdgesToProto(a.Edges)
		if !ok {
			return nil, nil, false
		}
		return bodies, edges, true
	}
	// Single create → one-element NodeBodies (the engine create_batch arm runs the
	// same CreateBatch primitive for one or N).
	if a.Type == "" {
		return nil, nil, false
	}
	return []*knowledgev1.NodeBody{nodeBodyToProto(nodeBody{
		Type:        a.Type,
		Name:        a.Name,
		Description: a.Description,
		Summary:     a.Summary,
		Content:     a.Content,
		Status:      a.Status,
		Metadata:    a.Metadata,
		ID:          a.ID,
		Source:      a.Source,
	})}, nil, true
}

// batchEdgesToProto lowers create_batch's edgeBody list onto proto BatchEdgeSpec,
// carrying the endpoint + type + the five edge-metadata fields AS-GIVEN.
// last_validated (RFC3339) converts to int64 unix-nanos via the shared link-arm
// parseLastValidatedNanos helper; an unparseable value returns ok=false so the
// caller falls through to legacy (where the RFC3339 error surfaces).
func batchEdgesToProto(in []edgeBody) ([]*knowledgev1.BatchEdgeSpec, bool) {
	if len(in) == 0 {
		return nil, true
	}
	out := make([]*knowledgev1.BatchEdgeSpec, 0, len(in))
	for _, e := range in {
		lastNanos, ok := parseLastValidatedNanos(e.LastValidated)
		if !ok {
			return nil, false
		}
		out = append(out, &knowledgev1.BatchEdgeSpec{
			FromIdx:       int32(e.FromIdx),
			ToIdx:         int32(e.ToIdx),
			FromId:        e.FromID,
			ToId:          e.ToID,
			Type:          e.Type,
			Weight:        e.Weight,
			Confidence:    e.Confidence,
			Method:        e.Method,
			Evidence:      e.Evidence,
			LastValidated: lastNanos,
		})
	}
	return out, true
}
