// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// statefulSetsSubCollector lists all StatefulSets across all namespaces.
type statefulSetsSubCollector struct {
	clientset kubernetes.Interface
}

func (s *statefulSetsSubCollector) Name() string { return "statefulsets" }

func (s *statefulSetsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list statefulsets: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, ss := range list.Items {
		id := resourceID(ss.Namespace, "StatefulSet", ss.Name)

		meta := labelsToMeta(ss.Labels)
		meta["namespace"] = ss.Namespace
		meta["replicas"] = formatInt32Ptr(ss.Spec.Replicas)
		if ss.Spec.ServiceName != "" {
			meta["service_name"] = ss.Spec.ServiceName
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         ss.Name,
			ResourceType: "StatefulSet",
			Region:       ss.Namespace,
			Content:      marshalJSON(ss),
			Metadata:     meta,
		})

		// Edges from pod template (SA, ConfigMap, Secret, PVC).
		result.Edges = append(result.Edges, extractPodTemplateEdges(id, ss.Namespace, ss.Spec.Template.Spec)...)

		// VolumeClaimTemplates produce USES_PVC edges.
		for _, vct := range ss.Spec.VolumeClaimTemplates {
			pvcID := resourceID(ss.Namespace, "PersistentVolumeClaim", vct.Name)
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     pvcID,
				Relationship: kgtypes.EdgeUsesPVC,
			})
		}

		// Cascade targets from container images.
		result.Targets = append(result.Targets, extractImageTargets(ss.Spec.Template.Spec.Containers)...)
	}

	return result, nil
}
