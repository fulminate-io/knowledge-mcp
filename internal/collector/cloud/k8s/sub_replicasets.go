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

// replicaSetsSubCollector lists all ReplicaSets across all namespaces.
type replicaSetsSubCollector struct {
	clientset kubernetes.Interface
}

func (s *replicaSetsSubCollector) Name() string { return "replicasets" }

func (s *replicaSetsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.AppsV1().ReplicaSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list replicasets: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, rs := range list.Items {
		id := resourceID(rs.Namespace, "ReplicaSet", rs.Name)

		meta := labelsToMeta(rs.Labels)
		meta["namespace"] = rs.Namespace
		meta["replicas"] = formatInt32Ptr(rs.Spec.Replicas)

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         rs.Name,
			ResourceType: "ReplicaSet",
			Region:       rs.Namespace,
			Content:      marshalJSON(rs),
			Metadata:     meta,
		})

		// OwnerReference edges (typically to a Deployment).
		for _, ref := range rs.OwnerReferences {
			ownerID := resourceID(rs.Namespace, ref.Kind, ref.Name)
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     ownerID,
				Relationship: kgtypes.EdgeOwnedBy,
			})
		}

		// Edges from pod template.
		result.Edges = append(result.Edges, extractPodTemplateEdges(id, rs.Namespace, rs.Spec.Template.Spec)...)
	}

	return result, nil
}
