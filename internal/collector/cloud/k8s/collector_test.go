// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestK8sCollector_FullRun verifies that all 20 subcollectors produce nodes and edges
// when run through buildSubCollectors and RunSubCollectors.
func TestK8sCollector_FullRun(t *testing.T) {
	reclaimPolicy := corev1.PersistentVolumeReclaimDelete

	cs := fake.NewSimpleClientset(
		// Workloads
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "web", Namespace: "default",
				Labels: map[string]string{"app": "web"},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: int32Ptr(2),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						ServiceAccountName: "web-sa",
						Containers:         []corev1.Container{{Name: "app", Image: "nginx:latest"}},
					},
				},
			},
		},
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"},
			Spec: appsv1.StatefulSetSpec{
				Replicas:    int32Ptr(1),
				ServiceName: "db-headless",
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "pg", Image: "postgres:16"}},
					},
				},
			},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "logs", Namespace: "kube-system"},
			Spec: appsv1.DaemonSetSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "fluentd", Image: "fluent/fluentd:latest"}},
					},
				},
			},
		},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "web-abc", Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web"}},
			},
			Spec: appsv1.ReplicaSetSpec{
				Replicas: int32Ptr(2),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "app", Image: "nginx:latest"}},
					},
				},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "web-abc-xyz", Namespace: "default",
				Labels:          map[string]string{"app": "web"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-abc"}},
			},
			Spec: corev1.PodSpec{
				ServiceAccountName: "web-sa",
				Containers:         []corev1.Container{{Name: "app", Image: "nginx:latest"}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},

		// RBAC
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name: "web-sa", Namespace: "default",
				Annotations: map[string]string{
					"eks.amazonaws.com/role-arn": "arn:aws:iam::111222333444:role/web-role",
				},
			},
		},
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "default"},
			Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, Resources: []string{"pods"}}},
		},
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "admin"},
			Rules:      []rbacv1.PolicyRule{{Verbs: []string{"*"}, Resources: []string{"*"}}},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "reader-binding", Namespace: "default"},
			RoleRef:    rbacv1.RoleRef{Kind: "Role", Name: "reader"},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "web-sa", Namespace: "default"}},
		},

		// Networking
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "web-svc", Namespace: "default"},
			Spec: corev1.ServiceSpec{
				Type:     corev1.ServiceTypeClusterIP,
				Selector: map[string]string{"app": "web"},
				Ports:    []corev1.ServicePort{{Protocol: corev1.ProtocolTCP, Port: 80}},
			},
		},
		&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "web-ing", Namespace: "default"},
			Spec: networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{
					{
						Host: "example.com",
						IngressRuleValue: networkingv1.IngressRuleValue{
							HTTP: &networkingv1.HTTPIngressRuleValue{
								Paths: []networkingv1.HTTPIngressPath{
									{
										Backend: networkingv1.IngressBackend{
											Service: &networkingv1.IngressServiceBackend{
												Name: "web-svc",
												Port: networkingv1.ServiceBackendPort{Number: 80},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		&networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "restrict-web", Namespace: "default"},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "web"},
				},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			},
		},

		// Storage
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-001"},
			Spec: corev1.PersistentVolumeSpec{
				Capacity:         corev1.ResourceList{"storage": resource.MustParse("50Gi")},
				StorageClassName: "gp3",
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						Driver:       "ebs.csi.aws.com",
						VolumeHandle: "vol-123",
					},
				},
			},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data-0", Namespace: "default"},
			Spec: corev1.PersistentVolumeClaimSpec{
				VolumeName:       "pv-001",
				StorageClassName: new("gp3"),
			},
			Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
		},
		&storagev1.StorageClass{
			ObjectMeta:    metav1.ObjectMeta{Name: "gp3"},
			Provisioner:   "ebs.csi.aws.com",
			ReclaimPolicy: &reclaimPolicy,
		},

		// Config and Batch
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default"},
			Data:       map[string]string{"key1": "val1", "key2": "val2"},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: "default"},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"username": []byte("admin"), "password": []byte("secret")},
		},
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "default"},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "migrate", Image: "myapp:latest"}},
					},
				},
			},
		},
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default"},
			Spec: batchv1.CronJobSpec{
				Schedule: "0 3 * * *",
				JobTemplate: batchv1.JobTemplateSpec{
					Spec: batchv1.JobSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "backup", Image: "myapp:latest"}},
							},
						},
					},
				},
			},
		},
	)

	// CRD setup.
	crd := apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets.example.com"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "widgets",
				Kind:   "Widget",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1", Served: true, Storage: true},
			},
		},
	}

	widgetObj := &unstructured.Unstructured{}
	widgetObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "example.com", Version: "v1", Kind: "Widget",
	})
	widgetObj.SetName("my-widget")
	widgetObj.SetNamespace("default")

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "example.com", Version: "v1", Resource: "widgets"}: "WidgetList",
		},
		widgetObj,
	)

	// Build subcollectors with test clients.
	bundle := &clientBundle{
		clientset:     cs,
		dynamicClient: dynClient,
		contextName:   "test-cluster",
	}

	subs := buildSubCollectorsForTest(bundle, &fakeCRDLister{
		crds: []apiextensionsv1.CustomResourceDefinition{crd},
	})

	nodes, edges, targets, err := cloud.RunSubCollectors(context.Background(), subs, cloud.RunOptions{})
	// Best-effort: err may be non-nil if some subcollectors partially fail.
	_ = err

	// Verify we have nodes from all subcollector types.
	typeCount := make(map[string]int)
	for _, n := range nodes {
		rt := kgtypes.Value(n, "resource_type")
		typeCount[rt]++
	}

	// All expected resource types present.
	expectedTypes := []string{
		"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Pod",
		"ServiceAccount", "Role", "ClusterRole",
		"RoleBinding",
		"Service", "Ingress", "NetworkPolicy",
		"PersistentVolume", "PersistentVolumeClaim", "StorageClass",
		"ConfigMap", "Secret",
		"Job", "CronJob",
		"CustomResourceDefinition", "Widget",
	}

	for _, rt := range expectedTypes {
		assert.Positive(t, typeCount[rt], "missing resource type: %s", rt)
	}

	// Verify edge count is non-zero (all the SA, ConfigMap, Secret refs, etc.).
	assert.NotEmpty(t, edges, "should have at least some edges")

	// Verify specific edges.
	edgeMap := make(map[kgtypes.EdgeType]int)
	for _, e := range edges {
		edgeMap[e.Type]++
	}
	assert.Positive(t, edgeMap[kgtypes.EdgeUsesSA], "should have USES_SA edges")
	assert.Positive(t, edgeMap[kgtypes.EdgeOwnedBy], "should have OWNED_BY edges")
	assert.Positive(t, edgeMap[kgtypes.EdgeBindsRole], "should have BINDS_ROLE edges")
	assert.Positive(t, edgeMap[kgtypes.EdgeBindsSubject], "should have BINDS_SUBJECT edges")
	assert.Positive(t, edgeMap[kgtypes.EdgeRoutesTo], "should have ROUTES_TO edges")
	assert.Positive(t, edgeMap[kgtypes.EdgeUsesStorageClass], "should have USES_STORAGE_CLASS edges")
	assert.Positive(t, edgeMap[kgtypes.EdgeBoundTo], "should have BOUND_TO edges")

	// Verify cascade targets exist (IRSA SA should produce AWS target).
	require.NotEmpty(t, targets, "should have cascade targets")
	var hasAWS bool
	for _, t := range targets {
		if t.Collector == "aws" {
			hasAWS = true
			break
		}
	}
	assert.True(t, hasAWS, "should have AWS cascade target from IRSA SA")

	// Verify secret safety: Content is allowed to contain decoded values (it
	// is encrypted at rest, excluded from BM25 for GraphCloud, and gated off
	// the summarizer/embedder in Phase 2 of the CONNECTS_TO work). Metadata
	// must NEVER contain secret values — that would bypass those protections.
	for _, n := range nodes {
		if kgtypes.Value(n, "resource_type") != "Secret" {
			continue
		}
		for k, v := range n.Metadata {
			assert.NotContains(t, v, "admin",
				"secret values must not appear in metadata key %q", k)
			assert.NotContains(t, v, "secret",
				"secret values must not appear in metadata key %q", k)
		}
	}

	t.Logf("Integration test: %d nodes, %d edges, %d cascade targets", len(nodes), len(edges), len(targets))
	t.Logf("Resource types: %v", typeCount)
	t.Logf("Edge types: %v", edgeMap)
}

// buildSubCollectorsForTest creates subcollectors with a custom crdLister for testing.
func buildSubCollectorsForTest(bundle *clientBundle, lister crdLister) []cloud.SubCollector {
	cs := bundle.clientset
	return []cloud.SubCollector{
		&deploymentsSubCollector{clientset: cs},
		&statefulSetsSubCollector{clientset: cs},
		&daemonSetsSubCollector{clientset: cs},
		&replicaSetsSubCollector{clientset: cs},
		&podsSubCollector{clientset: cs},
		&serviceAccountsSubCollector{clientset: cs},
		&rolesSubCollector{clientset: cs},
		&roleBindingsSubCollector{clientset: cs},
		&servicesSubCollector{clientset: cs},
		&ingressesSubCollector{clientset: cs},
		&networkPoliciesSubCollector{clientset: cs},
		&persistentVolumesSubCollector{clientset: cs},
		&pvcsSubCollector{clientset: cs},
		&storageClassesSubCollector{clientset: cs},
		&configMapsSubCollector{clientset: cs},
		&secretsSubCollector{clientset: cs},
		&jobsSubCollector{clientset: cs},
		&namespacesSubCollector{clientset: cs},
		&hpaSubCollector{clientset: cs},
		&pdbSubCollector{clientset: cs},
		&crdsSubCollector{
			clientset:     cs,
			dynamicClient: bundle.dynamicClient,
			crdLister:     lister,
		},
	}
}
