// SPDX-License-Identifier: Apache-2.0

package cloudresolver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// seedDepCheckerCloudGraph returns one cloud-graph GraphSlice shaped like the
// following (edge types intentionally vary):
//
//	api ─CONNECTS_TO─▶ db
//	api ─USES────────▶ cache
//	cache ─DEPENDS_ON─▶ redis
//	redis ─SINK──────▶ s3-logs
//	orphan            (disconnected)
//
// Every traversal scenario in HasDependency can be exercised against this
// shape: 1-hop direct, 2-hop transitive, 3-hop (the boundary), and >3 hops
// (falls outside maxDependencyHops). The slice feeds NewCloudSubgraph directly
// — no store engine.
func seedDepCheckerCloudGraph(account string) GraphSlice {
	nodes := []*knowledgev1.Node{
		mkCloudResource("api", "api", "ecs:service"),
		mkCloudResource("db", "db", "rds:db-instance"),
		mkCloudResource("cache", "cache", "elasticache:cluster"),
		mkCloudResource("redis", "redis", "elasticache:node"),
		mkCloudResource("s3-logs", "s3-logs", "s3:bucket"),
		mkCloudResource("orphan", "orphan", "ec2:instance"),
	}
	edges := []knowledgev1.Edge{
		{FromId: "api", ToId: "db", Type: string(kgtypes.EdgeType("CONNECTS_TO"))},
		{FromId: "api", ToId: "cache", Type: string(kgtypes.EdgeType("USES"))},
		{FromId: "cache", ToId: "redis", Type: string(kgtypes.EdgeType("DEPENDS_ON"))},
		{FromId: "redis", ToId: "s3-logs", Type: string(kgtypes.EdgeType("SINK"))},
	}
	return GraphSlice{Name: account, Nodes: nodes, Edges: edges}
}

// resWithin builds a ResolvedResource pinned to the given account.
// Helper exists so HasDependency calls in tests stay short and the
// shared "acct-1" account is named once per test rather than at every
// call site.
func resWithin(account, id string) logs.ResolvedResource {
	return logs.ResolvedResource{Account: account, ID: id}
}

func TestDepChecker_DirectDependency(t *testing.T) {
	ctx := context.Background()
	c := NewDependencyChecker(NewCloudSubgraph([]GraphSlice{seedDepCheckerCloudGraph("acct-1")}))
	assert.True(t, c.HasDependency(ctx,
		resWithin("acct-1", "api"), resWithin("acct-1", "db")),
		"api → db is one hop, must resolve")
}

func TestDepChecker_TwoHop(t *testing.T) {
	ctx := context.Background()
	c := NewDependencyChecker(NewCloudSubgraph([]GraphSlice{seedDepCheckerCloudGraph("acct-1")}))
	assert.True(t, c.HasDependency(ctx,
		resWithin("acct-1", "api"), resWithin("acct-1", "redis")),
		"api → cache → redis is two hops, under limit")
}

func TestDepChecker_ThreeHop_Boundary(t *testing.T) {
	ctx := context.Background()
	c := NewDependencyChecker(NewCloudSubgraph([]GraphSlice{seedDepCheckerCloudGraph("acct-1")}))
	assert.True(t, c.HasDependency(ctx,
		resWithin("acct-1", "api"), resWithin("acct-1", "s3-logs")),
		"api → cache → redis → s3-logs is exactly three hops, at the limit")
}

func TestDepChecker_ReverseDirection(t *testing.T) {
	ctx := context.Background()
	c := NewDependencyChecker(NewCloudSubgraph([]GraphSlice{seedDepCheckerCloudGraph("acct-1")}))
	// db has no outgoing edges, but api → db exists; BothEdges traversal
	// should still connect them.
	assert.True(t, c.HasDependency(ctx,
		resWithin("acct-1", "db"), resWithin("acct-1", "api")),
		"BothEdges traversal must follow incoming edges too")
}

func TestDepChecker_NoPath_Orphan(t *testing.T) {
	ctx := context.Background()
	c := NewDependencyChecker(NewCloudSubgraph([]GraphSlice{seedDepCheckerCloudGraph("acct-1")}))
	assert.False(t, c.HasDependency(ctx,
		resWithin("acct-1", "api"), resWithin("acct-1", "orphan")),
		"orphan node is not reachable from api")
}

func TestDepChecker_SameNode_True(t *testing.T) {
	ctx := context.Background()
	c := NewDependencyChecker(NewCloudSubgraph([]GraphSlice{seedDepCheckerCloudGraph("acct-1")}))
	assert.True(t, c.HasDependency(ctx,
		resWithin("acct-1", "api"), resWithin("acct-1", "api")),
		"self-identity is a trivial dependency")
}

func TestDepChecker_EmptyIDs_False(t *testing.T) {
	ctx := context.Background()
	c := NewDependencyChecker(NewCloudSubgraph([]GraphSlice{seedDepCheckerCloudGraph("acct-1")}))
	assert.False(t, c.HasDependency(ctx,
		resWithin("acct-1", ""), resWithin("acct-1", "db")))
	assert.False(t, c.HasDependency(ctx,
		resWithin("acct-1", "api"), resWithin("acct-1", "")))
	assert.False(t, c.HasDependency(ctx,
		resWithin("acct-1", ""), resWithin("acct-1", "")))
}

func TestDepChecker_NonexistentStartNode(t *testing.T) {
	ctx := context.Background()
	c := NewDependencyChecker(NewCloudSubgraph([]GraphSlice{seedDepCheckerCloudGraph("acct-1")}))
	assert.False(t, c.HasDependency(ctx,
		resWithin("acct-1", "ghost"), resWithin("acct-1", "db")))
}

func TestDepChecker_MissingAccount_False(t *testing.T) {
	// Empty subgraph — the requested account is absent entirely.
	c := NewDependencyChecker(NewCloudSubgraph(nil))
	assert.False(t, c.HasDependency(context.Background(),
		resWithin("missing-account", "api"), resWithin("missing-account", "db")))
}

func TestDepChecker_NilStore_False(t *testing.T) {
	c := NewDependencyChecker(nil)
	assert.False(t, c.HasDependency(context.Background(),
		resWithin("acct-1", "api"), resWithin("acct-1", "db")))
}

// TestDepChecker_CrossAccount_NoProxy_False confirms HasDependency
// returns false when the two ResolvedResources come from different
// cloud graphs AND neither graph contains a cross-graph proxy linking
// them. The presence of an isolated second graph is not enough — a
// real dependency requires either (a) both endpoints in the same
// graph, or (b) a proxy edge bridging the two graphs. Cross-graph
// proxy discovery is exercised by TestDepChecker_CrossGraph_ViaProxy
// and TestDepChecker_ProxyHopIsTransparent.
func TestDepChecker_CrossAccount_NoProxy_False(t *testing.T) {
	// Two isolated account slices so the BFS would otherwise have something to
	// walk if the checker accidentally fell back to the wrong graph. No proxy
	// edge links the two — the walker must not find a path.
	c := NewDependencyChecker(NewCloudSubgraph([]GraphSlice{
		seedDepCheckerCloudGraph("acct-1"),
		seedDepCheckerCloudGraph("acct-2"),
	}))
	assert.False(t, c.HasDependency(context.Background(),
		resWithin("acct-1", "api"), resWithin("acct-2", "db")),
		"cross-graph dependency without a proxy must not be reported")
}

// TestDepChecker_BeyondHopLimit seeds a chain longer than maxDependencyHops
// to confirm the BFS bound is enforced. We need 4+ hops to exceed the
// default of 3.
func TestDepChecker_BeyondHopLimit(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "n0", Type: string(kgtypes.NodeCloudResource), SymbolName: "n0", Source: "cloud"},
		{Id: "n1", Type: string(kgtypes.NodeCloudResource), SymbolName: "n1", Source: "cloud"},
		{Id: "n2", Type: string(kgtypes.NodeCloudResource), SymbolName: "n2", Source: "cloud"},
		{Id: "n3", Type: string(kgtypes.NodeCloudResource), SymbolName: "n3", Source: "cloud"},
		{Id: "n4", Type: string(kgtypes.NodeCloudResource), SymbolName: "n4", Source: "cloud"},
	}
	for i := range nodes {
		kgtypes.SetValue(nodes[i], "resource_type", "test:resource")
	}
	edges := []knowledgev1.Edge{
		{FromId: "n0", ToId: "n1", Type: string(kgtypes.EdgeType("DEPENDS_ON"))},
		{FromId: "n1", ToId: "n2", Type: string(kgtypes.EdgeType("DEPENDS_ON"))},
		{FromId: "n2", ToId: "n3", Type: string(kgtypes.EdgeType("DEPENDS_ON"))},
		{FromId: "n3", ToId: "n4", Type: string(kgtypes.EdgeType("DEPENDS_ON"))},
	}

	c := NewDependencyChecker(NewCloudSubgraph([]GraphSlice{
		{Name: "acct-hops", Nodes: nodes, Edges: edges},
	}))

	ctx := context.Background()
	// n0 → n3 is exactly 3 hops → within the limit.
	assert.True(t, c.HasDependency(ctx,
		resWithin("acct-hops", "n0"), resWithin("acct-hops", "n3")))
	// n0 → n4 is 4 hops → beyond the limit.
	assert.False(t, c.HasDependency(ctx,
		resWithin("acct-hops", "n0"), resWithin("acct-hops", "n4")),
		"4-hop path must exceed maxDependencyHops=3")
}
