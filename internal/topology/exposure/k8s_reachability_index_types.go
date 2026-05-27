// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// k8s_reachability_index_types.go holds the per-pod / per-service / per-ingress
// state types that the reachability index materializes from a scoped cloud
// graph. Extracted from k8s_reachability_index.go in Phase 6 Step 3 to keep
// that file under the 300-line soft cap. Behavior lives in
// k8s_reachability_index.go (build) and k8s_reachability_index_services.go
// (Service / Ingress walks).

// reachabilityIndex is the per-account lookup structure the K8s reachability
// analyzer walks during classification. Zero-valued fields mean "not yet
// populated"; the skipped flag short-circuits all downstream processing.
type reachabilityIndex struct {
	// pods maps pod node ID → podInfo. Populated on normal builds; nil
	// when skipped is true.
	pods map[string]*podInfo

	// policies caches NetworkPolicy node references so the classifier can
	// cite policies in its findings without re-querying the scoped graph.
	// Populated on normal builds; nil when skipped is true.
	policies map[string]*knowledgev1.Node

	// services maps Service node ID → serviceInfo. Populated on normal
	// builds from the scoped graph's Service nodes and their outbound
	// SELECTS edges. Phase 4.5 canReachService helper ORs over the
	// backing pods. nil when skipped is true.
	services map[string]*serviceInfo

	// ingresses maps Ingress node ID → ingressInfo. Populated on normal
	// builds from Ingress nodes and their outbound ROUTES_TO edges. Used
	// by the Phase 4.5 Ingress cross-namespace classifier. nil when no
	// Ingress nodes exist in the scoped graph or skipped is true.
	ingresses map[string]*ingressInfo

	// skipped is true when the builder short-circuited because the pod
	// count exceeded reachabilityPodCap. The classifier detects this
	// sentinel and emits exactly one reachability_skipped notice finding.
	skipped bool

	// podCount is the total number of pods seen in the scoped graph. Set
	// on every build so the classifier can surface the count in the
	// skipped finding (and so tests can assert the boundary behavior).
	podCount int

	// reverseAllowedIngress maps src pod ID → set of dst pod IDs that list
	// this src in their AllowedIngressFrom map. Populated by populatePodEdges
	// as a reverse view of the forward allow map. Streaming classifiers
	// consult this in O(1) to answer "does any pod explicitly permit ingress
	// from me?" without walking the whole pods map. nil when skipped.
	reverseAllowedIngress map[string]map[string]struct{}

	// reverseAllowedEgress maps dst pod ID → set of src pod IDs that list
	// this dst in their AllowedEgressTo map. Reverse view of the forward
	// egress allow map. Streaming classifiers consult this in O(1) to
	// answer "does any pod's egress policy reach me?". nil when skipped.
	reverseAllowedEgress map[string]map[string]struct{}
}

// serviceInfo holds the per-service state the Service composition helpers
// consult: the service identity, its namespace, and the list of backing pod
// IDs resolved via SELECTS edges at build time. Backing pod IDs that are not
// present in the index.pods map are filtered during build so the helpers can
// rely on every entry being a known pod.
type serviceInfo struct {
	// ID is the service's node ID — the same key used by index.services.
	ID string

	// Namespace is the service's Kubernetes namespace, read from node
	// metadata. Used by the cross-namespace classifier to decide which
	// source pods are "external".
	Namespace string

	// BackingPods holds the pod node IDs reached via outbound SELECTS
	// edges from this Service. Order is deterministic (sorted by ID) so
	// downstream classifier output stays stable.
	BackingPods []string
}

// ingressInfo holds the per-Ingress state the Ingress composition helpers
// consult: the ingress identity, its namespace, and the list of backing
// service IDs resolved via ROUTES_TO edges at build time. Service IDs that
// are not present in the index.services map are filtered during build so
// helpers can rely on every entry being a known service.
type ingressInfo struct {
	// ID is the ingress's node ID — the same key used by index.ingresses.
	ID string

	// Namespace is the ingress's Kubernetes namespace, read from node
	// metadata. Used by the cross-namespace classifier to decide which
	// source pods are "external".
	Namespace string

	// BackingServices holds the service node IDs reached via outbound
	// ROUTES_TO edges from this Ingress. Sorted by ID for stable output.
	BackingServices []string
}

// podInfo holds the per-pod state the canReach predicate consults: the pod
// identity, its default-deny flags (derived from inbound EdgeRestricts*
// edges), and the per-peer allow maps keyed by peer pod ID with a list of
// (protocol, port range) tuples parsed from Edge.Evidence.
type podInfo struct {
	// ID is the pod's node ID — the same key used by reachabilityIndex.pods.
	ID string

	// Namespace is the pod's Kubernetes namespace, read from node metadata.
	// May be empty for malformed nodes; the classifier tolerates empties.
	Namespace string

	// Labels holds the pod's K8s labels — reserved for Phase 4
	// over-exposure classification. Phase 3 populates the map but does
	// not read from it.
	Labels map[string]string

	// IngressRestricted is true when at least one EdgeRestrictsIngress edge
	// points at this pod from a NetworkPolicy. Default-deny ingress applies.
	IngressRestricted bool

	// EgressRestricted is true when at least one EdgeRestrictsEgress edge
	// points at this pod from a NetworkPolicy. Default-deny egress applies.
	EgressRestricted bool

	// AllowedIngressFrom is keyed by peer pod ID (the source that may reach
	// this pod) with a list of (protocol, port range) tuples — one per
	// EdgeAllowsIngressFrom edge whose dst is this pod. Empty entries (a
	// nil or zero-range port range) mean "all ports / all protocols".
	AllowedIngressFrom map[string][]portRange

	// AllowedEgressTo is keyed by peer pod ID (the destination this pod may
	// reach) with a list of (protocol, port range) tuples — one per
	// EdgeAllowsEgressTo edge whose src is this pod.
	AllowedEgressTo map[string][]portRange

	// ANPIngressFrom is the AdminNetworkPolicy ingress allow/deny/pass map.
	// Keyed by peer pod ID (the source) with a list of (priority, action,
	// port range) entries parsed from edges whose Evidence carries
	// is_anp=true. The canReach dispatch evaluates this map FIRST and falls
	// through to AllowedIngressFrom only when the ANP walk returns Pass or
	// finds no matching entry. See k8s_reachability_anp.go for the priority
	// semantics.
	ANPIngressFrom map[string][]anpRange

	// ANPEgressTo is the AdminNetworkPolicy egress counterpart of
	// ANPIngressFrom. Keyed by peer pod ID (the destination) with a list of
	// ANP entries. canReach evaluates this map FIRST on the egress side.
	ANPEgressTo map[string][]anpRange
}
