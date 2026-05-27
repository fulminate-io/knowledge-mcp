// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestEmitClusterLinkageClientSide_EKS(t *testing.T) {
	const (
		contextName = "arn:aws:eks:us-west-2:123456789012:cluster/prod"
		account     = "123456789012"
		region      = "us-west-2"
		cluster     = "prod"
	)
	expectedClusterID := contextName // EKS contextName IS the canonical ARN.
	expectedProxyID := "proxy:cloud:" + account + ":" + expectedClusterID

	result := &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCloud,
		GraphName: contextName,
		Nodes: []*knowledgev1.Node{
			clusterResNode("default", "Deployment", "web"),
			clusterResNode("default", "Pod", "web-0"),
		},
	}

	emitClusterLinkageClientSide(context.Background(), contextName, result)

	// Exactly one proxy appended.
	require.Len(t, result.Nodes, 3, "expected the two seeds + one proxy")
	proxy := result.Nodes[2]
	assert.Equal(t, expectedProxyID, proxy.Id)
	assert.Equal(t, string(kgtypes.NodeProxy), proxy.Type)
	assert.Equal(t, "proxy:cloud:"+account, proxy.Source)
	assert.Equal(t, cluster, proxy.SymbolName)

	// Metadata mirrors crossgraph.BuildCrossGraphProxy GraphCloud branch.
	assert.Equal(t, string(kgtypes.GraphCloud), kgtypes.Value(proxy, "foreign_graph"))
	assert.Equal(t, expectedClusterID, kgtypes.Value(proxy, "foreign_id"))
	assert.Equal(t, account, kgtypes.Value(proxy, "account"))
	assert.Equal(t, string(kgtypes.NodeCloudResource), kgtypes.Value(proxy, "foreign_type"))
	assert.Equal(t, "eks-cluster", kgtypes.Value(proxy, "resource_type"))
	assert.Equal(t, "aws", kgtypes.Value(proxy, "provider"))
	assert.Equal(t, region, kgtypes.Value(proxy, "region"))

	// Two RUNS_IN_CLUSTER edges, one per seed resource.
	require.Len(t, result.Edges, 2)
	gotFrom := map[string]bool{}
	for _, e := range result.Edges {
		assert.Equal(t, kgtypes.EdgeRunsInCluster, e.Type)
		assert.Equal(t, expectedProxyID, e.ToID)
		assert.Equal(t, -1, e.FromIdx)
		assert.Equal(t, -1, e.ToIdx)
		gotFrom[e.FromID] = true
	}
	assert.True(t, gotFrom[result.Nodes[0].Id])
	assert.True(t, gotFrom[result.Nodes[1].Id])
}

func TestEmitClusterLinkageClientSide_AKS(t *testing.T) {
	const (
		contextName  = "aks-dev"
		subscription = "11111111-2222-3333-4444-555555555555"
		armPath      = "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/rg/providers/Microsoft.ContainerService/managedClusters/aks-dev"
	)
	expectedProxyID := "proxy:cloud:" + subscription + ":" + armPath

	rm := cloud.NewResolutionMap()
	rm.Record(contextName, armPath)
	ctx := cloud.WithResolutionMap(context.Background(), rm)

	result := &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCloud,
		GraphName: contextName,
		Nodes: []*knowledgev1.Node{
			clusterResNode("default", "Deployment", "web"),
			clusterResNode("default", "Service", "web"),
		},
	}

	emitClusterLinkageClientSide(ctx, contextName, result)

	require.Len(t, result.Nodes, 3)
	proxy := result.Nodes[2]
	assert.Equal(t, expectedProxyID, proxy.Id)
	assert.Equal(t, string(kgtypes.NodeProxy), proxy.Type)
	assert.Equal(t, "proxy:cloud:"+subscription, proxy.Source)
	assert.Equal(t, contextName, proxy.SymbolName, "AKS proxy uses the contextName as display name")

	assert.Equal(t, string(kgtypes.GraphCloud), kgtypes.Value(proxy, "foreign_graph"))
	assert.Equal(t, armPath, kgtypes.Value(proxy, "foreign_id"))
	assert.Equal(t, subscription, kgtypes.Value(proxy, "account"))
	assert.Equal(t, "Microsoft.ContainerService/managedClusters", kgtypes.Value(proxy, "resource_type"))
	assert.Equal(t, "azure", kgtypes.Value(proxy, "provider"))
	assert.Empty(t, kgtypes.Value(proxy, "region"), "AKS region is intentionally empty per plan")

	require.Len(t, result.Edges, 2)
	for _, e := range result.Edges {
		assert.Equal(t, kgtypes.EdgeRunsInCluster, e.Type)
		assert.Equal(t, expectedProxyID, e.ToID)
	}
}

func TestEmitClusterLinkageClientSide_NoOp(t *testing.T) {
	cases := []struct {
		name        string
		contextName string
	}{
		{"GKE", "gke_my-project_us-central1_main"},
		{"plain context (minikube)", "minikube"},
		{"empty context", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := &collectorwire.CollectResult{
				Nodes: []*knowledgev1.Node{
					clusterResNode("default", "Deployment", "web"),
				},
			}
			beforeNodes := len(result.Nodes)
			beforeEdges := len(result.Edges)

			emitClusterLinkageClientSide(context.Background(), tc.contextName, result)

			assert.Len(t, result.Nodes, beforeNodes, "Nodes must be unchanged for non-EKS/non-AKS contextName")
			assert.Len(t, result.Edges, beforeEdges, "Edges must be unchanged for non-EKS/non-AKS contextName")
		})
	}
}

func TestEmitClusterLinkageClientSide_AKSWithoutResolutionMap(t *testing.T) {
	// "aks-dev" is a plausible AKS contextName, but ctx has no
	// ResolutionMap installed (k8s collector invoked directly without an
	// Azure cascade). Helper must silently skip.
	result := &collectorwire.CollectResult{
		Nodes: []*knowledgev1.Node{
			clusterResNode("default", "Deployment", "web"),
		},
	}
	beforeNodes := len(result.Nodes)
	beforeEdges := len(result.Edges)

	emitClusterLinkageClientSide(context.Background(), "aks-dev", result)

	assert.Len(t, result.Nodes, beforeNodes)
	assert.Len(t, result.Edges, beforeEdges)
}

func TestEmitClusterLinkageClientSide_AKSResolutionMapMiss(t *testing.T) {
	// ResolutionMap is on ctx but does not have "aks-dev" recorded.
	rm := cloud.NewResolutionMap()
	rm.Record("aks-other", "/subscriptions/sub/.../managedClusters/aks-other")
	ctx := cloud.WithResolutionMap(context.Background(), rm)

	result := &collectorwire.CollectResult{
		Nodes: []*knowledgev1.Node{
			clusterResNode("default", "Deployment", "web"),
		},
	}
	beforeNodes := len(result.Nodes)
	beforeEdges := len(result.Edges)

	emitClusterLinkageClientSide(ctx, "aks-dev", result)

	assert.Len(t, result.Nodes, beforeNodes)
	assert.Len(t, result.Edges, beforeEdges)
}

func TestEmitClusterLinkageClientSide_SkipsExistingProxies(t *testing.T) {
	const (
		contextName = "arn:aws:eks:us-west-2:123456789012:cluster/prod"
		account     = "123456789012"
	)
	expectedProxyID := "proxy:cloud:" + account + ":" + contextName

	// Pre-seed result.Nodes with an unrelated cross-graph proxy that
	// some other sub-collector might have emitted (e.g. a workload-identity
	// proxy). buildClusterLinkageBatchEdges must not emit a
	// RUNS_IN_CLUSTER edge from that proxy to the cluster proxy.
	otherProxy := &knowledgev1.Node{
		Id:   "proxy:cloud:other-account:arn:aws:iam::other-account:role/foo",
		Type: string(kgtypes.NodeProxy),
	}
	result := &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCloud,
		GraphName: contextName,
		Nodes: []*knowledgev1.Node{
			clusterResNode("default", "Deployment", "web"),
			otherProxy,
		},
	}

	emitClusterLinkageClientSide(context.Background(), contextName, result)

	// Cluster proxy was appended.
	require.Len(t, result.Nodes, 3)
	clusterProxy := result.Nodes[2]
	assert.Equal(t, expectedProxyID, clusterProxy.Id)

	// Exactly ONE edge — only the Deployment, not the other proxy.
	require.Len(t, result.Edges, 1)
	assert.Equal(t, result.Nodes[0].Id, result.Edges[0].FromID)
	assert.NotEqual(t, otherProxy.Id, result.Edges[0].FromID, "must not emit edge from a pre-existing proxy")
}

func TestParseAKSSubscriptionFromARMPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
		ok   bool
	}{
		{
			name: "happy path",
			path: "/subscriptions/sub-uuid/resourceGroups/rg/providers/Microsoft.ContainerService/managedClusters/aks-dev",
			want: "sub-uuid",
			ok:   true,
		},
		{
			name: "missing prefix",
			path: "subscriptions/sub-uuid/resourceGroups/rg/providers/...",
			ok:   false,
		},
		{
			name: "empty subscription segment",
			path: "/subscriptions//resourceGroups/rg/providers/...",
			ok:   false,
		},
		{
			name: "subscription only (no trailing slash)",
			path: "/subscriptions/sub-uuid",
			want: "sub-uuid",
			ok:   true,
		},
		{
			name: "empty",
			path: "",
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAKSSubscriptionFromARMPath(tc.path)
			assert.Equal(t, tc.ok, ok)
			if !tc.ok {
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
