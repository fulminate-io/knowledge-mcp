// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// persistedClustersFakeCaller serves the three read shapes DetectPersistedClusters
// drives: (1) the paged type=thought browse (Selection set, no Ids, not EDGES) →
// the seeded thought nodes for offset 0, empty for any later offset (so the drain
// terminates on a short page); (2) the charged-by RETURN_MODE_EDGES read → no
// charges (cluster aggregates fall back to zero — fine for grouping assertions);
// (3) the ids[] hydrate → nothing (no charges). It counts Execute calls so the
// test can assert no traverse/adjacency RPC beyond the drain + the one bulk
// charges fetch.
type persistedClustersFakeCaller struct {
	thoughtNodes []*knowledgev1.Node
	execCalls    int
}

func (c *persistedClustersFakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	c.execCalls++
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// charges_for edge read.
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{}, nil // no charges in these fixtures.
	}
	// ids[] hydrate (charge nodes) — none.
	if len(q.GetIds()) > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// Type-browse drain page: serve the full set at offset 0, empty afterwards.
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return &knowledgev1.ExecuteResponse{Nodes: c.thoughtNodes}, nil
}

func thoughtWithCluster(id, clusterID string) *knowledgev1.Node {
	n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought)}
	if clusterID != "" {
		kgtypes.SetValue(n, "cluster_id", clusterID)
	}
	return n
}

func TestDetectPersistedClusters(t *testing.T) {
	ctx := context.Background()

	// (1) Nodes carrying cluster_id group into correct clusters, sorted size-desc.
	t.Run("groups_by_persisted_cluster_id_sorted_desc", func(t *testing.T) {
		fc := &persistedClustersFakeCaller{thoughtNodes: []*knowledgev1.Node{
			thoughtWithCluster("t1", "cA"),
			thoughtWithCluster("t2", "cA"),
			thoughtWithCluster("t3", "cA"),
			thoughtWithCluster("t4", "cB"),
			thoughtWithCluster("t5", "cB"),
			thoughtWithCluster("t6", "cC"),
		}}
		clusters, err := DetectPersistedClusters(ctx, fc, nil)
		require.NoError(t, err)
		require.Len(t, clusters, 3)
		// Sorted by size desc: cA(3) > cB(2) > cC(1).
		assert.Equal(t, "cA", clusters[0].ID)
		assert.Equal(t, 3, clusters[0].Size)
		assert.Equal(t, "cB", clusters[1].ID)
		assert.Equal(t, 2, clusters[1].Size)
		assert.Equal(t, "cC", clusters[2].ID)
		assert.Equal(t, 1, clusters[2].Size)
		// Members landed in the right group.
		sort.Strings(clusters[0].ThoughtIDs)
		assert.Equal(t, []string{"t1", "t2", "t3"}, clusters[0].ThoughtIDs)
		// Bounded RPCs: 2 browse pages (page 0 + the empty short page) + the bulk
		// charges fetch (1 edge read + 0 hydrate since no charge IDs). No
		// traverse/adjacency RPC.
		assert.LessOrEqual(t, fc.execCalls, 4, "no adjacency/traverse beyond the paged drain + bulk charges read")
	})

	// (2) N>0 nodes with zero cluster_id → cold-case sentinel, NOT a silent empty.
	t.Run("cold_case_returns_sentinel", func(t *testing.T) {
		fc := &persistedClustersFakeCaller{thoughtNodes: []*knowledgev1.Node{
			thoughtWithCluster("t1", ""),
			thoughtWithCluster("t2", ""),
		}}
		clusters, err := DetectPersistedClusters(ctx, fc, nil)
		assert.Nil(t, clusters)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrClustersNotComputed,
			"non-empty corpus with zero cluster_id must return the cold-case sentinel, not a silent empty slice")
	})

	// (3) Zero nodes → ordinary empty case (nil, nil), distinct from the cold case.
	t.Run("empty_graph_returns_clean", func(t *testing.T) {
		fc := &persistedClustersFakeCaller{thoughtNodes: nil}
		clusters, err := DetectPersistedClusters(ctx, fc, nil)
		require.NoError(t, err)
		assert.Empty(t, clusters)
		assert.NotErrorIs(t, err, ErrClustersNotComputed)
	})
}
