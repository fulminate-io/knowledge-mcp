// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// storageClassesSubCollector lists all StorageClasses (cluster-scoped).
type storageClassesSubCollector struct {
	clientset kubernetes.Interface
}

func (s *storageClassesSubCollector) Name() string { return "storageclasses" }

func (s *storageClassesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list storageclasses: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, sc := range list.Items {
		// Cluster-scoped: StorageClass/name.
		id := resourceID("", "StorageClass", sc.Name)

		meta := labelsToMeta(sc.Labels)
		meta["provisioner"] = sc.Provisioner
		if sc.ReclaimPolicy != nil {
			meta["reclaim_policy"] = string(*sc.ReclaimPolicy)
		}
		if sc.VolumeBindingMode != nil {
			meta["volume_binding_mode"] = string(*sc.VolumeBindingMode)
		}
		if sc.AllowVolumeExpansion != nil && *sc.AllowVolumeExpansion {
			meta["allow_expansion"] = "true"
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         sc.Name,
			ResourceType: "StorageClass",
			Content:      marshalJSON(sc),
			Metadata:     meta,
		})
	}

	return result, nil
}
