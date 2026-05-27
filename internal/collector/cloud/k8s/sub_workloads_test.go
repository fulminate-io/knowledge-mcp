// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

//go:fix inline
func int32Ptr(i int32) *int32 { return new(i) }

func TestDeploymentsSubCollector(t *testing.T) {
	cs := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web",
			Namespace: "default",
			Labels:    map[string]string{"app": "web"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(3),
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: "web-sa",
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "123456789.dkr.ecr.us-east-1.amazonaws.com/web:latest",
							EnvFrom: []corev1.EnvFromSource{
								{ConfigMapRef: &corev1.ConfigMapEnvSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "web-config"},
								}},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "secret-vol",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{SecretName: "web-secret"},
							},
						},
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: "web-data",
								},
							},
						},
					},
				},
			},
		},
	})

	sub := &deploymentsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	// Verify resource.
	require.Len(t, result.Resources, 1)
	res := result.Resources[0]
	assert.Equal(t, "default/Deployment/web", res.ID)
	assert.Equal(t, "web", res.Name)
	assert.Equal(t, "Deployment", res.ResourceType)
	assert.Equal(t, "default", res.Region)
	assert.Equal(t, "3", res.Metadata["replicas"])
	assert.Equal(t, "RollingUpdate", res.Metadata["strategy"])
	assert.Equal(t, "web", res.Metadata["label/app"])

	// Verify edges.
	edgeMap := make(map[kgtypes.EdgeType][]string)
	for _, e := range result.Edges {
		edgeMap[e.Relationship] = append(edgeMap[e.Relationship], e.TargetID)
	}
	assert.Contains(t, edgeMap[kgtypes.EdgeUsesSA], "default/ServiceAccount/web-sa")
	assert.Contains(t, edgeMap[kgtypes.EdgeMountsConfigMap], "default/ConfigMap/web-config")
	assert.Contains(t, edgeMap[kgtypes.EdgeMountsSecret], "default/Secret/web-secret")
	assert.Contains(t, edgeMap[kgtypes.EdgeUsesPVC], "default/PersistentVolumeClaim/web-data")

	// Verify cascade target (ECR image).
	require.Len(t, result.Targets, 1)
	assert.Equal(t, "aws", result.Targets[0].Collector)
	assert.Equal(t, "123456789", result.Targets[0].ID)
}

func TestStatefulSetsSubCollector(t *testing.T) {
	cs := fake.NewSimpleClientset(&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db",
			Namespace: "data",
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    int32Ptr(3),
			ServiceName: "db-headless",
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "postgres", Image: "postgres:16"}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{ObjectMeta: metav1.ObjectMeta{Name: "data-vol"}},
			},
		},
	})

	sub := &statefulSetsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	assert.Equal(t, "data/StatefulSet/db", result.Resources[0].ID)
	assert.Equal(t, "db-headless", result.Resources[0].Metadata["service_name"])

	// VolumeClaimTemplate produces USES_PVC edge.
	var pvcEdges []string
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeUsesPVC {
			pvcEdges = append(pvcEdges, e.TargetID)
		}
	}
	assert.Contains(t, pvcEdges, "data/PersistentVolumeClaim/data-vol")
}

func TestDaemonSetsSubCollector(t *testing.T) {
	cs := fake.NewSimpleClientset(&appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fluentd",
			Namespace: "logging",
		},
		Spec: appsv1.DaemonSetSpec{
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.RollingUpdateDaemonSetStrategyType},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: "fluentd-sa",
					Containers:         []corev1.Container{{Name: "fluentd", Image: "fluent/fluentd:latest"}},
				},
			},
		},
	})

	sub := &daemonSetsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	assert.Equal(t, "logging/DaemonSet/fluentd", result.Resources[0].ID)
	assert.Equal(t, "RollingUpdate", result.Resources[0].Metadata["update_strategy"])

	// SA edge.
	var saEdges []string
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeUsesSA {
			saEdges = append(saEdges, e.TargetID)
		}
	}
	assert.Contains(t, saEdges, "logging/ServiceAccount/fluentd-sa")
}

func TestReplicaSetsSubCollector_OwnerReference(t *testing.T) {
	cs := fake.NewSimpleClientset(&appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc123",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", Name: "web"},
			},
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: int32Ptr(3),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "nginx:latest"}},
				},
			},
		},
	})

	sub := &replicaSetsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	assert.Equal(t, "default/ReplicaSet/web-abc123", result.Resources[0].ID)

	// OwnerReference edge to Deployment.
	var ownerEdges []string
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeOwnedBy {
			ownerEdges = append(ownerEdges, e.TargetID)
		}
	}
	assert.Contains(t, ownerEdges, "default/Deployment/web")
}

func TestPodsSubCollector(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc123-xyz",
			Namespace: "default",
			Labels:    map[string]string{"app": "web"},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "web-abc123"},
			},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: "web-sa",
			NodeName:           "node-1",
			Containers:         []corev1.Container{{Name: "app", Image: "nginx:latest"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.1",
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true, RestartCount: 0},
			},
		},
	})

	sub := &podsSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	res := result.Resources[0]
	assert.Equal(t, "default/Pod/web-abc123-xyz", res.ID)
	assert.Equal(t, "Running", res.Metadata["phase"])
	assert.Equal(t, "node-1", res.Metadata["node_name"])
	assert.Equal(t, "10.0.0.1", res.Metadata["pod_ip"])
	assert.Equal(t, "1/1", res.Metadata["ready"])
	assert.Equal(t, "0", res.Metadata["restarts"])
	assert.Equal(t, "web", res.Metadata["label/app"])

	// OwnerReference edge.
	var ownerEdges []string
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeOwnedBy {
			ownerEdges = append(ownerEdges, e.TargetID)
		}
	}
	assert.Contains(t, ownerEdges, "default/ReplicaSet/web-abc123")

	// SA edge.
	var saEdges []string
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeUsesSA {
			saEdges = append(saEdges, e.TargetID)
		}
	}
	assert.Contains(t, saEdges, "default/ServiceAccount/web-sa")
}

func TestExtractImageTargets(t *testing.T) {
	containers := []corev1.Container{
		{Image: "123456789.dkr.ecr.us-east-1.amazonaws.com/web:latest"},
		{Image: "gcr.io/my-project/api:v1"},
		{Image: "us-central1-docker.pkg.dev/my-project/repo/image:v2"},
		{Image: "myregistry.azurecr.io/app:latest"},
		{Image: "nginx:latest"},              // no cascade
		{Image: "docker.io/library/redis:7"}, // no cascade
	}

	targets := extractImageTargets(containers)
	// GCR and Artifact Registry targets deduplicate to same project.
	assert.Len(t, targets, 3)

	targetMap := make(map[string]string)
	for _, t := range targets {
		targetMap[t.Collector+":"+t.ID] = ""
	}
	assert.Contains(t, targetMap, "aws:123456789")
	assert.Contains(t, targetMap, "gcp:my-project")
	assert.Contains(t, targetMap, "azure:myregistry")
}
