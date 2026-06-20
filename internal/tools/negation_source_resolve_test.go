// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// citedSourceFake is an Execute fake for resolveThoughtCurrentSource. It
// dispatches on the request shape, mirroring the thought package's citedCodeFake
// (cited_code_staleness_test.go:25) so both resolution paths are driven by canned
// Execute responses with no LLM anywhere:
//   - RETURN_MODE_EDGES → the born-link relates-to(code-ref) edges;
//   - an ids[] query targeting the CODE graph → the hydrated code node(s)
//     (carrying Content = the live source);
//   - any other ids[] (knowledge) query → the proxy hydrate;
//   - a ByID query → the contradicted thought's own node (the render.FetchNode
//     own-content fallback path).
type citedSourceFake struct {
	edges       []*knowledgev1.Edge
	proxyNodes  []*knowledgev1.Node
	codeNodes   []*knowledgev1.Node
	thoughtNode *knowledgev1.Node
}

func (f *citedSourceFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: f.edges}, nil
	}
	if req.GetTarget().GetGraph() == string(kgtypes.GraphCode) {
		return enginetest.ResponseWithNodes(f.codeNodes...), nil
	}
	// A ByID query (render.FetchNode own-content fallback) carries a non-empty
	// ByID; the bulk proxy hydrate carries Selection.Ids with an empty ByID.
	if q.GetById() != "" {
		if f.thoughtNode != nil {
			return enginetest.ResponseWithNodes(f.thoughtNode), nil
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return enginetest.ResponseWithNodes(f.proxyNodes...), nil
}

// mkSourceCodeProxy builds a knowledge-graph code proxy node the way
// BuildCrossGraphProxy stamps one (foreign_graph + foreign_id + repo).
func mkSourceCodeProxy(id, repo, foreignID string) *knowledgev1.Node {
	p := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeProxy)}
	kgtypes.SetValue(p, "foreign_graph", string(kgtypes.GraphCode))
	kgtypes.SetValue(p, "foreign_id", foreignID)
	kgtypes.SetValue(p, "repo", repo)
	return p
}

// TestResolveThoughtCurrentSource_CodeRefPath: a thought whose
// ResolveCitedCodeNodes boundary resolves a code node yields that node's current
// Content as the source, Origin code:<nodeID>.
func TestResolveThoughtCurrentSource_CodeRefPath(t *testing.T) {
	const (
		thoughtID = "th-1"
		proxyID   = "proxy:knowledge:pkg/file.go:Sym"
		codeID    = "pkg/file.go:Sym"
		content   = "func Sym() { return 42 }"
	)
	fake := &citedSourceFake{
		edges: []*knowledgev1.Edge{
			{FromId: thoughtID, ToId: proxyID, Type: string(kgtypes.EdgeRelatesTo), Method: "code-ref"},
		},
		proxyNodes: []*knowledgev1.Node{mkSourceCodeProxy(proxyID, "knowledge", codeID)},
		codeNodes:  []*knowledgev1.Node{{Id: codeID, Type: "function", Content: content}},
	}

	sources, err := resolveThoughtCurrentSource(context.Background(), fake, thoughtID)
	require.NoError(t, err)
	require.Len(t, sources, 1, "the code-ref path yields one source from the resolved code node")
	assert.Equal(t, content, sources[0].Text, "source text is the code node's current Content")
	assert.Equal(t, "code:"+codeID, sources[0].Origin, "Origin is code:<nodeID>")
}

// TestResolveThoughtCurrentSource_OwnContentFallback: a thought with NO resolvable
// code-ref link falls back to its own live Summary+Content (the REQUIRE-OWN-CONTENT
// rule), Origin thought:<id>.
func TestResolveThoughtCurrentSource_OwnContentFallback(t *testing.T) {
	const (
		thoughtID = "th-2"
		summary   = "the claim summary"
		content   = "the full reasoning body"
	)
	fake := &citedSourceFake{
		edges:       nil, // no code-ref edge → ResolveCitedCodeNodes returns nothing
		thoughtNode: &knowledgev1.Node{Id: thoughtID, Type: string(kgtypes.NodeThought), Summary: summary, Content: content},
	}

	sources, err := resolveThoughtCurrentSource(context.Background(), fake, thoughtID)
	require.NoError(t, err)
	require.Len(t, sources, 1, "the own-content fallback yields one source")
	assert.Equal(t, summary+"\n"+content, sources[0].Text, "source text is the thought's own Summary+Content")
	assert.Equal(t, "thought:"+thoughtID, sources[0].Origin, "Origin is thought:<id>")
}

// TestResolveThoughtCurrentSource_MissingNode: a thought that resolves to neither a
// code node nor its own node yields an empty source set (the caller treats this as
// gate-fail — no first-party basis).
func TestResolveThoughtCurrentSource_MissingNode(t *testing.T) {
	fake := &citedSourceFake{} // no edges, no thought node
	sources, err := resolveThoughtCurrentSource(context.Background(), fake, "gone")
	require.NoError(t, err)
	assert.Empty(t, sources, "an unresolvable node yields no source (gate-fail)")
}

// TestResolveThoughtCurrentSource_NilGuards: nil gc / empty id short-circuit.
func TestResolveThoughtCurrentSource_NilGuards(t *testing.T) {
	sources, err := resolveThoughtCurrentSource(context.Background(), nil, "x")
	require.NoError(t, err)
	assert.Empty(t, sources)

	sources, err = resolveThoughtCurrentSource(context.Background(), &citedSourceFake{}, "")
	require.NoError(t, err)
	assert.Empty(t, sources)
}
