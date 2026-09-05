// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// originThoughtNodeBody returns the first created thought NodeBody across all
// recorded create_batch mutation plans (the create_batch carrying the thought is
// the one with a non-empty NodeBodies list).
func originThoughtNodeBody(fc *backfillFakeCaller) *knowledgev1.NodeBody {
	for _, m := range fc.mutations {
		if bodies := m.GetNodeBodies(); len(bodies) > 0 {
			return bodies[0]
		}
	}
	return nil
}

// producedEdges collects every EdgeProduced BatchEdgeSpec across all recorded
// mutation plans.
func producedEdges(fc *backfillFakeCaller) []*knowledgev1.BatchEdgeSpec {
	var out []*knowledgev1.BatchEdgeSpec
	for _, m := range fc.mutations {
		for _, e := range m.GetEdges() {
			if e.GetType() == string(kgtypes.EdgeProduced) {
				out = append(out, e)
			}
		}
	}
	return out
}

// TestComposeThoughtCreate_Origin asserts the three origin facets on the atomic
// think create_batch: a resolvable origin stamps origin metadata AND rides one
// agent--produced-->thought hub edge; an unresolvable origin stamps the metadata
// with NO produced edge; an absent origin normalizes to "main" metadata with NO
// produced edge.
func TestComposeThoughtCreate_Origin(t *testing.T) {
	ctx := context.Background()

	// (1) RESOLVABLE origin: a user-authored "planner" agent node exists.
	t.Run("resolvable stamps metadata + produced edge", func(t *testing.T) {
		fc := &backfillFakeCaller{
			agents:    []*knowledgev1.Node{agentNode("agent-planner", "planner")},
			mutateIDs: []string{"th-new"},
		}
		_, err := composeThoughtCreate(ctx, fc, composeThoughtArgs{
			Content: "a planner thought",
			Summary: "planner thought summary",
			Origin:  "planner",
		})
		require.NoError(t, err)

		body := originThoughtNodeBody(fc)
		require.NotNil(t, body, "a thought NodeBody must have been created")
		assert.Equal(t, "planner", body.GetMetadata()["origin"], "origin metadata must be stamped")

		edges := producedEdges(fc)
		require.Len(t, edges, 1, "exactly one agent--produced-->thought hub edge")
		e := edges[0]
		assert.Equal(t, "agent-planner", e.GetFromId(), "FromId is the agent node")
		assert.Equal(t, int32(-1), e.GetFromIdx(), "agent is an existing-node From endpoint")
		assert.Equal(t, int32(0), e.GetToIdx(), "thought is slot 0 (the To endpoint)")
		assert.Equal(t, string(kgtypes.EdgeProduced), e.GetType())
	})

	// (2) UNRESOLVABLE origin: no agent node carries this SymbolName.
	t.Run("unresolvable stamps metadata only, no edge", func(t *testing.T) {
		fc := &backfillFakeCaller{
			agents:    []*knowledgev1.Node{agentNode("agent-planner", "planner")},
			mutateIDs: []string{"th-new"},
		}
		_, err := composeThoughtCreate(ctx, fc, composeThoughtArgs{
			Content: "a main thought",
			Summary: "main thought summary",
			Origin:  "main",
		})
		require.NoError(t, err)

		body := originThoughtNodeBody(fc)
		require.NotNil(t, body)
		assert.Equal(t, "main", body.GetMetadata()["origin"], "origin=main metadata still stamped")
		assert.Empty(t, producedEdges(fc), "an unresolvable origin writes NO produced edge")
	})

	// (3) ABSENT origin: normalizes to "main" metadata, no edge.
	t.Run("absent normalizes to main metadata, no edge", func(t *testing.T) {
		fc := &backfillFakeCaller{
			agents:    []*knowledgev1.Node{agentNode("agent-planner", "planner")},
			mutateIDs: []string{"th-new"},
		}
		_, err := composeThoughtCreate(ctx, fc, composeThoughtArgs{
			Content: "an originless thought",
			Summary: "originless thought summary",
			// Origin intentionally left empty.
		})
		require.NoError(t, err)

		body := originThoughtNodeBody(fc)
		require.NotNil(t, body)
		assert.Equal(t, "main", body.GetMetadata()["origin"], "absent origin normalizes to main")
		assert.Empty(t, producedEdges(fc), "absent origin writes NO produced edge")
	})
}
