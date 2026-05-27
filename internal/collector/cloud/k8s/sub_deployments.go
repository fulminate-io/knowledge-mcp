// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// deploymentsSubCollector lists all Deployments across all namespaces.
type deploymentsSubCollector struct {
	clientset kubernetes.Interface
}

func (s *deploymentsSubCollector) Name() string { return "deployments" }

func (s *deploymentsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list deployments: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, d := range list.Items {
		id := resourceID(d.Namespace, "Deployment", d.Name)

		meta := labelsToMeta(d.Labels)
		meta["namespace"] = d.Namespace
		meta["replicas"] = formatInt32Ptr(d.Spec.Replicas)
		meta["strategy"] = string(d.Spec.Strategy.Type)

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         d.Name,
			ResourceType: "Deployment",
			Region:       d.Namespace,
			Content:      marshalJSON(d),
			Metadata:     meta,
		})

		// Edges from pod template (SA, ConfigMap, Secret, PVC).
		result.Edges = append(result.Edges, extractPodTemplateEdges(id, d.Namespace, d.Spec.Template.Spec)...)

		// Cascade targets from container images.
		result.Targets = append(result.Targets, extractImageTargets(d.Spec.Template.Spec.Containers)...)
	}

	return result, nil
}
