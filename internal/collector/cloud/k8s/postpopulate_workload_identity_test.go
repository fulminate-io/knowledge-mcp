// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// saResNode seeds a ServiceAccount-shaped cloud resource with the
// namespace + workload-identity annotation metadata that the resolver
// reads back. Tests write directly instead of running the subcollector
// because fake-clientset flows live in sub_serviceaccounts_test.go.
func saResNode(namespace, name string, meta map[string]string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         resourceID(namespace, "ServiceAccount", name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
	}
	kgtypes.SetValue(n, "resource_type", "ServiceAccount")
	kgtypes.SetValue(n, "namespace", namespace)
	for k, v := range meta {
		kgtypes.SetValue(n, k, v)
	}
	return n
}

// collectAssumesIdentityTargets returns the set of ASSUMES_IDENTITY edge
// targets originating from the given source ID in the named account graph.
func collectAssumesIdentityTargets(fake *k8sFake, account, from string) []string {
	var targets []string
	edges := fake.outgoingEdges(account, from, kgtypes.EdgeAssumesIdentity)
	for i := range edges {
		targets = append(targets, edges[i].ToId)
	}
	return targets
}

// TestResolveWorkloadIdentity_IRSA: AWS IRSA-annotated SA gets an
// ASSUMES_IDENTITY edge to a proxy in the {account} cloud graph under
// the role ARN. Proxy is deterministic and idempotent.
func TestResolveWorkloadIdentity_IRSA(t *testing.T) {
	ctx := newCtx(t)

	const (
		roleARN   = "arn:aws:iam::123456789012:role/my-eks-role"
		saID      = "default/ServiceAccount/my-sa"
		eksGraph  = "arn:aws:eks:us-west-2:123456789012:cluster/prod"
		wantProxy = "proxy:cloud:123456789012:" + roleARN
	)

	fake := newK8sFake()
	sa := saResNode("default", "my-sa", map[string]string{
		"irsa_role_arn": roleARN,
	})
	fake.seed(eksGraph, sa)

	require.NoError(t, resolveWorkloadIdentity(ctx, fake, eksGraph))

	proxy, ok := fake.nodeByID(eksGraph, wantProxy)
	require.True(t, ok, "IRSA SA must materialize a cloud proxy in the {account} graph")
	assert.Equal(t, string(kgtypes.NodeProxy), proxy.Type)
	assert.Equal(t, "iam:role", kgtypes.Value(proxy, "resource_type"))
	assert.Equal(t, "aws", kgtypes.Value(proxy, "provider"))

	targets := collectAssumesIdentityTargets(fake, eksGraph, saID)
	require.Len(t, targets, 1)
	assert.Equal(t, wantProxy, targets[0])

	// Re-run: edge count stays 1, proxy ID stays the same.
	require.NoError(t, resolveWorkloadIdentity(ctx, fake, eksGraph))
	targets = collectAssumesIdentityTargets(fake, eksGraph, saID)
	assert.Len(t, targets, 1, "resolver must be idempotent on re-run")
}

// TestResolveWorkloadIdentity_GCP: GKE Workload Identity SA gets an
// ASSUMES_IDENTITY edge to a proxy in the {project} cloud graph under
// projects/{project}/serviceAccounts/{email}.
func TestResolveWorkloadIdentity_GCP(t *testing.T) {
	ctx := newCtx(t)

	const (
		email     = "my-sa@my-project.iam.gserviceaccount.com"
		saID      = "default/ServiceAccount/gke-sa"
		gkeGraph  = "gke_my-project_us-central1_main"
		wantTgt   = "projects/my-project/serviceAccounts/" + email
		wantProxy = "proxy:cloud:my-project:" + wantTgt
	)

	fake := newK8sFake()
	sa := saResNode("default", "gke-sa", map[string]string{
		"gcp_service_account": email,
	})
	fake.seed(gkeGraph, sa)

	require.NoError(t, resolveWorkloadIdentity(ctx, fake, gkeGraph))

	proxy, ok := fake.nodeByID(gkeGraph, wantProxy)
	require.True(t, ok, "GCP WI SA must materialize a proxy")
	assert.Equal(t, "gcp:iam:serviceAccount", kgtypes.Value(proxy, "resource_type"))
	assert.Equal(t, "gcp", kgtypes.Value(proxy, "provider"))
	assert.Equal(t, wantTgt, kgtypes.Value(proxy, "foreign_id"))

	targets := collectAssumesIdentityTargets(fake, gkeGraph, saID)
	require.Len(t, targets, 1)
	assert.Equal(t, wantProxy, targets[0])
}

// TestResolveWorkloadIdentity_Azure: Azure Workload Identity SA
// produces a DANGLING proxy with empty account (per plan OQ).
// The client-id is preserved on the proxy foreign_id for future
// enrichment.
func TestResolveWorkloadIdentity_Azure(t *testing.T) {
	ctx := newCtx(t)

	const (
		clientID  = "abc-123-def"
		saID      = "default/ServiceAccount/azure-sa"
		aksGraph  = "aks_sub1_rg1_cluster"
		wantTgt   = "azure:workload-identity:client-id/" + clientID
		wantProxy = "proxy:cloud::" + wantTgt
	)

	fake := newK8sFake()
	sa := saResNode("default", "azure-sa", map[string]string{
		"azure_client_id": clientID,
	})
	fake.seed(aksGraph, sa)

	require.NoError(t, resolveWorkloadIdentity(ctx, fake, aksGraph))

	p, ok := fake.nodeByID(aksGraph, wantProxy)
	require.True(t, ok, "Azure WI SA must materialize a dangling proxy")
	assert.Equal(t, string(kgtypes.NodeProxy), p.Type)
	assert.Equal(t, "cloud", kgtypes.Value(p, "foreign_graph"))
	assert.Empty(t, kgtypes.Value(p, "account"), "Azure dangling proxy has empty account")
	assert.Equal(t, wantTgt, kgtypes.Value(p, "foreign_id"))
	assert.Equal(t, "true", kgtypes.Value(p, "dangling"))
	assert.Equal(t, "azure:managedidentity", kgtypes.Value(p, "resource_type"))

	targets := collectAssumesIdentityTargets(fake, aksGraph, saID)
	require.Len(t, targets, 1)
	assert.Equal(t, wantProxy, targets[0])
}

// TestResolveWorkloadIdentity_NoAnnotation: ServiceAccount without any
// WI annotation is silently skipped — no proxy, no edge.
func TestResolveWorkloadIdentity_NoAnnotation(t *testing.T) {
	ctx := newCtx(t)

	const (
		saID  = "default/ServiceAccount/plain-sa"
		graph = "plain-cluster"
	)

	fake := newK8sFake()
	sa := saResNode("default", "plain-sa", nil) // no WI metadata
	fake.seed(graph, sa)

	require.NoError(t, resolveWorkloadIdentity(ctx, fake, graph))

	targets := collectAssumesIdentityTargets(fake, graph, saID)
	assert.Empty(t, targets, "SA without WI annotation must not get an edge")
}

// TestResolveWorkloadIdentity_MixedBatch: a single graph with SAs of all
// three provider shapes + one plain SA produces exactly three edges.
func TestResolveWorkloadIdentity_MixedBatch(t *testing.T) {
	ctx := newCtx(t)

	const graph = "mixed-cluster"

	fake := newK8sFake()
	fake.seed(graph,
		saResNode("ns1", "irsa-sa", map[string]string{
			"irsa_role_arn": "arn:aws:iam::111111111111:role/r1",
		}),
		saResNode("ns1", "gcp-sa", map[string]string{
			"gcp_service_account": "sa@proj.iam.gserviceaccount.com",
		}),
		saResNode("ns1", "az-sa", map[string]string{
			"azure_client_id": "guid-1",
		}),
		saResNode("ns1", "plain-sa", nil),
	)

	require.NoError(t, resolveWorkloadIdentity(ctx, fake, graph))

	// Exactly three SAs get an edge; the plain one gets zero.
	assert.Len(t, collectAssumesIdentityTargets(fake, graph, "ns1/ServiceAccount/irsa-sa"), 1)
	assert.Len(t, collectAssumesIdentityTargets(fake, graph, "ns1/ServiceAccount/gcp-sa"), 1)
	assert.Len(t, collectAssumesIdentityTargets(fake, graph, "ns1/ServiceAccount/az-sa"), 1)
	assert.Empty(t, collectAssumesIdentityTargets(fake, graph, "ns1/ServiceAccount/plain-sa"))
}

// TestBuildIRSATarget_MalformedARN: a malformed IRSA ARN yields ok=false
// rather than panicking or producing a proxy with an empty account ID.
func TestBuildIRSATarget_MalformedARN(t *testing.T) {
	sa := &knowledgev1.Node{}
	kgtypes.SetValue(sa, "irsa_role_arn", "not-an-arn")
	_, ok := buildIRSATarget(sa)
	assert.False(t, ok, "malformed ARN must not produce a target")
}

// TestBuildGCPWorkloadIdentityTarget_MalformedEmail: a malformed GCP SA
// email yields ok=false.
func TestBuildGCPWorkloadIdentityTarget_MalformedEmail(t *testing.T) {
	cases := []string{"", "no-at-sign", "@no-domain"}
	for _, email := range cases {
		sa := &knowledgev1.Node{}
		if email != "" {
			kgtypes.SetValue(sa, "gcp_service_account", email)
		}
		_, ok := buildGCPWorkloadIdentityTarget(sa)
		assert.False(t, ok, "malformed email %q must not produce a target", email)
	}
}
