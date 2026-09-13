// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// validateStorageRelationships checks reused proxies as well as literal refs.
// One read resolves existing endpoints; every direction is checked before writes.
func (r *Router) validateStorageRelationships(ctx context.Context, req *knowledgev1.ExecuteRequest) error {
	m := req.GetMutation()
	if m == nil || (m.EdgeSpec == nil && len(m.Edges) == 0) {
		return nil
	}
	host, bound := StorageDestination(ctx)
	if !bound {
		return nil
	}
	identities, wanted, err := storageRelationshipEndpoints(m)
	if err != nil {
		return err
	}
	bodyIdentities, err := storageBodyIdentities(m, host, wanted, identities)
	if err != nil {
		return err
	}

	if err := r.readStorageEndpointIdentities(ctx, req.Target, host, wanted, identities); err != nil {
		return err
	}

	return validateStorageEndpointDirections(m, host, identities, bodyIdentities)
}

func validateStorageEndpointDirections(m *knowledgev1.MutationPlan, host Destination, identities map[string]Destination, bodyIdentities []Destination) error {
	identity := func(id string) Destination {
		if d, ok := identities[id]; ok {
			return d
		}
		return host
	}

	if edge := m.EdgeSpec; edge != nil {
		for _, id := range m.GetSelection().GetIds() {
			from, to := identity(id), identity(edge.ToId)
			if !edge.Forward {
				from, to = to, from
			}
			if err := validateStorageDirection(from, to); err != nil {
				return err
			}
		}
	}
	for _, edge := range m.Edges {
		from, to := identity(edge.FromId), identity(edge.ToId)
		if edge.FromId == "" && edge.FromIdx >= 0 && int(edge.FromIdx) < len(m.NodeBodies) {
			from = bodyIdentities[edge.FromIdx]
		}
		if edge.ToId == "" && edge.ToIdx >= 0 && int(edge.ToIdx) < len(m.NodeBodies) {
			to = bodyIdentities[edge.ToIdx]
		}
		if err := validateStorageDirection(from, to); err != nil {
			return err
		}
	}
	return nil
}

func storageRelationshipEndpoints(m *knowledgev1.MutationPlan) (map[string]Destination, map[string]bool, error) {
	identities := make(map[string]Destination)
	wanted := make(map[string]bool)
	add := func(id string) error {
		if id == "" {
			return nil
		}
		ref, qualified, err := ParseReference(id)
		if err != nil {
			return err
		}
		if qualified {
			identities[id] = ref.Destination
		} else {
			wanted[id] = true
		}
		return nil
	}
	for _, id := range m.GetSelection().GetIds() {
		if err := add(id); err != nil {
			return nil, nil, err
		}
	}
	if m.EdgeSpec != nil {
		if err := add(m.EdgeSpec.ToId); err != nil {
			return nil, nil, err
		}
	}
	for _, edge := range m.Edges {
		if err := add(edge.FromId); err != nil {
			return nil, nil, err
		}
		if err := add(edge.ToId); err != nil {
			return nil, nil, err
		}
	}
	return identities, wanted, nil
}

func (r *Router) readStorageEndpointIdentities(ctx context.Context, target *knowledgev1.GraphSelector, host Destination, wanted map[string]bool, identities map[string]Destination) error {
	if len(wanted) == 0 {
		return nil
	}
	ids := make([]string, 0, len(wanted))
	for id := range wanted {
		ids = append(ids, id)
	}
	backend, err := r.pick(ctx)
	if err != nil {
		return err
	}
	response, err := backend.Execute(ctx, &knowledgev1.ExecuteRequest{Target: target, Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: ids}}})
	if err != nil {
		return err
	}
	for _, node := range response.Nodes {
		identities[node.Id] = host
		if value := node.Metadata["storage_reference"]; value != "" {
			ref, qualified, err := ParseReference(value)
			if err != nil {
				return err
			}
			if !qualified {
				return fmt.Errorf("proxy %q has an invalid storage reference", node.Id)
			}
			identities[node.Id] = ref.Destination
		}
	}
	return nil
}

func storageBodyIdentities(m *knowledgev1.MutationPlan, host Destination, wanted map[string]bool, identities map[string]Destination) ([]Destination, error) {
	bodyIdentities := make([]Destination, len(m.NodeBodies))
	for i, node := range m.NodeBodies {
		delete(wanted, node.Id)
		bodyIdentities[i] = host
		if value := node.Metadata["storage_reference"]; value != "" {
			ref, _, err := ParseReference(value)
			if err != nil {
				return nil, err
			}
			bodyIdentities[i] = ref.Destination
		}
		if node.Id != "" {
			identities[node.Id] = bodyIdentities[i]
		}
	}
	return bodyIdentities, nil
}

func validateStorageDirection(from, to Destination) error {
	if from.Storage == "cloud" && to.Storage == "local" {
		return fmt.Errorf("cloud-to-local relationships are not allowed")
	}
	if from.Storage == "cloud" && to.Storage == "cloud" && from.AccountID != to.AccountID {
		return fmt.Errorf("cross-account relationships are not allowed")
	}
	return nil
}
