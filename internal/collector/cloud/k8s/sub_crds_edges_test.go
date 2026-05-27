// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestCRDEdges_CertManager_Certificate(t *testing.T) {
	crd := apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "certificates.cert-manager.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "cert-manager.io",
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "certificates",
				Kind:   "Certificate",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1", Served: true, Storage: true},
			},
		},
	}

	certObj := &unstructured.Unstructured{}
	certObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "cert-manager.io", Version: "v1", Kind: "Certificate",
	})
	certObj.SetName("my-cert")
	certObj.SetNamespace("default")
	certObj.Object["spec"] = map[string]any{
		"issuerRef": map[string]any{
			"name": "my-issuer",
			"kind": "Issuer",
		},
	}

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}: "CertificateList",
		},
		certObj,
	)

	sub := &crdsSubCollector{
		clientset:     fake.NewSimpleClientset(),
		dynamicClient: dynClient,
		crdLister:     &fakeCRDLister{crds: []apiextensionsv1.CustomResourceDefinition{crd}},
	}

	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "default/Certificate/my-cert", result.Edges[0].SourceID)
	assert.Equal(t, "default/Issuer/my-issuer", result.Edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeIssuedBy, result.Edges[0].Relationship)
}

func TestCRDEdges_CertManager_ClusterIssuer(t *testing.T) {
	crd := apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "certificates.cert-manager.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "cert-manager.io",
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "certificates",
				Kind:   "Certificate",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1", Served: true, Storage: true},
			},
		},
	}

	certObj := &unstructured.Unstructured{}
	certObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "cert-manager.io", Version: "v1", Kind: "Certificate",
	})
	certObj.SetName("tls-cert")
	certObj.SetNamespace("production")
	certObj.Object["spec"] = map[string]any{
		"issuerRef": map[string]any{
			"name": "letsencrypt-prod",
			"kind": "ClusterIssuer",
		},
	}

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}: "CertificateList",
		},
		certObj,
	)

	sub := &crdsSubCollector{
		clientset:     fake.NewSimpleClientset(),
		dynamicClient: dynClient,
		crdLister:     &fakeCRDLister{crds: []apiextensionsv1.CustomResourceDefinition{crd}},
	}

	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, result.Edges, 1)
	// ClusterIssuer is cluster-scoped: no namespace prefix.
	assert.Equal(t, "ClusterIssuer/letsencrypt-prod", result.Edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeIssuedBy, result.Edges[0].Relationship)
}

func TestCRDEdges_ExternalSecrets(t *testing.T) {
	crd := apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "externalsecrets.external-secrets.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "external-secrets.io",
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "externalsecrets",
				Kind:   "ExternalSecret",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1beta1", Served: true, Storage: true},
			},
		},
	}

	esObj := &unstructured.Unstructured{}
	esObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "external-secrets.io", Version: "v1beta1", Kind: "ExternalSecret",
	})
	esObj.SetName("db-creds")
	esObj.SetNamespace("default")
	esObj.Object["spec"] = map[string]any{
		"secretStoreRef": map[string]any{
			"name": "vault-store",
			"kind": "SecretStore",
		},
	}

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "external-secrets.io", Version: "v1beta1", Resource: "externalsecrets"}: "ExternalSecretList",
		},
		esObj,
	)

	sub := &crdsSubCollector{
		clientset:     fake.NewSimpleClientset(),
		dynamicClient: dynClient,
		crdLister:     &fakeCRDLister{crds: []apiextensionsv1.CustomResourceDefinition{crd}},
	}

	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "default/ExternalSecret/db-creds", result.Edges[0].SourceID)
	assert.Equal(t, "default/SecretStore/vault-store", result.Edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeReferencesStore, result.Edges[0].Relationship)
}

func TestCRDEdges_ExternalSecrets_ClusterStore(t *testing.T) {
	crd := apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "externalsecrets.external-secrets.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "external-secrets.io",
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "externalsecrets",
				Kind:   "ExternalSecret",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1beta1", Served: true, Storage: true},
			},
		},
	}

	esObj := &unstructured.Unstructured{}
	esObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "external-secrets.io", Version: "v1beta1", Kind: "ExternalSecret",
	})
	esObj.SetName("api-keys")
	esObj.SetNamespace("staging")
	esObj.Object["spec"] = map[string]any{
		"secretStoreRef": map[string]any{
			"name": "global-vault",
			"kind": "ClusterSecretStore",
		},
	}

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "external-secrets.io", Version: "v1beta1", Resource: "externalsecrets"}: "ExternalSecretList",
		},
		esObj,
	)

	sub := &crdsSubCollector{
		clientset:     fake.NewSimpleClientset(),
		dynamicClient: dynClient,
		crdLister:     &fakeCRDLister{crds: []apiextensionsv1.CustomResourceDefinition{crd}},
	}

	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, result.Edges, 1)
	// ClusterSecretStore is cluster-scoped.
	assert.Equal(t, "ClusterSecretStore/global-vault", result.Edges[0].TargetID)
	assert.Equal(t, kgtypes.EdgeReferencesStore, result.Edges[0].Relationship)
}

func TestCRDEdges_Istio_VirtualService(t *testing.T) {
	crd := apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "virtualservices.networking.istio.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "networking.istio.io",
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "virtualservices",
				Kind:   "VirtualService",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1beta1", Served: true, Storage: true},
			},
		},
	}

	vsObj := &unstructured.Unstructured{}
	vsObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "networking.istio.io", Version: "v1beta1", Kind: "VirtualService",
	})
	vsObj.SetName("reviews-vs")
	vsObj.SetNamespace("default")
	vsObj.Object["spec"] = map[string]any{
		"http": []any{
			map[string]any{
				"route": []any{
					map[string]any{
						"destination": map[string]any{
							"host": "reviews",
						},
					},
					map[string]any{
						"destination": map[string]any{
							"host": "ratings.prod.svc.cluster.local",
						},
					},
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "networking.istio.io", Version: "v1beta1", Resource: "virtualservices"}: "VirtualServiceList",
		},
		vsObj,
	)

	sub := &crdsSubCollector{
		clientset:     fake.NewSimpleClientset(),
		dynamicClient: dynClient,
		crdLister:     &fakeCRDLister{crds: []apiextensionsv1.CustomResourceDefinition{crd}},
	}

	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, result.Edges, 2)

	edgeTargets := make(map[string]kgtypes.EdgeType)
	for _, e := range result.Edges {
		edgeTargets[e.TargetID] = e.Relationship
	}

	// Short name: uses VirtualService's namespace.
	assert.Equal(t, kgtypes.EdgeRoutesTo, edgeTargets["default/Service/reviews"])
	// FQDN: extracts namespace from second segment.
	assert.Equal(t, kgtypes.EdgeRoutesTo, edgeTargets["prod/Service/ratings"])
}

func TestCRDEdges_Traefik_IngressRoute(t *testing.T) {
	crd := apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "ingressroutes.traefik.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "traefik.io",
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "ingressroutes",
				Kind:   "IngressRoute",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1alpha1", Served: true, Storage: true},
			},
		},
	}

	irObj := &unstructured.Unstructured{}
	irObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "traefik.io", Version: "v1alpha1", Kind: "IngressRoute",
	})
	irObj.SetName("web-route")
	irObj.SetNamespace("default")
	irObj.Object["spec"] = map[string]any{
		"routes": []any{
			map[string]any{
				"services": []any{
					map[string]any{
						"name": "web-svc",
					},
					map[string]any{
						"name":      "api-svc",
						"namespace": "backend",
					},
				},
				"middlewares": []any{
					map[string]any{
						"name": "rate-limit",
					},
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}: "IngressRouteList",
		},
		irObj,
	)

	sub := &crdsSubCollector{
		clientset:     fake.NewSimpleClientset(),
		dynamicClient: dynClient,
		crdLister:     &fakeCRDLister{crds: []apiextensionsv1.CustomResourceDefinition{crd}},
	}

	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	require.Len(t, result.Edges, 3)

	edgeMap := make(map[string]kgtypes.EdgeType)
	for _, e := range result.Edges {
		edgeMap[e.TargetID] = e.Relationship
	}

	// Services.
	assert.Equal(t, kgtypes.EdgeRoutesTo, edgeMap["default/Service/web-svc"])
	assert.Equal(t, kgtypes.EdgeRoutesTo, edgeMap["backend/Service/api-svc"])
	// Middleware.
	assert.Equal(t, kgtypes.EdgeUsesMiddleware, edgeMap["default/Middleware/rate-limit"])
}

func TestCRDEdges_UnknownGroup(t *testing.T) {
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

	sub := &crdsSubCollector{
		clientset:     fake.NewSimpleClientset(),
		dynamicClient: dynClient,
		crdLister:     &fakeCRDLister{crds: []apiextensionsv1.CustomResourceDefinition{crd}},
	}

	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	// Unknown group produces no edges.
	assert.Empty(t, result.Edges)
	// But resources are still collected.
	assert.Len(t, result.Resources, 2) // CRD + 1 instance
}
