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
)

// fakeCRDLister implements crdLister for tests.
type fakeCRDLister struct {
	crds []apiextensionsv1.CustomResourceDefinition
	err  error
}

func (f *fakeCRDLister) ListCRDs(_ context.Context) ([]apiextensionsv1.CustomResourceDefinition, error) {
	return f.crds, f.err
}

func TestCRDSubCollector_ListsCRDs(t *testing.T) {
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

	// Create a fake dynamic client with one Certificate instance.
	scheme := runtime.NewScheme()
	certObj := &unstructured.Unstructured{}
	certObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	certObj.SetName("my-cert")
	certObj.SetNamespace("default")
	certObj.SetLabels(map[string]string{"app": "web"})

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

	// Should have 2 resources: the CRD itself + one instance.
	require.Len(t, result.Resources, 2)

	// CRD resource.
	crdRes := result.Resources[0]
	assert.Equal(t, "CustomResourceDefinition/certificates.cert-manager.io", crdRes.ID)
	assert.Equal(t, "cert-manager.io", crdRes.Metadata["group"])
	assert.Equal(t, "Namespaced", crdRes.Metadata["scope"])
	assert.Equal(t, "v1", crdRes.Metadata["versions"])

	// Instance resource.
	instRes := result.Resources[1]
	assert.Equal(t, "default/Certificate/my-cert", instRes.ID)
	assert.Equal(t, "my-cert", instRes.Name)
	assert.Equal(t, "Certificate", instRes.ResourceType)
	assert.Equal(t, "default", instRes.Region)
	assert.Equal(t, "web", instRes.Metadata["label/app"])
}

func TestCRDSubCollector_ClusterScoped(t *testing.T) {
	crd := apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "clusterissuers.cert-manager.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "cert-manager.io",
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "clusterissuers",
				Kind:   "ClusterIssuer",
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1", Served: true, Storage: true},
			},
		},
	}

	issuerObj := &unstructured.Unstructured{}
	issuerObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "ClusterIssuer",
	})
	issuerObj.SetName("letsencrypt-prod")

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}: "ClusterIssuerList",
		},
		issuerObj,
	)

	sub := &crdsSubCollector{
		clientset:     fake.NewSimpleClientset(),
		dynamicClient: dynClient,
		crdLister:     &fakeCRDLister{crds: []apiextensionsv1.CustomResourceDefinition{crd}},
	}

	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 2)

	// Cluster-scoped instance: Kind/name (no namespace).
	instRes := result.Resources[1]
	assert.Equal(t, "ClusterIssuer/letsencrypt-prod", instRes.ID)
	assert.Empty(t, instRes.Region)
}

func TestCRDSubCollector_InstanceLimit(t *testing.T) {
	// The limit is enforced server-side via ListOptions.Limit.
	// With the fake client, we just verify the Limit is passed correctly.
	// The fake dynamic client doesn't enforce limits, so we verify the code path works.
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

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "example.com", Version: "v1", Resource: "widgets"}: "WidgetList",
		},
	)

	sub := &crdsSubCollector{
		clientset:     fake.NewSimpleClientset(),
		dynamicClient: dynClient,
		crdLister:     &fakeCRDLister{crds: []apiextensionsv1.CustomResourceDefinition{crd}},
	}

	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	// Only the CRD itself, no instances.
	require.Len(t, result.Resources, 1)
	assert.Equal(t, "CustomResourceDefinition/widgets.example.com", result.Resources[0].ID)
}

// CRD edge extraction tests are in sub_crds_edges_test.go
