// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestServiceAccountsSubCollector_IRSA(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-sa",
			Namespace: "default",
			Annotations: map[string]string{
				"eks.amazonaws.com/role-arn": "arn:aws:iam::123456789012:role/app-role",
			},
		},
		ImagePullSecrets: []corev1.LocalObjectReference{
			{Name: "ecr-creds"},
		},
	})

	sub := &serviceAccountsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	res := result.Resources[0]
	assert.Equal(t, "default/ServiceAccount/app-sa", res.ID)
	assert.Equal(t, "arn:aws:iam::123456789012:role/app-role", res.Metadata["irsa_role_arn"])

	// AWS cascade target.
	require.Len(t, result.Targets, 1)
	assert.Equal(t, "aws", result.Targets[0].Collector)
	assert.Equal(t, "123456789012", result.Targets[0].ID)

	// ImagePullSecret edge.
	require.Len(t, result.Edges, 1)
	assert.Equal(t, kgtypes.EdgeMountsSecret, result.Edges[0].Relationship)
	assert.Equal(t, "default/Secret/ecr-creds", result.Edges[0].TargetID)
}

func TestServiceAccountsSubCollector_GCPWorkloadIdentity(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gcp-sa",
			Namespace: "prod",
			Annotations: map[string]string{
				"iam.gke.io/gcp-service-account": "my-sa@my-project.iam.gserviceaccount.com",
			},
		},
	})

	sub := &serviceAccountsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Targets, 1)
	assert.Equal(t, "gcp", result.Targets[0].Collector)
	assert.Equal(t, "my-project", result.Targets[0].ID)
}

func TestServiceAccountsSubCollector_AzureWorkloadIdentity(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "azure-sa",
			Namespace: "default",
			Annotations: map[string]string{
				"azure.workload.identity/client-id": "12345678-abcd-efgh-ijkl-123456789012",
			},
		},
	})

	sub := &serviceAccountsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Targets, 1)
	assert.Equal(t, "azure", result.Targets[0].Collector)
	assert.Equal(t, "12345678-abcd-efgh-ijkl-123456789012", result.Targets[0].ID)
}

func TestRolesSubCollector(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "default"},
			Rules: []rbacv1.PolicyRule{
				{Verbs: []string{"get", "list"}, Resources: []string{"pods"}},
			},
		},
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "admin"},
			Rules: []rbacv1.PolicyRule{
				{Verbs: []string{"*"}, Resources: []string{"*"}},
			},
		},
	)

	sub := &rolesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 2)

	// Namespaced role.
	assert.Equal(t, "default/Role/reader", result.Resources[0].ID)
	assert.Equal(t, "1", result.Resources[0].Metadata["rule_count"])

	// Cluster-scoped role (no namespace prefix).
	assert.Equal(t, "ClusterRole/admin", result.Resources[1].ID)
	assert.Equal(t, "1", result.Resources[1].Metadata["rule_count"])
}

func TestRoleBindingsSubCollector(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "reader-binding", Namespace: "default"},
			RoleRef: rbacv1.RoleRef{
				Kind: "Role",
				Name: "reader",
			},
			Subjects: []rbacv1.Subject{
				{Kind: "ServiceAccount", Name: "app-sa", Namespace: "default"},
			},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "admin-binding"},
			RoleRef: rbacv1.RoleRef{
				Kind: "ClusterRole",
				Name: "admin",
			},
			Subjects: []rbacv1.Subject{
				{Kind: "ServiceAccount", Name: "admin-sa", Namespace: "kube-system"},
				{Kind: "User", Name: "alice"}, // should be skipped
			},
		},
	)

	sub := &roleBindingsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 2)

	// Namespaced RoleBinding.
	assert.Equal(t, "default/RoleBinding/reader-binding", result.Resources[0].ID)

	// ClusterRoleBinding (cluster-scoped).
	assert.Equal(t, "ClusterRoleBinding/admin-binding", result.Resources[1].ID)

	// Check edges.
	edgeMap := make(map[kgtypes.EdgeType][]string)
	for _, e := range result.Edges {
		edgeMap[e.Relationship] = append(edgeMap[e.Relationship], e.TargetID)
	}

	// BINDS_ROLE edges.
	assert.Contains(t, edgeMap[kgtypes.EdgeBindsRole], "default/Role/reader")
	assert.Contains(t, edgeMap[kgtypes.EdgeBindsRole], "ClusterRole/admin")

	// BINDS_SUBJECT edges (User "alice" should NOT be present).
	assert.Contains(t, edgeMap[kgtypes.EdgeBindsSubject], "default/ServiceAccount/app-sa")
	assert.Contains(t, edgeMap[kgtypes.EdgeBindsSubject], "kube-system/ServiceAccount/admin-sa")
	assert.Len(t, edgeMap[kgtypes.EdgeBindsSubject], 2) // no User edge
}
