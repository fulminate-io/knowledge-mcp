// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

func (m *Manager) searchDestinations(ctx context.Context, gt kgtypes.GraphType, name string, search func(context.Context) ([]searchengine.Hit, error), k int) ([]searchengine.Hit, error) {
	destinations := graphclient.SearchDestinations(ctx)
	results := make([][]searchengine.Hit, len(destinations))
	errors := make([]error, len(destinations))
	var workers sync.WaitGroup
	for i, d := range destinations {
		workers.Go(func() {
			leg := graphclient.WithDestination(graphclient.WithSearchDestinations(ctx, nil), d)
			hits, err := search(leg)
			if err != nil {
				errors[i] = fmt.Errorf("%s search: %w", d.Storage, err)
				return
			}
			target := graphSelector(gt, name)
			if gt == kgtypes.GraphCode {
				if base, branch, ok := strings.Cut(target.Repo, "@"); ok {
					target.Repo = base
					target.Branch = branch
				}
			}
			for j := range hits {
				ref := graphclient.Reference{Destination: d, Graph: target.GetGraph(), Repo: target.GetRepo(), Name: target.GetName(), Language: target.GetLanguage(), Branch: target.GetBranch(), ID: hits[j].ID}
				hits[j].ID, err = ref.Encode()
				if err != nil {
					errors[i] = err
					return
				}
			}
			results[i] = hits
		})
	}
	workers.Wait()
	var merged []searchengine.Hit
	for i, hits := range results {
		if errors[i] != nil {
			return nil, errors[i]
		}
		merged = append(merged, hits...)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Score == merged[j].Score {
			return merged[i].ID < merged[j].ID
		}
		return merged[i].Score > merged[j].Score
	})
	if k > 0 && len(merged) > k {
		merged = merged[:k]
	}
	return merged, nil
}
