// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// postPopulate resolves label-selector-based edges after all nodes are written
// to the graph. This runs as the CollectResult.PostPopulate hook.
//
// It creates:
//   - SELECTS edges from Services to matching Pods (via service selector)
//   - RESTRICTS edges from NetworkPolicies to matching Pods (via podSelector)
//   - ALLOWS_INGRESS_FROM / ALLOWS_EGRESS_TO directional reachability edges
//     between pods based on NetworkPolicy ingress/egress peer selectors
func postPopulate(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	// Load all Pods keyed by namespace+labels for selector matching and
	// all Namespace label maps keyed by namespace name (needed for
	// NetworkPolicy namespaceSelector resolution).
	podIndex, nsLabelIndex, err := buildPodIndex(ctx, gc, graphName)
	if err != nil {
		return err
	}

	// Build a pod-id → container-ports index so NetworkPolicy rules that
	// reference named ports can be resolved at edge-emission time.
	podPortIndex, err := buildPodPortIndex(ctx, gc, graphName)
	if err != nil {
		return err
	}

	// Structural namespace membership runs BEFORE selector resolvers: it
	// has no dependency on the pod index and its IN_NAMESPACE edges are
	// pure (namespaced_resource → Namespace) with no metadata, so ordering
	// is independent of downstream selector resolution.
	if err := resolveNamespaceMembership(ctx, gc, graphName); err != nil {
		return err
	}

	// Cluster linkage runs after namespace membership but before selector
	// resolvers: it's independent of the pod index (queries all
	// NodeCloudResource nodes) and the proxy it creates has no effect on
	// downstream SELECTS/RESTRICTS matching. A silent no-op for non-GKE
	// graphs keeps EKS/AKS/bare-kubeconfig flows unchanged.
	if err := resolveClusterLinkage(ctx, gc, graphName); err != nil {
		return err
	}

	// Node → VM cross-graph linkage. Runs AFTER resolveClusterLinkage
	// (OQ2 decision) so cluster-level proxies land first; it is
	// independent otherwise — queries only NodeCloudResource nodes with
	// resource_type=Node and emits BACKED_BY_VM edges to per-provider
	// compute-instance proxies. Silent no-op when there are no Node
	// resources (non-K8s graphs) or no recognizable providerIDs.
	if err := resolveNodeVMLinkage(ctx, gc, graphName); err != nil {
		return err
	}

	// Service → cloud LB cross-graph linkage. Emits EXPOSED_BY edges from LoadBalancer Services
	// whose Status.LoadBalancer.Ingress resolves to a GCP forwardingRule
	// (by IP) or AWS ELB (by Hostname). Silent no-op when no cloud graphs
	// are loaded. Runs before workload/external resolvers to match the
	// cross-graph-first → intra-cluster-second ordering of the other
	// linkage resolvers (cluster + VM); the edges it emits target proxies
	// in a disjoint ID namespace so ordering is functionally independent.
	if err := resolveServiceCloudLBLinkage(ctx, gc, graphName); err != nil {
		return err
	}

	// Ingress → cloud LB cross-graph linkage (Phase 4 of the same plan).
	// Emits EXPOSED_BY edges from Ingress resources whose
	// Status.LoadBalancer.Ingress[] address resolves to a GCP forwardingRule
	// (by IP) or AWS ELB (by Hostname). Same shape as the Service resolver
	// minus the Spec.Type filter — if Status.LoadBalancer is populated, the
	// controller already realized the LB. Entry-point only: the intra-cloud
	// chain (forwardingRule → targetHttpsProxy → urlMap → backendService)
	// is already wired by the GCP collector and reachable via BFS.
	if err := resolveIngressCloudLBLinkage(ctx, gc, graphName); err != nil {
		return err
	}

	// Gateway → cloud LB cross-graph linkage (Phase 5 of the same plan).
	// Emits EXPOSED_BY edges from Gateway (gateway.networking.k8s.io/v1)
	// resources whose Status.Addresses[] resolves to a GCP forwardingRule
	// (Type=IPAddress) or AWS ELB (Type=Hostname). Implementation-specific
	// address Types are silently skipped. HTTPRoute/GRPCRoute → Gateway
	// ROUTES_TO edges are already emitted by sub_gatewayapi.go, so a 2-hop
	// traversal from a route surfaces the cloud LB once this edge is in
	// place — no Route-specific resolver needed.
	if err := resolveGatewayCloudLBLinkage(ctx, gc, graphName); err != nil {
		return err
	}

	// Workload → external cloud resource edges. Three resolvers in
	// deterministic → heuristic order:
	//  1. resolveWorkloadIdentity — reads typed SA annotations to emit
	//     ASSUMES_IDENTITY edges. Most deterministic; no false positives.
	//  2. resolvePVDiskLinkage — reads PV disk metadata (pre-extracted
	//     by sub_persistentvolumes.go) to emit USES_DISK edges. Typed
	//     source, no heuristics.
	//  3. resolveExternalConnections — scans container env values for
	//     cloud URIs and emits CONNECTS_TO edges. Heuristic; runs last
	//     so typed edges land first and env matches don't shadow them.
	if err := resolveWorkloadIdentity(ctx, gc, graphName); err != nil {
		return err
	}
	if err := resolvePVDiskLinkage(ctx, gc, graphName); err != nil {
		return err
	}
	if err := resolveExternalConnections(ctx, gc, graphName); err != nil {
		return err
	}

	if err := resolveServiceSelectors(ctx, gc, graphName, podIndex); err != nil {
		return err
	}

	if err := resolveNetworkPolicySelectors(ctx, gc, graphName, podIndex); err != nil {
		return err
	}

	if err := resolveNetworkPolicyReachabilityEdges(ctx, gc, graphName, podIndex, nsLabelIndex, podPortIndex); err != nil {
		return err
	}

	// AdminNetworkPolicy edges run AFTER regular NetworkPolicy resolution so
	// the topology analyzer can layer ANP priority semantics on top of the
	// existing default-deny / allow edges. The collector does not currently
	// populate ANP nodes (see postpopulate_netpol_anp.go for the gap note);
	// this call is a no-op until a future subcollector lands. Tests build
	// ANP nodes directly to exercise the resolution path.
	if err := resolveANPReachabilityEdges(ctx, gc, graphName, podIndex, nsLabelIndex, podPortIndex); err != nil {
		return err
	}

	// PDB selector resolution previously emitted RESTRICTS edges, but PDBs
	// are eviction constraints, not network restrictions — no consumer ever
	// read them, and reusing RESTRICTS_INGRESS/EGRESS would mis-state the
	// semantics. Dropped pre-launch. Add a
	// dedicated edge type if PDB-aware reachability becomes a feature.

	// Image lineage: match workload container images against known registries
	// (ECR, ACR, Artifact Registry) and emit USES_IMAGE edges. Fail-fast like
	// every other resolver in this orchestrator — a graph-query/write failure
	// here is the same severity as one in resolveServiceSelectors etc.
	if err := resolveImageLineage(ctx, gc, graphName); err != nil {
		return err
	}

	return nil
}

// podEntry holds the namespace, labels, and ID of a Pod node for selector matching.
type podEntry struct {
	id        string
	namespace string
	labels    map[string]string
}

// buildPodIndex queries all Pod and Namespace nodes and indexes them for
// label selector matching. It returns:
//   - pods: slice of podEntry (id + namespace + labels) for every Pod node
//   - nsLabels: map from namespace name → labels map; namespaces with no
//     labels are present with an empty (non-nil) map so selector matchers
//     can distinguish "unlabeled" from "missing"
func buildPodIndex(ctx context.Context, gc postpopulate.GraphCaller, graphName string) ([]podEntry, map[string]map[string]string, error) {
	podNodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("Pod"))
	if err != nil {
		return nil, nil, err
	}

	pods := make([]podEntry, 0, len(podNodes))
	for _, node := range podNodes {
		labels := extractLabels(node.Metadata)
		pods = append(pods, podEntry{
			id:        node.Id,
			namespace: kgtypes.Value(node, "namespace"),
			labels:    labels,
		})
	}

	nsNodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("Namespace"))
	if err != nil {
		return nil, nil, err
	}

	nsLabels := make(map[string]map[string]string, len(nsNodes))
	for _, node := range nsNodes {
		// Namespace node IDs use resourceID("", "Namespace", name) = "Namespace/<name>".
		// The actual namespace name is the SymbolName (set by labelsToMeta caller chain)
		// — but to stay resilient we fall back to stripping the "Namespace/" prefix.
		name := node.SymbolName
		if name == "" {
			const prefix = "Namespace/"
			if len(node.Id) > len(prefix) && node.Id[:len(prefix)] == prefix {
				name = node.Id[len(prefix):]
			}
		}
		if name == "" {
			continue
		}
		nsLabels[name] = extractLabels(node.Metadata)
	}

	return pods, nsLabels, nil
}

// k8sResourceQuery is the BrowseNodes filter for a K8s cloud node by its
// resource_type (Pod / Namespace / Service / NetworkPolicy / ...).
func k8sResourceQuery(resourceType string) map[string]any {
	return map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": resourceType},
		"limit": 0,
	}
}

// extractLabels pulls all "label/<key>" entries out of a node metadata map
// and returns them as a plain key→value map. The returned map is never nil
// so callers can feed it directly into apimachinery label selectors.
func extractLabels(meta map[string]string) map[string]string {
	labels := make(map[string]string)
	for k, v := range meta {
		if len(k) > 6 && k[:6] == "label/" {
			labels[k[6:]] = v
		}
	}
	return labels
}

// resolveServiceSelectors creates SELECTS edges from Services to matching Pods.
// All matching edges accumulate into a single slice and ride ONE LinkEdgesBatch
// — no per-pod Link inside the loop (the batched-write requirement).
func resolveServiceSelectors(ctx context.Context, gc postpopulate.GraphCaller, graphName string, pods []podEntry) error {
	services, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("Service"))
	if err != nil {
		return err
	}

	var edges []knowledgev1.Edge
	for _, node := range services {
		selectorJSON := kgtypes.Value(node, "selector")
		if selectorJSON == "" {
			continue
		}

		var selector map[string]string
		if err := json.Unmarshal([]byte(selectorJSON), &selector); err != nil {
			continue
		}
		if len(selector) == 0 {
			continue
		}

		namespace := kgtypes.Value(node, "namespace")

		for _, pod := range pods {
			if pod.namespace != namespace {
				continue
			}
			if matchesSelector(pod.labels, selector) {
				edges = append(edges, knowledgev1.Edge{FromId: node.Id, ToId: pod.id, Type: string(kgtypes.EdgeSelects)})
			}
		}
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, edges); err != nil {
		return err
	}
	if len(edges) > 0 {
		slog.Debug("postPopulate: created SELECTS edges", "count", len(edges))
	}
	return nil
}

// resolveNetworkPolicySelectors creates per-direction RESTRICTS edges from
// NetworkPolicies to matching Pods. Direction is taken from the policy's
// policy_types metadata (comma-separated "Ingress"/"Egress"). Per K8s
// semantics ingress is the default when policy_types is unset.
//
// The reachability analyzer reads EdgeRestrictsIngress / EdgeRestrictsEgress
// to populate podInfo.IngressRestricted / EgressRestricted; the generic
// EdgeRestricts had no consumer and is gone.
func resolveNetworkPolicySelectors(ctx context.Context, gc postpopulate.GraphCaller, graphName string, pods []podEntry) error {
	policies, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("NetworkPolicy"))
	if err != nil {
		return err
	}

	// Accumulate every RESTRICTS_{INGRESS,EGRESS} edge across all policies and
	// all matching pods, then emit ONE LinkEdgesBatch — no per-pod Link inside
	// the loop (the batched-write requirement).
	var edges []knowledgev1.Edge
	for _, node := range policies {
		selectorJSON := kgtypes.Value(node, "pod_selector")
		if selectorJSON == "" {
			continue
		}

		var selector map[string]string
		if err := json.Unmarshal([]byte(selectorJSON), &selector); err != nil {
			continue
		}
		// Empty selector means "select all pods in namespace".
		namespace := kgtypes.Value(node, "namespace")
		ingress, egress := policyDirectionsFromMeta(kgtypes.Value(node, "policy_types"))
		edges = append(edges, podSelectorEdges(node.Id, namespace, selector, ingress, egress, pods)...)
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, edges); err != nil {
		return err
	}
	if len(edges) > 0 {
		slog.Debug("postPopulate: created RESTRICTS_{INGRESS,EGRESS} edges", "count", len(edges))
	}
	return nil
}

// policyDirectionsFromMeta parses the policy_types metadata value (comma-
// separated "Ingress"/"Egress" tokens emitted by sub_networkpolicies.go).
// When unset, K8s semantics imply ingress only at this layer — egress
// implicit-default needs spec.egress non-emptiness which lives in Content;
// effectivePolicyTypes (postpopulate_netpol.go) already handles that side.
func policyDirectionsFromMeta(raw string) (ingress, egress bool) {
	if raw == "" {
		return true, false
	}
	for part := range strings.SplitSeq(raw, ",") {
		switch strings.TrimSpace(part) {
		case "Ingress":
			ingress = true
		case "Egress":
			egress = true
		}
	}
	return ingress, egress
}

// podSelectorEdges returns one RESTRICTS_INGRESS and/or one RESTRICTS_EGRESS
// edge per matching pod in the policy's namespace. PURE — it accumulates into a
// returned slice so the caller can batch all policies' edges into ONE
// LinkEdgesBatch (no per-pod Link RPC inside the loop).
func podSelectorEdges(
	policyID, namespace string,
	selector map[string]string,
	ingress, egress bool,
	pods []podEntry,
) []knowledgev1.Edge {
	var out []knowledgev1.Edge
	for _, pod := range pods {
		if pod.namespace != namespace {
			continue
		}
		if !matchesSelector(pod.labels, selector) {
			continue
		}
		if ingress {
			out = append(out, knowledgev1.Edge{FromId: policyID, ToId: pod.id, Type: string(kgtypes.EdgeRestrictsIngress)})
		}
		if egress {
			out = append(out, knowledgev1.Edge{FromId: policyID, ToId: pod.id, Type: string(kgtypes.EdgeRestrictsEgress)})
		}
	}
	return out
}

// matchesSelector returns true if the given labels satisfy all selector requirements.
// An empty selector matches all labels.
func matchesSelector(labels, selector map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
