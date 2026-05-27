// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// rolesSubCollector lists all Roles and ClusterRoles.
type rolesSubCollector struct {
	clientset kubernetes.Interface
}

func (s *rolesSubCollector) Name() string { return "roles" }

func (s *rolesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	// Namespaced Roles.
	roles, err := s.clientset.RbacV1().Roles("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list roles: %w", err)
	}

	for _, r := range roles.Items {
		id := resourceID(r.Namespace, "Role", r.Name)

		meta := labelsToMeta(r.Labels)
		meta["namespace"] = r.Namespace
		meta["rule_count"] = formatInt(len(r.Rules))

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         r.Name,
			ResourceType: "Role",
			Region:       r.Namespace,
			Content:      marshalJSON(r),
			Metadata:     meta,
		})
	}

	// Cluster-scoped ClusterRoles.
	clusterRoles, err := s.clientset.RbacV1().ClusterRoles().List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list clusterroles: %w", err)
	}

	for _, cr := range clusterRoles.Items {
		// Cluster-scoped: Kind/name (no namespace prefix).
		id := resourceID("", "ClusterRole", cr.Name)

		meta := labelsToMeta(cr.Labels)
		meta["rule_count"] = formatInt(len(cr.Rules))

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         cr.Name,
			ResourceType: "ClusterRole",
			Content:      marshalJSON(cr),
			Metadata:     meta,
		})
	}

	return result, nil
}
