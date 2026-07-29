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

// universeFake serves the three reads the charge-seeded universe build issues: the
// type=charge seed browse (paged on GetLimit()/GetOffset() exactly as the server's
// applyNodePage does), the bulk EdgeChargedBy read, and the parent hydrate. Its
// backing corpus deliberately includes a charged DECISION parent and an UNCHARGED
// thought, so the test can prove both narrowings — the claim-type filter and the
// charged-only universe.
type universeFake struct {
	charges     []*knowledgev1.Node
	chargeEdges []*knowledgev1.Edge
	nodes       map[string]*knowledgev1.Node
	// browsedTypes records every Selection.NodeType browsed, so the test can assert
	// the claim corpus is NEVER drained by type.
	browsedTypes []string
}

func (f *universeFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: f.chargeEdges}, nil
	}
	if len(q.GetIds()) > 0 {
		var out []*knowledgev1.Node
		for _, id := range q.GetIds() {
			if n, ok := f.nodes[id]; ok {
				out = append(out, n)
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: out}, nil
	}
	sel := q.GetSelection()
	if sel == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	f.browsedTypes = append(f.browsedTypes, sel.GetNodeType())
	if sel.GetNodeType() != string(kgtypes.NodeCharge) {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	offset, limit := int(q.GetOffset()), int(q.GetLimit())
	if offset >= len(f.charges) {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(f.charges) {
		end = len(f.charges)
	}
	return &knowledgev1.ExecuteResponse{Nodes: f.charges[offset:end]}, nil
}

func node(id, nodeType string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: nodeType}
}

func chargedBy(parent, charge string) *knowledgev1.Edge {
	return &knowledgev1.Edge{Type: string(kgtypes.EdgeChargedBy), FromId: parent, ToId: charge}
}

// newUniverseFake seeds one charge per claim parent across the three admissible
// types plus a charged decision (must be filtered out by type) and an uncharged
// thought (must never enter — nothing links it to a charge).
func newUniverseFake() *universeFake {
	return &universeFake{
		charges: []*knowledgev1.Node{
			node("c-th-1", string(kgtypes.NodeCharge)),
			node("c-th-2", string(kgtypes.NodeCharge)),
			node("c-f-1", string(kgtypes.NodeCharge)),
			node("c-r-1", string(kgtypes.NodeCharge)),
			node("c-d-1", string(kgtypes.NodeCharge)),
		},
		chargeEdges: []*knowledgev1.Edge{
			chargedBy("th-1", "c-th-1"),
			chargedBy("th-2", "c-th-2"),
			chargedBy("f-1", "c-f-1"),
			chargedBy("r-1", "c-r-1"),
			chargedBy("d-1", "c-d-1"),
		},
		nodes: map[string]*knowledgev1.Node{
			"th-1":      node("th-1", string(kgtypes.NodeThought)),
			"th-2":      node("th-2", string(kgtypes.NodeThought)),
			"f-1":       node("f-1", string(kgtypes.NodeFinding)),
			"r-1":       node("r-1", string(kgtypes.NodeResearch)),
			"d-1":       node("d-1", string(kgtypes.NodeDecision)),
			"uncharged": node("uncharged", string(kgtypes.NodeThought)),
		},
	}
}

// TestFetchTensionUniverseNodes proves the charge-seeded universe admits exactly the
// three chargeable claim types (thought + finding + research) reached as charge
// PARENTS, filters out a charged 4th type (decision), never returns an uncharged
// node, and never drains a claim-type browse at all.
func TestFetchTensionUniverseNodes(t *testing.T) {
	f := newUniverseFake()

	got, charges, err := fetchTensionUniverseNodes(context.Background(), f, nil)
	require.NoError(t, err)

	gotIDs := make(map[string]string, len(got))
	for _, n := range got {
		gotIDs[n.Id] = n.Type
	}
	assert.Len(t, got, 4, "the charged thought+finding+research parents enter the universe (2+1+1)")
	assert.Contains(t, gotIDs, "th-1")
	assert.Contains(t, gotIDs, "th-2")
	assert.Contains(t, gotIDs, "f-1")
	assert.Contains(t, gotIDs, "r-1")
	assert.NotContains(t, gotIDs, "d-1", "a 4th type (decision) must NOT enter the tension universe even when charged")
	assert.NotContains(t, gotIDs, "uncharged",
		"an uncharged node can never qualify (magnitude 0) and must not enter the universe")

	// The charge map comes back joined per parent, so ReflectTensions needs no
	// second full-universe charge read.
	require.Len(t, charges["th-1"], 1)
	assert.Equal(t, "c-th-1", charges["th-1"][0].GetId())
	assert.NotContains(t, charges, "d-1", "a filtered-out parent carries no charge entry")

	assert.Equal(t, []string{string(kgtypes.NodeCharge)}, f.browsedTypes,
		"the ONLY type browse is the charge seed — the claim corpus is never drained by type")
}

// TestFetchTensionUniverseNodes_NilCaller returns nil without panicking.
func TestFetchTensionUniverseNodes_NilCaller(t *testing.T) {
	got, charges, err := fetchTensionUniverseNodes(context.Background(), nil, nil)
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Nil(t, charges)
}
