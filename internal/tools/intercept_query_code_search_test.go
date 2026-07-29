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
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// codeSearchEngineFake drives the CLIENT code-search path: Manager.Search
// returns canned RRF Hits per (repo, query) and the hydrate Execute serves the
// ranked ids[] read from a shared node table. It satisfies BOTH the
// SegmentSearcher seam (Search) and the GraphCaller seam (Execute), so one fake
// backs cdeps.mgr and cdeps.gc.
type codeSearchEngineFake struct {
	mu          sync.Mutex
	searchCalls int
	hitsByRepo  map[string][]searchengine.Hit
	nodes       map[string]*knowledgev1.Node
}

func (f *codeSearchEngineFake) Search(
	_ context.Context, _ kgtypes.GraphType, name, _ string, _ []byte, _ int,
) ([]searchengine.Hit, error) {
	f.mu.Lock()
	f.searchCalls++
	f.mu.Unlock()
	return f.hitsByRepo[name], nil
}

func (f *codeSearchEngineFake) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	// Serve the ids[] hydrate read from the shared node table.
	q := req.GetQuery()
	out := make([]*knowledgev1.Node, 0)
	if q != nil {
		for _, id := range q.GetIds() {
			if n, ok := f.nodes[id]; ok {
				out = append(out, n)
			}
		}
	}
	return &knowledgev1.ExecuteResponse{Nodes: out}, nil
}

func (f *codeSearchEngineFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searchCalls
}

// cdepsFor wires a codeSearchDeps where both the Searcher and the hydrate
// GraphCaller are the same fake.
func cdepsFor(f *codeSearchEngineFake) codeSearchDeps {
	return codeSearchDeps{mgr: f, gc: f, exec: f.Execute}
}

// TestInterceptQueryCodeSearch covers single-repo (one client Search per query),
// multi-repo parallel fan-out + merge-by-score, group_by_file, comment exclusion —
// all driven through the CLIENT engine (no server-search fallback).
func TestInterceptQueryCodeSearch(t *testing.T) {
	t.Run("single repo one search per query + render", func(t *testing.T) {
		f := &codeSearchEngineFake{
			hitsByRepo: map[string][]searchengine.Hit{"knowledge": {{ID: "f.go:Foo", Score: 0.9}}},
			nodes: map[string]*knowledgev1.Node{
				"f.go:Foo": {Id: "f.go:Foo", SymbolName: "Foo", Type: "function", FilePath: "f.go", StartLine: 1},
			},
		}
		res := composeCodeSearchSingleRepo(context.Background(), nil, cdepsFor(f),
			codeSearchArgs{Graph: "code", Repo: "knowledge", Text: "foo"}, []string{"foo"}, nil, 10, true, false)
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.Equal(t, 1, f.calls(), "single query → one client Search")
		assert.Contains(t, body, "[knowledge]")
		// No vector threaded (nil queryVecs) → mode label is "text", not "hybrid".
		assert.Contains(t, body, "Found 1 results for \"foo\" (mode: text):")
		assert.Contains(t, body, "### 1. Foo (function) — f.go:1 (score: 0.9000)")
	})

	t.Run("comment results excluded by default", func(t *testing.T) {
		f := &codeSearchEngineFake{
			hitsByRepo: map[string][]searchengine.Hit{"r": {{ID: "c1", Score: 0.9}, {ID: "fn", Score: 0.8}}},
			nodes: map[string]*knowledgev1.Node{
				"c1": {Id: "c1", Type: "comment", SymbolName: "c"},
				"fn": {Id: "fn", Type: "function", SymbolName: "Fn", FilePath: "a.go"},
			},
		}
		res := composeCodeSearchSingleRepo(context.Background(), nil, cdepsFor(f),
			codeSearchArgs{Graph: "code", Repo: "r", Text: "x"}, []string{"x"}, nil, 10, true, false)
		body := textBodyTools(res)
		assert.Contains(t, body, "Found 1 results")
		assert.Contains(t, body, "Fn")
		assert.NotContains(t, body, "### 1. c ")
	})

	t.Run("group_by_file groups returned results", func(t *testing.T) {
		f := &codeSearchEngineFake{
			hitsByRepo: map[string][]searchengine.Hit{"r": {{ID: "a", Score: 0.9}, {ID: "b", Score: 0.8}}},
			nodes: map[string]*knowledgev1.Node{
				"a": {Id: "a", SymbolName: "A", Type: "function", FilePath: "a.go", StartLine: 1, EndLine: 5},
				"b": {Id: "b", SymbolName: "B", Type: "function", FilePath: "a.go", StartLine: 7, EndLine: 9},
			},
		}
		res := composeCodeSearchSingleRepo(context.Background(), nil, cdepsFor(f),
			codeSearchArgs{Graph: "code", Repo: "r", Text: "x"}, []string{"x"}, nil, 10, true, true)
		body := textBodyTools(res)
		assert.Contains(t, body, "### a.go (2 symbols)")
		assert.Contains(t, body, "- `A` (function, L1-5)")
	})

	t.Run("multi-repo parallel fan-out + merge by score", func(t *testing.T) {
		f := &codeSearchEngineFake{
			hitsByRepo: map[string][]searchengine.Hit{
				"repo1": {{ID: "r1", Score: 0.7}},
				"repo2": {{ID: "r2", Score: 0.95}},
			},
			nodes: map[string]*knowledgev1.Node{
				"r1": {Id: "r1", SymbolName: "Low", Type: "function", FilePath: "x.go"},
				"r2": {Id: "r2", SymbolName: "High", Type: "function", FilePath: "y.go"},
			},
		}
		res := composeCodeSearchMultiRepo(context.Background(), nil, cdepsFor(f),
			codeSearchArgs{Graph: "code", Repos: []string{"repo1", "repo2"}, Text: "x"}, []string{"x"}, nil, 10, true, false)
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.Equal(t, 2, f.calls(), "one client Search per repo")
		assert.Contains(t, body, "Cross-repo search across repo1, repo2")
		// High (0.95) sorts before Low (0.70).
		assert.Less(t, indexOf(body, "High"), indexOf(body, "Low"))
		assert.Contains(t, body, "[repo2] High")
	})

	t.Run("gate: id → not claimed (analyze)", func(t *testing.T) {
		handled, _ := InterceptQueryCodeSearch(opCtx(), nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"code","id":"x"}`)})
		assert.False(t, handled)
	})
	t.Run("gate: no query → not claimed", func(t *testing.T) {
		handled, _ := InterceptQueryCodeSearch(opCtx(), nil, kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"graph":"code"}`)})
		assert.False(t, handled)
	})
}
