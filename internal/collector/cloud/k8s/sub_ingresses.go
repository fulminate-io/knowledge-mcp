// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// ingressesSubCollector lists all Ingresses across all namespaces.
type ingressesSubCollector struct {
	clientset kubernetes.Interface
}

func (s *ingressesSubCollector) Name() string { return "ingresses" }

func (s *ingressesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list ingresses: %w", err)
	}

	var result cloud.SubCollectorResult

	for _, ing := range list.Items {
		id := resourceID(ing.Namespace, "Ingress", ing.Name)

		meta := labelsToMeta(ing.Labels)
		meta["namespace"] = ing.Namespace

		// Capture hosts.
		var hosts []string
		for _, rule := range ing.Spec.Rules {
			if rule.Host != "" {
				hosts = append(hosts, rule.Host)
			}
		}
		if len(hosts) > 0 {
			meta["hosts"] = strings.Join(hosts, ",")
		}

		// Capture ingress class.
		if ing.Spec.IngressClassName != nil {
			meta["ingress_class"] = *ing.Spec.IngressClassName
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         ing.Name,
			ResourceType: "Ingress",
			Region:       ing.Namespace,
			Content:      marshalJSON(ing),
			Metadata:     meta,
		})

		// ROUTES_TO edges to backend services.
		result.Edges = append(result.Edges, extractIngressBackendEdges(id, ing.Namespace, ing.Spec)...)

		// ALB Ingress Controller annotations → AWS cascade.
		if hasALBAnnotation(ing.Annotations) {
			result.Targets = append(result.Targets, cloud.CollectTarget{
				Collector: "aws",
				ID:        "",
			})
		}
	}

	return result, nil
}

// extractIngressBackendEdges extracts ROUTES_TO edges from ingress backends.
func extractIngressBackendEdges(ingressID, namespace string, spec networkingv1.IngressSpec) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	// Default backend.
	if spec.DefaultBackend != nil && spec.DefaultBackend.Service != nil {
		svcID := resourceID(namespace, "Service", spec.DefaultBackend.Service.Name)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     ingressID,
			TargetID:     svcID,
			Relationship: kgtypes.EdgeRoutesTo,
		})
	}

	// Rule-based backends.
	for _, rule := range spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, path := range rule.HTTP.Paths {
			if path.Backend.Service != nil {
				svcID := resourceID(namespace, "Service", path.Backend.Service.Name)
				edges = append(edges, cloud.EdgeSpec{
					SourceID:     ingressID,
					TargetID:     svcID,
					Relationship: kgtypes.EdgeRoutesTo,
				})
			}
		}
	}

	return edges
}

// hasALBAnnotation returns true if any annotation has the ALB ingress controller prefix.
func hasALBAnnotation(annotations map[string]string) bool {
	for k := range annotations {
		if strings.HasPrefix(k, "alb.ingress.kubernetes.io/") {
			return true
		}
	}
	return false
}
