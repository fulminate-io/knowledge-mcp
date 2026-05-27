// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// buildANPUnstructured returns a minimal (Baseline)AdminNetworkPolicy
// unstructured object matching the anpSpec shape the postpopulate layer parses.
func buildANPUnstructured(kind, name string, priority int64, ingressAction string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "policy.networking.k8s.io",
		Version: "v1alpha1",
		Kind:    kind,
	})
	obj.SetName(name)
	obj.SetLabels(map[string]string{"tier": "security"})

	spec := map[string]any{
		"subject": map[string]any{
			"namespaces": map[string]any{
				"matchLabels": map[string]any{"env": "prod"},
			},
		},
		"ingress": []any{
			map[string]any{
				"action": ingressAction,
				"from": []any{
					map[string]any{
						"namespaces": map[string]any{
							"matchLabels": map[string]any{"env": "prod"},
						},
					},
				},
			},
		},
	}
	// BaselineAdminNetworkPolicy has no priority field; only include it when
	// the caller asked for one (>=0).
	if priority >= 0 {
		spec["priority"] = priority
	}
	obj.Object["spec"] = spec

	return obj
}

// newANPFakeDynamic wires the fake dynamic client with both ANP list kinds so
// Resource(gvr).List() round-trips the seeded objects.
func newANPFakeDynamic(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			adminNetworkPolicyGVR:         "AdminNetworkPolicyList",
			baselineAdminNetworkPolicyGVR: "BaselineAdminNetworkPolicyList",
		},
		objs...,
	)
}

func TestAdminNetworkPoliciesSubCollector_Basic(t *testing.T) {
	anp := buildANPUnstructured("AdminNetworkPolicy", "deny-cross-env", 50, "Deny")
	banp := buildANPUnstructured("BaselineAdminNetworkPolicy", "default", -1, "Allow")
	dyn := newANPFakeDynamic(anp, banp)

	sub := &adminNetworkPoliciesSubCollector{dynamicClient: dyn}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 2)
	assert.Empty(t, result.Edges, "ANP subcollector emits no edges — all resolution lives in postpopulate")

	byID := make(map[string]int, len(result.Resources))
	for i, r := range result.Resources {
		byID[r.ID] = i
	}

	// AdminNetworkPolicy (cluster-scoped, so ID = Kind/name).
	anpIdx, ok := byID["AdminNetworkPolicy/deny-cross-env"]
	require.True(t, ok, "AdminNetworkPolicy node missing")
	anpRes := result.Resources[anpIdx]
	assert.Equal(t, "deny-cross-env", anpRes.Name)
	assert.Equal(t, "AdminNetworkPolicy", anpRes.ResourceType)
	assert.Empty(t, anpRes.Region, "cluster-scoped resources have no namespace")
	assert.Equal(t, "security", anpRes.Metadata["label/tier"])
	assert.Equal(t, "policy.networking.k8s.io/v1alpha1", anpRes.Metadata["api_version"])
	assert.Equal(t, "50", anpRes.Metadata["priority"])

	// Content must parse into the existing anpSpec shape used by
	// postpopulate_netpol_anp.go — this is the wire contract between the
	// collector and the postpopulate layer.
	var parsed anpSpec
	require.NoError(t, json.Unmarshal(anpRes.Content, &parsed))
	assert.Equal(t, 50, parsed.Spec.Priority)
	require.Len(t, parsed.Spec.Ingress, 1)
	assert.Equal(t, "Deny", parsed.Spec.Ingress[0].Action)

	// BaselineAdminNetworkPolicy also flows through as its own resource_type
	// so the postpopulate loop picks it up in the second query.
	banpIdx, ok := byID["BaselineAdminNetworkPolicy/default"]
	require.True(t, ok, "BaselineAdminNetworkPolicy node missing")
	banpRes := result.Resources[banpIdx]
	assert.Equal(t, "BaselineAdminNetworkPolicy", banpRes.ResourceType)
	// BANP has no priority — metadata must NOT contain a priority key.
	_, hasPriority := banpRes.Metadata["priority"]
	assert.False(t, hasPriority, "BaselineAdminNetworkPolicy should not carry a priority metadata key")
}

func TestAdminNetworkPoliciesSubCollector_EmptyCluster(t *testing.T) {
	// Fake dynamic client with zero ANP objects — Resource(gvr).List() returns
	// an empty UnstructuredList (not NotFound) which is the standard fake
	// behavior once the GVR is registered.
	dyn := newANPFakeDynamic()
	sub := &adminNetworkPoliciesSubCollector{dynamicClient: dyn}

	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, result.Resources)
}

func TestIsANPAPIMissing(t *testing.T) {
	// IsNotFound path — typical for a cluster without the ANP CRDs installed.
	notFound := apierrors.NewNotFound(
		schema.GroupResource{Group: "policy.networking.k8s.io", Resource: "adminnetworkpolicies"},
		"",
	)
	assert.True(t, isANPAPIMissing(notFound))

	// A real permission error must NOT be swallowed — we want the collector
	// aggregator to surface it so operators notice.
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: "policy.networking.k8s.io", Resource: "adminnetworkpolicies"},
		"x",
		errors.New("denied"),
	)
	assert.False(t, isANPAPIMissing(forbidden))

	// nil error is never "missing".
	assert.False(t, isANPAPIMissing(nil))

	// Plain error is not missing — only typed k8s errors count.
	assert.False(t, isANPAPIMissing(errors.New("boom")))
}

func TestAdminNetworkPoliciesSubCollector_ContentRoundTrip(t *testing.T) {
	// End-to-end: collector produces Content that postpopulate_netpol_anp.go
	// can unmarshal into anpSpec without loss. This guards the wire contract
	// between the two halves of Phase 5.5.
	anp := buildANPUnstructured("AdminNetworkPolicy", "trip", 10, "Allow")
	dyn := newANPFakeDynamic(anp)

	sub := &adminNetworkPoliciesSubCollector{dynamicClient: dyn}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	var parsed anpSpec
	require.NoError(t, json.Unmarshal(result.Resources[0].Content, &parsed))
	assert.Equal(t, 10, parsed.Spec.Priority)
	require.NotNil(t, parsed.Spec.Subject.Namespaces, "subject.namespaces must round-trip into anpSubject")
	assert.Equal(t, "prod", parsed.Spec.Subject.Namespaces.MatchLabels["env"])
}
