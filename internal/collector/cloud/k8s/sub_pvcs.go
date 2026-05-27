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

// pvcsSubCollector lists all PersistentVolumeClaims across all namespaces.
type pvcsSubCollector struct {
	clientset kubernetes.Interface
}

func (s *pvcsSubCollector) Name() string { return "pvcs" }

func (s *pvcsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list pvcs: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, pvc := range list.Items {
		id := resourceID(pvc.Namespace, "PersistentVolumeClaim", pvc.Name)

		meta := labelsToMeta(pvc.Labels)
		meta["namespace"] = pvc.Namespace
		meta["phase"] = string(pvc.Status.Phase)
		if capacity, ok := pvc.Status.Capacity["storage"]; ok {
			meta["capacity"] = capacity.String()
		}
		if len(pvc.Spec.AccessModes) > 0 {
			var modes []string
			for _, m := range pvc.Spec.AccessModes {
				modes = append(modes, string(m))
			}
			meta["access_modes"] = strings.Join(modes, ",")
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         pvc.Name,
			ResourceType: "PersistentVolumeClaim",
			Region:       pvc.Namespace,
			Content:      marshalJSON(pvc),
			Metadata:     meta,
		})

		// BOUND_TO edge: PVC → PV when bound.
		if pvc.Spec.VolumeName != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     resourceID("", "PersistentVolume", pvc.Spec.VolumeName),
				Relationship: kgtypes.EdgeBoundTo,
			})
		}

		// USES_STORAGE_CLASS edge.
		if pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     resourceID("", "StorageClass", *pvc.Spec.StorageClassName),
				Relationship: kgtypes.EdgeUsesStorageClass,
			})
		}
	}

	return result, nil
}
