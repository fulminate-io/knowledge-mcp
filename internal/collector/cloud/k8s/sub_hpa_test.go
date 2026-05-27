// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestHPASubCollector(t *testing.T) {
	cs := fake.NewSimpleClientset(&autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-hpa",
			Namespace: "default",
			Labels:    map[string]string{"app": "web"},
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MinReplicas: int32Ptr(2),
			MaxReplicas: 10,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				Kind: "Deployment",
				Name: "web",
			},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			CurrentReplicas: 5,
		},
	})

	sub := &hpaSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	// Verify resource.
	require.Len(t, result.Resources, 1)
	res := result.Resources[0]
	assert.Equal(t, "default/HorizontalPodAutoscaler/web-hpa", res.ID)
	assert.Equal(t, "web-hpa", res.Name)
	assert.Equal(t, "HorizontalPodAutoscaler", res.ResourceType)
	assert.Equal(t, "default", res.Region)
	assert.Equal(t, "2", res.Metadata["min_replicas"])
	assert.Equal(t, "10", res.Metadata["max_replicas"])
	assert.Equal(t, "5", res.Metadata["current_replicas"])
	assert.Equal(t, "web", res.Metadata["label/app"])

	// Verify EdgeScales edge to target deployment.
	require.Len(t, result.Edges, 1)
	edge := result.Edges[0]
	assert.Equal(t, "default/HorizontalPodAutoscaler/web-hpa", edge.SourceID)
	assert.Equal(t, "default/Deployment/web", edge.TargetID)
	assert.Equal(t, kgtypes.EdgeScales, edge.Relationship)
}

func TestHPASubCollector_StatefulSetTarget(t *testing.T) {
	cs := fake.NewSimpleClientset(&autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-hpa",
			Namespace: "data",
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MaxReplicas: 5,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				Kind: "StatefulSet",
				Name: "db",
			},
		},
	})

	sub := &hpaSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	// Edge targets a StatefulSet.
	require.Len(t, result.Edges, 1)
	assert.Equal(t, "data/StatefulSet/db", result.Edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeScales, result.Edges[0].Relationship)
}
