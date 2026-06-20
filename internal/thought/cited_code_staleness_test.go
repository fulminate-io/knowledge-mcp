// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// citedCodeFake is an Execute fake for the cross-graph cited-code resolution
// chain. Its exec callback dispatches on the request shape and counts each stage
// so the no-N+1 contract can be asserted by call count:
//   - a RETURN_MODE_EDGES query → the born-link edges (thought--relates-to-->proxy,
//     Method="code-ref");
//   - an ids[] query targeting the CODE graph (req.Target.Graph=="code") → the
//     hydrated code nodes (carrying UpdatedAt + Content);
//   - any other ids[] query → the knowledge-graph proxy hydrate.
type citedCodeFake struct {
	edges       []*knowledgev1.Edge
	proxyNodes  []*knowledgev1.Node
	codeNodes   []*knowledgev1.Node
	edgeFetches int
	proxyHydra  int
	codeHydra   int
}

func (f *citedCodeFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		f.edgeFetches++
		return &knowledgev1.ExecuteResponse{Edges: f.edges}, nil
	}
	if req.GetTarget().GetGraph() == string(kgtypes.GraphCode) {
		f.codeHydra++
		return enginetest.ResponseWithNodes(f.codeNodes...), nil
	}
	f.proxyHydra++
	return enginetest.ResponseWithNodes(f.proxyNodes...), nil
}

// mkCodeProxy builds a knowledge-graph code proxy node the way BuildCrossGraphProxy
// stamps one: foreign_graph + foreign_id on the node (the parent sets these,
// proxy_builder.go:38-43) and repo (buildCodeProxy, :88).
func mkCodeProxy(id, repo, foreignID string) *knowledgev1.Node {
	p := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeProxy)}
	kgtypes.SetValue(p, "foreign_graph", string(kgtypes.GraphCode))
	kgtypes.SetValue(p, "foreign_id", foreignID)
	kgtypes.SetValue(p, "repo", repo)
	return p
}

// TestResolveCitedCodeNodes is the primary boundary test: a cited thought resolves
// through edge → proxy → code node to a NON-EMPTY hydrated code-node slice carrying
// BOTH the fixture UpdatedAt (the staleness signal) AND Content (so a current-source
// consumer can read the live source off the same boundary). The exec call counts
// pin the no-N+1 contract: exactly one edge read + one proxy hydrate + one code
// hydrate per repo.
func TestResolveCitedCodeNodes(t *testing.T) {
	const (
		thoughtID = "th-1"
		proxyID   = "proxy:knowledge:pkg/file.go:Sym"
		codeID    = "pkg/file.go:Sym"
	)
	const fixtureUpdatedAt int64 = 1_700_000_000_000_000_000
	const fixtureContent = "func Sym() {}"

	fake := &citedCodeFake{
		edges: []*knowledgev1.Edge{
			{FromId: thoughtID, ToId: proxyID, Type: string(kgtypes.EdgeRelatesTo), Method: "code-ref"},
		},
		proxyNodes: []*knowledgev1.Node{mkCodeProxy(proxyID, "knowledge", codeID)},
		codeNodes: []*knowledgev1.Node{
			{Id: codeID, Type: "function", UpdatedAt: fixtureUpdatedAt, Content: fixtureContent},
		},
	}

	out := ResolveCitedCodeNodes(context.Background(), fake, []string{thoughtID})

	nodes := out[thoughtID]
	require.NotEmpty(t, nodes, "the cited thought resolves to a non-empty code-node slice")
	require.Len(t, nodes, 1, "one cited code node")
	assert.Equal(t, fixtureUpdatedAt, nodes[0].UpdatedAt,
		"the resolved node carries the fixture UpdatedAt (the staleness signal)")
	assert.Equal(t, fixtureContent, nodes[0].GetContent(),
		"the resolved node carries Content — the current-source consumer reads it off this same boundary")

	// No-N+1 contract: one of each bulk read, one code hydrate for the single repo.
	assert.Equal(t, 1, fake.edgeFetches, "exactly one bulk edge read")
	assert.Equal(t, 1, fake.proxyHydra, "exactly one knowledge-graph proxy hydrate")
	assert.Equal(t, 1, fake.codeHydra, "exactly one code-graph hydrate (one distinct repo)")
}

// TestResolveCitedCodeNodes_UpdatedAtFold confirms buildCitedCodeUpdatedAt is the
// thin .UpdatedAt→max fold over the boundary: a code node whose UpdatedAt exceeds a
// stand-in newest-charge nanos yields a map value greater than that nanos (the
// gate would flag), while a code node at-or-below it yields a value not exceeding
// the charge nanos (no flag). The fold MAXes over the resolved nodes.
func TestResolveCitedCodeNodes_UpdatedAtFold(t *testing.T) {
	const (
		thoughtID = "th-1"
		proxyID   = "proxy:knowledge:pkg/file.go:Sym"
		codeID    = "pkg/file.go:Sym"
	)
	// Stand-in for a thought's newest-charge time (unix nanos).
	const chargeNanos int64 = 1_700_000_000_000_000_000

	newFake := func(updatedAt int64) *citedCodeFake {
		return &citedCodeFake{
			edges: []*knowledgev1.Edge{
				{FromId: thoughtID, ToId: proxyID, Type: string(kgtypes.EdgeRelatesTo), Method: "code-ref"},
			},
			proxyNodes: []*knowledgev1.Node{mkCodeProxy(proxyID, "knowledge", codeID)},
			codeNodes: []*knowledgev1.Node{
				{Id: codeID, Type: "function", UpdatedAt: updatedAt, Content: "x"},
			},
		}
	}

	// Code newer than the charge → fold value > chargeNanos (would flag).
	newer := buildCitedCodeUpdatedAt(context.Background(), newFake(chargeNanos+int64(3600)*1_000_000_000), []string{thoughtID})
	require.Contains(t, newer, thoughtID)
	assert.Greater(t, newer[thoughtID], chargeNanos,
		"cited code newer than the newest charge yields a fold value the gate flags")

	// Code older-or-equal → fold value <= chargeNanos (no flag).
	older := buildCitedCodeUpdatedAt(context.Background(), newFake(chargeNanos-int64(3600)*1_000_000_000), []string{thoughtID})
	require.Contains(t, older, thoughtID)
	assert.LessOrEqual(t, older[thoughtID], chargeNanos,
		"cited code older than the newest charge yields a fold value the gate does not flag")
}
