// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// podsSubCollector lists all Pods across all namespaces.
type podsSubCollector struct {
	clientset kubernetes.Interface
}

func (s *podsSubCollector) Name() string { return "pods" }

func (s *podsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list pods: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, p := range list.Items {
		id := resourceID(p.Namespace, "Pod", p.Name)

		meta := labelsToMeta(p.Labels)
		// Preserve annotations under "annotation/<key>" so downstream
		// analyzers (e.g. topology orphan detection) can read static-pod
		// markers like "kubernetes.io/config.source=file" without
		// re-parsing the raw resource JSON. Mirrors the "label/<key>"
		// scheme used by labelsToMeta.
		for k, v := range p.Annotations {
			meta["annotation/"+k] = v
		}
		meta["namespace"] = p.Namespace
		meta["phase"] = string(p.Status.Phase)
		meta["node_name"] = p.Spec.NodeName
		if p.Status.PodIP != "" {
			meta["pod_ip"] = p.Status.PodIP
		}

		// Capture container status summary.
		var readyCount, totalCount int
		for _, cs := range p.Status.ContainerStatuses {
			totalCount++
			if cs.Ready {
				readyCount++
			}
		}
		meta["ready"] = fmt.Sprintf("%d/%d", readyCount, totalCount)

		// Capture restart count.
		var restarts int32
		for _, cs := range p.Status.ContainerStatuses {
			restarts += cs.RestartCount
		}
		meta["restarts"] = formatInt32(restarts)

		// Build container images list for metadata.
		var images []string
		for _, c := range p.Spec.Containers {
			if c.Image != "" {
				images = append(images, c.Image)
			}
		}
		if len(images) > 0 {
			meta["images"] = strings.Join(images, ",")
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         p.Name,
			ResourceType: "Pod",
			Region:       p.Namespace,
			Content:      marshalJSON(p),
			Metadata:     meta,
		})

		// OwnerReference edges (to ReplicaSet, DaemonSet, StatefulSet, Job, etc.).
		for _, ref := range p.OwnerReferences {
			ownerID := resourceID(p.Namespace, ref.Kind, ref.Name)
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     ownerID,
				Relationship: kgtypes.EdgeOwnedBy,
			})
		}

		// RUNS_ON edge: scheduled pod → Node/<nodeName>. Emitted uniformly
		// for every scheduled pod including static control-plane pods
		// (kubernetes.io/config.source=file) — the Node resource exists
		// regardless of how the pod was created, and topology analyzers
		// that care about the static-pod distinction can key off the
		// "annotation/kubernetes.io/config.source" metadata preserved
		// above. Unscheduled pods (Spec.NodeName == "") get no edge.
		if p.Spec.NodeName != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     resourceID("", "Node", p.Spec.NodeName),
				Relationship: kgtypes.EdgeRunsOn,
			})
		}

		// Pod template edges (SA, ConfigMap, Secret, PVC).
		result.Edges = append(result.Edges, extractPodTemplateEdges(id, p.Namespace, p.Spec)...)
	}

	return result, nil
}
