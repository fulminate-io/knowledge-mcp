// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

var ( // Gateway API GVRs — gateway.networking.k8s.io/v1.
	gatewayClassGVR = schema.GroupVersionResource{
		Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses",
	}
	gatewayGVR = schema.GroupVersionResource{
		Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways",
	}
	httpRouteGVR = schema.GroupVersionResource{
		Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes",
	}
	grpcRouteGVR = schema.GroupVersionResource{
		Group: "gateway.networking.k8s.io", Version: "v1", Resource: "grpcroutes",
	}
)

// gatewayAPISubCollector lists Gateway API resources (GatewayClass, Gateway,
// HTTPRoute, GRPCRoute) via the dynamic client. Gateway API CRDs are not
// installed on every cluster, so missing API errors are handled gracefully.
type gatewayAPISubCollector struct {
	dynamicClient dynamic.Interface
}

func (s *gatewayAPISubCollector) Name() string { return "gateway-api" }

func (s *gatewayAPISubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	// Probe: try listing GatewayClasses to check if Gateway API is installed.
	if !s.apiAvailable(ctx) {
		slog.Debug("gateway-api: CRDs not installed, skipping")
		return cloud.SubCollectorResult{}, nil
	}

	var result cloud.SubCollectorResult

	if err := s.collectGatewayClasses(ctx, &result); err != nil {
		return result, err
	}
	if err := s.collectGateways(ctx, &result); err != nil {
		return result, err
	}
	if err := s.collectRoutes(ctx, &result, httpRouteGVR, "HTTPRoute"); err != nil {
		return result, err
	}
	// GRPCRoute uses the same backendRefs structure as HTTPRoute.
	if err := s.collectRoutes(ctx, &result, grpcRouteGVR, "GRPCRoute"); err != nil {
		// GRPCRoute may not be available even when HTTPRoute is — degrade gracefully.
		if isGatewayAPIMissing(err) {
			slog.Debug("gateway-api: GRPCRoute CRD not installed, skipping")
		} else {
			return result, err
		}
	}

	return result, nil
}

// apiAvailable probes GatewayClass to determine if Gateway API CRDs are installed.
func (s *gatewayAPISubCollector) apiAvailable(ctx context.Context) (available bool) {
	// The fake dynamic client panics for unregistered GVRs instead of returning
	// an error. Recover gracefully so tests and clusters without Gateway API work.
	defer func() {
		if r := recover(); r != nil {
			available = false
		}
	}()
	_, err := s.dynamicClient.Resource(gatewayClassGVR).List(ctx, metav1.ListOptions{Limit: 1})
	return !isGatewayAPIMissing(err)
}

// collectGatewayClasses lists cluster-scoped GatewayClass resources.
func (s *gatewayAPISubCollector) collectGatewayClasses(ctx context.Context, result *cloud.SubCollectorResult) error {
	list, err := s.dynamicClient.Resource(gatewayClassGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list gatewayclasses: %w", err)
	}

	for i := range list.Items {
		item := &list.Items[i]
		name := item.GetName()
		id := resourceID("", "GatewayClass", name)

		meta := labelsToMeta(item.GetLabels())
		meta["api_version"] = item.GetAPIVersion()

		controllerName, _, _ := unstructured.NestedString(item.Object, "spec", "controllerName")
		if controllerName != "" {
			meta["controller_name"] = controllerName
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         name,
			ResourceType: "GatewayClass",
			Content:      marshalUnstructured(item),
			Metadata:     meta,
		})
	}
	return nil
}

// collectGateways lists namespace-scoped Gateway resources and emits edges
// to their GatewayClass.
func (s *gatewayAPISubCollector) collectGateways(ctx context.Context, result *cloud.SubCollectorResult) error {
	list, err := s.dynamicClient.Resource(gatewayGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list gateways: %w", err)
	}

	for i := range list.Items {
		item := &list.Items[i]
		ns := item.GetNamespace()
		name := item.GetName()
		id := resourceID(ns, "Gateway", name)

		m := labelsToMeta(item.GetLabels())
		m["namespace"] = ns
		m["api_version"] = item.GetAPIVersion()

		className, _, _ := unstructured.NestedString(item.Object, "spec", "gatewayClassName")
		if className != "" {
			m["gateway_class"] = className
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     id,
				TargetID:     resourceID("", "GatewayClass", className),
				Relationship: kgtypes.EdgeRoutesTo,
			})
		}

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         name,
			ResourceType: "Gateway",
			Region:       ns,
			Content:      marshalUnstructured(item),
			Metadata:     m,
		})
	}
	return nil
}

// collectRoutes lists namespace-scoped route resources (HTTPRoute, GRPCRoute)
// and emits ROUTES_TO edges to Service backends and parent Gateway references.
func (s *gatewayAPISubCollector) collectRoutes(
	ctx context.Context, result *cloud.SubCollectorResult,
	gvr schema.GroupVersionResource, kind string,
) error {
	list, err := s.dynamicClient.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	for i := range list.Items {
		item := &list.Items[i]
		ns := item.GetNamespace()
		name := item.GetName()
		id := resourceID(ns, kind, name)

		m := labelsToMeta(item.GetLabels())
		m["namespace"] = ns
		m["api_version"] = item.GetAPIVersion()

		result.Resources = append(result.Resources, cloud.ResourceSpec{
			ID:           id,
			Name:         name,
			ResourceType: kind,
			Region:       ns,
			Content:      marshalUnstructured(item),
			Metadata:     m,
		})

		result.Edges = append(result.Edges, extractRouteBackendEdges(id, ns, item.Object)...)
		result.Edges = append(result.Edges, extractRouteParentEdges(id, ns, item.Object)...)
	}
	return nil
}

// extractRouteBackendEdges extracts ROUTES_TO edges from spec.rules[].backendRefs[].
func extractRouteBackendEdges(routeID, namespace string, obj map[string]any) []cloud.EdgeSpec {
	rules, ok, _ := unstructured.NestedSlice(obj, "spec", "rules")
	if !ok {
		return nil
	}

	var edges []cloud.EdgeSpec
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]any)
		if !ok {
			continue
		}
		refs, ok := ruleMap["backendRefs"].([]any)
		if !ok {
			continue
		}
		for _, ref := range refs {
			refMap, ok := ref.(map[string]any)
			if !ok {
				continue
			}
			edges = append(edges, backendRefEdge(routeID, namespace, refMap)...)
		}
	}
	return edges
}

// backendRefEdge creates a ROUTES_TO edge from a single backendRef to a Service.
func backendRefEdge(routeID, defaultNS string, ref map[string]any) []cloud.EdgeSpec {
	kind, _ := ref["kind"].(string)
	if kind == "" {
		kind = "Service" // default per Gateway API spec
	}
	if kind != "Service" {
		return nil // only emit edges to Services
	}

	name, _ := ref["name"].(string)
	if name == "" {
		return nil
	}

	ns, _ := ref["namespace"].(string)
	if ns == "" {
		ns = defaultNS
	}

	return []cloud.EdgeSpec{{
		SourceID:     routeID,
		TargetID:     resourceID(ns, "Service", name),
		Relationship: kgtypes.EdgeRoutesTo,
	}}
}

// extractRouteParentEdges extracts ROUTES_TO edges from spec.parentRefs[] to Gateways.
func extractRouteParentEdges(routeID, namespace string, obj map[string]any) []cloud.EdgeSpec {
	refs, ok, _ := unstructured.NestedSlice(obj, "spec", "parentRefs")
	if !ok {
		return nil
	}

	var edges []cloud.EdgeSpec
	for _, ref := range refs {
		refMap, ok := ref.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := refMap["kind"].(string)
		if kind == "" {
			kind = "Gateway" // default
		}
		if kind != "Gateway" {
			continue
		}
		name, _ := refMap["name"].(string)
		if name == "" {
			continue
		}
		ns, _ := refMap["namespace"].(string)
		if ns == "" {
			ns = namespace
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     routeID,
			TargetID:     resourceID(ns, "Gateway", name),
			Relationship: kgtypes.EdgeRoutesTo,
		})
	}
	return edges
}

func marshalUnstructured(item *unstructured.Unstructured) []byte {
	b, err := json.Marshal(item.Object)
	if err != nil {
		return nil
	}
	return b
}

// isGatewayAPIMissing returns true when the Gateway API CRDs are not installed.
func isGatewayAPIMissing(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsNotFound(err) {
		return true
	}
	return meta.IsNoMatchError(err)
}
