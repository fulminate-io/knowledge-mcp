// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// blindSpotCaller serves the two reads ReflectBlindSpots issues over its member
// set: fetchChargesFor (EdgeChargedBy edge read + charge hydrate) and
// fetchNodesByIDs (member hydrate, carrying source/origin metadata for the genre
// facets). Members with no charge entry are zero-charge (charge-thin).
type blindSpotCaller struct {
	nodes    map[string]*knowledgev1.Node
	charges  map[string]*knowledgev1.Node
	chargeOf map[string][]string // thoughtID → charge node ids
}

func newBlindSpotCaller() *blindSpotCaller {
	return &blindSpotCaller{
		nodes:    map[string]*knowledgev1.Node{},
		charges:  map[string]*knowledgev1.Node{},
		chargeOf: map[string][]string{},
	}
}

// addMember registers a member node with optional source/origin facets (for genre)
// and a charge count (0 = charge-thin).
func (c *blindSpotCaller) addMember(id, source, origin string, chargeCount int) {
	n := &knowledgev1.Node{Id: id, SymbolName: id, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	if source != "" {
		kgtypes.SetValue(n, "source", source)
	}
	if origin != "" {
		kgtypes.SetValue(n, "origin", origin)
	}
	c.nodes[id] = n
	for i := range chargeCount {
		chID := fmt.Sprintf("c-%s-%d", id, i)
		ch := &knowledgev1.Node{Id: chID, Type: string(kgtypes.NodeCharge), UpdatedAt: 1000}
		kgtypes.SetValue(ch, "polarity", "positive")
		kgtypes.SetValue(ch, "weight", "3")
		c.charges[chID] = ch
		c.chargeOf[id] = append(c.chargeOf[id], chID)
	}
}

func (c *blindSpotCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		// fetchChargesFor reads EdgeChargedBy over the requested member ids.
		var ce []*knowledgev1.Edge
		for _, id := range q.GetIds() {
			for _, chID := range c.chargeOf[id] {
				ce = append(ce, &knowledgev1.Edge{Type: string(kgtypes.EdgeChargedBy), FromId: id, ToId: chID})
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: ce}, nil
	}
	if len(q.GetIds()) > 0 {
		var nodes []*knowledgev1.Node
		for _, id := range q.GetIds() {
			if n, ok := c.nodes[id]; ok {
				nodes = append(nodes, n)
			} else if ch, ok := c.charges[id]; ok {
				nodes = append(nodes, ch)
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// mkCluster builds a ThoughtCluster of n members with ids prefixed by `prefix`,
// each a human-genre charge-thin (zero-charge) member registered on the caller.
func mkCluster(c *blindSpotCaller, id, prefix string, n int) ThoughtCluster {
	ids := make([]string, n)
	for i := range ids {
		mid := fmt.Sprintf("%s%d", prefix, i)
		ids[i] = mid
		c.addMember(mid, "", "implementer", 0)
	}
	return ThoughtCluster{ID: id, ThoughtIDs: ids, Size: n, Label: id}
}

// TestReflectBlindSpots_Rank (FAILS-WHEN-ABSENT) proves the impact rank prefers a
// large high-influence charge-thin cluster over a tiny low-influence one of equal
// (full) thinness. Both clusters are zero-charge → thinness=1; the big/high-
// influence cluster has the higher Size×Influence×thinness impact.
func TestReflectBlindSpots_Rank(t *testing.T) {
	c := newBlindSpotCaller()
	big := mkCluster(c, "big", "b", 10)
	small := mkCluster(c, "small", "s", 2)

	influence := map[string]float64{}
	for _, id := range big.ThoughtIDs {
		influence[id] = 0.1 // high per-member influence
	}
	for _, id := range small.ThoughtIDs {
		influence[id] = 0.001 // low per-member influence
	}

	res := ReflectBlindSpots(context.Background(), c, []ThoughtCluster{small, big}, nil, influence, nil)
	require.Len(t, res.Spots, 2)
	assert.Equal(t, "big", res.Spots[0].Cluster.ID,
		"the large high-influence charge-thin cluster ranks above the tiny low-influence one of equal thinness")
	assert.Greater(t, res.Spots[0].Impact, res.Spots[1].Impact)
}

// TestReflectBlindSpots_GenreExclusion (FAILS-WHEN-ABSENT) proves a machine-genre
// (dream-source) charge-thin cluster is EXCLUDED from the spots slice and counted
// into ExcludedMachineGenre, while a human-genre charge-thin cluster surfaces.
func TestReflectBlindSpots_GenreExclusion(t *testing.T) {
	c := newBlindSpotCaller()
	// A dream-genre cluster: all members source="dream:analyze" → machine majority.
	dreamIDs := []string{"d0", "d1", "d2"}
	for _, id := range dreamIDs {
		c.addMember(id, "dream:analyze", "main", 0)
	}
	dreamCluster := ThoughtCluster{ID: "dream", ThoughtIDs: dreamIDs, Size: 3, Label: "dream"}
	// A human cluster (charge-thin).
	human := mkCluster(c, "human", "h", 3)

	influence := map[string]float64{}
	for _, id := range append(append([]string{}, dreamIDs...), human.ThoughtIDs...) {
		influence[id] = 0.05
	}

	res := ReflectBlindSpots(context.Background(), c, []ThoughtCluster{dreamCluster, human}, nil, influence, nil)
	assert.Equal(t, 1, res.ExcludedMachineGenre, "the dream-genre cluster is counted as excluded")
	require.Len(t, res.Spots, 1, "only the human-genre cluster surfaces as a spot")
	assert.Equal(t, "human", res.Spots[0].Cluster.ID)
	for _, sp := range res.Spots {
		assert.NotEqual(t, "dream", sp.Cluster.ID, "the dream-genre charge-thin cluster must NOT appear as a spot")
	}
}

// TestReflectBlindSpots_Cap (FAILS-WHEN-ABSENT) proves >blindSpotReportCap
// qualifying human-genre clusters render exactly blindSpotReportCap ranked spots.
func TestReflectBlindSpots_Cap(t *testing.T) {
	c := newBlindSpotCaller()
	total := blindSpotReportCap + 5
	clusters := make([]ThoughtCluster, 0, total)
	influence := map[string]float64{}
	for i := range total {
		cl := mkCluster(c, fmt.Sprintf("cl%d", i), fmt.Sprintf("c%d_", i), 3)
		for _, id := range cl.ThoughtIDs {
			influence[id] = 0.01
		}
		clusters = append(clusters, cl)
	}

	res := ReflectBlindSpots(context.Background(), c, clusters, nil, influence, nil)
	assert.Len(t, res.Spots, blindSpotReportCap,
		"output caps at blindSpotReportCap even with more qualifying clusters")
	assert.Equal(t, total, res.TotalUnderEvidenced,
		"TotalUnderEvidenced counts all qualifying clusters before the cap")
}
