// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// validateStorageRetarget checks existing incident edges against prospective
// proxy identities before changing metadata. It uses the existing edge carrier.
func (r *Router) validateStorageRetarget(ctx context.Context, req *knowledgev1.ExecuteRequest) error {
	m := req.GetMutation()
	if m == nil {
		return nil
	}
	if _, bound := StorageDestination(ctx); !bound {
		return nil
	}
	bodies := []*knowledgev1.NodeBody{}
	for _, item := range m.UpdateItems {
		if value, ok := item.Metadata["storage_reference"]; ok {
			bodies = append(bodies, &knowledgev1.NodeBody{Id: item.Id, Metadata: map[string]string{"storage_reference": value}})
		}
	}
	for _, body := range m.NodeBodies {
		if _, ok := body.Metadata["storage_reference"]; ok && body.Id != "" {
			bodies = append(bodies, body)
		}
	}
	value, selected := m.SetMetadata["storage_reference"]
	if len(bodies) == 0 && !selected {
		return nil
	}
	backend, err := r.pick(ctx)
	if err != nil {
		return err
	}
	if selected {
		rows, err := backend.Execute(ctx, &knowledgev1.ExecuteRequest{Target: req.Target, Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Selection: m.Selection, Ids: m.GetSelection().GetIds(), ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_NODES}}})
		if err != nil {
			return err
		}
		if rows.Truncated {
			return fmt.Errorf("storage retarget selection is incomplete; narrow the selection")
		}
		for _, node := range rows.Nodes {
			bodies = append(bodies, &knowledgev1.NodeBody{Id: node.Id, Metadata: map[string]string{"storage_reference": value}})
		}
	}
	if len(bodies) == 0 {
		return nil
	}
	ids := make([]string, 0, len(bodies))
	for _, body := range bodies {
		ids = append(ids, body.Id)
	}
	rows, err := backend.Execute(ctx, &knowledgev1.ExecuteRequest{Target: req.Target, Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: ids, ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES}}})
	if err != nil {
		return err
	}
	if rows.Truncated {
		return fmt.Errorf("storage retarget incident edges are incomplete; narrow the operation")
	}
	check := &knowledgev1.MutationPlan{NodeBodies: bodies}
	for _, edge := range rows.Edges {
		check.Edges = append(check.Edges, &knowledgev1.BatchEdgeSpec{FromId: edge.FromId, ToId: edge.ToId})
	}
	return r.validateStorageRelationships(ctx, &knowledgev1.ExecuteRequest{Target: req.Target, Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: check}})
}
