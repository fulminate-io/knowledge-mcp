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

// hpaSubCollector lists all HorizontalPodAutoscalers across all namespaces.
type hpaSubCollector struct {
	clientset kubernetes.Interface
}

func (s *hpaSubCollector) Name() string { return "hpa" }

func (s *hpaSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.AutoscalingV2().HorizontalPodAutoscalers("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list hpa: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, hpa := range list.Items {
		id := resourceID(hpa.Namespace, "HorizontalPodAutoscaler", hpa.Name)

		meta := labelsToMeta(hpa.Labels)
		meta["namespace"] = hpa.Namespace
		meta["min_replicas"] = formatInt32Ptr(hpa.Spec.MinReplicas)
		meta["max_replicas"] = formatInt32(hpa.Spec.MaxReplicas)
		meta["current_replicas"] = formatInt32(hpa.Status.CurrentReplicas)

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         hpa.Name,
			ResourceType: "HorizontalPodAutoscaler",
			Region:       hpa.Namespace,
			Content:      marshalJSON(hpa),
			Metadata:     meta,
		})

		// Edge to the scale target (Deployment, StatefulSet, etc.).
		ref := hpa.Spec.ScaleTargetRef
		targetID := resourceID(hpa.Namespace, ref.Kind, ref.Name)
		result.Edges = append(result.Edges, cloud.EdgeSpec{
			SourceID:     id,
			TargetID:     targetID,
			Relationship: kgtypes.EdgeScales,
		})
	}

	return result, nil
}
