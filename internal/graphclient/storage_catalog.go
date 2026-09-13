// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"sort"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// searchCatalog enumerates both storage locations before the search layer fans
// out over graph names. Hits retain their full destination independently.
func (r *Router) searchCatalog(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, bool, error) {
	destinations := SearchDestinations(ctx)
	if len(destinations) < 2 || req.GetQuery().GetReturnMode() != knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return nil, false, nil
	}
	responses := make([]*knowledgev1.ExecuteResponse, len(destinations))
	errors := make([]error, len(destinations))
	var workers sync.WaitGroup
	for i, destination := range destinations {
		workers.Go(func() {
			leg := WithDestination(WithSearchDestinations(ctx, nil), destination)
			responses[i], errors[i] = r.Execute(leg, req)
		})
	}
	workers.Wait()
	out := &knowledgev1.ExecuteResponse{}
	seen := map[string]bool{}
	for i, response := range responses {
		if errors[i] != nil {
			return nil, true, errors[i]
		}
		out.Truncated = out.Truncated || response.Truncated
		for _, graph := range response.GraphNames {
			if !seen[graph.Name] {
				out.GraphNames = append(out.GraphNames, graph)
				seen[graph.Name] = true
			}
		}
	}
	sort.Slice(out.GraphNames, func(i, j int) bool { return out.GraphNames[i].Name < out.GraphNames[j].Name })
	out.Total = int64(len(out.GraphNames))
	return out, true, nil
}
