// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// blindSpotRenderCaller serves the reads the blind-spots handler path issues:
// the type=thought browse (cluster_id-stamped nodes drive DetectPersistedClusters),
// EdgeChargedBy reads (charge-thin corpus → no charge edges), generic edge reads
// (adjacency / EdgeKGContains → empty), and Ids hydrate. Members carry cluster_id +
// source/origin so DetectPersistedClusters groups them and the genre classifier
// reads the facets.
type blindSpotRenderCaller struct {
	thoughts map[string]*knowledgev1.Node
	order    []string
}

func newBlindSpotRenderCaller() *blindSpotRenderCaller {
	return &blindSpotRenderCaller{thoughts: map[string]*knowledgev1.Node{}}
}

func (c *blindSpotRenderCaller) addThought(id, clusterID, source, origin string) {
	n := &knowledgev1.Node{Id: id, SymbolName: id, Type: string(kgtypes.NodeThought), UpdatedAt: 1000}
	kgtypes.SetValue(n, "cluster_id", clusterID)
	if source != "" {
		kgtypes.SetValue(n, "source", source)
	}
	if origin != "" {
		kgtypes.SetValue(n, "origin", origin)
	}
	c.thoughts[id] = n
	c.order = append(c.order, id)
}

func (c *blindSpotRenderCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		// Charge-thin corpus + no sessions/adjacency edges → every edge read empty.
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetById() != "" {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if len(q.GetIds()) > 0 {
		var nodes []*knowledgev1.Node
		for _, id := range q.GetIds() {
			if n, ok := c.thoughts[id]; ok {
				nodes = append(nodes, n)
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
	}
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	var nodes []*knowledgev1.Node
	for _, id := range c.order {
		nodes = append(nodes, c.thoughts[id])
	}
	return &knowledgev1.ExecuteResponse{Nodes: nodes}, nil
}

// TestHandleReflectBlindSpots (FAILS-WHEN-ABSENT) asserts the blind-spots render
// totals header names the under-evidenced count, total clusters, shown count, and
// the machine-genre excluded count. Corpus: one human-genre charge-thin cluster
// (surfaces) and one dream-genre cluster (excluded from the denominator).
func TestHandleReflectBlindSpots(t *testing.T) {
	c := newBlindSpotRenderCaller()
	// Human cluster "H": three charge-thin implementer thoughts → under-evidenced.
	c.addThought("h0", "H", "", "implementer")
	c.addThought("h1", "H", "", "implementer")
	c.addThought("h2", "H", "", "implementer")
	// Dream cluster "D": three dream-source thoughts → machine-genre, excluded.
	c.addThought("d0", "D", "dream:analyze", "main")
	c.addThought("d1", "D", "dream:analyze", "main")
	c.addThought("d2", "D", "dream:analyze", "main")

	deps := interceptTestDeps{gc: c}
	res := handleReflectBlindSpots(context.Background(), deps, queryReflectArgs{})
	text := resultText(res)

	assert.True(t, strings.HasPrefix(text, "# Blind Spots"), "render leads with the blind-spots header; got: %s", text)
	assert.Contains(t, text, "of 2 clusters", "header names the total cluster count (human + dream)")
	assert.Contains(t, text, "under-evidenced", "header names the under-evidenced count")
	assert.Contains(t, text, "showing top", "header names the shown count")
	assert.Contains(t, text, "1 machine-genre clusters excluded", "header names the machine-genre excluded count")
}
