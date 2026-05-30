// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
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
	out, err := renderSearchResponse(resp, "q", "text", nil, knowledgev1.SearchMode_SEARCH_MODE_HYBRID, "BM25-only")
	require.NoError(t, err)
	require.False(t, out.IsError)
	text := out.Content[0].Text
	assert.Contains(t, text, "Search: q (1 results)")
	assert.Contains(t, text, "[finding] Hit One (score=0.900)")
	assert.Contains(t, text, "sum1")
	assert.Contains(t, text, "_search mode: BM25-only_")
}

func TestRenderSearchResponse_PPRSuffix(t *testing.T) {
	resp := searchResp(t, []SearchResult{
		{Score: 0.5, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hub"}},
	})
	out, err := renderSearchResponse(resp, "hub", "text", nil, knowledgev1.SearchMode_SEARCH_MODE_PPR, "BM25-only")
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "Search: hub (PPR graph-reach) (1 results)")
}

func TestRenderSearchResponse_TemporalSuffix(t *testing.T) {
	resp := searchResp(t, []SearchResult{
		{Score: 0.5, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Fresh"}},
	})
	out, err := renderSearchResponse(resp, "fresh", "text", nil, knowledgev1.SearchMode_SEARCH_MODE_TEMPORAL, "BM25-only")
	require.NoError(t, err)
	assert.Contains(t, out.Content[0].Text, "Search: fresh (recency-boosted) (1 results)")
}

func TestRenderSearchResponse_JSON(t *testing.T) {
	results := []SearchResult{
		{Score: 0.9, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit"}},
	}
	resp := searchResp(t, results)
	out, err := renderSearchResponse(resp, "q", "json", nil, knowledgev1.SearchMode_SEARCH_MODE_HYBRID, "vector")
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
	out, err := renderSearchResponse(resp, "q", "json", []string{"id", "metadata.dsl_pattern"}, knowledgev1.SearchMode_SEARCH_MODE_HYBRID, "vector+rerank")
	require.NoError(t, err)
	// Projected response carries only requested keys.
	assert.Contains(t, out.Content[0].Text, `"id":"n1"`)
	assert.Contains(t, out.Content[0].Text, `"metadata.dsl_pattern":"p"`)
	assert.NotContains(t, out.Content[0].Text, `"symbol_name"`)
	// JSON arm is untouched — no mode footer leaks into structured output.
	assert.NotContains(t, out.Content[0].Text, "search mode:")
}

func TestRenderForCaller_EmptyResults(t *testing.T) {
	out := RenderForCaller("q", nil, "text", nil, "BM25-only")
	assert.False(t, out.IsError)
	assert.True(t, strings.HasPrefix(out.Content[0].Text, "Search: q (0 results)"))
	assert.Contains(t, out.Content[0].Text, "_search mode: BM25-only_")
}

func TestSearchModeLabel(t *testing.T) {
	assert.Equal(t, "vector+rerank", searchModeLabel(true, true))
	assert.Equal(t, "vector", searchModeLabel(true, false))
	assert.Equal(t, "BM25-only", searchModeLabel(false, false))
	// rerank cannot run without an embedding; the !embedded branch wins.
	assert.Equal(t, "BM25-only", searchModeLabel(false, true))
}

// TestRenderSearchTool_FooterEmbedSignal proves the search-tool arm's footer is
// keyed on the PER-REQUEST query_vector presence, not on any key state: a
// query_vector → "vector", absent → "BM25-only".
func TestRenderSearchTool_FooterEmbedSignal(t *testing.T) {
	resp := searchResp(t, []SearchResult{
		{Score: 0.9, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit"}},
	})

	t.Run("query_vector present → vector", func(t *testing.T) {
		args, err := json.Marshal(searchArgs{Query: "q", Format: "text", QueryVector: "AAAA"})
		require.NoError(t, err)
		out, err := renderSearchTool(args, resp)
		require.NoError(t, err)
		assert.Contains(t, out.Content[0].Text, "_search mode: vector_")
	})

	t.Run("no query_vector → BM25-only", func(t *testing.T) {
		args, err := json.Marshal(searchArgs{Query: "q", Format: "text"})
		require.NoError(t, err)
		out, err := renderSearchTool(args, resp)
		require.NoError(t, err)
		assert.Contains(t, out.Content[0].Text, "_search mode: BM25-only_")
	})
}

// TestRenderQueryTool_FooterInvertedLie is the load-bearing regression guard:
// query(mode:"text") with NO query_vector must render BM25-only EVEN if a
// Voyage key is configured (the server never embeds, InterceptQuery does not
// embed the text mode). Keying the footer off key-presence would invert the
// truth here. A request that DID carry a query_vector renders vector.
func TestRenderQueryTool_FooterInvertedLie(t *testing.T) {
	resp := searchResp(t, []SearchResult{
		{Score: 0.9, Node: &knowledgev1.Node{Id: "n1", Type: "finding", SymbolName: "Hit"}},
	})

	t.Run("text mode, no query_vector → BM25-only", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{Mode: "text", Text: "q", Format: "text"})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		assert.Contains(t, out.Content[0].Text, "_search mode: BM25-only_")
	})

	t.Run("bare default text, no query_vector → BM25-only", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{Text: "q", Format: "text"})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		assert.Contains(t, out.Content[0].Text, "_search mode: BM25-only_")
	})

	t.Run("graph_reach, no query_vector → BM25-only", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{Mode: "graph_reach", Text: "q", Format: "text"})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		assert.Contains(t, out.Content[0].Text, "_search mode: BM25-only_")
	})

	t.Run("query carrying a query_vector → vector", func(t *testing.T) {
		args := queryArgsJSON(t, queryArgs{Mode: "text", Text: "q", Format: "text", QueryVector: "AAAA"})
		out, err := renderQueryTool(args, resp)
		require.NoError(t, err)
		assert.Contains(t, out.Content[0].Text, "_search mode: vector_")
	})
}
