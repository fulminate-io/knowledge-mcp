// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnvelope_JSONRoundTrip confirms the envelope is a plain encoding/json
// shape: a JSON object with graph_type/graph_name/nodes/edges decodes into a
// *Result, the node's typed fields and free-form metadata land, and re-encoding
// is stable. This is the proof that Node is a hand-written json-tagged struct
// (a proto knowledgev1.Node would not round-trip cleanly through encoding/json).
func TestEnvelope_JSONRoundTrip(t *testing.T) {
	raw := `{
		"graph_type": "jira",
		"graph_name": "acme-board",
		"nodes": [
			{
				"id": "ISSUE-1",
				"type": "issue",
				"symbol_name": "Login broken",
				"summary": "users cannot log in",
				"is_exported": true,
				"metadata": {"priority": "high", "assignee": "alice"}
			}
		],
		"edges": [
			{"from_id": "ISSUE-1", "to_id": "ISSUE-2", "type": "blocks", "confidence": 0.9}
		]
	}`

	var r Result
	require.NoError(t, json.Unmarshal([]byte(raw), &r))

	assert.Equal(t, "jira", r.GraphType)
	assert.Equal(t, "acme-board", r.GraphName)
	require.Len(t, r.Nodes, 1)
	assert.Equal(t, "ISSUE-1", r.Nodes[0].ID)
	assert.Equal(t, "issue", r.Nodes[0].Type)
	assert.Equal(t, "Login broken", r.Nodes[0].SymbolName)
	assert.True(t, r.Nodes[0].IsExported)
	assert.Equal(t, "high", r.Nodes[0].Metadata["priority"])
	assert.Equal(t, "alice", r.Nodes[0].Metadata["assignee"])
	require.Len(t, r.Edges, 1)
	assert.Equal(t, "ISSUE-1", r.Edges[0].FromID)
	assert.Equal(t, "ISSUE-2", r.Edges[0].ToID)
	assert.Equal(t, "blocks", r.Edges[0].Type)
	assert.InEpsilon(t, 0.9, r.Edges[0].Confidence, 1e-9)

	// Re-encode and re-decode: the shape is stable through encoding/json.
	encoded, err := json.Marshal(&r)
	require.NoError(t, err)
	var r2 Result
	require.NoError(t, json.Unmarshal(encoded, &r2))
	assert.Equal(t, r, r2)
}
