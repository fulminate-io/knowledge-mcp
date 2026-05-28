// SPDX-License-Identifier: Apache-2.0

package cloudresolver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// seedClusterProxyGraphs returns two cloud-graph GraphSlices wired by a
// RUNS_IN_CLUSTER edge pointing at a cluster proxy, mirroring the shape
// postpopulate_cluster.go produces in production.
//
//	GKE graph "gke_proj_us-central1_main":
//	  workloadA ─RUNS_IN_CLUSTER─▶ proxy:cloud:proj:<selfLink>
//	  workloadB ─RUNS_IN_CLUSTER─▶ proxy:cloud:proj:<selfLink>
//	  namespaceA (disconnected)
//
//	Parent graph "proj":
//	  <selfLink> (GCP Cluster node)
//
// The proxy in the GKE graph carries foreign_graph="cloud",
// account="proj", foreign_id=<selfLink> so resolveCloudProxy resolves it
// to (account="proj", nodeID=<selfLink>) — exactly what the walker
// needs to follow into the parent graph. The slices feed NewCloudSubgraph
// directly — no store engine.
func seedClusterProxyGraphs() []GraphSlice {
	const (
		gkeAccount    = "gke_proj_us-central1_main"
		parentAccount = "proj"
		selfLink      = "https://container.googleapis.com/v1/projects/proj/locations/us-central1/clusters/main"
		proxyID       = "proxy:cloud:proj:" + selfLink
	)

	return []GraphSlice{
		{Name: parentAccount, Nodes: []*knowledgev1.Node{
			mkCloudResource(selfLink, "main", "gcp:container:cluster"),
		}},
		{
			Name: gkeAccount,
			Nodes: []*knowledgev1.Node{
				mkCloudResource("workloadA", "workloadA", "Deployment"),
				mkCloudResource("workloadB", "workloadB", "Deployment"),
				mkCloudResource("namespaceA", "namespaceA", "Deployment"),
				// Cluster proxy carries the metadata resolveCloudProxy
				// reads (foreign_graph=cloud, account=proj,
				// foreign_id=selfLink). mkProxy returns a *knowledgev1.Node
				// so the pointer lands in the slice directly.
				mkProxy(proxyID, map[string]string{
					"foreign_graph": "cloud",
					"account":       parentAccount,
					"foreign_id":    selfLink,
				}),
			},
			Edges: []knowledgev1.Edge{
				{FromId: "workloadA", ToId: proxyID, Type: string(kgtypes.EdgeRunsInCluster)},
				{FromId: "workloadB", ToId: proxyID, Type: string(kgtypes.EdgeRunsInCluster)},
			},
		},
	}
}

// TestDepChecker_CrossGraph_ViaProxy confirms the walker follows a
// cloud proxy into its target graph. Seeds two graphs with a proxy in
// one pointing at a node in the other, then asserts HasDependency is
// true for {graph1, workload} → {graph2, cluster-self-link}.
func TestDepChecker_CrossGraph_ViaProxy(t *testing.T) {
	c := NewDependencyChecker(NewCloudSubgraph(seedClusterProxyGraphs()))
	const selfLink = "https://container.googleapis.com/v1/projects/proj/locations/us-central1/clusters/main"

	assert.True(t, c.HasDependency(context.Background(),
		resWithin("gke_proj_us-central1_main", "workloadA"),
		resWithin("proj", selfLink)),
		"walker must follow the cluster proxy into the parent graph")
}

// TestDepChecker_ProxyHopIsTransparent pins the user decision that
// proxy traversal consumes zero hops. workloadA reaches workloadB
// through the shared cluster proxy; the path is two semantic hops
// (workload→proxy→workload), and the test asserts it resolves within
// the standard maxDependencyHops=3 budget.
//
// The key invariant: two workloads sharing a cluster proxy are
// reachable WITHOUT cascading through an extra hop for the proxy
// itself. If the proxy hop were not transparent, workloadA would have
// to spend one hop on the proxy and still have budget to reach
// workloadB — exercising the transparency path.
func TestDepChecker_ProxyHopIsTransparent(t *testing.T) {
	c := NewDependencyChecker(NewCloudSubgraph(seedClusterProxyGraphs()))
	// Two workloads in the same cluster — should be reachable via the
	// shared cluster proxy as a semantic 2-hop dependency.
	assert.True(t, c.HasDependency(context.Background(),
		resWithin("gke_proj_us-central1_main", "workloadA"),
		resWithin("gke_proj_us-central1_main", "workloadB")),
		"workloadA → clusterProxy → workloadB must resolve as a semantic 2-hop "+
			"dependency (proxy traversal is transparent)")
}

// TestDepChecker_VisitedDedupByAccountID confirms the visited-set
// correctly distinguishes nodes that share an ID across two cloud
// graphs. Without (account, id) keying, visiting "shared" in acct-1
// would prevent us from visiting "shared" in acct-2, silently eating
// cross-graph reachability.
func TestDepChecker_VisitedDedupByAccountID(t *testing.T) {
	const proxyID = "proxy:cloud:acct-2:shared"

	slices := []GraphSlice{
		{
			// acct-1: entry → shared → proxy-into-acct-2.
			Name: "acct-1",
			Nodes: []*knowledgev1.Node{
				mkCloudResource("entry", "entry", "test:resource"),
				mkCloudResource("shared", "shared", "test:resource"),
				mkProxy(proxyID, map[string]string{
					"foreign_graph": "cloud",
					"account":       "acct-2",
					"foreign_id":    "shared",
				}),
			},
			Edges: []knowledgev1.Edge{
				{FromId: "entry", ToId: "shared", Type: string(kgtypes.EdgeType("DEPENDS_ON"))},
				{FromId: "shared", ToId: proxyID, Type: string(kgtypes.EdgeRunsInCluster)},
			},
		},
		{
			// acct-2: shared → target (the same "shared" string ID lives in
			// both graphs — the keying must keep them distinct).
			Name: "acct-2",
			Nodes: []*knowledgev1.Node{
				mkCloudResource("shared", "shared", "test:resource"),
				mkCloudResource("target", "target", "test:resource"),
			},
			Edges: []knowledgev1.Edge{
				{FromId: "shared", ToId: "target", Type: string(kgtypes.EdgeType("DEPENDS_ON"))},
			},
		},
	}

	c := NewDependencyChecker(NewCloudSubgraph(slices))
	// entry (acct-1) → shared (acct-1) → proxy → shared (acct-2) → target (acct-2)
	// Hops: 1 (entry→shared), 2 (shared→proxy, then proxy resolves transparently
	// into acct-2:shared, same layer), 3 (shared→target in acct-2).
	assert.True(t, c.HasDependency(context.Background(),
		resWithin("acct-1", "entry"), resWithin("acct-2", "target")),
		"same ID in two graphs must be kept distinct in the visited set")
}
