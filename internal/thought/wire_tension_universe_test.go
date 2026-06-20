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

// universeFake serves a single-type browse drain per requested Selection.NodeType
// (the singular node_type browse selector buildDefaultModePlan emits), slicing the
// matching backing corpus on GetLimit()/GetOffset() exactly as the server's
// applyNodePage does. A type with no seeded corpus returns an empty page, so a
// drain for it terminates cleanly returning nothing — which is how the test proves
// fetchTensionUniverseNodes NEVER requests a 4th type (decision): the decision
// corpus is seeded but, because the helper only drains thought/finding/research,
// it is never browsed and never surfaces.
type universeFake struct {
	byType map[string][]*knowledgev1.Node
}

func (f *universeFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	sel := q.GetSelection()
	if sel == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	corpus := f.byType[sel.GetNodeType()]
	offset := int(q.GetOffset())
	limit := int(q.GetLimit())
	if offset >= len(corpus) {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(corpus) {
		end = len(corpus)
	}
	return &knowledgev1.ExecuteResponse{Nodes: corpus[offset:end]}, nil
}

func node(id, nodeType string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: nodeType}
}

// TestFetchTensionUniverseNodes proves the helper drains exactly the three
// chargeable claim node types (thought + finding + research) and merges them,
// while a 4th type (decision) seeded in the corpus is NOT returned (the helper
// never browses it).
func TestFetchTensionUniverseNodes(t *testing.T) {
	f := &universeFake{byType: map[string][]*knowledgev1.Node{
		string(kgtypes.NodeThought):  {node("th-1", string(kgtypes.NodeThought)), node("th-2", string(kgtypes.NodeThought))},
		string(kgtypes.NodeFinding):  {node("f-1", string(kgtypes.NodeFinding))},
		string(kgtypes.NodeResearch): {node("r-1", string(kgtypes.NodeResearch))},
		// Decision corpus is seeded but must never be browsed/returned.
		string(kgtypes.NodeDecision): {node("d-1", string(kgtypes.NodeDecision))},
	}}

	got, err := fetchTensionUniverseNodes(context.Background(), f)
	require.NoError(t, err)

	gotIDs := make(map[string]string, len(got))
	for _, n := range got {
		gotIDs[n.Id] = n.Type
	}
	assert.Len(t, got, 4, "all thought+finding+research nodes drained (2+1+1)")
	assert.Contains(t, gotIDs, "th-1")
	assert.Contains(t, gotIDs, "th-2")
	assert.Contains(t, gotIDs, "f-1")
	assert.Contains(t, gotIDs, "r-1")
	assert.NotContains(t, gotIDs, "d-1", "a 4th type (decision) must NOT enter the tension universe")
}

// TestFetchTensionUniverseNodes_NilCaller returns nil without panicking.
func TestFetchTensionUniverseNodes_NilCaller(t *testing.T) {
	got, err := fetchTensionUniverseNodes(context.Background(), nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}
