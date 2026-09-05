// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// bulkGc answers an Ids[] by-id read out of a node map, records the size of
// every id page it was asked for, and can stamp Truncated on its responses.
type bulkGc struct {
	nodes     map[string]*knowledgev1.Node
	pageSizes []int
	truncate  bool
}

func (b *bulkGc) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.TextResult(""), nil
}

func (b *bulkGc) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	ids := req.GetQuery().GetIds()
	b.pageSizes = append(b.pageSizes, len(ids))
	var out []*knowledgev1.Node
	for _, id := range ids {
		if n, ok := b.nodes[id]; ok {
			out = append(out, n)
		}
	}
	return &knowledgev1.ExecuteResponse{Nodes: out, Truncated: b.truncate}, nil
}

// TestFetchNodesByIDs_PagedOneExecutePerPage pins the bulk hydrate's three
// load-bearing properties: it costs one Execute per PAGE rather than one per id,
// it returns every requested node keyed by id, and it carries the server's
// truncation verdict out instead of dropping it.
//
// The truncation leg matters most. The server flags truncation off the request's
// id count alone, so a clamped bulk read comes back short with the missing nodes
// simply absent — indistinguishable, without the verdict, from a complete list.
func TestFetchNodesByIDs_PagedOneExecutePerPage(t *testing.T) {
	// More ids than one page holds, so the paging path actually executes rather
	// than being covered only at single-page width.
	const total = paging.BrowsePageSize + 37
	nodes := make(map[string]*knowledgev1.Node, total)
	ids := make([]string, 0, total)
	for i := range total {
		id := fmt.Sprintf("n-%04d", i)
		ids = append(ids, id)
		nodes[id] = &knowledgev1.Node{Id: id, SymbolName: "Node " + id, Type: string(kgtypes.NodeStep)}
	}

	t.Run("one Execute per page, every node returned", func(t *testing.T) {
		gc := &bulkGc{nodes: nodes}
		got, truncated, err := foundation.FetchNodesByIDs(context.Background(), gc, "", "", ids, foundation.IncludeTombstones)
		require.NoError(t, err)

		assert.False(t, truncated)
		require.Len(t, got, total, "every requested node comes back")
		assert.Equal(t, "Node n-0000", got["n-0000"].SymbolName)
		assert.Equal(t, "Node n-0536", got["n-0536"].SymbolName)

		// Two pages, not 537 single fetches — and the page sizes are asserted
		// rather than just their count, so a helper that issued one Execute per
		// id would fail here on the sizes as well as the length.
		assert.Equal(t, []int{paging.BrowsePageSize, 37}, gc.pageSizes)
	})

	t.Run("an id with no node is simply absent from the map", func(t *testing.T) {
		gc := &bulkGc{nodes: nodes}
		got, _, err := foundation.FetchNodesByIDs(context.Background(), gc, "", "", []string{"n-0001", "nope"}, foundation.IncludeTombstones)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.NotContains(t, got, "nope", "a missing id means not-found, the same thing FetchNode says with a nil node")
	})

	t.Run("the truncation verdict is carried out, not dropped", func(t *testing.T) {
		gc := &bulkGc{nodes: nodes, truncate: true}
		_, truncated, err := foundation.FetchNodesByIDs(context.Background(), gc, "", "", ids, foundation.IncludeTombstones)
		require.NoError(t, err)
		assert.True(t, truncated, "a clamped read must not render as a complete list")
	})

	t.Run("an empty id list costs no Execute at all", func(t *testing.T) {
		gc := &bulkGc{nodes: nodes}
		got, truncated, err := foundation.FetchNodesByIDs(context.Background(), gc, "", "", nil, foundation.IncludeTombstones)
		require.NoError(t, err)
		assert.Empty(t, got)
		assert.False(t, truncated)
		assert.Empty(t, gc.pageSizes, "no ids means no wire call")
	})
}

// edgeSetGc answers RETURN_MODE_EDGES with every edge incident to any pivot in
// the request, which is what the server's node-SET edge carrier returns.
type edgeSetGc struct{ edges []knowledgev1.Edge }

func (e *edgeSetGc) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.TextResult(""), nil
}

func (e *edgeSetGc) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	pivots := map[string]bool{}
	for _, id := range req.GetQuery().GetIds() {
		pivots[id] = true
	}
	var out []knowledgev1.Edge
	for i := range e.edges {
		s := &e.edges[i]
		if pivots[s.FromId] || pivots[s.ToId] {
			out = append(out, knowledgev1.Edge{FromId: s.FromId, ToId: s.ToId, Type: s.Type})
		}
	}
	return &knowledgev1.ExecuteResponse{Edges: edgesToProtoForTest(out)}, nil
}

// TestIterEdgesFor_PerPivotDirection is written for one specific defect: a
// set-form edge read that reuses the SINGLE-pivot direction rule.
//
// The fixture joins two pivots with an edge, which is the only shape that
// distinguishes the two rules. Under the correct per-pivot rule that edge is
// outgoing for the pivot it leaves AND incoming for the pivot it enters, so it
// appears under both filters. Under the single-pivot rule it would be classified
// once, globally, and one of those two nodes' incoming edge would be reported as
// its outgoing one.
func TestIterEdgesFor_PerPivotDirection(t *testing.T) {
	const (
		a = "pivot-a"
		b = "pivot-b"
		c = "outsider"
	)
	fixture := &edgeSetGc{edges: []knowledgev1.Edge{
		// The discriminating edge: BOTH endpoints are pivots.
		{FromId: a, ToId: b, Type: string(kgtypes.EdgeDependsOn)},
		// One endpoint a pivot, in each direction.
		{FromId: a, ToId: c, Type: string(kgtypes.EdgeInformedBy)},
		{FromId: c, ToId: b, Type: string(kgtypes.EdgeInformedBy)},
	}}
	pivots := []string{a, b}

	pairs := func(edges []*knowledgev1.Edge) []string {
		out := make([]string, 0, len(edges))
		for _, e := range edges {
			out = append(out, e.FromId+"->"+e.ToId)
		}
		return out
	}

	t.Run("the two-pivot edge is outgoing AND incoming", func(t *testing.T) {
		out, err := IterEdgesFor(context.Background(), fixture, pivots, kgwire.OutgoingEdges)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{a + "->" + b, a + "->" + c}, pairs(out),
			"outgoing: every edge LEAVING a pivot, including the one that also enters one")

		in, err := IterEdgesFor(context.Background(), fixture, pivots, kgwire.IncomingEdges)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{a + "->" + b, c + "->" + b}, pairs(in),
			"incoming: every edge ENTERING a pivot, including the one that also leaves one")
	})

	t.Run("the edge-type filter still applies over the set", func(t *testing.T) {
		out, err := IterEdgesFor(context.Background(), fixture, pivots, kgwire.BothEdges, kgtypes.EdgeDependsOn)
		require.NoError(t, err)
		assert.Equal(t, []string{a + "->" + b}, pairs(out))
	})

	t.Run("the single-pivot IterEdges agrees with the set form on one pivot", func(t *testing.T) {
		for _, dir := range []kgwire.EdgeDirection{kgwire.OutgoingEdges, kgwire.IncomingEdges, kgwire.BothEdges} {
			single, err := IterEdges(context.Background(), fixture, a, dir)
			require.NoError(t, err)
			set, err := IterEdgesFor(context.Background(), fixture, []string{a}, dir)
			require.NoError(t, err)
			assert.ElementsMatch(t, pairs(single), pairs(set),
				"the one-pivot set form must be the single-pivot form, direction %v", dir)
		}
		// Known positive: the comparison above would be satisfied by two empty
		// results, so assert the single-pivot read is non-empty in the same run.
		single, err := IterEdges(context.Background(), fixture, a, kgwire.BothEdges)
		require.NoError(t, err)
		assert.Len(t, single, 2, "pivot-a is incident to two edges; an empty agreement proves nothing")
	})
}
