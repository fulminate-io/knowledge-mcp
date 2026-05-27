// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

func searchResp(t *testing.T, results []SearchResult) *knowledgev1.ExecuteResponse {
	t.Helper()
	return enginetest.SearchResponseWith(searchResultsToProtoForTest(results)...)
}

func TestLabelForSearch(t *testing.T) {
	assert.Equal(t, "q", labelForSearch("q", knowledgev1.SearchMode_SEARCH_MODE_HYBRID))
	assert.Equal(t, "q", labelForSearch("q", knowledgev1.SearchMode_SEARCH_MODE_UNSPECIFIED))
	assert.Equal(t, "q (PPR graph-reach)", labelForSearch("q", knowledgev1.SearchMode_SEARCH_MODE_PPR))
	assert.Equal(t, "q (recency-boosted)", labelForSearch("q", knowledgev1.SearchMode_SEARCH_MODE_TEMPORAL))
}

func TestRenderSearchResponse_Text(t *testing.T) {
	results := []SearchResult{
		{Score: 0.9, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit One", Summary: "sum1"}},
	}
	resp := searchResp(t, results)
	out, err := renderSearchResponse(resp, "q", "text", nil, knowledgev1.SearchMode_SEARCH_MODE_HYBRID)
	require.NoError(t, err)
	require.False(t, out.IsError)
	text := out.Content[0].Text
	assert.Contains(t, text, "Search: q (1 results)")
	assert.Contains(t, text, "[finding] Hit One (score=0.900)")
	assert.Contains(t, text, "sum1")
}

func TestRenderSearchResponse_PPRSuffix(t *testing.T) {
	resp := searchResp(t, []SearchResult{
		{Score: 0.5, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hub"}},
	})
	out, err := renderSearchResponse(resp, "hub", "text", nil, knowledgev1.SearchMode_SEARCH_MODE_PPR)
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "Search: hub (PPR graph-reach) (1 results)")
}

func TestRenderSearchResponse_TemporalSuffix(t *testing.T) {
	resp := searchResp(t, []SearchResult{
		{Score: 0.5, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Fresh"}},
	})
	out, err := renderSearchResponse(resp, "fresh", "text", nil, knowledgev1.SearchMode_SEARCH_MODE_TEMPORAL)
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "Search: fresh (recency-boosted) (1 results)")
}

func TestRenderSearchResponse_JSON(t *testing.T) {
	results := []SearchResult{
		{Score: 0.9, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit"}},
	}
	resp := searchResp(t, results)
	out, err := renderSearchResponse(resp, "q", "json", nil, knowledgev1.SearchMode_SEARCH_MODE_HYBRID)
	require.NoError(t, err)
	var env SearchJSONResponse
	require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &env))
	assert.Equal(t, "q", env.Query)
	require.Len(t, env.Results, 1)
	assert.Equal(t, "n1", env.Results[0].ID)
	assert.InEpsilon(t, 0.9, env.Results[0].Score, 0.0001)
}

func TestRenderSearchResponse_JSONProjected(t *testing.T) {
	results := []SearchResult{
		{Score: 0.9, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit",
			Metadata: map[string]string{"dsl_pattern": "p"}}},
	}
	resp := searchResp(t, results)
	out, err := renderSearchResponse(resp, "q", "json", []string{"id", "metadata.dsl_pattern"}, knowledgev1.SearchMode_SEARCH_MODE_HYBRID)
	require.NoError(t, err)
	// Projected response carries only requested keys.
	assert.Contains(t, out.Content[0].Text, `"id":"n1"`)
	assert.Contains(t, out.Content[0].Text, `"metadata.dsl_pattern":"p"`)
	assert.NotContains(t, out.Content[0].Text, `"symbol_name"`)
}

func TestRenderForCaller_EmptyResults(t *testing.T) {
	out := RenderForCaller("q", nil, "text", nil)
	assert.False(t, out.IsError)
	assert.True(t, strings.HasPrefix(out.Content[0].Text, "Search: q (0 results)"))
}
