// SPDX-License-Identifier: Apache-2.0

package crossgraph

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeCaller is a Call+Execute-capable test client. It records every gc.Call (to
// prove ResolveAndLink issues NONE) and every Execute plan (to assert the
// UPSERT(proxy)+LINK shape + Target). Reads are served from seeded per-graph
// nodes + per-type graph-name lists.
type fakeCaller struct {
	calls        []string
	plans        []*knowledgev1.ExecuteRequest
	nodesByGraph map[string]map[string]*knowledgev1.Node // graphType → id → node
	graphNames   map[string][]string                     // graphType → names

	// edgesByType is the target graph's edge VOCABULARY, which ResolveAndLink
	// reads through LinkRequest.Stats before declaring an edge type. An empty
	// map is not a degenerate fixture — it is the BOOTSTRAP case, a graph with
	// no edges yet, where a write declares a new family; the tests in this file
	// link into exactly such a graph.
	edgesByType map[string]int64
}

// Stats serves the edge vocabulary through the engine.StatsFn seam
// LinkRequest.Stats requires. A nil edgesByType yields an empty EdgesByType,
// which is the bootstrap answer: no stored family matches, so a WRITE admits
// the caller's spelling as the graph's first.
func (f *fakeCaller) Stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{
		GraphStats: &knowledgev1.GraphStats{EdgesByType: f.edgesByType},
	}, nil
}

func (f *fakeCaller) Call(_ context.Context, tool string, _ json.RawMessage) (kgtools.ToolResult, error) {
	f.calls = append(f.calls, tool)
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
}

func (f *fakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.plans = append(f.plans, req)
	if m := req.GetMutation(); m != nil {
		if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_UPSERT {
			ids := make([]string, 0, len(m.GetNodeBodies()))
			for _, b := range m.GetNodeBodies() {
				ids = append(ids, b.GetId())
			}
			return &knowledgev1.ExecuteResponse{Ids: ids}, nil
		}
		return &knowledgev1.ExecuteResponse{AffectedCount: 1}, nil
	}
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		infos := make([]*knowledgev1.GraphInfo, 0)
		for _, n := range f.graphNames[req.GetTarget().GetGraph()] {
			infos = append(infos, &knowledgev1.GraphInfo{Name: n})
		}
		return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
	}
	// By-id node resolution (FetchNodeIn) — serve the typed Nodes carrier.
	var nodes []*knowledgev1.Node
	if n, ok := f.nodesByGraph[req.GetTarget().GetGraph()][q.GetById()]; ok {
		nodes = []*knowledgev1.Node{n}
	}
	return enginetest.ResponseWithNodes(nodes...), nil
}

// TestResolveAndLink_ProxyUpsertAndLink covers that ResolveAndLink
// performs the proxy UPSERT + the from→to LINK through Execute (NOT a gc.Call),
// both targeting req.TargetGraph, and the proxy id matches BuildCrossGraphProxy.
func TestResolveAndLink_ProxyUpsertAndLink(t *testing.T) {
	cloudNode := &knowledgev1.Node{
		Id: "default/Deployment/app", Type: string(kgtypes.NodeCloudResource), SymbolName: "app",
		Metadata: map[string]string{"resource_type": "Deployment"},
	}
	f := &fakeCaller{
		nodesByGraph: map[string]map[string]*knowledgev1.Node{
			"cloud": {cloudNode.Id: cloudNode},
		},
		graphNames: map[string][]string{"cloud": {"prod"}},
	}

	handled, res, err := ResolveAndLink(context.Background(), f, f, LinkRequest{
		From: cloudNode.Id, To: "myrepo", Relationship: "BUILDS",
		TargetGraph: "linkage", Method: "tier1-image", Confidence: 0.9,
		Stats: f.Stats,
	})
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, res.IsError)

	// (ii) NO mutate/link gc.Call — everything rides Execute.
	for _, c := range f.calls {
		assert.NotEqual(t, "mutate", c, "ResolveAndLink must not issue a mutate gc.Call")
	}

	// (i) Captured plans: an UPSERT(proxy) targeting linkage + a LINK targeting
	// linkage. (FROM is a cloud node → proxy; TO=myrepo is not a node → best-effort
	// raw, no second UPSERT.)
	var upsert, link *knowledgev1.ExecuteRequest
	for _, p := range f.plans {
		if m := p.GetMutation(); m != nil {
			switch m.GetKind() {
			case knowledgev1.MutationPlan_MUTATION_KIND_UPSERT:
				upsert = p
			case knowledgev1.MutationPlan_MUTATION_KIND_LINK:
				link = p
			}
		}
	}
	require.NotNil(t, upsert, "a proxy UPSERT plan was issued")
	require.NotNil(t, link, "a from→to LINK plan was issued")
	assert.Equal(t, "linkage", upsert.GetTarget().GetGraph(), "proxy UPSERT targets req.TargetGraph")
	assert.Equal(t, "linkage", link.GetTarget().GetGraph(), "LINK targets req.TargetGraph")

	// (iii) the proxy id matches BuildCrossGraphProxy for the same target. The
	// relocated client builder takes the proto carrier directly (proto ProxyTarget
	// input + *knowledgev1.Node source) — no store-wrapper bridge.
	wantProxy, berr := BuildCrossGraphProxy(&knowledgev1.ProxyTarget{
		GraphType: string(kgtypes.GraphCloud), Name: "prod", NodeId: cloudNode.Id,
	}, cloudNode)
	require.NoError(t, berr)
	assert.Equal(t, wantProxy.GetId(), upsert.GetMutation().GetNodeBodies()[0].GetId(),
		"proxy id matches the shared BuildCrossGraphProxy builder")

	// The LINK uses the proxy id as FROM, the raw best-effort id as TO, with the
	// RESOLVED edge type + the edge metadata. The fixture's linkage graph holds
	// no edges, so the declaration path admits BUILDS as a new family and the
	// caller's spelling stands — not a casing rule, which no longer exists.
	spec := link.GetMutation().GetEdgeSpec()
	assert.Equal(t, []string{wantProxy.Id}, link.GetMutation().GetSelection().GetIds())
	assert.Equal(t, "myrepo", spec.GetToId(), "non-node TO stays raw (best-effort)")
	assert.Equal(t, "BUILDS", spec.GetRelationship(),
		"an empty linkage vocabulary admits the caller's spelling as the graph's first edge family")
	assert.Equal(t, "tier1-image", spec.GetMethod())
	assert.InDelta(t, 0.9, spec.GetConfidence(), 1e-9)
}

// TestResolveAndLink_KnowledgeTargetGuards covers the knowledge-target guard: an
// unresolvable endpoint returns handled=false (fall through to legacy), NOT a
// best-effort raw link.
func TestResolveAndLink_KnowledgeTargetGuards(t *testing.T) {
	f := &fakeCaller{
		nodesByGraph: map[string]map[string]*knowledgev1.Node{},
		graphNames:   map[string][]string{},
	}
	handled, _, err := ResolveAndLink(context.Background(), f, f, LinkRequest{
		From: "unresolvable-from", To: "unresolvable-to", Relationship: "relates-to",
		TargetGraph: "knowledge",
		Stats:       f.Stats,
	})
	require.NoError(t, err)
	assert.False(t, handled, "knowledge target guards on an unresolvable endpoint (→ legacy)")
	for _, p := range f.plans {
		assert.Nil(t, p.GetMutation(), "no LINK/UPSERT issued when guarding")
	}
}
