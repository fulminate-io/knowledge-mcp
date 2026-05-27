// SPDX-License-Identifier: Apache-2.0

package content

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// fakeCaller is a scripted foundation.GraphCaller that answers each inbound
// ExecuteRequest from a seeded node + edge corpus, faithfully reproducing the
// real graph server's plan-shape routing so the content analyzers run without a
// live store. It is the content-package twin of foundation/wire_test.go's
// fakeCaller, extended with type-filtered node browse and node-set edge
// matching (the two access shapes the content family exercises).
//
//   - RETURN_MODE_EDGES        → edges incident to the plan's Ids set, optionally
//     filtered to the plan's EdgeTypes.
//   - ById != ""               → the single matching node.
//   - Selection.NodeTypes set  → nodes whose Type is in that set (FetchNodesByType).
//   - otherwise                → every seeded node (FetchAllNodes).
type fakeCaller struct {
	nodes []*knowledgev1.Node
	edges []*knowledgev1.Edge
}

func (f *fakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	resp := &knowledgev1.ExecuteResponse{}
	switch {
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		resp.Edges = f.matchEdges(q.GetIds(), q.GetSelection().GetEdgeTypes())
	case q.GetById() != "":
		for _, n := range f.nodes {
			if n.GetId() == q.GetById() {
				resp.Nodes = []*knowledgev1.Node{n}
				break
			}
		}
	case len(q.GetSelection().GetNodeTypes()) > 0:
		resp.Nodes = f.matchNodeTypes(q.GetSelection().GetNodeTypes())
	default:
		resp.Nodes = f.nodes
	}
	return resp, nil
}

// matchNodeTypes returns the seeded nodes whose Type is in the requested set,
// preserving seed order (the order FetchAllNodes / IterateAll would yield).
func (f *fakeCaller) matchNodeTypes(types []string) []*knowledgev1.Node {
	want := make(map[string]struct{}, len(types))
	for _, t := range types {
		want[t] = struct{}{}
	}
	var out []*knowledgev1.Node
	for _, n := range f.nodes {
		if _, ok := want[n.GetType()]; ok {
			out = append(out, n)
		}
	}
	return out
}

// matchEdges returns the seeded edges incident to any node in ids, optionally
// filtered to edgeTypes — the both-direction node-set union the real server's
// RETURN_MODE_EDGES node-set carrier produces.
func (f *fakeCaller) matchEdges(ids, edgeTypes []string) []*knowledgev1.Edge {
	idset := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		idset[id] = struct{}{}
	}
	var typeset map[string]struct{}
	if len(edgeTypes) > 0 {
		typeset = make(map[string]struct{}, len(edgeTypes))
		for _, t := range edgeTypes {
			typeset[t] = struct{}{}
		}
	}
	var out []*knowledgev1.Edge
	for _, e := range f.edges {
		_, fromIn := idset[e.GetFromId()]
		_, toIn := idset[e.GetToId()]
		if !fromIn && !toIn {
			continue
		}
		if typeset != nil {
			if _, ok := typeset[e.GetType()]; !ok {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// mkNode builds a wire node with the given id, type, and symbol name.
func mkNode(id, typ, symbol string) *knowledgev1.Node {
	n := &knowledgev1.Node{}
	n.Id = id
	n.Type = typ
	n.SymbolName = symbol
	return n
}

// mkContent builds a wire node carrying Content (for the content-target tests).
func mkContent(id, typ, symbol, content string) *knowledgev1.Node {
	n := mkNode(id, typ, symbol)
	n.Content = content
	return n
}

// mkMeta builds a wire node carrying a single metadata key (for the
// attribute-target tests).
func mkMeta(id, typ, symbol, key, value string) *knowledgev1.Node {
	n := mkNode(id, typ, symbol)
	kgtypes.SetValue(n, key, value)
	return n
}

// containsEdge builds an EdgeContains edge between two node IDs.
func containsEdge(from, to string) *knowledgev1.Edge {
	e := &knowledgev1.Edge{}
	e.FromId = from
	e.ToId = to
	e.Type = string(kgtypes.EdgeContains)
	return e
}

// refEdge builds an EdgeReferences edge between two node IDs (the degree
// fixture's hub→leaf links).
func refEdge(from, to string) *knowledgev1.Edge {
	e := &knowledgev1.Edge{}
	e.FromId = from
	e.ToId = to
	e.Type = string(kgtypes.EdgeReferences)
	return e
}

// req builds a content-analyzer Request over the given fake caller with the
// supplied Extra knobs. Every content analyzer is graph-type-agnostic; the
// tests use the web graph type the originals used.
func req(f *fakeCaller, extra map[string]string) foundation.Request {
	return foundation.Request{
		Caller: f,
		Graph:  kgtypes.GraphWebRaw,
		Name:   "content-test",
		Extra:  extra,
	}
}
