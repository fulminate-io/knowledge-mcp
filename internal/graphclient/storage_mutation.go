// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// executeReferenceMutation partitions existing bulk operations by owner. Every
// partition is validated before the first write; each backend retains its own
// transaction boundary. An execution error is returned even after earlier writes.
func (r *Router) executeReferenceMutation(ctx context.Context, request *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, bool, error) {
	m := request.GetMutation()
	if m == nil {
		return nil, false, nil
	}
	ids := referenceMutationIDs(m)
	var refs []Reference
	qualified := false
	for _, id := range ids {
		ref, ok, err := ParseReference(id)
		if err != nil {
			return nil, true, err
		}
		qualified = qualified || ok
		refs = append(refs, ref)
	}
	if !qualified {
		return nil, false, nil
	}
	bound, err := r.BindStorage(ctx, "")
	if err != nil {
		return nil, true, err
	}
	d, _ := StorageDestination(bound)
	target := request.GetTarget()
	base := Reference{Destination: d, Graph: target.GetGraph(), Repo: target.GetRepo(), Name: target.GetName(), Language: target.GetLanguage(), Branch: target.GetBranch()}
	if base.Graph == "" {
		base.Graph = "knowledge"
	}
	groups := make(map[Reference][]int)
	var order []Reference
	for i, ref := range refs {
		if ref.Storage == "" {
			ref = base
		}
		ref.ID = ""
		if _, seen := groups[ref]; !seen {
			order = append(order, ref)
		}
		groups[ref] = append(groups[ref], i)
	}
	if len(order) < 2 {
		return nil, false, nil
	}
	contexts, requests, err := r.prepareReferenceMutations(ctx, request, order, groups, refs)
	if err != nil {
		return nil, true, err
	}
	merged := &knowledgev1.ExecuteResponse{}
	// Preserve partition order for mutations; no distributed transaction is implied.
	for i, req := range requests {
		response, err := r.Execute(contexts[i], req)
		if err != nil {
			return nil, true, fmt.Errorf("%s mutation failed after %d completed storage partitions: %w", order[i].Storage, i, err)
		}
		if err := qualifyStorageResponse(response, order[i], nil); err != nil {
			return nil, true, err
		}
		merged.Nodes = append(merged.Nodes, response.Nodes...)
		merged.Ids = append(merged.Ids, response.Ids...)
		merged.AffectedCount += response.AffectedCount
		merged.SkippedCount += response.SkippedCount
		merged.Total += response.Total
		merged.Truncated = merged.Truncated || response.Truncated
	}
	return merged, true, nil
}

func referenceMutationPartition(request *knowledgev1.ExecuteRequest, key Reference, indices []int, refs []Reference) *knowledgev1.ExecuteRequest {
	m := request.GetMutation()
	req := proto.CloneOf(request)
	req.Target = key.Selector()
	part := req.GetMutation()
	switch m.Kind {
	case knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS:
		part.UpdateItems = nil
		for _, index := range indices {
			item := proto.CloneOf(m.UpdateItems[index])
			item.Id = refs[index].ID
			part.UpdateItems = append(part.UpdateItems, item)
		}
	case knowledgev1.MutationPlan_MUTATION_KIND_UPSERT:
		part.NodeBodies = nil
		for _, index := range indices {
			node := proto.CloneOf(m.NodeBodies[index])
			node.Id = refs[index].ID
			part.NodeBodies = append(part.NodeBodies, node)
		}
	default:
		part.Selection.Ids = nil
		for _, index := range indices {
			part.Selection.Ids = append(part.Selection.Ids, refs[index].ID)
		}
	}
	return req
}

func referenceMutationIDs(m *knowledgev1.MutationPlan) []string {
	ids := m.GetSelection().GetIds()
	switch m.Kind {
	case knowledgev1.MutationPlan_MUTATION_KIND_UPDATE_ITEMS:
		ids = make([]string, len(m.UpdateItems))
		for i, item := range m.UpdateItems {
			ids[i] = item.Id
		}
	case knowledgev1.MutationPlan_MUTATION_KIND_UPSERT:
		if len(m.Edges) != 0 {
			return nil
		}
		ids = make([]string, len(m.NodeBodies))
		for i, node := range m.NodeBodies {
			ids[i] = node.Id
		}
	}
	return ids
}

func (r *Router) prepareReferenceMutations(ctx context.Context, request *knowledgev1.ExecuteRequest, order []Reference, groups map[Reference][]int, refs []Reference) ([]context.Context, []*knowledgev1.ExecuteRequest, error) {
	var err error
	requests := make([]*knowledgev1.ExecuteRequest, len(order))
	contexts := make([]context.Context, len(order))
	for i, key := range order {
		req := referenceMutationPartition(request, key, groups[key], refs)
		leg := WithDestination(ctx, key.Destination)
		leg, req, err = resolveRequestReferences(leg, req)
		if err != nil {
			return nil, nil, err
		}
		if _, _, err := resolveAdmissionTarget(req); err != nil {
			return nil, nil, err
		}
		if err := r.validateStorageRetarget(leg, req); err != nil {
			return nil, nil, err
		}
		if err := r.validateStorageRelationships(leg, req); err != nil {
			return nil, nil, err
		}
		contexts[i], requests[i] = leg, req
	}
	return contexts, requests, nil
}
