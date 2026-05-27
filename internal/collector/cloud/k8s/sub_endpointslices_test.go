// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// boolPtr / protoPtr build the *bool / *corev1.Protocol pointers the
// discoveryv1 EndpointConditions and EndpointPort types require. A nil *bool
// on Conditions signals the legacy "unknown" state. (int32Ptr lives in
// sub_workloads_test.go — package-level helpers are shared.)
//

func protoPtr(p corev1.Protocol) *corev1.Protocol { return new(p) }

// findEdge returns the first edge matching (src, tgt, rel) or nil if absent.
func findEdge(edges []cloud.EdgeSpec, src, tgt string, rel kgtypes.EdgeType) *cloud.EdgeSpec {
	for i := range edges {
		e := &edges[i]
		if e.SourceID == src && e.TargetID == tgt && e.Relationship == rel {
			return e
		}
	}
	return nil
}

// TestEndpointSlicesSubCollector_ServiceEdge verifies that a slice carrying
// the canonical kubernetes.io/service-name label produces exactly one
// HAS_ENDPOINT_SLICE edge whose source is the owning Service and whose target
// is the slice node ID. This is the primary linkage between Services and the
// pods they front: without it, downstream queries cannot find the pods backing
// a service.
func TestEndpointSlicesSubCollector_ServiceEdge(t *testing.T) {
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-abc",
			Namespace: "default",
			Labels:    map[string]string{serviceNameLabel: "api"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
	}

	cs := fake.NewSimpleClientset(slice)
	sub := &endpointSlicesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	sliceID := "default/EndpointSlice/api-abc"
	svcID := "default/Service/api"

	edge := findEdge(result.Edges, svcID, sliceID, kgtypes.EdgeHasEndpointSlice)
	require.NotNil(t, edge,
		"slice with kubernetes.io/service-name label must produce one HAS_ENDPOINT_SLICE edge from Service to slice")

	// And exactly one HAS_ENDPOINT_SLICE edge total.
	var hasCount int
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeHasEndpointSlice {
			hasCount++
		}
	}
	assert.Equal(t, 1, hasCount, "expected exactly one HAS_ENDPOINT_SLICE edge")
}

// TestEndpointSlicesSubCollector_PodEdges verifies that every endpoint with
// a Pod targetRef produces a BACKS edge from the slice to the pod, with the
// per-endpoint readiness state encoded into edge metadata. This is the edge
// downstream callers walk to enumerate the pods served by a service.
func TestEndpointSlicesSubCollector_PodEdges(t *testing.T) {
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-xyz",
			Namespace: "default",
			Labels:    map[string]string{serviceNameLabel: "api"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports: []discoveryv1.EndpointPort{{
			Port: int32Ptr(8080), Protocol: protoPtr(corev1.ProtocolTCP),
		}},
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses: []string{"10.0.0.1"},
				TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "api-1", Namespace: "default"},
				Conditions: discoveryv1.EndpointConditions{
					Ready: new(true), Serving: new(true), Terminating: new(false),
				},
			},
			{
				Addresses: []string{"10.0.0.2"},
				// TargetRef.Namespace omitted — must fall back to slice's namespace.
				TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "api-2"},
				Conditions: discoveryv1.EndpointConditions{
					Ready: new(true), Serving: new(true), Terminating: new(false),
				},
			},
			{
				Addresses: []string{"10.0.0.3"},
				TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "api-3", Namespace: "default"},
				Conditions: discoveryv1.EndpointConditions{
					Ready: new(false), Serving: new(true), Terminating: new(true),
				},
			},
		},
	}

	cs := fake.NewSimpleClientset(slice)
	sub := &endpointSlicesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	sliceID := "default/EndpointSlice/api-xyz"
	for _, name := range []string{"api-1", "api-2", "api-3"} {
		podID := "default/Pod/" + name
		edge := findEdge(result.Edges, sliceID, podID, kgtypes.EdgeBacks)
		require.NotNilf(t, edge, "missing BACKS edge to pod %s (covers TargetRef.Namespace fallback)", name)
	}

	// Per-endpoint metadata: api-3 is the terminating-not-ready endpoint.
	terminating := findEdge(result.Edges, sliceID, "default/Pod/api-3", kgtypes.EdgeBacks)
	require.NotNil(t, terminating)
	assert.Equal(t, "false", terminating.Metadata["ready"])
	assert.Equal(t, "true", terminating.Metadata["serving"])
	assert.Equal(t, "true", terminating.Metadata["terminating"])
}

// TestEndpointSlicesSubCollector_SkipNonPodTargetRef confirms that endpoints
// pointing at a non-Pod kind (rare manually-managed slices targeting another
// Service or external resource) do NOT produce BACKS edges, while sibling
// Pod-kind endpoints in the same slice still do. This protects downstream
// pod-existence assumptions on the BACKS edge contract.
func TestEndpointSlicesSubCollector_SkipNonPodTargetRef(t *testing.T) {
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mixed",
			Namespace: "default",
			Labels:    map[string]string{serviceNameLabel: "mixed"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{"10.0.0.1"},
				TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: "real-pod", Namespace: "default"},
				Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
			},
			{
				Addresses:  []string{"10.0.0.2"},
				TargetRef:  &corev1.ObjectReference{Kind: "Service", Name: "external-svc", Namespace: "default"},
				Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
			},
			{
				Addresses:  []string{"10.0.0.3"},
				TargetRef:  &corev1.ObjectReference{Kind: "Node", Name: "node-1"},
				Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
			},
		},
	}

	cs := fake.NewSimpleClientset(slice)
	sub := &endpointSlicesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	var backsCount int
	for _, e := range result.Edges {
		if e.Relationship == kgtypes.EdgeBacks {
			backsCount++
			assert.Contains(t, e.TargetID, "/Pod/",
				"BACKS edge target %q must reference a Pod", e.TargetID)
		}
	}
	assert.Equal(t, 1, backsCount,
		"expected exactly one BACKS edge (only the Pod-kind endpoint should match)")
}

// TestEndpointSlicesSubCollector_SkipNilTargetRef confirms endpoints without
// any TargetRef (headless IP-only slices for external services) do NOT produce
// BACKS edges. Without this guard the collector would dereference a nil
// pointer or emit edges with empty target IDs.
func TestEndpointSlicesSubCollector_SkipNilTargetRef(t *testing.T) {
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "headless",
			Namespace: "default",
			Labels:    map[string]string{serviceNameLabel: "external"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{"203.0.113.1"}, TargetRef: nil,
				Conditions: discoveryv1.EndpointConditions{Ready: new(true)}},
			{Addresses: []string{"203.0.113.2"}, TargetRef: nil,
				Conditions: discoveryv1.EndpointConditions{Ready: new(true)}},
		},
	}

	cs := fake.NewSimpleClientset(slice)
	sub := &endpointSlicesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)

	for _, e := range result.Edges {
		assert.NotEqual(t, kgtypes.EdgeBacks, e.Relationship,
			"slice with no TargetRefs must not produce any BACKS edges; found %+v", e)
	}
	// The slice itself must still be collected, and the Service edge still emitted.
	require.Len(t, result.Resources, 1)
	require.NotNil(t, findEdge(result.Edges,
		"default/Service/external", "default/EndpointSlice/headless", kgtypes.EdgeHasEndpointSlice))
}

// TestEndpointSlicesSubCollector_NoServiceNameLabel covers manually-managed
// slices (no kubernetes.io/service-name label). These must still produce a
// slice resource + BACKS edges for Pod endpoints, but NOT a HAS_ENDPOINT_SLICE
// edge — there is no owning Service to attribute it to.
func TestEndpointSlicesSubCollector_NoServiceNameLabel(t *testing.T) {
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "manual",
			Namespace: "default",
			// Labels intentionally lacking serviceNameLabel.
			Labels: map[string]string{"app.kubernetes.io/managed-by": "custom-operator"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.0.0.5"},
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: "manual-pod", Namespace: "default"},
			Conditions: discoveryv1.EndpointConditions{Ready: new(true)},
		}},
	}

	cs := fake.NewSimpleClientset(slice)
	sub := &endpointSlicesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1, "slice resource must still be emitted even without service-name label")

	for _, e := range result.Edges {
		assert.NotEqual(t, kgtypes.EdgeHasEndpointSlice, e.Relationship,
			"slice without kubernetes.io/service-name must NOT emit HAS_ENDPOINT_SLICE; found %+v", e)
	}

	// BACKS edge for the Pod-kind endpoint must still fire.
	require.NotNil(t, findEdge(result.Edges,
		"default/EndpointSlice/manual", "default/Pod/manual-pod", kgtypes.EdgeBacks),
		"BACKS edge must still be emitted for Pod targetRefs on manually-managed slices")
}

// TestEndpointSlicesSubCollector_ReadinessAggregates verifies the aggregate
// readiness counters on slice metadata, including the Q4 contract that nil
// *bool conditions (legacy/unknown) do NOT count toward any aggregate. Per-
// endpoint detail remains available via Content (full JSON); asserted via
// endpoint_count == len(slice.Endpoints) and Content non-empty.
func TestEndpointSlicesSubCollector_ReadinessAggregates(t *testing.T) {
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mixed-readiness",
			Namespace: "default",
			Labels:    map[string]string{serviceNameLabel: "api"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			// Two ready+serving, one terminating, one all-nil (legacy).
			{Conditions: discoveryv1.EndpointConditions{
				Ready: new(true), Serving: new(true), Terminating: new(false)}},
			{Conditions: discoveryv1.EndpointConditions{
				Ready: new(true), Serving: new(true), Terminating: new(false)}},
			{Conditions: discoveryv1.EndpointConditions{
				Ready: new(false), Serving: new(true), Terminating: new(true)}},
			{Conditions: discoveryv1.EndpointConditions{
				Ready: nil, Serving: nil, Terminating: nil}},
		},
	}

	cs := fake.NewSimpleClientset(slice)
	sub := &endpointSlicesSubCollector{clientset: cs}
	result, err := sub.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, result.Resources, 1)

	res := result.Resources[0]
	assert.Equal(t, "2", res.Metadata["ready_count"], "two endpoints have Ready=true")
	assert.Equal(t, "3", res.Metadata["serving_count"], "three endpoints have Serving=true (nil does not count)")
	assert.Equal(t, "1", res.Metadata["terminating_count"], "one endpoint has Terminating=true")
	assert.Equal(t, "4", res.Metadata["endpoint_count"], "endpoint_count must equal len(slice.Endpoints)")
	assert.Equal(t, "default", res.Metadata["namespace"])
	assert.Equal(t, string(discoveryv1.AddressTypeIPv4), res.Metadata["address_type"])
	assert.NotEmpty(t, res.Content,
		"full slice JSON must be preserved in Content so callers can recover per-endpoint detail")
}
