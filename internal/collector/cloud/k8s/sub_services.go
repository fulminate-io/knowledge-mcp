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

// servicesSubCollector lists all Services across all namespaces.
type servicesSubCollector struct {
	clientset kubernetes.Interface
}

func (s *servicesSubCollector) Name() string { return "services" }

func (s *servicesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list services: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, svc := range list.Items {
		id := resourceID(svc.Namespace, "Service", svc.Name)

		meta := labelsToMeta(svc.Labels)
		meta["namespace"] = svc.Namespace
		meta["type"] = string(svc.Spec.Type)
		if svc.Spec.ClusterIP != "" {
			meta["cluster_ip"] = svc.Spec.ClusterIP
		}

		// Capture ports.
		var ports []string
		for _, p := range svc.Spec.Ports {
			ports = append(ports, fmt.Sprintf("%s/%d", p.Protocol, p.Port))
		}
		if len(ports) > 0 {
			meta["ports"] = strings.Join(ports, ",")
		}

		// Store selector in metadata for PostPopulate SELECTS edge resolution.
		if len(svc.Spec.Selector) > 0 {
			selectorJSON, err := json.Marshal(svc.Spec.Selector)
			if err == nil {
				meta["selector"] = string(selectorJSON)
			}
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         svc.Name,
			ResourceType: "Service",
			Region:       svc.Namespace,
			Content:      marshalJSON(svc),
			Metadata:     meta,
		})

		// LoadBalancer services with AWS annotations → cascade target.
		if svc.Spec.Type == "LoadBalancer" {
			for k := range svc.Annotations {
				if strings.HasPrefix(k, "service.beta.kubernetes.io/aws-load-balancer-") {
					// Try to extract AWS account from annotations, otherwise just signal AWS.
					result.Targets = append(result.Targets, cloud.CollectTarget{
						Collector: "aws",
						ID:        "", // account discovery happens via cluster-level SA
					})
					break
				}
			}
		}
	}

	return result, nil
}
