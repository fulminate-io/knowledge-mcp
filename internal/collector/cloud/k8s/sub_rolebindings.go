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

// roleBindingsSubCollector lists all RoleBindings and ClusterRoleBindings.
type roleBindingsSubCollector struct {
	clientset kubernetes.Interface
}

func (s *roleBindingsSubCollector) Name() string { return "rolebindings" }

func (s *roleBindingsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	if err := s.collectRoleBindings(ctx, &result); err != nil {
		return cloud.SubCollectorResult{}, err
	}

	if err := s.collectClusterRoleBindings(ctx, &result); err != nil {
		return cloud.SubCollectorResult{}, err
	}

	return result, nil
}

// collectRoleBindings lists namespaced RoleBindings and appends them to result.
func (s *roleBindingsSubCollector) collectRoleBindings(ctx context.Context, result *cloud.SubCollectorResult) error {
	rbs, err := s.clientset.RbacV1().RoleBindings("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list rolebindings: %w", err)
	}

	for _, rb := range rbs.Items {
		id := resourceID(rb.Namespace, "RoleBinding", rb.Name)

		meta := labelsToMeta(rb.Labels)
		meta["namespace"] = rb.Namespace
		meta["role_kind"] = rb.RoleRef.Kind
		meta["role_name"] = rb.RoleRef.Name

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         rb.Name,
			ResourceType: "RoleBinding",
			Region:       rb.Namespace,
			Content:      marshalJSON(rb),
			Metadata:     meta,
		})

		// BINDS_ROLE edge to the referenced Role or ClusterRole.
		var roleID string
		if rb.RoleRef.Kind == "ClusterRole" {
			roleID = resourceID("", "ClusterRole", rb.RoleRef.Name)
		} else {
			roleID = resourceID(rb.Namespace, "Role", rb.RoleRef.Name)
		}
		result.Edges = append(result.Edges, cloud.EdgeSpec{
			SourceID:     id,
			TargetID:     roleID,
			Relationship: kgtypes.EdgeBindsRole,
			Metadata:     map[string]string{"role_type": rb.RoleRef.Kind, "namespace": rb.Namespace},
		})

		// BINDS_SUBJECT edge to each subject.
		for _, subj := range rb.Subjects {
			var subjID string
			switch subj.Kind {
			case "ServiceAccount":
				ns := subj.Namespace
				if ns == "" {
					ns = rb.Namespace
				}
				subjID = resourceID(ns, "ServiceAccount", subj.Name)
			default:
				continue
			}
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     subjID,
				Relationship: kgtypes.EdgeBindsSubject,
				Metadata:     map[string]string{"subject_kind": subj.Kind},
			})
		}
	}
	return nil
}

// collectClusterRoleBindings lists cluster-scoped ClusterRoleBindings and appends them to result.
func (s *roleBindingsSubCollector) collectClusterRoleBindings(ctx context.Context, result *cloud.SubCollectorResult) error {
	crbs, err := s.clientset.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list clusterrolebindings: %w", err)
	}

	for _, crb := range crbs.Items {
		id := resourceID("", "ClusterRoleBinding", crb.Name)

		meta := labelsToMeta(crb.Labels)
		meta["role_kind"] = crb.RoleRef.Kind
		meta["role_name"] = crb.RoleRef.Name

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         crb.Name,
			ResourceType: "ClusterRoleBinding",
			Content:      marshalJSON(crb),
			Metadata:     meta,
		})

		// BINDS_ROLE edge.
		roleID := resourceID("", "ClusterRole", crb.RoleRef.Name)
		result.Edges = append(result.Edges, cloud.EdgeSpec{
			SourceID:     id,
			TargetID:     roleID,
			Relationship: kgtypes.EdgeBindsRole,
			Metadata:     map[string]string{"role_type": crb.RoleRef.Kind},
		})

		// BINDS_SUBJECT edges.
		for _, subj := range crb.Subjects {
			var subjID string
			switch subj.Kind {
			case "ServiceAccount":
				subjID = resourceID(subj.Namespace, "ServiceAccount", subj.Name)
			default:
				continue
			}
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     subjID,
				Relationship: kgtypes.EdgeBindsSubject,
				Metadata:     map[string]string{"subject_kind": subj.Kind},
			})
		}
	}
	return nil
}
