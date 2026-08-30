// SPDX-License-Identifier: Apache-2.0

package tools

// fake_graph_caller_seed_test.go holds the scripted GraphCaller's seeded-node
// decode/encode helpers and the seed constructor its tests drive it with.
//
// It is a sibling rather than part of fake_graph_caller_test.go for the same
// reason the graph-names and call-log helpers are already siblings: that file sits
// against the repo's per-file length ceiling, and a change to the fake cannot land
// while it is over.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// decodeSeededNode turns one seeded JSON body into a Node. Factored out of
// encodeNodeResult so the single-id and bulk paths share ONE decode — two copies
// would be free to drift on exactly the metadata round-trip the charge property
// fold depends on.
func decodeSeededNode(res kgtools.ToolResult) (*knowledgev1.Node, bool) {
	var body string
	if len(res.Content) > 0 {
		body = res.Content[0].Text
	}
	var n knowledgev1.Node
	if uerr := json.Unmarshal([]byte(body), &n); uerr != nil {
		return nil, false
	}
	return &n, true
}

// encodeNodeResult decodes a seeded single-node JSON body into a knowledgev1.Node and
// re-emits it as the nodes_json carrier ([]knowledgev1.Node), the shape render.Fetch-
// NodeIn decodes. A malformed seed surfaces as not-found.
func (f *fakeGraphCaller) encodeNodeResult(res kgtools.ToolResult) (*knowledgev1.ExecuteResponse, error) {
	n, decoded := decodeSeededNode(res)
	if !decoded {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return enginetest.ResponseWithNodes(n), nil
}

func nodeResultJSON(t *testing.T, id, typ string, metadata map[string]string) kgtools.ToolResult {
	t.Helper()
	payload := map[string]any{
		"id":       id,
		"type":     typ,
		"metadata": metadata,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	require.NoError(t, err)
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(b)}}}
}
