// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// orphan_rules_k8s.go registers the v1 Kubernetes orphan rules:
//
//   - Deployment / StatefulSet / DaemonSet  → no inbound OWNED_BY pods
//                                              (conf 0.8)
//   - Pod              → no outbound OWNED_BY AND not a static pod
//                        (conf 1.0 — static pods filtered via the
//                        annotation/kubernetes.io/config.source meta key
//                        preserved by cloud/k8s/sub_pods.go)
//   - Service          → no outbound SELECTS edges                (conf 1.0)
//   - PersistentVolume → no inbound BOUND_TO from any PVC; phase-aware
//                        confidence: 1.0 when phase != Bound, 0.9 otherwise
//                        (conf 0.9-1.0)
//   - ConfigMap        → no inbound MOUNTS_CONFIGMAP from any workload
//                        (conf 0.8)
//   - Secret           → no inbound MOUNTS_SECRET from any workload
//                        (conf 0.8)
//
// All rules read edge presence via the orphanGraph edge index — a future
// change to the underlying read API touches one place.
//
// CONTROLLER OWNERSHIP NOTE — Deployment pods are owned by an intermediate
// ReplicaSet, not directly by the Deployment. The v1 workloadControllerRule
// uses a direct EdgeOwnedBy check because the cloud collector emits
// pod → controller edges via OwnerReferences which include the
// ReplicaSet, not the Deployment. This means a healthy Deployment with
// pods owned by its ReplicaSet will appear orphaned. Until the collector
// or the orphan rule learns to walk transitive ownership, the 0.8
// confidence reflects this known false-positive source. StatefulSet and
// DaemonSet do not have this issue (their pods are directly owned), so the
// false-positive risk is Deployment-specific.

// Confidence constants for the K8s rules. Same hardcoded-by-design rule
// as the AWS file.
const (
	confidenceWorkloadController = 0.8
	confidencePod                = 1.0
	confidenceService            = 1.0
	confidencePVUnbound          = 1.0
	confidencePVBoundUnknown     = 0.9
	confidenceConfigMap          = 0.8
	confidenceSecret             = 0.8
)

// staticPodAnnotationKey is the key kubelet writes when a pod was created
// from a static manifest on the node's filesystem (i.e. a static pod). The
// value is "file" for filesystem-sourced static pods. Static pods have no
// owner reference because they are not managed by the API server, so the
// orphan rule must filter them out before flagging "no owner" as a finding.
const staticPodAnnotationKey = "annotation/kubernetes.io/config.source"
const staticPodAnnotationValue = "file"

// workloadControllerRule returns an OrphanRule that flags a controller
// (Deployment, StatefulSet, DaemonSet) as orphaned when no pod has an
// inbound OWNED_BY edge pointing to it. cloud/k8s/sub_pods.go emits
// pod → controller OWNED_BY edges from each pod's OwnerReferences, so a
// controller with zero owned pods has no inbound OWNED_BY edges in the
// scoped graph.
//
// The kind parameter is used in the orphan finding's summary text and
// makes the function reusable across all three controller types without
// duplicating the closure body.
func workloadControllerRule(kind string) OrphanRule {
	return func(
		_ context.Context,
		_ foundation.GraphCaller,
		_ string,
		graph *orphanGraph,
		node *knowledgev1.Node,
	) (bool, float64, string, error) {
		if graph.edges.hasIncoming(node.Id, kgtypes.EdgeOwnedBy) {
			return false, confidenceWorkloadController, "", nil
		}
		return true, confidenceWorkloadController,
			fmt.Sprintf("%s %s owns no pods.", kind, displayName(node)),
			nil
	}
}

// podRule reports a Pod as orphaned when it has no outbound OWNED_BY edge
// AND it is not a static pod. The static-pod filter reads the
// annotation/kubernetes.io/config.source metadata key, which is preserved
// from pod.Annotations by cloud/k8s/sub_pods.go.
//
// Static pods are bare on the API server side — they have no owner
// reference because kubelet manages them directly from a manifest file.
// Without the annotation check we would flag every system-critical static
// pod (kube-apiserver, etcd, kube-scheduler) as orphaned on every node.
// With the check the rule's confidence is 1.0: a non-static pod with no
// owner is unambiguously bare and should be investigated.
func podRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if metaValue(node, staticPodAnnotationKey) == staticPodAnnotationValue {
		return false, confidencePod, "", nil
	}
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeOwnedBy) {
		return false, confidencePod, "", nil
	}
	return true, confidencePod,
		fmt.Sprintf("Pod %s has no owner reference and is not a static pod.", displayName(node)),
		nil
}

// serviceRule reports a Service as orphaned when it selects no workloads
// — i.e. the postpopulate pass found no pods matching its selector and
// emitted zero outbound EdgeSelects edges. ExternalName Services and
// headless Services with manual Endpoints will appear orphaned in v1
// (no SELECTS edges); future scope expansion may add an exemption.
func serviceRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeSelects) {
		return false, confidenceService, "", nil
	}
	return true, confidenceService,
		fmt.Sprintf("Service %s selects no workloads.", displayName(node)),
		nil
}

// persistentVolumeRule reports a PV as orphaned when no PVC binds to it.
// cloud/k8s/sub_pvcs.go emits PVC → PV BOUND_TO edges, so a PV with zero
// inbound BOUND_TO edges has no claim. The confidence is phase-aware: a
// PV whose phase is not "Bound" (Available, Released, Failed) is
// authoritatively unbound (1.0); a PV whose phase is "Bound" but has no
// inbound edge in our snapshot is more likely a collection race and earns
// 0.9 confidence.
func persistentVolumeRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeBoundTo) {
		return false, confidencePVUnbound, "", nil
	}
	phase := metaValue(node, "phase")
	confidence := confidencePVUnbound
	if phase == "Bound" {
		confidence = confidencePVBoundUnknown
	}
	summary := fmt.Sprintf("PersistentVolume %s is not bound to any PVC", displayName(node))
	if phase != "" {
		summary += fmt.Sprintf(" (phase=%s).", phase)
	} else {
		summary += "."
	}
	return true, confidence, summary, nil
}

// configMapRule reports a ConfigMap as orphaned when no workload mounts it
// via volume or env reference. cloud/k8s/helpers.go emits workload →
// ConfigMap MOUNTS_CONFIGMAP edges from PodSpec analysis, so a ConfigMap
// with zero inbound MOUNTS_CONFIGMAP edges has no static reference. The
// 0.8 confidence reflects the known limitation that applications can also
// fetch ConfigMaps directly via the API at runtime — a pattern the static
// graph cannot see.
func configMapRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeMountsConfigMap) {
		return false, confidenceConfigMap, "", nil
	}
	return true, confidenceConfigMap,
		fmt.Sprintf("ConfigMap %s is not mounted by any workload (may still be consumed via API).", displayName(node)),
		nil
}

// secretRule reports a Secret as orphaned when no workload mounts it.
// Same caveat as configMapRule: applications may fetch Secrets at runtime,
// hence the 0.8 confidence. ServiceAccount-token secrets and TLS secrets
// referenced by Ingress resources will appear orphaned in v1 because the
// collectors do not yet emit dedicated edges for those reference patterns.
func secretRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeMountsSecret) {
		return false, confidenceSecret, "", nil
	}
	return true, confidenceSecret,
		fmt.Sprintf("Secret %s is not mounted by any workload (may still be consumed via API).", displayName(node)),
		nil
}

// init self-registers all eight K8s orphan rules with the dispatch table.
// Resource type strings match the values emitted by cloud/k8s/* collectors.
func init() {
	registerOrphanRule("Deployment", workloadControllerRule("Deployment"))
	registerOrphanRule("StatefulSet", workloadControllerRule("StatefulSet"))
	registerOrphanRule("DaemonSet", workloadControllerRule("DaemonSet"))
	registerOrphanRule("Pod", podRule)
	registerOrphanRule("Service", serviceRule)
	registerOrphanRule("PersistentVolume", persistentVolumeRule)
	registerOrphanRule("ConfigMap", configMapRule)
	registerOrphanRule("Secret", secretRule)
}
