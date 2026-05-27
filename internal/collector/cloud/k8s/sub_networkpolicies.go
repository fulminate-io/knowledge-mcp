// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// networkPoliciesSubCollector lists all NetworkPolicies across all namespaces.
type networkPoliciesSubCollector struct {
	clientset kubernetes.Interface
}

func (s *networkPoliciesSubCollector) Name() string { return "networkpolicies" }

func (s *networkPoliciesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.NetworkingV1().NetworkPolicies("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list networkpolicies: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, np := range list.Items {
		id := resourceID(np.Namespace, "NetworkPolicy", np.Name)

		meta := labelsToMeta(np.Labels)
		meta["namespace"] = np.Namespace

		// Capture policy types.
		var policyTypes []string
		for _, pt := range np.Spec.PolicyTypes {
			policyTypes = append(policyTypes, string(pt))
		}
		if len(policyTypes) > 0 {
			meta["policy_types"] = strings.Join(policyTypes, ",")
		}

		// Store podSelector in metadata for PostPopulate RESTRICTS edge resolution.
		if len(np.Spec.PodSelector.MatchLabels) > 0 {
			selectorJSON, err := json.Marshal(np.Spec.PodSelector.MatchLabels)
			if err == nil {
				meta["pod_selector"] = string(selectorJSON)
			}
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         np.Name,
			ResourceType: "NetworkPolicy",
			Region:       np.Namespace,
			Content:      marshalJSON(np),
			Metadata:     meta,
		})
	}

	return result, nil
}
