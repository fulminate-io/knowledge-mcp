// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// nsNode builds a Namespace cloud-resource node whose ID matches the
// convention resolveNamespaceMembership expects (resourceID("", "Namespace",
// name) == "Namespace/<name>").
func nsNode(name string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         resourceID("", "Namespace", name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
	}
	kgtypes.SetValue(n, "resource_type", "Namespace")
	return n
}

// namespacedNode builds an arbitrary namespaced cloud-resource node with the
// given (namespace, resource_type, name). The returned node carries the same
// metadata shape the K8s subcollectors emit: a "namespace" key plus a
// "resource_type" key.
func namespacedNode(namespace, resourceType, name string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         resourceID(namespace, resourceType, name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
	}
	kgtypes.SetValue(n, "resource_type", resourceType)
	kgtypes.SetValue(n, "namespace", namespace)
	return n
}

// clusterScopedNode builds a cluster-scoped resource node (no namespace meta).
func clusterScopedNode(resourceType, name string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         resourceID("", resourceType, name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
	}
	kgtypes.SetValue(n, "resource_type", resourceType)
	return n
}

func TestBuildNamespaceMembershipEdges(t *testing.T) {
	cases := []struct {
		name        string
		input       []*knowledgev1.Node
		nsSet       map[string]bool // known Namespace node IDs; edges to absent targets are dropped
		wantFrom    []string        // ordered list of FromIDs expected (order-agnostic assertion below)
		wantTo      map[string]string
		wantSkipped int
	}{
		{
			name: "namespaced resource emits IN_NAMESPACE edge",
			input: []*knowledgev1.Node{
				namespacedNode("kube-system", "DaemonSet", "kube-proxy"),
			},
			nsSet:    map[string]bool{"Namespace/kube-system": true},
			wantFrom: []string{"kube-system/DaemonSet/kube-proxy"},
			wantTo: map[string]string{
				"kube-system/DaemonSet/kube-proxy": "Namespace/kube-system",
			},
		},
		{
			name: "cluster-scoped resource (empty namespace meta) is skipped",
			input: []*knowledgev1.Node{
				clusterScopedNode("ClusterRole", "view"),
				clusterScopedNode("PersistentVolume", "pv-1"),
			},
			wantFrom: nil,
		},
		{
			name:  "Namespace node itself is skipped",
			input: []*knowledgev1.Node{nsNode("kube-system")},
			// Namespace nodes have empty namespace meta AND resource_type ==
			// "Namespace"; either guard alone is sufficient. Exercising both
			// confirms the resource_type guard precedes the empty check.
			wantFrom: nil,
		},
		{
			name: "mixed input emits edges only for namespaced non-Namespace resources",
			input: []*knowledgev1.Node{
				nsNode("default"),
				namespacedNode("default", "Deployment", "web"),
				namespacedNode("default", "Service", "web"),
				clusterScopedNode("ClusterRole", "admin"),
			},
			nsSet: map[string]bool{"Namespace/default": true},
			wantFrom: []string{
				"default/Deployment/web",
				"default/Service/web",
			},
			wantTo: map[string]string{
				"default/Deployment/web": "Namespace/default",
				"default/Service/web":    "Namespace/default",
			},
		},
		{
			name: "missing target Namespace is filtered against nsSet",
			// The builder now gates each candidate against nsSet (the
			// known-namespace ID set). A resource whose namespace has no
			// Namespace node is dropped and counted in skipped.
			input: []*knowledgev1.Node{
				namespacedNode("ghost-ns", "Deployment", "orphan"),
			},
			nsSet:       map[string]bool{}, // ghost-ns has no Namespace node
			wantFrom:    nil,
			wantSkipped: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edges, skipped := buildNamespaceMembershipEdges(tc.input, tc.nsSet)

			require.Len(t, edges, len(tc.wantFrom),
				"unexpected edge count: got %d edges", len(edges))
			assert.Equal(t, tc.wantSkipped, skipped,
				"unexpected skipped count")

			gotFrom := map[string]string{}
			for i := range edges {
				e := &edges[i]
				assert.Equal(t, string(kgtypes.EdgeInNamespace), e.Type,
					"edges must use kgtypes.EdgeInNamespace")
				assert.Empty(t, e.Method,
					"IN_NAMESPACE edges must be bare (no Method metadata)")
				gotFrom[e.FromId] = e.ToId
			}
			for _, from := range tc.wantFrom {
				assert.Contains(t, gotFrom, from,
					"expected edge from %q not emitted", from)
				if tc.wantTo != nil {
					assert.Equal(t, tc.wantTo[from], gotFrom[from],
						"wrong target for edge from %q", from)
				}
			}
		})
	}
}

// TestResolveNamespaceMembership_Integration drives the full postPopulate path
// against the wire fake. It builds a Namespace node + two DaemonSets
// in the same namespace, runs postPopulate, and confirms both DaemonSets are
// reachable from (or, equivalently, reach) the Namespace node through the
// IN_NAMESPACE edges emitted by the new resolver.
func TestResolveNamespaceMembership_Integration(t *testing.T) {
	ctx := newCtx(t)
	const acct = "k8s-test"

	// Seed a Namespace + 2 DaemonSets + 1 cluster-scoped resource + 1
	// resource pointing at a non-existent namespace (the "ghost" case the
	// resolver must filter silently).
	ns := nsNode("kube-system")
	ds1 := namespacedNode("kube-system", "DaemonSet", "kube-proxy")
	ds2 := namespacedNode("kube-system", "DaemonSet", "fluentd")
	clusterRole := clusterScopedNode("ClusterRole", "view")
	ghost := namespacedNode("ghost-ns", "Deployment", "orphan")

	fake := newK8sFake()
	fake.seed(acct, ns, ds1, ds2, clusterRole, ghost)

	// Run the resolver over the wire fake, routed to the per-account graph.
	require.NoError(t, resolveNamespaceMembership(ctx, fake, acct))

	// Collect all IN_NAMESPACE edges incoming to the Namespace node; both
	// DaemonSets must be reachable via this single hop. The ghost deployment
	// must NOT have produced an edge — its target namespace does not exist.
	members := fake.incomingEdges(acct, ns.Id, kgtypes.EdgeInNamespace)

	assert.Contains(t, members, ds1.Id, "DaemonSet kube-proxy must link to Namespace")
	assert.Contains(t, members, ds2.Id, "DaemonSet fluentd must link to Namespace")
	assert.NotContains(t, members, clusterRole.Id,
		"ClusterRole is cluster-scoped — must not link to any Namespace")
	assert.NotContains(t, members, ghost.Id,
		"ghost Deployment links to non-existent Namespace — edge must be skipped")
}
