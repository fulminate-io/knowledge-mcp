// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// recordingStatsRPC captures the last QueryPlan passed to Execute and the last
// StatsRequest, and hands back canned responses, so the lowered plan shape can
// be asserted (the OP_PREFIX predicate guard) without a live server.
type recordingStatsRPC struct {
	lastQuery *knowledgev1.QueryPlan
	nodes     []*knowledgev1.Node
	stats     *knowledgev1.GraphStats
}

func (r *recordingStatsRPC) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	r.lastQuery = req.GetQuery()
	return enginetest.ResponseWithNodes(r.nodes...), nil
}

func (r *recordingStatsRPC) Stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	return &knowledgev1.StatsResponse{GraphStats: r.stats}, nil
}

// TestResourceBrowse_OPPrefixPredicate is the BOUNDED guard (criterion
// 769013f5): the resource_type browse compiles to an Execute query carrying the
// OP_PREFIX metadata predicate on resource_type — NOT a client-side full-scan +
// filter. Asserted on the lowered QueryPlan for both cloud and cicd.
func TestResourceBrowse_OPPrefixPredicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind resourceGraphKind
	}{
		{"cloud", cloudGraphKind},
		{"cicd", cicdGraphKind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingStatsRPC{nodes: []*knowledgev1.Node{{Id: "r1", SymbolName: "res", Metadata: map[string]string{"resource_type": "ec2:instance"}}}}
			a := queryArgs{Graph: tc.kind.graph, Account: "acme", ResourceType: "ec2"}
			res := resourceBrowse(context.Background(), rec.Execute, tc.kind, a)
			require.False(t, res.IsError, textBodyTools(res))

			require.NotNil(t, rec.lastQuery)
			sel := rec.lastQuery.GetSelection()
			require.NotNil(t, sel)
			assert.Equal(t, string(tc.kind.nodeType), sel.GetNodeType())
			preds := sel.GetMetadataPredicates()
			require.Len(t, preds, 1, "browse must push a single metadata predicate, not iterate client-side")
			assert.Equal(t, "resource_type", preds[0].GetKey())
			assert.Equal(t, knowledgev1.MetadataPredicate_OP_PREFIX, preds[0].GetOp())
			assert.Equal(t, "ec2", preds[0].GetValue())
		})
	}
}

// TestResourceBrowse_NoResourceType asserts a plain browse carries the node-type
// selection but NO metadata predicate.
func TestResourceBrowse_NoResourceType(t *testing.T) {
	rec := &recordingStatsRPC{nodes: []*knowledgev1.Node{{Id: "r1", SymbolName: "res"}}}
	a := queryArgs{Graph: "cloud", Account: "acme"}
	res := resourceBrowse(context.Background(), rec.Execute, cloudGraphKind, a)
	require.False(t, res.IsError)
	require.NotNil(t, rec.lastQuery.GetSelection())
	assert.Empty(t, rec.lastQuery.GetSelection().GetMetadataPredicates())
}

// TestResourceGetNode_BothKinds drives id getNode for cloud (Region) and cicd
// (Provider), asserting the node render headers + secondary lines.
func TestResourceGetNode_BothKinds(t *testing.T) {
	node := knowledgev1.Node{
		Id: "r1", SymbolName: "res", Metadata: map[string]string{"resource_type": "ec2:instance", "region": "us-east-1", "provider": "aws"},
	}
	t.Run("cloud", func(t *testing.T) {
		rec := &recordingStatsRPC{nodes: []*knowledgev1.Node{&node}}
		res := resourceGetNode(context.Background(), rec.Execute, cloudGraphKind, queryArgs{Graph: "cloud", Account: "acme", ID: "r1"})
		assert.Contains(t, textBodyTools(res), "## Cloud Resource [acme]")
		assert.Contains(t, textBodyTools(res), "Region: us-east-1")
	})
	t.Run("cicd", func(t *testing.T) {
		rec := &recordingStatsRPC{nodes: []*knowledgev1.Node{&node}}
		res := resourceGetNode(context.Background(), rec.Execute, cicdGraphKind, queryArgs{Graph: "cicd", Account: "acme", ID: "r1"})
		assert.Contains(t, textBodyTools(res), "## CI/CD Resource [acme]")
		assert.Contains(t, textBodyTools(res), "Provider: aws")
	})
}

// TestResourceStats_BothKinds drives mode=stats for both graphs, asserting the
// per-account header + the shared stats breakdown body.
func TestResourceStats_BothKinds(t *testing.T) {
	stats := &knowledgev1.GraphStats{NodeCount: 4, EdgeCount: 2, NodesByType: map[string]int64{"cloud-resource": 4}}
	t.Run("cloud", func(t *testing.T) {
		rec := &recordingStatsRPC{stats: stats}
		res := resourceStats(context.Background(), rec, cloudGraphKind, queryArgs{Graph: "cloud", Account: "acme", Mode: "stats"})
		body := textBodyTools(res)
		assert.Contains(t, body, "## Cloud Graph: acme")
		assert.Contains(t, body, "Nodes: 4")
		assert.Contains(t, body, "### Nodes by Type")
	})
	t.Run("cicd", func(t *testing.T) {
		rec := &recordingStatsRPC{stats: stats}
		res := resourceStats(context.Background(), rec, cicdGraphKind, queryArgs{Graph: "cicd", Account: "acme", Mode: "stats"})
		assert.Contains(t, textBodyTools(res), "## CI/CD Graph: acme")
	})
}

// textBodyTools concatenates a ToolResult's text content for assertions.
func textBodyTools(r kgtools.ToolResult) string {
	var sb []byte
	for _, c := range r.Content {
		if c.Type == "text" {
			sb = append(sb, c.Text...)
		}
	}
	return string(sb)
}
