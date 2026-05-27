// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// TestNodesSubCollector_Basic covers the representative GKE / EKS / AKS
// flavors plus a minimal "bare" node with no providerID. Structured as a
// table-driven test so metadata-flattening assertions stay focused on the
// flavor-specific bits (providerID, cloud labels) and share the
// type/resource/region checks.
func TestNodesSubCollector_Basic(t *testing.T) {
	gkeNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gke-main-default-pool-abc",
			Labels: map[string]string{
				"topology.kubernetes.io/zone":      "us-central1-a",
				"node.kubernetes.io/instance-type": "e2-medium",
				"kubernetes.io/hostname":           "gke-main-default-pool-abc",
			},
			Annotations: map[string]string{
				"node.alpha.kubernetes.io/ttl": "0",
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID:    "gce://my-project/us-central1-a/gke-main-default-pool-abc",
			Unschedulable: false,
			Taints: []corev1.Taint{
				{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
			},
		},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				OSImage:          "Container-Optimized OS from Google",
				KernelVersion:    "5.15.0",
				KubeletVersion:   "v1.28.5-gke.1",
				KubeProxyVersion: "v1.28.5-gke.1",
				Architecture:     "amd64",
				OperatingSystem:  "linux",
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("3840m"),
				corev1.ResourceMemory: resource.MustParse("14Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4000m"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
			},
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.5"},
				{Type: corev1.NodeHostName, Address: "gke-main-default-pool-abc"},
			},
		},
	}

	eksNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "ip-10-0-1-15.ec2.internal",
			Labels: map[string]string{"topology.kubernetes.io/zone": "us-east-1a"},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "aws:///us-east-1a/i-0abc123def456",
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}

	aksNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "aks-nodepool1-12345678-vmss000000",
			Labels: map[string]string{"topology.kubernetes.io/zone": "eastus-1"},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "azure:///subscriptions/sub-123/resourceGroups/mc_rg/providers/Microsoft.Compute/virtualMachines/aks-nodepool1-vm",
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}

	bareNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "bare-minikube"},
		// No providerID, no conditions, no labels.
	}

	cs := fake.NewSimpleClientset(gkeNode, eksNode, aksNode, bareNode)
	sub := &nodesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 4)
	assert.Empty(t, result.Edges, "nodes subcollector emits no edges — cross-graph VM linkage lives in postPopulate")

	byID := make(map[string]cloud.ResourceSpec, 4)
	for _, r := range result.Resources {
		byID[r.ID] = r
	}

	assertGKENodeMetadata(t, byID)
	assertEKSNodeMetadata(t, byID)
	assertAKSNodeMetadata(t, byID)
	assertBareNodeMetadata(t, byID)
}

func assertGKENodeMetadata(t *testing.T, byID map[string]cloud.ResourceSpec) {
	t.Helper()
	const id = "Node/gke-main-default-pool-abc"
	require.Contains(t, byID, id)
	res := byID[id]

	assert.Equal(t, "Node", res.ResourceType)
	assert.Empty(t, res.Region, "Nodes are cluster-scoped; cloud region lives on the VM, not the Node")
	assert.Equal(t, "gke-main-default-pool-abc", res.Name)

	m := res.Metadata
	assert.Equal(t, "gce://my-project/us-central1-a/gke-main-default-pool-abc", m["provider_id"])
	assert.Equal(t, "false", m["unschedulable"])
	assert.Equal(t, "us-central1-a", m["label/topology.kubernetes.io/zone"])
	assert.Equal(t, "e2-medium", m["label/node.kubernetes.io/instance-type"])
	assert.Equal(t, "0", m["annotation/node.alpha.kubernetes.io/ttl"])
	assert.Equal(t, "Container-Optimized OS from Google", m["os_image"])
	assert.Equal(t, "v1.28.5-gke.1", m["kubelet_version"])
	assert.Equal(t, "v1.28.5-gke.1", m["kube_proxy_version"])
	assert.Equal(t, "amd64", m["architecture"])

	// Conditions flattened per type.
	assert.Equal(t, "True", m["condition/Ready"])
	assert.Equal(t, "False", m["condition/MemoryPressure"])
	assert.Equal(t, "False", m["condition/DiskPressure"])

	// Taints flattened per positional index.
	assert.Equal(t, "dedicated=gpu:NoSchedule", m["taint/0"])

	// Quantity values round-trip through Quantity.String().
	assert.Equal(t, "3840m", m["allocatable/cpu"])
	assert.Equal(t, "14Gi", m["allocatable/memory"])
	assert.Equal(t, "110", m["allocatable/pods"])
	assert.Equal(t, "16Gi", m["capacity/memory"])
	assert.Equal(t, "4", m["capacity/cpu"])
}

func assertEKSNodeMetadata(t *testing.T, byID map[string]cloud.ResourceSpec) {
	t.Helper()
	const id = "Node/ip-10-0-1-15.ec2.internal"
	require.Contains(t, byID, id)
	res := byID[id]
	m := res.Metadata

	assert.Equal(t, "Node", res.ResourceType)
	assert.Equal(t, "aws:///us-east-1a/i-0abc123def456", m["provider_id"])
	assert.Equal(t, "us-east-1a", m["label/topology.kubernetes.io/zone"])
	assert.Equal(t, "True", m["condition/Ready"])
}

func assertAKSNodeMetadata(t *testing.T, byID map[string]cloud.ResourceSpec) {
	t.Helper()
	const id = "Node/aks-nodepool1-12345678-vmss000000"
	require.Contains(t, byID, id)
	res := byID[id]
	m := res.Metadata

	assert.Equal(t,
		"azure:///subscriptions/sub-123/resourceGroups/mc_rg/providers/Microsoft.Compute/virtualMachines/aks-nodepool1-vm",
		m["provider_id"])
}

func assertBareNodeMetadata(t *testing.T, byID map[string]cloud.ResourceSpec) {
	t.Helper()
	const id = "Node/bare-minikube"
	require.Contains(t, byID, id)
	res := byID[id]
	m := res.Metadata

	_, hasProvider := m["provider_id"]
	assert.False(t, hasProvider, "bare node must omit provider_id when Spec.ProviderID is empty")
	assert.Equal(t, "false", m["unschedulable"])
	assert.Empty(t, res.Region)
	assert.Equal(t, "Node", res.ResourceType)
}
