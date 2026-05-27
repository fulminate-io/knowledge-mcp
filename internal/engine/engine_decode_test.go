// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// decodeResult is the typed-wire shape the round-trip drives: each return mode
// owns one sub-list, all keyed off the typed *knowledgev1.Node carriers. P2-T5
// (FUL-295) deleted the nodes_json/node_json blob path, so the fixtures build
// the typed Nodes / SearchResults / TraversalResults fields directly via the
// shared enginetest builders — never a store engine, never a JSON blob.
type decodeResult struct {
	nodeList      []*knowledgev1.Node
	searchList    []SearchResult
	traversalList []TraversalResult
	idList        []string
	total         int
}

// encodeResultToResponse builds the typed ExecuteResponse the decoders consume,
// delegating to the shared enginetest builders for the node/search/traversal
// carriers (each gated on a populated sub-list) and threading Ids/Total natively.
func encodeResultToResponse(res decodeResult) *knowledgev1.ExecuteResponse {
	var resp *knowledgev1.ExecuteResponse
	switch {
	case len(res.nodeList) > 0:
		resp = enginetest.ResponseWithNodes(res.nodeList...)
	case len(res.searchList) > 0:
		resp = enginetest.SearchResponseWith(searchResultsToProtoForTest(res.searchList)...)
	case len(res.traversalList) > 0:
		resp = enginetest.TraversalResponseWith(traversalResultsToProtoForTest(res.traversalList)...)
	default:
		resp = &knowledgev1.ExecuteResponse{}
	}
	resp.Ids = res.idList
	resp.Total = int64(res.total)
	return resp
}

func TestEngineDecode_RoundTrip(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "n1", Type: "finding", SymbolName: "Finding One", Summary: "s1",
			Metadata: map[string]string{"k": "v"}},
		{Id: "n2", Type: "decision", Description: "d2"},
	}
	search := []SearchResult{
		{Score: 0.9, Node: &knowledgev1.Node{Id: "s1", Type: "finding", SymbolName: "Hit"}},
		{Score: 0.5, Node: &knowledgev1.Node{Id: "s2", Type: "rule"}},
	}
	traversal := []TraversalResult{
		{Distance: 0, Node: &knowledgev1.Node{Id: "t0", Type: "plan"}},
		{Distance: 2, Node: &knowledgev1.Node{Id: "t2", Type: "phase"}},
	}

	tests := []struct {
		name string
		res  decodeResult
	}{
		{
			name: "nodes mode",
			res:  decodeResult{nodeList: nodes, total: 2},
		},
		{
			name: "search mode",
			res:  decodeResult{searchList: search, total: 2},
		},
		{
			name: "traversal mode",
			res:  decodeResult{traversalList: traversal, total: 2},
		},
		{
			name: "ids mode",
			res:  decodeResult{idList: []string{"a", "b", "c"}, total: 3},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := encodeResultToResponse(tc.res)

			gotNodes, err := decodeNodes(resp)
			require.NoError(t, err)
			assert.Equal(t, tc.res.nodeList, gotNodes)

			gotSearch, err := decodeSearch(resp)
			require.NoError(t, err)
			assert.Equal(t, tc.res.searchList, gotSearch)

			gotTraversal, err := decodeTraversal(resp)
			require.NoError(t, err)
			assert.Equal(t, tc.res.traversalList, gotTraversal)

			// IDs / Total ride native — verify the getters reproduce them.
			assert.Equal(t, tc.res.idList, resp.GetIds())
			assert.Equal(t, int64(tc.res.total), resp.GetTotal())
		})
	}
}

// TestEngineDecode_EmptyBlob asserts each decoder returns an empty slice with
// nil error when its carrier is absent (the normal case for the other modes).
func TestEngineDecode_EmptyBlob(t *testing.T) {
	resp := &knowledgev1.ExecuteResponse{}

	gotNodes, err := decodeNodes(resp)
	require.NoError(t, err)
	assert.Empty(t, gotNodes)

	gotSearch, err := decodeSearch(resp)
	require.NoError(t, err)
	assert.Empty(t, gotSearch)

	gotTraversal, err := decodeTraversal(resp)
	require.NoError(t, err)
	assert.Empty(t, gotTraversal)
}
