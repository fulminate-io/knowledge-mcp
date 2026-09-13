// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"crypto/sha256"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// materializeStorageTargets resolves every foreign endpoint before writing any
// proxy. The final edge and each proxy live beside the source node.
func (r *Router) materializeStorageTargets(ctx context.Context, req *knowledgev1.ExecuteRequest) error {
	m := req.GetMutation()
	if m == nil {
		return nil
	}
	var targets []*string
	if m.EdgeSpec != nil {
		targets = append(targets, &m.EdgeSpec.ToId)
	}
	for _, edge := range m.Edges {
		targets = append(targets, &edge.ToId)
	}
	proxies := map[string]*knowledgev1.NodeBody{}
	for _, id := range targets {
		ref, qualified, err := ParseReference(*id)
		if err != nil {
			return err
		}
		if !qualified {
			continue
		}
		encoded, err := ref.Encode()
		if err != nil {
			return err
		}
		proxyID := fmt.Sprintf("storage-proxy:%x", sha256.Sum256([]byte(encoded)))
		if proxies[proxyID] == nil {
			response, err := r.Execute(WithDestination(ctx, ref.Destination), &knowledgev1.ExecuteRequest{Target: ref.Selector(), Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: ref.ID}}})
			if err != nil {
				return err
			}
			if len(response.Nodes) != 1 {
				return fmt.Errorf("reference endpoint %q was not found", ref.ID)
			}
			node := response.Nodes[0]
			proxies[proxyID] = &knowledgev1.NodeBody{Id: proxyID, Type: "proxy", Name: node.SymbolName, Description: node.Description, Source: encoded, Metadata: map[string]string{"storage_reference": encoded, "foreign_graph": ref.Graph}}
		}
		*id = proxyID
	}
	if len(proxies) == 0 {
		return nil
	}
	bodies := make([]*knowledgev1.NodeBody, 0, len(proxies))
	for _, body := range proxies {
		bodies = append(bodies, body)
	}
	_, err := r.Execute(ctx, &knowledgev1.ExecuteRequest{Target: req.Target, Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, NodeBodies: bodies}}})
	return err
}
