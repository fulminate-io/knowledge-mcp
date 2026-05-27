// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"
	"strings"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// serviceNameLabel is the Kubernetes-canonical label EndpointSlices use to
// reference their owning Service. Slices without this label are considered
// manually-managed (no owning Service edge emitted) — see
// https://kubernetes.io/docs/concepts/services-networking/endpoint-slices/.
const serviceNameLabel = "kubernetes.io/service-name"

// endpointSlicesSubCollector lists all EndpointSlices across all namespaces.
// It emits:
//   - one EndpointSlice resource per slice (Content = full JSON for per-endpoint
//     condition detail; metadata carries aggregate ready/serving/terminating
//     counters for inexpensive queries)
//   - one Service → EndpointSlice edge (HAS_ENDPOINT_SLICE) per slice whose
//     kubernetes.io/service-name label is set
//   - one EndpointSlice → Pod edge (BACKS) per endpoint whose TargetRef.Kind
//     is "Pod", with edge metadata carrying the per-endpoint readiness state
//     so downstream consumers can filter without rehydrating Content.
type endpointSlicesSubCollector struct {
	clientset kubernetes.Interface
}

func (s *endpointSlicesSubCollector) Name() string { return "endpointslices" }

func (s *endpointSlicesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	list, err := s.clientset.DiscoveryV1().EndpointSlices("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return cloud.SubCollectorResult{}, fmt.Errorf("list endpointslices: %w", err)
	}

	var result cloud.SubCollectorResult

	for i := range list.Items {
		slice := &list.Items[i]
		id := resourceID(slice.Namespace, "EndpointSlice", slice.Name)

		meta := buildEndpointSliceMeta(slice)

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         slice.Name,
			ResourceType: "EndpointSlice",
			Region:       slice.Namespace,
			Content:      marshalJSON(slice),
			Metadata:     meta,
		})

		result.Edges = append(result.Edges, serviceEndpointSliceEdge(slice, id)...)
		result.Edges = append(result.Edges, sliceBacksPodEdges(slice, id)...)
	}

	return result, nil
}

// buildEndpointSliceMeta builds the metadata map for an EndpointSlice resource:
// labels, namespace, address type, port summary, and readiness aggregates.
func buildEndpointSliceMeta(slice *discoveryv1.EndpointSlice) map[string]string {
	meta := labelsToMeta(slice.Labels)
	meta["namespace"] = slice.Namespace
	meta["address_type"] = string(slice.AddressType)

	// Port summary — same shape as sub_services.go port formatting.
	var ports []string
	for _, p := range slice.Ports {
		proto := ""
		if p.Protocol != nil {
			proto = string(*p.Protocol)
		}
		portNum := int32(0)
		if p.Port != nil {
			portNum = *p.Port
		}
		ports = append(ports, fmt.Sprintf("%s/%d", proto, portNum))
	}
	if len(ports) > 0 {
		meta["ports"] = strings.Join(ports, ",")
	}

	// Aggregate readiness counters. Conditions.* are *bool — nil means
	// "unknown/legacy" and does NOT count toward any of ready/serving/
	// terminating per the design note in the plan.
	var ready, serving, terminating int
	for _, ep := range slice.Endpoints {
		if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
			ready++
		}
		if ep.Conditions.Serving != nil && *ep.Conditions.Serving {
			serving++
		}
		if ep.Conditions.Terminating != nil && *ep.Conditions.Terminating {
			terminating++
		}
	}
	meta["ready_count"] = formatInt(ready)
	meta["serving_count"] = formatInt(serving)
	meta["terminating_count"] = formatInt(terminating)
	meta["endpoint_count"] = formatInt(len(slice.Endpoints))

	return meta
}

// serviceEndpointSliceEdge emits a single Service → EndpointSlice edge if
// the slice carries the kubernetes.io/service-name label. Manually-managed
// slices (no label) return an empty slice and no edge is emitted.
func serviceEndpointSliceEdge(slice *discoveryv1.EndpointSlice, sliceID string) []cloud.EdgeSpec {
	svcName := slice.Labels[serviceNameLabel]
	if svcName == "" {
		return nil
	}
	return []cloud.EdgeSpec{{
		SourceID:     resourceID(slice.Namespace, "Service", svcName),
		TargetID:     sliceID,
		Relationship: kgtypes.EdgeHasEndpointSlice,
	}}
}

// sliceBacksPodEdges emits EndpointSlice → Pod edges (BACKS) for each endpoint
// whose TargetRef.Kind == "Pod". Non-Pod targetRefs (rare, used for manually-
// managed slices pointing at external services) and nil TargetRefs (headless
// IP-only slices) are skipped. Each edge carries the per-endpoint readiness
// state in Metadata so downstream can filter without reading Content.
func sliceBacksPodEdges(slice *discoveryv1.EndpointSlice, sliceID string) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, ep := range slice.Endpoints {
		if ep.TargetRef == nil || ep.TargetRef.Kind != "Pod" {
			continue
		}
		// TargetRef.Namespace may be unset on in-namespace endpoints; fall
		// back to the slice's namespace. Cross-namespace endpoints (rare)
		// honor the explicit ref namespace.
		targetNS := ep.TargetRef.Namespace
		if targetNS == "" {
			targetNS = slice.Namespace
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     sliceID,
			TargetID:     resourceID(targetNS, "Pod", ep.TargetRef.Name),
			Relationship: kgtypes.EdgeBacks,
			Metadata:     endpointConditionMeta(ep.Conditions),
		})
	}
	return edges
}

// endpointConditionMeta serializes EndpointConditions into string-valued
// metadata. nil *bool values are surfaced as "unknown" so consumers can
// distinguish legacy slices from explicit-false. Present-and-true/false
// are surfaced as the canonical "true"/"false" strings.
func endpointConditionMeta(c discoveryv1.EndpointConditions) map[string]string {
	return map[string]string{
		"ready":       boolPtrString(c.Ready),
		"serving":     boolPtrString(c.Serving),
		"terminating": boolPtrString(c.Terminating),
	}
}

// boolPtrString renders a *bool as "true" / "false" / "unknown".
func boolPtrString(b *bool) string {
	if b == nil {
		return "unknown"
	}
	if *b {
		return "true"
	}
	return "false"
}
