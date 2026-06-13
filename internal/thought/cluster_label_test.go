// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// clusterWritebackFake serves the reads buildClusterObjects drives (ids[] hydrate
// → bare member nodes; charges_for edges → none) and CAPTURES the cluster_id
// assignments from the bulk_update_metadata mutation so a test can compare the
// persisted labels across runs.
type clusterWritebackFake struct {
	lastAssignments map[string]string // member id → persisted cluster_id
}

func (f *clusterWritebackFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	// Capture the cluster_id writeback (bulk_update_metadata → UPDATE_ITEMS).
	if m := req.GetMutation(); m != nil {
		f.lastAssignments = map[string]string{}
		for _, it := range m.GetUpdateItems() {
			if cid, ok := it.GetMetadata()["cluster_id"]; ok {
				f.lastAssignments[it.GetId()] = cid
			}
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// charges_for edge read → no charges.
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	// ids[] hydrate → bare nodes for the requested members.
	if len(q.GetIds()) > 0 {
		nodes := make([]*knowledgev1.Node, 0, len(q.GetIds()))
		for _, id := range q.GetIds() {
			nodes = append(nodes, &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought)})
		}
		return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// TestBuildClusterObjects_CanonicalLabelStableAcrossRuns (FAILS-WHEN-ABSENT)
// proves cluster.ID is the canonical groups-map key (the min-member-node-ID
// community label), not a positional fmt.Sprintf("cluster-%d"). Running
// buildClusterObjects twice over the SAME partition must persist IDENTICAL
// cluster_id for every member — Go map-iteration order must not perturb the label.
func TestBuildClusterObjects_CanonicalLabelStableAcrossRuns(t *testing.T) {
	ctx := context.Background()

	// Canonical partition: communityOf values are min-member labels "t1" and "t4".
	groups := map[string][]string{
		"t1": {"t1", "t2", "t3"},
		"t4": {"t4", "t5"},
	}

	run := func() map[string]string {
		fake := &clusterWritebackFake{}
		clusters := buildClusterObjects(ctx, fake, groups)
		require.Len(t, clusters, 2)
		// cluster.ID equals the groups key (canonical label), never "cluster-N".
		for _, c := range clusters {
			assert.NotContains(t, c.ID, "cluster-", "cluster.ID must be the canonical label, not positional")
		}
		require.NotNil(t, fake.lastAssignments)
		return fake.lastAssignments
	}

	first := run()
	second := run()

	// The canonical label is the min-member node ID of each group.
	assert.Equal(t, "t1", first["t1"], "member t1's cluster_id is its community's min-member label")
	assert.Equal(t, "t1", first["t3"], "t3 shares t1's community → same canonical label")
	assert.Equal(t, "t4", first["t5"], "member t5's cluster_id is its community's min-member label")

	// Map-order independence: two runs over the same partition persist identical IDs.
	assert.Equal(t, first, second,
		"buildClusterObjects must assign identical cluster_id across runs (no map-iteration-order dependence)")
}
