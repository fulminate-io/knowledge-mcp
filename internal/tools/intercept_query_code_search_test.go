// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// codeSearchFake routes RETURN_MODE_SEARCH Executes per (repo, query), counts
// them, and returns canned per-repo hits.
type codeSearchFake struct {
	mu          sync.Mutex
	searchCalls int
	byRepo      map[string][]engine.SearchResult
}

func (f *codeSearchFake) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	f.mu.Lock()
	f.searchCalls++
	f.mu.Unlock()
	repo := req.GetTarget().GetRepo()
	results := f.byRepo[repo]
	_ = q
	return &knowledgev1.ExecuteResponse{SearchResults: searchResultsToProtoForTest(results)}, nil
}

// TestInterceptQueryCodeSearch covers single-repo (one Execute per query),
// multi-repo parallel fan-out + merge-by-score, group_by_file, comment exclusion.
func TestInterceptQueryCodeSearch(t *testing.T) {
	t.Run("single repo one search per query + render", func(t *testing.T) {
		f := &codeSearchFake{byRepo: map[string][]engine.SearchResult{
			"knowledge": {
				{Score: 0.9, Node: &knowledgev1.Node{Id: "f.go:Foo", SymbolName: "Foo", Type: "function", FilePath: "f.go", StartLine: 1}},
			},
		}}
		res := composeCodeSearchSingleRepo(context.Background(), nil, f.exec,
			codeSearchArgs{Graph: "code", Repo: "knowledge", Text: "foo"}, []string{"foo"}, 10, true, false)
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.Equal(t, 1, f.searchCalls, "single query → one search Execute")
		assert.Contains(t, body, "[knowledge]")
		assert.Contains(t, body, "Found 1 results for \"foo\" (mode: hybrid):")
		assert.Contains(t, body, "### 1. Foo (function) — f.go:1 (score: 0.9000)")
	})

	t.Run("comment results excluded by default", func(t *testing.T) {
		f := &codeSearchFake{byRepo: map[string][]engine.SearchResult{
			"r": {
				{Score: 0.9, Node: &knowledgev1.Node{Id: "c1", Type: "comment", SymbolName: "c"}},
				{Score: 0.8, Node: &knowledgev1.Node{Id: "fn", Type: "function", SymbolName: "Fn", FilePath: "a.go"}},
			},
		}}
		res := composeCodeSearchSingleRepo(context.Background(), nil, f.exec,
			codeSearchArgs{Graph: "code", Repo: "r", Text: "x"}, []string{"x"}, 10, true, false)
		body := textBodyTools(res)
		assert.Contains(t, body, "Found 1 results")
		assert.Contains(t, body, "Fn")
		assert.NotContains(t, body, "### 1. c ")
	})

	t.Run("group_by_file groups returned results", func(t *testing.T) {
		f := &codeSearchFake{byRepo: map[string][]engine.SearchResult{
			"r": {
				{Score: 0.9, Node: &knowledgev1.Node{Id: "a", SymbolName: "A", Type: "function", FilePath: "a.go", StartLine: 1, EndLine: 5}},
				{Score: 0.8, Node: &knowledgev1.Node{Id: "b", SymbolName: "B", Type: "function", FilePath: "a.go", StartLine: 7, EndLine: 9}},
			},
		}}
		res := composeCodeSearchSingleRepo(context.Background(), nil, f.exec,
			codeSearchArgs{Graph: "code", Repo: "r", Text: "x"}, []string{"x"}, 10, true, true)
		body := textBodyTools(res)
		assert.Contains(t, body, "### a.go (2 symbols)")
		assert.Contains(t, body, "- `A` (function, L1-5)")
	})

	t.Run("multi-repo parallel fan-out + merge by score", func(t *testing.T) {
		f := &codeSearchFake{byRepo: map[string][]engine.SearchResult{
			"repo1": {{Score: 0.7, Node: &knowledgev1.Node{Id: "r1", SymbolName: "Low", Type: "function", FilePath: "x.go"}}},
			"repo2": {{Score: 0.95, Node: &knowledgev1.Node{Id: "r2", SymbolName: "High", Type: "function", FilePath: "y.go"}}},
		}}
		res := composeCodeSearchMultiRepo(context.Background(), nil, f.exec,
			codeSearchArgs{Graph: "code", Repos: []string{"repo1", "repo2"}, Text: "x"}, []string{"x"}, 10, true, false)
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.Equal(t, 2, f.searchCalls, "one search Execute per repo")
		assert.Contains(t, body, "Cross-repo search across repo1, repo2")
		// High (0.95) sorts before Low (0.70).
		assert.Less(t, indexOf(body, "High"), indexOf(body, "Low"))
		assert.Contains(t, body, "[repo2] High")
	})

	t.Run("gate: id → not claimed (analyze)", func(t *testing.T) {
		handled, _ := InterceptQueryCodeSearch(nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"code","id":"x"}`)})
		assert.False(t, handled)
	})
	t.Run("gate: no query → not claimed", func(t *testing.T) {
		handled, _ := InterceptQueryCodeSearch(nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"code"}`)})
		assert.False(t, handled)
	})
}
