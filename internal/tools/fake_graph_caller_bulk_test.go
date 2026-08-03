// SPDX-License-Identifier: Apache-2.0

package tools

// fake_graph_caller_bulk_test.go holds the self-test for fakeGraphCaller's
// PLURAL-Ids arms. Split from fake_graph_caller_test.go (which owns the fake
// itself) only to keep that file inside the repo's file-length cap.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestFakeGraphCaller_BulkIDsEdgesAndHydrate makes the two plural-Ids arms'
// semantics falsifiable rather than inferred from a downstream test, following
// the same precedent as the ordinal-knob test above.
//
// It matters more than a usual harness self-test because BOTH omissions render
// the SAME downstream symptom: drop the edges arm and the charge readout shows
// "Charges: 0" because no edge resolves; drop the hydrate arm and it shows
// "Charges: 0" because the edge resolves but the charge never hydrates. That
// string is also the pre-fix bug symptom, so without this test a broken harness
// and a real defect are indistinguishable.
func TestFakeGraphCaller_BulkIDsEdgesAndHydrate(t *testing.T) {
	const (
		thoughtID = "t-full"
		chargeID  = "c1"
	)
	fc := &fakeGraphCaller{
		edgesByID: map[string][]*knowledgev1.Edge{
			thoughtID: {{Type: string(kgtypes.EdgeChargedBy), FromId: thoughtID, ToId: chargeID}},
		},
		queryResponses: map[string]kgtools.ToolResult{
			chargeID: nodeResultJSON(t, chargeID, "charge", map[string]string{
				"polarity": "positive", "weight": "7",
			}),
		},
	}

	t.Run("plural EDGES read returns the seeded charged-by edge", func(t *testing.T) {
		resp, err := fc.Execute(context.Background(), &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Ids:        []string{thoughtID},
				ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_EDGES,
			}},
		})
		require.NoError(t, err)
		require.Len(t, resp.GetEdges(), 1, "the plural EDGES arm must serve the seeded edge")
		assert.Equal(t, thoughtID, resp.GetEdges()[0].GetFromId())
		assert.Equal(t, chargeID, resp.GetEdges()[0].GetToId())
	})

	t.Run("plural default-mode read hydrates the charge node with its metadata", func(t *testing.T) {
		resp, err := fc.Execute(context.Background(), &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Ids: []string{chargeID},
			}},
		})
		require.NoError(t, err)
		nodes, derr := engine.DecodeNodes(resp)
		require.NoError(t, derr)
		require.Len(t, nodes, 1, "the bulk-hydrate arm must serve the seeded charge node")
		assert.Equal(t, chargeID, nodes[0].GetId())
		// The metadata round-trip is the load-bearing half: it carries the
		// polarity and weight the charge property fold consumes.
		assert.Equal(t, "positive", nodes[0].GetMetadata()["polarity"])
		assert.Equal(t, "7", nodes[0].GetMetadata()["weight"])
	})
}
