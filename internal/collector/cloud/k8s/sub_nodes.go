// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// nodesSubCollector lists every Node in the cluster. Nodes are
// cluster-scoped (no namespace) and the subcollector emits no edges —
// Pod → Node edges live in sub_pods.go (Phase 3) and the Node → VM
// cross-graph proxy is wired in postpopulate_nodes.go (Phase 5).
type nodesSubCollector struct {
	clientset kubernetes.Interface
}

func (s *nodesSubCollector) Name() string { return "nodes" }

func (s *nodesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list nodes: %w", err)
	}

	var result cloud.SubCollectorResult

	for i := range list.Items {
		n := &list.Items[i]
		id := resourceID("", "Node", n.Name)

		meta := labelsToMeta(n.Labels)
		for k, v := range n.Annotations {
			meta["annotation/"+k] = v
		}
		populateNodeSpecMeta(n, meta)
		populateNodeInfoMeta(n, meta)
		populateNodeConditionMeta(n, meta)
		populateNodeTaintMeta(n, meta)
		populateNodeResourceMeta(n, meta)
		populateNodeAddressMeta(n, meta)

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         n.Name,
			ResourceType: "Node",
			Content:      marshalJSON(n),
			Metadata:     meta,
		})
	}

	return result, nil
}

// populateNodeSpecMeta copies Node.Spec fields we care about into meta.
// Spec.ProviderID is the key field Phase 5 parses to create the cloud
// VM cross-graph proxy; the Unschedulable flag is surfaced for topology
// analyzers that want to distinguish cordoned nodes.
func populateNodeSpecMeta(n *corev1.Node, meta map[string]string) {
	if n.Spec.ProviderID != "" {
		meta["provider_id"] = n.Spec.ProviderID
	}
	meta["unschedulable"] = strconv.FormatBool(n.Spec.Unschedulable)
}

// populateNodeInfoMeta flattens Status.NodeInfo into descriptive meta
// keys (os_image, kubelet_version, etc.) so downstream queries can
// filter on version or image without re-parsing the raw JSON content.
func populateNodeInfoMeta(n *corev1.Node, meta map[string]string) {
	info := n.Status.NodeInfo
	if info.OSImage != "" {
		meta["os_image"] = info.OSImage
	}
	if info.KernelVersion != "" {
		meta["kernel_version"] = info.KernelVersion
	}
	if info.KubeletVersion != "" {
		meta["kubelet_version"] = info.KubeletVersion
	}
	if info.KubeProxyVersion != "" {
		meta["kube_proxy_version"] = info.KubeProxyVersion
	}
	if info.ContainerRuntimeVersion != "" {
		meta["container_runtime_version"] = info.ContainerRuntimeVersion
	}
	if info.Architecture != "" {
		meta["architecture"] = info.Architecture
	}
	if info.OperatingSystem != "" {
		meta["operating_system"] = info.OperatingSystem
	}
}

// populateNodeConditionMeta flattens Status.Conditions into per-type
// entries: "condition/Ready=True", "condition/MemoryPressure=False",
// etc. Matches the same flattening scheme used for labels and taints.
func populateNodeConditionMeta(n *corev1.Node, meta map[string]string) {
	for _, cond := range n.Status.Conditions {
		meta["condition/"+string(cond.Type)] = string(cond.Status)
	}
}

// populateNodeTaintMeta flattens Spec.Taints into positional entries so
// every taint is preserved even when two taints share a key. Format:
// "taint/<index>=<key>=<value>:<effect>" — the string shape lets
// humans read the taint back without reconstructing from JSON.
func populateNodeTaintMeta(n *corev1.Node, meta map[string]string) {
	for i, taint := range n.Spec.Taints {
		meta[fmt.Sprintf("taint/%d", i)] = fmt.Sprintf("%s=%s:%s",
			taint.Key, taint.Value, taint.Effect)
	}
}

// populateNodeResourceMeta flattens Status.Allocatable and
// Status.Capacity into per-resource-name keys: "allocatable/cpu=3840m",
// "capacity/memory=16298076Ki". Uses Quantity.String() so the string
// round-trips through apimachinery/resource.Quantity.
func populateNodeResourceMeta(n *corev1.Node, meta map[string]string) {
	for res, q := range n.Status.Allocatable {
		meta["allocatable/"+string(res)] = q.String()
	}
	for res, q := range n.Status.Capacity {
		meta["capacity/"+string(res)] = q.String()
	}
}

// populateNodeAddressMeta flattens Status.Addresses by type so callers
// can look up "address/InternalIP", "address/ExternalIP", or
// "address/Hostname" directly. Duplicate types overwrite — in
// practice a Node has at most one address of each type.
func populateNodeAddressMeta(n *corev1.Node, meta map[string]string) {
	for _, addr := range n.Status.Addresses {
		meta["address/"+string(addr.Type)] = addr.Address
	}
}
