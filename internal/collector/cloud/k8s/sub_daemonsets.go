// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// daemonSetsSubCollector lists all DaemonSets across all namespaces.
type daemonSetsSubCollector struct {
	clientset kubernetes.Interface
}

func (s *daemonSetsSubCollector) Name() string { return "daemonsets" }

func (s *daemonSetsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list daemonsets: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, ds := range list.Items {
		id := resourceID(ds.Namespace, "DaemonSet", ds.Name)

		meta := labelsToMeta(ds.Labels)
		meta["namespace"] = ds.Namespace
		meta["update_strategy"] = string(ds.Spec.UpdateStrategy.Type)

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         ds.Name,
			ResourceType: "DaemonSet",
			Region:       ds.Namespace,
			Content:      marshalJSON(ds),
			Metadata:     meta,
		})

		// Edges from pod template.
		result.Edges = append(result.Edges, extractPodTemplateEdges(id, ds.Namespace, ds.Spec.Template.Spec)...)

		// Cascade targets from container images.
		result.Targets = append(result.Targets, extractImageTargets(ds.Spec.Template.Spec.Containers)...)
	}

	return result, nil
}
