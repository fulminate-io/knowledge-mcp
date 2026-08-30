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
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(f.edges, q)}, nil
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

// sourceProxyRepo is the repo every fixture proxy in this package points at. It is
// a constant rather than a parameter because the resolution under test groups the
// code hydrate BY repo, so a second repo would exercise the grouping rather than
// this package's subject; every fixture here has always used this one.
const sourceProxyRepo = "knowledge"

// mkSourceCodeProxy builds a knowledge-graph code proxy node the way
// BuildCrossGraphProxy stamps one (foreign_graph + foreign_id + repo).
func mkSourceCodeProxy(id, foreignID string) *knowledgev1.Node {
	p := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeProxy)}
	kgtypes.SetValue(p, "foreign_graph", string(kgtypes.GraphCode))
	kgtypes.SetValue(p, "foreign_id", foreignID)
	kgtypes.SetValue(p, "repo", sourceProxyRepo)
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
		proxyNodes: []*knowledgev1.Node{mkSourceCodeProxy(proxyID, codeID)},
		codeNodes:  []*knowledgev1.Node{{Id: codeID, Type: "function", Content: content}},
	}

	sources, _, err := resolveThoughtCurrentSource(context.Background(), fake, thoughtID)
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

	sources, _, err := resolveThoughtCurrentSource(context.Background(), fake, thoughtID)
	require.NoError(t, err)
	require.Len(t, sources, 1, "the own-content fallback yields one source")
	assert.Equal(t, summary+"\n"+content, sources[0].Text, "source text is the thought's own Summary+Content")
	assert.Equal(t, "thought:"+thoughtID, sources[0].Origin, "Origin is thought:<id>")
}

// TestResolveThoughtCurrentSource_ContentLessCitationsExcluded: a thought whose only
// resolvable citation is a content-less FILE node yields ONE source — the thought's
// own live Summary+Content — not a code source with empty Text. The governing rule is
// that bad input errors: an empty resolution must never be silently compared, so it is
// excluded from the comparison set and the require-own-content path serves the thought.
func TestResolveThoughtCurrentSource_ContentLessCitationsExcluded(t *testing.T) {
	const (
		thoughtID = "th-content-less"
		summary   = "claim: the cache is write-through"
		content   = "the reasoning body, which quotes no source line"
	)

	sources, contentLess, err := resolveThoughtCurrentSource(context.Background(), fileOnlyFake(thoughtID, summary, content), thoughtID)
	require.NoError(t, err)
	require.Len(t, sources, 1, "the content-less citation is excluded, leaving only the own-content fallback")
	assert.Equal(t, "thought:"+thoughtID, sources[0].Origin,
		"Origin is the thought's own — a content-less code node contributes no source")
	assert.Equal(t, summary+"\n"+content, sources[0].Text,
		"source text is the thought's own Summary+Content, never an empty string")

	// The exclusion is REPORTED as well as applied (CEO amendment, 2026-08-28): the
	// resolver hands the excluded id back so the rejection can name it. Asserted
	// against the fixture's own code-node id rather than against a count, so a
	// resolver that reported some other id would fail here.
	assert.Equal(t, []string{fileOnlyCodeID}, contentLess,
		"the excluded citation is named to the caller, not silently dropped")
}

// TestResolveThoughtCurrentSource_NoExclusionsToReport is the known-positive's
// counterpart: a thought with a content-BEARING citation, and one with no citation
// at all, both report an EMPTY exclusion list. Without this the assertion above
// would pass equally against a resolver that reported every citation as excluded.
func TestResolveThoughtCurrentSource_NoExclusionsToReport(t *testing.T) {
	t.Run("content-bearing citation", func(t *testing.T) {
		const (
			thoughtID = "th-bearing"
			proxyID   = "proxy:knowledge:bearing"
			codeID    = "pkg/file.go:Sym"
		)
		fake := &citedSourceFake{
			edges: []*knowledgev1.Edge{
				{FromId: thoughtID, ToId: proxyID, Type: string(kgtypes.EdgeRelatesTo), Method: "code-ref"},
			},
			proxyNodes: []*knowledgev1.Node{mkSourceCodeProxy(proxyID, codeID)},
			codeNodes:  []*knowledgev1.Node{{Id: codeID, Type: "function", Content: "func Sym() {}"}},
		}
		sources, contentLess, err := resolveThoughtCurrentSource(context.Background(), fake, thoughtID)
		require.NoError(t, err)
		require.Len(t, sources, 1, "the citation carries content, so it IS the comparison set")
		assert.Empty(t, contentLess, "nothing was excluded, so nothing is reported")
	})

	t.Run("no citation at all", func(t *testing.T) {
		const thoughtID = "th-none"
		fake := &citedSourceFake{
			thoughtNode: &knowledgev1.Node{Id: thoughtID, Type: string(kgtypes.NodeThought), Summary: "s", Content: "c"},
		}
		_, contentLess, err := resolveThoughtCurrentSource(context.Background(), fake, thoughtID)
		require.NoError(t, err)
		assert.Empty(t, contentLess, "a thought citing no code excludes no citation")
	})
}

// TestResolveThoughtCurrentSource_MissingNode: a thought that resolves to neither a
// code node nor its own node yields an empty source set (the caller treats this as
// gate-fail — no first-party basis).
func TestResolveThoughtCurrentSource_MissingNode(t *testing.T) {
	fake := &citedSourceFake{} // no edges, no thought node
	sources, _, err := resolveThoughtCurrentSource(context.Background(), fake, "gone")
	require.NoError(t, err)
	assert.Empty(t, sources, "an unresolvable node yields no source (gate-fail)")
}

// TestResolveThoughtCurrentSource_NilGuards: nil gc / empty id short-circuit.
func TestResolveThoughtCurrentSource_NilGuards(t *testing.T) {
	sources, _, err := resolveThoughtCurrentSource(context.Background(), nil, "x")
	require.NoError(t, err)
	assert.Empty(t, sources)

	sources, _, err = resolveThoughtCurrentSource(context.Background(), &citedSourceFake{}, "")
	require.NoError(t, err)
	assert.Empty(t, sources)
}
