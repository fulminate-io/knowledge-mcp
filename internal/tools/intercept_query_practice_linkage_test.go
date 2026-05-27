// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// plFake routes Execute by plan shape (search vs by-id vs Match) and Stats by
// canned graph stats, for the practice/linkage composer tests.
type plFake struct {
	searchResults []engine.SearchResult
	byIDNode      *knowledgev1.Node
	matchNodes    []knowledgev1.Node
	stats         *knowledgev1.GraphStats
}

func (f *plFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	switch {
	case len(q.GetQueries()) > 0:
		return &knowledgev1.ExecuteResponse{SearchResults: searchResultsToProtoForTest(f.searchResults)}, nil
	case q.GetById() != "":
		var nodes []*knowledgev1.Node
		if f.byIDNode != nil {
			nodes = []*knowledgev1.Node{f.byIDNode}
		}
		resp := enginetest.ResponseWithNodes(nodes...)
		return resp, nil
	default:
		resp := enginetest.ResponseWithNodes(nodePtrs(f.matchNodes)...)
		return resp, nil
	}
}

func (f *plFake) Stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{GraphStats: f.stats}, nil
}

// TestPracticeRoute_StatsAndSearch covers practice mode=stats and search.
func TestPracticeRoute_StatsAndSearch(t *testing.T) {
	t.Run("stats", func(t *testing.T) {
		f := &plFake{stats: &knowledgev1.GraphStats{NodeCount: 9, EdgeCount: 1, NodesByType: map[string]int64{"pattern": 9}}}
		res := routePracticeClient(context.Background(), nil, f, queryArgs{Graph: "practice", Language: "go", Mode: "stats"})
		body := textBodyTools(res)
		assert.Contains(t, body, "## Practice Graph: go")
		assert.Contains(t, body, "Nodes: 9")
	})

	t.Run("search renders Best Practices shape", func(t *testing.T) {
		f := &plFake{searchResults: []engine.SearchResult{
			{Score: 0.88, Node: &knowledgev1.Node{Id: "p1", SymbolName: "Use errgroup", Content: "do x", Status: "active",
				Metadata: map[string]string{"category": "concurrency", "importance": "high"}}},
		}}
		res := routePracticeClient(context.Background(), nil, f, queryArgs{Graph: "practice", Language: "go", Text: "errgroup"})
		body := textBodyTools(res)
		assert.Contains(t, body, "## Go Best Practices — 1 results for \"errgroup\"")
		assert.Contains(t, body, "### 1. Use errgroup [high] (concurrency)")
		assert.Contains(t, body, "ID: p1 | Status: active")
	})
}

// TestLinkageRoute_AllShapes covers linkage stats, id getNode, and search (proxy
// annotation reuse).
func TestLinkageRoute_AllShapes(t *testing.T) {
	t.Run("stats with proxy breakdown", func(t *testing.T) {
		f := &plFake{
			stats: &knowledgev1.GraphStats{NodeCount: 4, EdgeCount: 2, NodesByType: map[string]int64{"proxy": 4}},
			matchNodes: []knowledgev1.Node{
				{Id: "x1", Type: string(kgtypes.NodeProxy), Metadata: map[string]string{"foreign_graph": "code"}},
				{Id: "x2", Type: string(kgtypes.NodeProxy), Metadata: map[string]string{"foreign_graph": "cloud"}},
			},
		}
		res := routeLinkageClient(context.Background(), f, queryArgs{Graph: "linkage", Mode: "stats"})
		body := textBodyTools(res)
		assert.Contains(t, body, "## Linkage Graph")
		assert.Contains(t, body, "### Proxy Breakdown")
		assert.Contains(t, body, "- cloud: 1 proxies")
		assert.Contains(t, body, "- code: 1 proxies")
	})

	t.Run("id getNode", func(t *testing.T) {
		n := knowledgev1.Node{Id: "proxy:code:foo", SymbolName: "Foo", Type: string(kgtypes.NodeProxy)}
		f := &plFake{byIDNode: &n}
		res := routeLinkageClient(context.Background(), f, queryArgs{Graph: "linkage", ID: "proxy:code:foo"})
		assert.Contains(t, textBodyTools(res), "## linkage node")
	})

	t.Run("search reuses proxy annotation", func(t *testing.T) {
		// A proxy node renders with the engine proxyMetadataAnnotation shape
		// "[proxy → code:repo:nodeid]" — proving the helper is reused.
		proxy := knowledgev1.Node{
			Id: "proxy:code:repo:foo.go:Bar", SymbolName: "Bar", Type: string(kgtypes.NodeProxy),
			Metadata: map[string]string{"foreign_graph": "code", "foreign_name": "repo", "foreign_id": "foo.go:Bar"},
		}
		f := &plFake{searchResults: []engine.SearchResult{{Score: 0.7, Node: &proxy}}}
		res := routeLinkageClient(context.Background(), f, queryArgs{Graph: "linkage", Text: "bar"})
		body := textBodyTools(res)
		assert.Contains(t, body, "## Linkage — 1 results for \"bar\"")
		assert.Contains(t, body, "[proxy")
	})
}

// TestInterceptQueryPracticeLinkage_Gate asserts the intercept claims only
// practice/linkage and falls through (false) for other graphs/tools.
func TestInterceptQueryPracticeLinkage_Gate(t *testing.T) {
	handled, _ := InterceptQueryPracticeLinkage(nil, kgtools.CallToolParams{Name: "search", Arguments: json.RawMessage(`{}`)})
	assert.False(t, handled, "non-query tool not claimed")
}
