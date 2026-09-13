// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"sync"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func (r *Router) executeReferenceRead(ctx context.Context, request *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, bool, error) {
	q := request.GetQuery()
	if q == nil {
		return nil, false, nil
	}
	groups, err := r.referenceReadGroups(ctx, request)
	if err != nil {
		return nil, true, err
	}
	if len(groups) == 0 {
		return nil, false, nil
	}
	type result struct {
		response *knowledgev1.ExecuteResponse
		err      error
	}
	results := make([]result, len(groups))
	var workers sync.WaitGroup
	i := 0
	for key, refs := range groups {
		slot := i
		i++
		workers.Go(func() {
			req := proto.CloneOf(request)
			req.Target = key.Selector()
			plan := req.GetQuery()
			plan.ById = ""
			plan.Ids = nil
			original := map[string]string{}
			for _, encoded := range refs {
				ref, _, _ := ParseReference(encoded)
				plan.Ids = append(plan.Ids, ref.ID)
				original[ref.ID] = encoded
			}
			if q.ById != "" {
				plan.ById = plan.Ids[0]
				plan.Ids = nil
			}
			response, err := r.Execute(WithDestination(ctx, key.Destination), req)
			if err != nil {
				results[slot].err = err
				return
			}
			if err := qualifyStorageResponse(response, key, original); err != nil {
				results[slot].err = err
				return
			}
			results[slot].response = response
		})
	}
	workers.Wait()
	if len(results) == 1 {
		return results[0].response, true, results[0].err
	}
	merged := &knowledgev1.ExecuteResponse{}
	for _, part := range results {
		if part.err != nil {
			return nil, true, part.err
		}
		merged.Nodes = append(merged.Nodes, part.response.Nodes...)
		merged.Ids = append(merged.Ids, part.response.Ids...)
		merged.Edges = append(merged.Edges, part.response.Edges...)
		merged.TraversalEdges = append(merged.TraversalEdges, part.response.TraversalEdges...)
		merged.TraversalResults = append(merged.TraversalResults, part.response.TraversalResults...)
		merged.SearchResults = append(merged.SearchResults, part.response.SearchResults...)
		merged.Total += part.response.Total
		merged.Truncated = merged.Truncated || part.response.Truncated
	}
	return merged, true, nil
}

func (r *Router) referenceReadGroups(ctx context.Context, request *knowledgev1.ExecuteRequest) (map[Reference][]string, error) {
	q := request.GetQuery()
	ids := q.Ids
	if q.ById != "" {
		ids = []string{q.ById}
	}
	groups := map[Reference][]string{}
	qualified := false
	for _, id := range ids {
		ref, ok, err := ParseReference(id)
		if err != nil {
			return nil, err
		}
		if ok {
			qualified = true
			ref.ID = ""
			groups[ref] = append(groups[ref], id)
		}
	}
	if !qualified {
		return nil, nil
	}
	for _, id := range ids {
		_, ok, _ := ParseReference(id)
		if !ok {
			bound, err := r.BindStorage(ctx, "")
			if err != nil {
				return nil, err
			}
			d, _ := StorageDestination(bound)
			target := request.GetTarget()
			key := Reference{Destination: d, Graph: target.GetGraph(), Repo: target.GetRepo(), Name: target.GetName(), Language: target.GetLanguage(), Branch: target.GetBranch()}
			if key.Graph == "" {
				key.Graph = "knowledge"
			}
			groups[key] = append(groups[key], id)
		}
	}
	return groups, nil
}
