// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// namespacesSubCollector lists all Namespaces. Cluster-scoped, no outbound edges.
type namespacesSubCollector struct {
	clientset kubernetes.Interface
}

func (s *namespacesSubCollector) Name() string { return "namespaces" }

func (s *namespacesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list namespaces: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, ns := range list.Items {
		id := resourceID("", "Namespace", ns.Name)

		meta := labelsToMeta(ns.Labels)
		meta["phase"] = string(ns.Status.Phase)
		meta["annotation_count"] = formatInt(len(ns.Annotations))

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         ns.Name,
			ResourceType: "Namespace",
			Content:      marshalJSON(ns),
			Metadata:     meta,
		})
	}

	return result, nil
}
