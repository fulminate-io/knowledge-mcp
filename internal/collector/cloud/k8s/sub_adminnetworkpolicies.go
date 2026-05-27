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
)

// sub_adminnetworkpolicies.go is the collector glue for the (Baseline)AdminNetworkPolicy
// API from policy.networking.k8s.io/v1alpha1. This is the "collector-follow" half of
// Phase 5.5 — postpopulate_netpol_anp.go already consumes cloud nodes whose
// resource_type is "AdminNetworkPolicy" or "BaselineAdminNetworkPolicy" and parses
// their Content as the anpSpec shape. This subcollector produces exactly those
// nodes by listing the CRs via the dynamic client so we avoid taking a direct
// build-time dep on sigs.k8s.io/network-policy-api (keeps go.mod small and
// insulates the collector from future schema changes in the typed module).
//
// The ANP API is still alpha in upstream K8s (v1.32+) — the CRDs are typically
// not installed, so the common case is a NotFound / NoResourceMatch response.
// We treat that as "no ANP resources collected" and return empty, with a debug
// log for operators that expected coverage.

// adminNetworkPolicyGVR is the GroupVersionResource for AdminNetworkPolicy —
// a cluster-scoped policy.networking.k8s.io/v1alpha1 CR.
var adminNetworkPolicyGVR = schema.GroupVersionResource{
	Group:    "policy.networking.k8s.io",
	Version:  "v1alpha1",
	Resource: "adminnetworkpolicies",
}

// baselineAdminNetworkPolicyGVR is the GVR for BaselineAdminNetworkPolicy — a
// cluster-scoped singleton that sets the default fallback network policy.
var baselineAdminNetworkPolicyGVR = schema.GroupVersionResource{
	Group:    "policy.networking.k8s.io",
	Version:  "v1alpha1",
	Resource: "baselineadminnetworkpolicies",
}

// anpCollectTarget pairs an ANP GVR with the ResourceType string the
// postpopulate layer queries for. Keeping the mapping in one place makes it
// trivial to wire additional ANP variants (e.g. a future v1beta1) without
// duplicating List / parse logic.
type anpCollectTarget struct {
	gvr          schema.GroupVersionResource
	resourceType string // matches postpopulate_netpol_anp.go Meta("resource_type", ...) query
	kind         string // ID kind segment, e.g. "AdminNetworkPolicy"
}

// anpCollectTargets is the full set of ANP-family resources this subcollector
// enumerates. Order matters only for deterministic test output.
var anpCollectTargets = []anpCollectTarget{
	{gvr: adminNetworkPolicyGVR, resourceType: "AdminNetworkPolicy", kind: "AdminNetworkPolicy"},
	{gvr: baselineAdminNetworkPolicyGVR, resourceType: "BaselineAdminNetworkPolicy", kind: "BaselineAdminNetworkPolicy"},
}

// adminNetworkPoliciesSubCollector lists AdminNetworkPolicy and
// BaselineAdminNetworkPolicy resources via the dynamic client. Both are
// cluster-scoped so there is no namespace loop — a single unscoped List per
// GVR collects everything. The CRDs are typically absent on clusters that
// don't have the network-policy-api component installed; that case is handled
// gracefully by skipping NotFound / NoResourceMatch errors.
type adminNetworkPoliciesSubCollector struct {
	dynamicClient dynamic.Interface
}

// Name returns the subcollector registration key used by the SubCollector
// orchestrator. "adminnetworkpolicies" groups both ANP variants since they
// share the same API group and are collected in a single pass.
func (s *adminNetworkPoliciesSubCollector) Name() string { return "adminnetworkpolicies" }

// Collect lists every (Baseline)AdminNetworkPolicy in the cluster. Missing
// CRDs / API groups are handled gracefully — the common case for real clusters.
func (s *adminNetworkPoliciesSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	for _, target := range anpCollectTargets {
		resources, err := s.listTarget(ctx, target)
		if err != nil {
			// Degrade gracefully when the CRD is not installed — the typical
			// case today. Other errors propagate via the aggregator so operators
			// see real permission / networking failures.
			if isANPAPIMissing(err) {
				slog.Debug("adminnetworkpolicies: API not installed, skipping",
					"gvr", target.gvr.String(), "err", err)
				continue
			}
			return result, fmt.Errorf("list %s: %w", target.gvr.Resource, err)
		}
		result.Resources = append(result.Resources, resources...)
	}

	return result, nil
}

// listTarget enumerates a single ANP variant and converts each item into the
// ResourceSpec shape postpopulate_netpol_anp.go expects (resource_type match +
// full spec in Content).
func (s *adminNetworkPoliciesSubCollector) listTarget(
	ctx context.Context,
	target anpCollectTarget,
) ([]cloud.ResourceSpec, error) {
	// Cluster-scoped — no Namespace("") call (dynamic.ResourceInterface is
	// embedded in NamespaceableResourceInterface, so Resource(gvr).List() is
	// the right path for cluster resources).
	list, err := s.dynamicClient.Resource(target.gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var resources []cloud.ResourceSpec
	for i := range list.Items {
		item := &list.Items[i]
		spec := buildANPResourceSpec(item, target)
		if spec.ID == "" {
			continue
		}
		resources = append(resources, spec)
	}
	return resources, nil
}

// buildANPResourceSpec converts a raw unstructured ANP CR into a ResourceSpec.
// Content is the full JSON body of the object — postpopulate_netpol_anp.go
// unmarshals that into anpSpec which reads the top-level "spec" field, so the
// marshaled object layout must preserve the spec key verbatim (unstructured
// always does — item.Object is a map keyed by the original field names).
func buildANPResourceSpec(item *unstructured.Unstructured, target anpCollectTarget) cloud.ResourceSpec {
	name := item.GetName()
	if name == "" {
		return cloud.ResourceSpec{}
	}

	id := resourceID("", target.kind, name)

	content, err := json.Marshal(item.Object)
	if err != nil {
		// Degrade gracefully — skip rather than fail the whole pass.
		slog.Debug("adminnetworkpolicies: marshal content failed",
			"name", name, "err", err)
		return cloud.ResourceSpec{}
	}

	meta := labelsToMeta(item.GetLabels())
	meta["api_version"] = item.GetAPIVersion()
	if priority, ok := extractANPPriority(item.Object); ok {
		meta["priority"] = formatInt(priority)
	}

	return cloud.ResourceSpec{
		ID:           id,
		Name:         name,
		ResourceType: target.resourceType,
		Content:      content,
		Metadata:     meta,
	}
}

// extractANPPriority pulls spec.priority from the raw object map. Exposed as
// metadata for discoverability (the authoritative copy lives in Content and is
// re-parsed by the postpopulate layer). Returns false if the field is missing
// or not an integer, which is valid for BaselineAdminNetworkPolicy.
func extractANPPriority(obj map[string]any) (int, bool) {
	priority, ok, err := unstructured.NestedInt64(obj, "spec", "priority")
	if err != nil || !ok {
		return 0, false
	}
	return int(priority), true
}

// isANPAPIMissing returns true when the error indicates the ANP API isn't
// installed on the cluster. Two shapes: NotFound (404) from the API server
// and NoResourceMatch / NoKindMatch from the REST mapper when the discovery
// cache hasn't seen the GroupVersion. Both are expected on clusters without
// the network-policy-api component.
func isANPAPIMissing(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsNotFound(err) {
		return true
	}
	if meta.IsNoMatchError(err) {
		return true
	}
	return false
}
