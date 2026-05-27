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

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// widgetCRD is a reusable generic CRD fixture ("widgets.example.com") for
// ownerRef tests. Using a deliberately non-registered group (no edge extractor
// wired for "example.com") ensures tests observe ONLY the generic ownerRef
// edges — any other edges would signal cross-contamination. Namespace-scoped
// because OwnerReferences are required to be same-namespace per the k8s spec,
// so ClusterScoped + owners isn't a meaningful combination to exercise here.
var widgetCRD = apiextensionsv1.CustomResourceDefinition{
	ObjectMeta: metav1.ObjectMeta{Name: "widgets.example.com"},
	Spec: apiextensionsv1.CustomResourceDefinitionSpec{
		Group: "example.com",
		Scope: apiextensionsv1.NamespaceScoped,
		Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "widgets", Kind: "Widget"},
		Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
			{Name: "v1", Served: true, Storage: true},
		},
	},
}

// newWidget builds a namespaced Widget CR with the given ownerReferences.
func newWidget(name, namespace string, owners []metav1.OwnerReference) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Widget"})
	u.SetName(name)
	if namespace != "" {
		u.SetNamespace(namespace)
	}
	u.SetOwnerReferences(owners)
	return u
}

// widgetDynClient wires up a fake dynamic client that knows how to list Widget
// via the required custom-list-kinds mapping. The dynamic fake refuses to list
// unknown GVRs, so this registration is mandatory.
func widgetDynClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "example.com", Version: "v1", Resource: "widgets"}: "WidgetList",
		},
		objs...,
	)
}

// collectOwnerEdges runs the CRD subcollector against fixtures and returns
// the emitted OWNED_BY edges, filtered so ordering from the fake client
// doesn't leak into assertions.
func collectOwnerEdges(t *testing.T, crd apiextensionsv1.CustomResourceDefinition,
	objs ...runtime.Object) []cloud.EdgeSpec {
	t.Helper()
	sub := &crdsSubCollector{
		clientset:     fake.NewSimpleClientset(),
		dynamicClient: widgetDynClient(objs...),
		crdLister:     &fakeCRDLister{crds: []apiextensionsv1.CustomResourceDefinition{crd}},
	}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	var edges []cloud.EdgeSpec
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeOwnedBy {
			edges = append(edges, e)
		}
	}
	return edges
}

// TestCRDSubCollector_GenericOwnerRefs_SingleOwner is the happy-path check:
// a generic CR with one controller ownerRef to another CR emits exactly one
// OWNED_BY edge with the controller metadata flag set.
func TestCRDSubCollector_GenericOwnerRefs_SingleOwner(t *testing.T) {
	ctrl := true
	widget := newWidget("child", "default", []metav1.OwnerReference{{
		APIVersion: "example.com/v1", Kind: "WidgetSet", Name: "parent", Controller: &ctrl,
	}})

	edges := collectOwnerEdges(t, widgetCRD, widget)
	require.Len(t, edges, 1)
	assert.Equal(t, "default/Widget/child", edges[0].SourceID)
	assert.Equal(t, "default/WidgetSet/parent", edges[0].TargetID,
		"OWNED_BY target must use resourceID(CR.namespace, ownerKind, ownerName) — ownerRefs are same-namespace per k8s spec")
	assert.Equal(t, "true", edges[0].Metadata["controller"],
		"controller=true ownerRefs must surface controller metadata for downstream filtering")
}

// TestCRDSubCollector_GenericOwnerRefs_MultipleOwners is the critical Q5
// regression: the new code must emit an OWNED_BY edge for EVERY owner, not
// just the controller. Legacy Jobs sometimes point at both a CronJob
// (controller) and a hand-stamped blame parent (not controller) and we need
// both in the graph.
func TestCRDSubCollector_GenericOwnerRefs_MultipleOwners(t *testing.T) {
	ctrlTrue := true
	ctrlFalse := false
	widget := newWidget("widget-1", "prod", []metav1.OwnerReference{
		{APIVersion: "example.com/v1", Kind: "WidgetSet", Name: "set-a", Controller: &ctrlTrue},
		{APIVersion: "example.com/v1", Kind: "Blueprint", Name: "blueprint-b", Controller: &ctrlFalse},
	})

	edges := collectOwnerEdges(t, widgetCRD, widget)
	require.Len(t, edges, 2,
		"both controller and non-controller ownerRefs must emit edges (Q5 no-filter contract)")

	byTarget := make(map[string]cloud.EdgeSpec, 2)
	for _, e := range edges {
		byTarget[e.TargetID] = e
	}

	setEdge, ok := byTarget["prod/WidgetSet/set-a"]
	require.True(t, ok, "controller ownerRef must produce edge with resourceID(ns, kind, name)")
	assert.Equal(t, "true", setEdge.Metadata["controller"],
		"controller=true must surface as controller metadata")

	blueprintEdge, ok := byTarget["prod/Blueprint/blueprint-b"]
	require.True(t, ok, "non-controller ownerRef must also produce an edge")
	// sub_crds.go only adds `controller` when ref.Controller != nil && *ref.Controller —
	// explicit false leaves the key absent, keeping edge Evidence JSON compact.
	_, hasKey := blueprintEdge.Metadata["controller"]
	assert.False(t, hasKey,
		"non-controller ownerRef must NOT add controller metadata (keeps edge Evidence clean)")
}

// TestCRDSubCollector_GenericOwnerRefs_ClusterScopedOwnerKind asserts the
// target-ID shape for namespaced CRs: resourceID namespaces the owner because
// k8s ownerReferences are required to be same-namespace. Even if the kind
// name sounds cluster-scoped ("ClusterRole"), the collector keeps it within
// the CR's namespace since the OwnerReference spec enforces this.
func TestCRDSubCollector_GenericOwnerRefs_ClusterScopedOwnerKind(t *testing.T) {
	widget := newWidget("widget", "staging", []metav1.OwnerReference{{
		APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole", Name: "widget-owner",
	}})

	edges := collectOwnerEdges(t, widgetCRD, widget)
	require.Len(t, edges, 1)
	assert.Equal(t, "staging/ClusterRole/widget-owner", edges[0].TargetID,
		"owner target must be namespaced to CR's namespace — OwnerReferences are same-namespace per k8s API")
}

// TestCRDSubCollector_GenericOwnerRefs_CoexistsWithRegisteredExtractor guards
// against a bug where registering ownerRef emission could replace rather than
// append to an existing extractor. Uses cert-manager.io because it has a
// registered extractor (extractCertManagerEdges → spec.issuerRef) — a
// Certificate with both an ownerRef AND an issuerRef must emit BOTH edges.
func TestCRDSubCollector_GenericOwnerRefs_CoexistsWithRegisteredExtractor(t *testing.T) {
	crd := apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "certificates.cert-manager.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "cert-manager.io",
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "certificates", Kind: "Certificate"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1", Served: true, Storage: true},
			},
		},
	}

	ctrl := true
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "cert-manager.io", Version: "v1", Kind: "Certificate",
	})
	cert.SetName("my-cert")
	cert.SetNamespace("default")
	cert.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Controller: &ctrl,
	}})
	cert.Object["spec"] = map[string]any{
		"issuerRef": map[string]any{"name": "prod-issuer", "kind": "Issuer"},
	}

	scheme := runtime.NewScheme()
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}: "CertificateList",
		},
		cert,
	)

	sub := &crdsSubCollector{
		clientset:     fake.NewSimpleClientset(),
		dynamicClient: dynClient,
		crdLister:     &fakeCRDLister{crds: []apiextensionsv1.CustomResourceDefinition{crd}},
	}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	// Both edges must coexist on the Certificate CR:
	//   - OWNED_BY → default/Deployment/web (from ownerRef)
	//   - ISSUED_BY → default/Issuer/prod-issuer (from cert-manager extractor)
	require.NotNil(t,
		findEdge(result.Edges, "default/Certificate/my-cert", "default/Deployment/web", kgtypes.EdgeOwnedBy),
		"ownerRef edge must be emitted alongside registered extractor's edges")
	require.NotNil(t,
		findEdge(result.Edges, "default/Certificate/my-cert", "default/Issuer/prod-issuer", kgtypes.EdgeIssuedBy),
		"registered cert-manager extractor edge must still be emitted alongside ownerRef edge")
}

// TestCRDSubCollector_GenericOwnerRefs_NoOwners is a regression guard against
// the off-by-one bug where a CR without ownerRefs accidentally produces an
// OWNED_BY edge to an empty target ID. A CR with empty OwnerReferences
// (freshly kubectl-applied with no parent) must emit ZERO OWNED_BY edges,
// while the resource itself still materializes.
func TestCRDSubCollector_GenericOwnerRefs_NoOwners(t *testing.T) {
	widget := newWidget("orphan-widget", "default", nil)

	sub := &crdsSubCollector{
		clientset:     fake.NewSimpleClientset(),
		dynamicClient: widgetDynClient(widget),
		crdLister: &fakeCRDLister{
			crds: []apiextensionsv1.CustomResourceDefinition{widgetCRD},
		},
	}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	// CRD definition + CR instance, both present even with no owners.
	require.Len(t, result.Resources, 2)
	assert.Equal(t, "default/Widget/orphan-widget", result.Resources[1].ID)

	for _, e := range result.Edges {
		assert.NotEqual(t, kgtypes.EdgeOwnedBy, e.Relationship,
			"CR with no ownerRefs must not emit any OWNED_BY edges; found spurious %+v", e)
	}
}
