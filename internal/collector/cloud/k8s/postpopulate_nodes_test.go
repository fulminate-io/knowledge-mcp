// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// nodeResNode seeds a Node-shaped cloud resource into a graph with the
// minimal metadata resolveNodeVMLinkage reads back (resource_type and
// provider_id). Tests write directly instead of running the
// subcollector because fake-clientset flows live in sub_nodes_test.go.
func nodeResNode(name, providerID string) *knowledgev1.Node {
	n := &knowledgev1.Node{
		Id:         resourceID("", "Node", name),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: name,
		Source:     "cloud",
	}
	kgtypes.SetValue(n, "resource_type", "Node")
	if providerID != "" {
		kgtypes.SetValue(n, "provider_id", providerID)
	}
	return n
}

// collectBackedByVMTargets returns the set of BACKED_BY_VM edge targets
// originating from the given source ID. Used by every integration test
// to assert exactly one proxy edge per Node.
func collectBackedByVMTargets(fake *k8sFake, account, from string) []string {
	var targets []string
	edges := fake.outgoingEdges(account, from, kgtypes.EdgeBackedByVM)
	for i := range edges {
		targets = append(targets, edges[i].ToId)
	}
	return targets
}

// TestResolveNodeVMLinkage_GKE verifies the full GKE path: a Node with a
// gce:// providerID gets a cross-graph proxy with the canonical Compute
// Engine selfLink ID, and a BACKED_BY_VM edge from the Node to the
// proxy. Nodes with empty or malformed providerIDs get no edge. A
// repeat postPopulate run must not duplicate edges (idempotency).
func TestResolveNodeVMLinkage_GKE(t *testing.T) {
	ctx := newCtx(t)

	const (
		gkeGraph    = "gke_my-project_us-central1_main"
		healthyName = "gke-main-default-pool-abc-123"
		healthyPID  = "gce://my-project/us-central1-a/gke-main-default-pool-abc-123"
		healthyID   = "https://www.googleapis.com/compute/v1/projects/my-project/zones/us-central1-a/instances/gke-main-default-pool-abc-123"
		proxyID     = "proxy:cloud:my-project:" + healthyID
	)

	fake := newK8sFake()
	fake.seed(gkeGraph,
		nodeResNode(healthyName, healthyPID),
		nodeResNode("bare-minikube", ""),                      // no providerID → skipped
		nodeResNode("malformed", "openstack:///instance-999"), // unknown scheme → skipped
	)

	require.NoError(t, resolveNodeVMLinkage(ctx, fake, gkeGraph))

	// Proxy exists with deterministic ID.
	proxy, ok := fake.nodeByID(gkeGraph, proxyID)
	require.True(t, ok, "GKE Node must materialize a VM proxy")
	assert.Equal(t, string(kgtypes.NodeProxy), proxy.Type)
	assert.Equal(t, "cloud", kgtypes.Value(proxy, "foreign_graph"))
	assert.Equal(t, "my-project", kgtypes.Value(proxy, "account"))
	assert.Equal(t, healthyID, kgtypes.Value(proxy, "foreign_id"))
	assert.Equal(t, "gcp:compute:instance", kgtypes.Value(proxy, "resource_type"))
	assert.Equal(t, "gcp", kgtypes.Value(proxy, "provider"))
	assert.Equal(t, "us-central1-a", kgtypes.Value(proxy, "region"))
	assert.Equal(t, healthyName, proxy.SymbolName)

	// BACKED_BY_VM edge from Node to proxy.
	healthyNodeID := resourceID("", "Node", healthyName)
	assert.Equal(t, []string{proxyID},
		collectBackedByVMTargets(fake, gkeGraph, healthyNodeID),
		"healthy Node must gain exactly one BACKED_BY_VM edge")

	// Skipped nodes produce no edges.
	bareID := resourceID("", "Node", "bare-minikube")
	malformedID := resourceID("", "Node", "malformed")
	assert.Empty(t, collectBackedByVMTargets(fake, gkeGraph, bareID))
	assert.Empty(t, collectBackedByVMTargets(fake, gkeGraph, malformedID))

	// Idempotent.
	require.NoError(t, resolveNodeVMLinkage(ctx, fake, gkeGraph))
	assert.Equal(t, []string{proxyID},
		collectBackedByVMTargets(fake, gkeGraph, healthyNodeID),
		"second run must not duplicate BACKED_BY_VM edges")
}

// TestResolveNodeVMLinkage_EKS exercises the AWS account recovery path:
// the enclosing graph name is the EKS cluster ARN and the resolver must
// parse (region, account) from it to build the EC2 instance ARN.
func TestResolveNodeVMLinkage_EKS(t *testing.T) {
	ctx := newCtx(t)

	const (
		eksGraph    = "arn:aws:eks:us-east-1:111122223333:cluster/prod"
		healthyName = "ip-10-0-1-15.ec2.internal"
		healthyPID  = "aws:///us-east-1a/i-0abc123def456"
		healthyID   = "arn:aws:ec2:us-east-1:111122223333:instance/i-0abc123def456"
		proxyID     = "proxy:cloud:111122223333:" + healthyID
	)

	fake := newK8sFake()
	fake.seed(eksGraph, nodeResNode(healthyName, healthyPID))

	require.NoError(t, resolveNodeVMLinkage(ctx, fake, eksGraph))

	proxy, ok := fake.nodeByID(eksGraph, proxyID)
	require.True(t, ok, "EKS Node must materialize an EC2 instance proxy")
	assert.Equal(t, "111122223333", kgtypes.Value(proxy, "account"))
	assert.Equal(t, healthyID, kgtypes.Value(proxy, "foreign_id"))
	assert.Equal(t, "ec2-instance", kgtypes.Value(proxy, "resource_type"))
	assert.Equal(t, "aws", kgtypes.Value(proxy, "provider"))
	assert.Equal(t, "us-east-1", kgtypes.Value(proxy, "region"))

	healthyNodeID := resourceID("", "Node", healthyName)
	assert.Equal(t, []string{proxyID}, collectBackedByVMTargets(fake, eksGraph, healthyNodeID))
}

// TestResolveNodeVMLinkage_EKS_GraphNameNotARN covers the defensive
// path: EKS providerID encountered in a graph whose name is NOT an EKS
// cluster ARN (e.g. a plain kubeconfig context). The resolver cannot
// recover the account and silently skips — no proxy, no edge, no error.
func TestResolveNodeVMLinkage_EKS_GraphNameNotARN(t *testing.T) {
	ctx := newCtx(t)

	const plainGraph = "eks-test-context"

	fake := newK8sFake()
	fake.seed(plainGraph, nodeResNode("ip-10-0-0-1", "aws:///us-east-1a/i-abc"))

	require.NoError(t, resolveNodeVMLinkage(ctx, fake, plainGraph))

	// No proxy was created (no NodeProxy materialized into the graph).
	assert.Empty(t, fake.allEdges(plainGraph),
		"EKS Node in non-ARN graph must emit no BACKED_BY_VM edges")
	nodeID := resourceID("", "Node", "ip-10-0-0-1")
	assert.Empty(t, collectBackedByVMTargets(fake, plainGraph, nodeID),
		"EKS Node in non-ARN graph must emit no BACKED_BY_VM edges")
}

// TestResolveNodeVMLinkage_Azure verifies the Azure path: providerID
// carries the subscription and full resource ID, so no graph-name
// recovery is needed. The VMSS-instance form produces a dangling proxy
// on purpose (OQ6 decision), covered by the second seed.
func TestResolveNodeVMLinkage_Azure(t *testing.T) {
	ctx := newCtx(t)

	const (
		aksGraph    = "my-aks-cluster"
		vmName      = "aks-nodepool1-vm"
		vmPID       = "azure:///subscriptions/sub-xyz/resourceGroups/MC_foo/providers/Microsoft.Compute/virtualMachines/aks-nodepool1-vm"
		vmID        = "/subscriptions/sub-xyz/resourceGroups/MC_foo/providers/Microsoft.Compute/virtualMachines/aks-nodepool1-vm"
		vmProxyID   = "proxy:cloud:sub-xyz:" + vmID
		vmssName    = "aks-vmss-node"
		vmssPID     = "azure:///subscriptions/sub-xyz/resourceGroups/MC_foo/providers/Microsoft.Compute/virtualMachineScaleSets/ss1/virtualMachines/0"
		vmssID      = "/subscriptions/sub-xyz/resourceGroups/MC_foo/providers/Microsoft.Compute/virtualMachineScaleSets/ss1/virtualMachines/0"
		vmssProxyID = "proxy:cloud:sub-xyz:" + vmssID
	)

	fake := newK8sFake()
	fake.seed(aksGraph,
		nodeResNode(vmName, vmPID),
		nodeResNode(vmssName, vmssPID),
	)

	require.NoError(t, resolveNodeVMLinkage(ctx, fake, aksGraph))

	// VM proxy — healthy.
	vmProxy, ok := fake.nodeByID(aksGraph, vmProxyID)
	require.True(t, ok, "Azure VM Node must materialize a proxy")
	assert.Equal(t, "sub-xyz", kgtypes.Value(vmProxy, "account"))
	assert.Equal(t, "Microsoft.Compute/virtualMachines", kgtypes.Value(vmProxy, "resource_type"))

	assert.Equal(t, []string{vmProxyID},
		collectBackedByVMTargets(fake, aksGraph, resourceID("", "Node", vmName)))

	// VMSS proxy — created with the deterministic ID (dangling is fine;
	// OQ6 defers VMSS handling to a follow-up ticket).
	_, ok = fake.nodeByID(aksGraph, vmssProxyID)
	require.True(t, ok, "VMSS-instance providerID still materializes a proxy — dangling is acceptable per OQ6")
	assert.Equal(t, []string{vmssProxyID},
		collectBackedByVMTargets(fake, aksGraph, resourceID("", "Node", vmssName)))
}

// TestResolveNodeVMLinkage_NoNodes is a smoke test for the zero-Node
// case — no Node resources in the graph means no queries past the
// initial Match(resource_type="Node") and no edges emitted.
func TestResolveNodeVMLinkage_NoNodes(t *testing.T) {
	ctx := newCtx(t)

	// Seed a non-Node resource so the graph isn't completely empty.
	other := &knowledgev1.Node{
		Id:         resourceID("default", "Pod", "solo"),
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "solo",
		Source:     "cloud",
	}
	kgtypes.SetValue(other, "resource_type", "Pod")
	kgtypes.SetValue(other, "namespace", "default")

	fake := newK8sFake()
	fake.seed("empty-graph", other)

	require.NoError(t, resolveNodeVMLinkage(ctx, fake, "empty-graph"))

	assert.Empty(t, fake.allEdges("empty-graph"),
		"graph with no Node resources must not create VM proxies/edges")
}
