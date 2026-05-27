// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// pdbSubCollector lists all PodDisruptionBudgets across all namespaces.
// Label-selector-to-workload resolution is deferred to PostPopulate,
// consistent with the NetworkPolicy/Service pattern. The selector is
// stored in metadata as JSON for later resolution.
type pdbSubCollector struct {
	clientset kubernetes.Interface
}

func (s *pdbSubCollector) Name() string { return "pdb" }

func (s *pdbSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list pdb: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, pdb := range list.Items {
		id := resourceID(pdb.Namespace, "PodDisruptionBudget", pdb.Name)

		meta := labelsToMeta(pdb.Labels)
		meta["namespace"] = pdb.Namespace

		if pdb.Spec.MinAvailable != nil {
			meta["min_available"] = pdb.Spec.MinAvailable.String()
		}
		if pdb.Spec.MaxUnavailable != nil {
			meta["max_unavailable"] = pdb.Spec.MaxUnavailable.String()
		}
		meta["current_healthy"] = formatInt32(pdb.Status.CurrentHealthy)
		meta["desired_healthy"] = formatInt32(pdb.Status.DesiredHealthy)
		meta["disruptions_allowed"] = formatInt32(pdb.Status.DisruptionsAllowed)

		// Store label selector for PostPopulate resolution (NetworkPolicy pattern).
		if pdb.Spec.Selector != nil && len(pdb.Spec.Selector.MatchLabels) > 0 {
			selectorJSON, jsonErr := json.Marshal(pdb.Spec.Selector.MatchLabels)
			if jsonErr == nil {
				meta["pod_selector"] = string(selectorJSON)
			}
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         pdb.Name,
			ResourceType: "PodDisruptionBudget",
			Region:       pdb.Namespace,
			Content:      marshalJSON(pdb),
			Metadata:     meta,
		})
	}

	return result, nil
}
