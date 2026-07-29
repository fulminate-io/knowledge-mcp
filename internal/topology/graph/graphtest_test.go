// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"maps"
	"sort"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// graphtest_test.go provides the shared test fixture for the gonum analyzer
// family. The analyzers read every node and edge over the wire through the
// foundation GraphCaller seam, so the fixture is a scripted Execute that
// serves a synthetic graph: it answers the type-browse, match-all, by-id, and
// bulk-edges plans the foundation read-helpers emit. Tests build the exact
// synthetic graph they need via AddNode / AddEdge, then drive an analyzer with
// a foundation.Request carrying the fixture as req.Caller.
//
// This replaces the prior store-backed TestFixture (in-memory store.DB) while
// preserving byte-stable analyzer outputs: the analyzer ALGORITHMS are
// unchanged, only the data-access layer moved to the wire.

// fakeNode and fakeEdge are the synthetic records the fixture stores. The
// fixture builds *knowledgev1.Node / *knowledgev1.Edge on demand inside
// Execute, ranging by index to avoid copylocks on the proto mutex.
type fakeNode struct {
	id         string
	symbolName string
	nodeType   kgtypes.NodeType
	language   string
	metadata   map[string]string
}

type fakeEdge struct {
	from     string
	to       string
	edgeType kgtypes.EdgeType
	weight   float64
}

// graphFixture is the scripted GraphCaller backing the gonum analyzer tests.
type graphFixture struct {
	nodes []fakeNode
	edges []fakeEdge
}

// newGraphFixture returns an empty fixture.
func newGraphFixture() *graphFixture { return &graphFixture{} }

// AddNode appends one node with no metadata and no language.
func (f *graphFixture) AddNode(id string, nodeType kgtypes.NodeType) {
	f.AddNodeFull(id, id, nodeType, "", nil)
}

// AddNodeFull appends one fully-specified node.
func (f *graphFixture) AddNodeFull(id, symbolName string, nodeType kgtypes.NodeType, language string, meta map[string]string) {
	f.nodes = append(f.nodes, fakeNode{
		id:         id,
		symbolName: symbolName,
		nodeType:   nodeType,
		language:   language,
		metadata:   meta,
	})
}

// AddEdge appends one directed unweighted (weight 1) edge.
func (f *graphFixture) AddEdge(from, to string, edgeType kgtypes.EdgeType) {
	f.edges = append(f.edges, fakeEdge{from: from, to: to, edgeType: edgeType, weight: 1})
}

// AddWeightedEdge appends one directed edge carrying an explicit weight.
func (f *graphFixture) AddWeightedEdge(from, to string, edgeType kgtypes.EdgeType, weight float64) {
	f.edges = append(f.edges, fakeEdge{from: from, to: to, edgeType: edgeType, weight: weight})
}

// Execute decodes the inbound plan and serves the matching carrier. It
// recognizes: RETURN_MODE_EDGES (bulk-edges over an id set + optional type
// filter), by-id node lookups, and the default node browse (type-filtered or
// match-all).
func (f *graphFixture) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	resp := &knowledgev1.ExecuteResponse{}
	switch {
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		resp.Edges = f.edgesFor(q.GetIds(), q.GetSelection().GetEdgeTypes())
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES:
		resp.GraphNames = nil
	case q.GetById() != "":
		resp.Nodes = f.nodeByID(q.GetById())
	default:
		resp.Nodes = f.nodesFor(q.GetSelection().GetNodeTypes())
	}
	return resp, nil
}

// nodesFor returns the fixture's nodes filtered to nodeTypes (empty = all).
func (f *graphFixture) nodesFor(nodeTypes []string) []*knowledgev1.Node {
	typeSet := map[string]bool{}
	for _, t := range nodeTypes {
		typeSet[t] = true
	}
	var out []*knowledgev1.Node
	for i := range f.nodes {
		fn := &f.nodes[i]
		if len(typeSet) > 0 && !typeSet[string(fn.nodeType)] {
			continue
		}
		out = append(out, buildNode(fn))
	}
	return out
}

// nodeByID returns the single node matching id, or an empty slice.
func (f *graphFixture) nodeByID(id string) []*knowledgev1.Node {
	for i := range f.nodes {
		if f.nodes[i].id == id {
			return []*knowledgev1.Node{buildNode(&f.nodes[i])}
		}
	}
	return nil
}

// edgesFor returns the fixture's edges incident to any node in ids and
// matching one of edgeTypes (empty = any). Both directions are unioned,
// matching the foundation FetchEdges node-SET both-direction semantics. An
// EMPTY id set is the MATCH-ALL form (foundation.FetchAllEdges): every edge of
// the fixture, type filter still applied — mirroring the engine, where a plan
// with no pivot discriminant means "all edges of the graph".
func (f *graphFixture) edgesFor(ids, edgeTypes []string) []*knowledgev1.Edge {
	matchAll := len(ids) == 0
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	typeSet := map[string]bool{}
	for _, et := range edgeTypes {
		typeSet[et] = true
	}
	var out []*knowledgev1.Edge
	for i := range f.edges {
		e := &f.edges[i]
		if !matchAll && !idSet[e.from] && !idSet[e.to] {
			continue
		}
		if len(typeSet) > 0 && !typeSet[string(e.edgeType)] {
			continue
		}
		out = append(out, buildEdge(e))
	}
	return out
}

// buildNode constructs a wire node from a synthetic record. Metadata is
// copied so callers cannot mutate the fixture through the returned node.
func buildNode(fn *fakeNode) *knowledgev1.Node {
	n := &knowledgev1.Node{}
	n.Id = fn.id
	n.Type = string(fn.nodeType)
	n.SymbolName = fn.symbolName
	n.Language = fn.language
	if len(fn.metadata) > 0 {
		md := make(map[string]string, len(fn.metadata))
		maps.Copy(md, fn.metadata)
		n.Metadata = md
	}
	return n
}

// buildEdge constructs a wire edge from a synthetic record.
func buildEdge(e *fakeEdge) *knowledgev1.Edge {
	out := &knowledgev1.Edge{}
	out.FromId = e.from
	out.ToId = e.to
	out.Type = string(e.edgeType)
	out.Weight = e.weight
	return out
}

// req builds a foundation.Request backed by the fixture for the given graph
// type, name, and top-K.
func (f *graphFixture) req(graphType kgtypes.GraphType, name string, topK int) foundation.Request {
	return foundation.Request{
		Caller: f,
		Graph:  graphType,
		Name:   name,
		TopK:   topK,
	}
}

// newTestCtx returns a background context for analyzer Run calls.
func newTestCtx(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

// sortedStrings returns a sorted copy of s (kept local for fixture stability
// assertions).
func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// compile-time assertion: the fixture satisfies the wire seam.
var _ foundation.GraphCaller = (*graphFixture)(nil)
