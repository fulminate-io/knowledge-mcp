// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// clusterResNode builds an arbitrary K8s cloud-resource node the way the
// subcollectors emit them: namespace + resource_type metadata and a
// deterministic resourceID ID. Used to seed the cluster linkage tests.
func clusterResNode(namespace, resourceType, name string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         resourceID(namespace, resourceType, name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
	}
	kgtypes.SetValue(n, "resource_type", resourceType)
	if namespace != "" {
		kgtypes.SetValue(n, "namespace", namespace)
	}
	return n
}

// proxyResNode builds a pre-existing cross-graph proxy node — used to
// assert that buildClusterLinkageEdges skips proxies rather than
// double-linking them.
func proxyResNode(id string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id:   id,
		Type: string(kgtypes.NodeProxy),
	}
}

func TestBuildClusterLinkageEdges(t *testing.T) {
	const proxyID = "proxy:cloud:my-project:https://container.googleapis.com/v1/projects/my-project/locations/us-central1/clusters/main"

	cases := []struct {
		name     string
		input    []*knowledgev1.Node
		proxyID  string
		wantFrom []string // order-agnostic set of FromIDs expected
	}{
		{
			name: "every non-proxy cloud resource emits one edge",
			input: []*knowledgev1.Node{
				clusterResNode("default", "Deployment", "web"),
				clusterResNode("default", "DaemonSet", "node-exporter"),
				clusterResNode("default", "StatefulSet", "postgres"),
				clusterResNode("default", "Pod", "web-0"),
				clusterResNode("default", "Service", "web"),
				clusterResNode("default", "ConfigMap", "settings"),
				clusterResNode("", "Namespace", "default"),
			},
			proxyID: proxyID,
			wantFrom: []string{
				"default/Deployment/web",
				"default/DaemonSet/node-exporter",
				"default/StatefulSet/postgres",
				"default/Pod/web-0",
				"default/Service/web",
				"default/ConfigMap/settings",
				"Namespace/default",
			},
		},
		{
			name: "proxy nodes are skipped (no self-loop, no proxy-to-proxy)",
			input: []*knowledgev1.Node{
				clusterResNode("default", "Deployment", "web"),
				proxyResNode(proxyID),
				proxyResNode("proxy:cloud:other-project:other-node"),
			},
			proxyID:  proxyID,
			wantFrom: []string{"default/Deployment/web"},
		},
		{
			name: "node whose ID equals proxy ID is skipped even if type=cloud_resource",
			input: []*knowledgev1.Node{
				{Id: proxyID, Type: string(kgtypes.NodeCloudResource), SymbolName: "impostor"},
				clusterResNode("default", "Pod", "web-0"),
			},
			proxyID:  proxyID,
			wantFrom: []string{"default/Pod/web-0"},
		},
		{
			name:     "empty resource list produces no edges",
			input:    nil,
			proxyID:  proxyID,
			wantFrom: nil,
		},
		{
			name: "empty proxyID returns no edges (guard)",
			input: []*knowledgev1.Node{
				clusterResNode("default", "Deployment", "web"),
			},
			proxyID:  "",
			wantFrom: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edges := buildClusterLinkageEdges(tc.input, tc.proxyID)

			require.Len(t, edges, len(tc.wantFrom),
				"unexpected edge count — got %d edges", len(edges))

			gotFrom := map[string]string{}
			for i := range edges {
				e := &edges[i]
				assert.Equal(t, string(kgtypes.EdgeRunsInCluster), e.Type,
					"edges must use kgtypes.EdgeRunsInCluster")
				assert.Equal(t, tc.proxyID, e.ToId,
					"every edge must target the cluster proxy")
				assert.Empty(t, e.Method,
					"RUNS_IN_CLUSTER edges must be bare (no Method metadata)")
				gotFrom[e.FromId] = e.ToId
			}
			for _, from := range tc.wantFrom {
				assert.Contains(t, gotFrom, from,
					"expected edge from %q not emitted", from)
			}
		})
	}
}

// TestResolveClusterLinkage_Integration drives the full postPopulate
// path against a GKE-named cloud graph. It seeds a handful of cloud
// resources, runs postPopulate, and verifies: (a) a cross-graph proxy
// node was created with the expected deterministic ID; (b) every
// NodeCloudResource gained a RUNS_IN_CLUSTER edge to the proxy; (c) two
// workloads in the same cluster can reach each other via a 2-hop walk
// through the proxy.
func TestResolveClusterLinkage_Integration(t *testing.T) {
	ctx := newCtx(t)

	const (
		gkeGraph    = "gke_my-project_us-central1_main"
		parentGraph = "my-project"
		selfLink    = "https://container.googleapis.com/v1/projects/my-project/locations/us-central1/clusters/main"
		proxyID     = "proxy:cloud:my-project:" + selfLink
	)

	fake := newK8sFake()

	// Seed the parent project graph with a real Cluster node. The
	// assertions below pass even without this, but it mirrors the real
	// topology (the proxy created in the GKE graph points at this node).
	clusterNode := &knowledgev1.Node{
		Id:         selfLink,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "main",
		Source:     "cloud",
	}
	kgtypes.SetValue(clusterNode, "resource_type", "gcp:container:cluster")
	fake.seed(parentGraph, clusterNode)

	// Seed the GKE graph with a mix of resources.
	seeds := []*knowledgev1.Node{
		clusterResNode("default", "Deployment", "web"),
		clusterResNode("default", "DaemonSet", "node-exporter"),
		clusterResNode("default", "StatefulSet", "postgres"),
		clusterResNode("default", "Pod", "web-0"),
		clusterResNode("default", "Service", "web"),
		clusterResNode("", "Namespace", "default"),
	}
	fake.seed(gkeGraph, seeds...)

	require.NoError(t, postPopulate(ctx, fake, gkeGraph))

	// Verify the proxy exists with the expected deterministic ID.
	proxy, ok := fake.nodeByID(gkeGraph, proxyID)
	require.True(t, ok, "cluster proxy must be created in the GKE graph")
	assert.Equal(t, string(kgtypes.NodeProxy), proxy.Type)
	assert.Equal(t, "cloud", kgtypes.Value(proxy, "foreign_graph"))
	assert.Equal(t, "my-project", kgtypes.Value(proxy, "account"))
	assert.Equal(t, selfLink, kgtypes.Value(proxy, "foreign_id"))
	assert.Equal(t, "main", proxy.SymbolName)
	assert.Equal(t, "gcp:container:cluster", kgtypes.Value(proxy, "resource_type"))
	assert.Equal(t, "us-central1", kgtypes.Value(proxy, "region"))

	// Verify every seed gained a RUNS_IN_CLUSTER edge to the proxy.
	members := collectIncomingRunsInCluster(fake, gkeGraph, proxyID)
	for _, seed := range seeds {
		assert.Contains(t, members, seed.Id,
			"every cloud resource must get a RUNS_IN_CLUSTER edge to the cluster proxy")
	}
	// The proxy itself must NOT link to itself.
	assert.NotContains(t, members, proxyID,
		"cluster proxy must not link to itself")

	// Two-hop-through-proxy: Deployment → proxy → Namespace reaches the
	// sibling namespace. This is the cross-graph walk the dep checker
	// will later exercise end-to-end.
	reached := twoHopThroughCluster(fake, gkeGraph, seeds[0].Id, proxyID)
	assert.Contains(t, reached, "Namespace/default",
		"2-hop via cluster proxy must surface sibling resources")

	// Idempotency: a second postPopulate run must not create duplicate
	// edges (the upsert on the proxy returns the same ID; edges are
	// deduped by (FromID, Type, ToID) at the store layer).
	require.NoError(t, postPopulate(ctx, fake, gkeGraph))
	membersAfter := collectIncomingRunsInCluster(fake, gkeGraph, proxyID)
	assert.Len(t, membersAfter, len(members),
		"idempotent postPopulate must not multiply edges")
}

// TestResolveClusterLinkage_NonGKEContext_NoOp confirms the resolver is
// a silent no-op for graphs whose names do not match the GKE kubecontext
// convention — EKS, AKS, plain kubeconfigs all pass through with no
// proxy, no edges, and no error.
func TestResolveClusterLinkage_NonGKEContext_NoOp(t *testing.T) {
	ctx := newCtx(t)

	const nonGKEGraph = "eks_acme_prod-cluster"

	fake := newK8sFake()
	seeds := []*knowledgev1.Node{
		clusterResNode("default", "Deployment", "web"),
		clusterResNode("default", "Pod", "web-0"),
	}
	fake.seed(nonGKEGraph, seeds...)

	require.NoError(t, resolveClusterLinkage(ctx, fake, nonGKEGraph))

	// No proxy: scanning for any NodeProxy in the graph must return
	// nothing — no proxy node was materialized.
	for _, n := range fake.allNodes(nonGKEGraph) {
		assert.NotEqual(t, string(kgtypes.NodeProxy), n.Type,
			"non-GKE graphs must not gain a cluster proxy")
	}

	// No RUNS_IN_CLUSTER edges.
	for _, seed := range seeds {
		assert.Empty(t, fake.outgoingEdges(nonGKEGraph, seed.Id, kgtypes.EdgeRunsInCluster),
			"non-GKE graphs must not emit RUNS_IN_CLUSTER edges from %q", seed.Id)
	}
}

// collectIncomingRunsInCluster returns the set of source IDs of
// RUNS_IN_CLUSTER edges pointing at target in the named account graph.
func collectIncomingRunsInCluster(fake *k8sFake, account, target string) map[string]bool {
	return fake.incomingEdges(account, target, kgtypes.EdgeRunsInCluster)
}

// twoHopThroughCluster walks one RUNS_IN_CLUSTER hop from start to the
// cluster proxy, then one RUNS_IN_CLUSTER hop back to its sibling
// members. Returns the set of siblings reached (excluding start).
func twoHopThroughCluster(fake *k8sFake, account, start, proxyID string) map[string]bool {
	hit := false
	startEdges := fake.outgoingEdges(account, start, kgtypes.EdgeRunsInCluster)
	for i := range startEdges {
		if startEdges[i].ToId == proxyID {
			hit = true
			break
		}
	}
	if !hit {
		return nil
	}
	siblings := collectIncomingRunsInCluster(fake, account, proxyID)
	delete(siblings, start)
	return siblings
}
