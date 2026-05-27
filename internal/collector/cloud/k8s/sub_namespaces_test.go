// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNamespacesSubCollector_Basic(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "default",
				Labels:      map[string]string{"kubernetes.io/metadata.name": "default"},
				Annotations: map[string]string{"note": "primary"},
			},
			Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kube-system",
			},
			Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
	)

	sub := &namespacesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	// Two namespaces.
	require.Len(t, result.Resources, 2)

	// No edges — namespaces have no outbound relationships.
	assert.Empty(t, result.Edges)

	// Find the "default" namespace.
	var defNS *int
	for i, r := range result.Resources {
		if r.Name == "default" {
			defNS = &i
			break
		}
	}
	require.NotNil(t, defNS, "default namespace not found")

	res := result.Resources[*defNS]
	assert.Equal(t, "Namespace/default", res.ID)
	assert.Equal(t, "default", res.Name)
	assert.Equal(t, "Namespace", res.ResourceType)
	assert.Empty(t, res.Region, "namespaces are cluster-scoped")
	assert.Equal(t, "Active", res.Metadata["phase"])
	assert.Equal(t, "1", res.Metadata["annotation_count"])
	assert.Equal(t, "default", res.Metadata["label/kubernetes.io/metadata.name"])
}

func TestNamespacesSubCollector_TerminatingPhase(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "dying"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
	})

	sub := &namespacesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	assert.Equal(t, "Namespace/dying", result.Resources[0].ID)
	assert.Equal(t, "Terminating", result.Resources[0].Metadata["phase"])
}
