// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"fmt"
	"sort"
)

// k8s_reachability_findings_namespace.go holds the namespace-fully-open
// classifier and a small cluster of pod-ID + namespace helpers used by
// every K8s reachability classifier.
//
// WHOLE-NAMESPACE PRECONDITION. A namespace is "fully open" exactly when
// every pod in the namespace is default-allow on both ingress and egress
// AND no pod carries any AdminNetworkPolicy rule that could narrow traffic
// on a specific (protocol, port). If that precondition holds, canReach
// returns true for every intra-namespace ordered pair on every probe — no
// NetworkPolicy narrows any edge, no ANP Allow/Deny/Pass overrides it, so
// the verification walk the earlier implementation performed is redundant.
//
// This check runs in O(|namespace|) with no canReach calls, so it scales
// trivially regardless of namespace size. The earlier quadratic fallback
// (a full pair × probe verification) has been removed as dead code.

// findNamespaceFullyOpen returns a finding for each namespace whose pods are
// all reachable from all other pods in the SAME namespace. A namespace with
// one or zero pods is skipped. Whole-namespace property; no per-probe split —
// the namespaceAllDefaultAllow precondition is both necessary and sufficient,
// so the probe set is not consulted (kept in the signature for dispatcher
// uniformity with the other classifiers).
//
//nolint:unparam // probes kept for dispatcher uniformity; namespaceAllDefaultAllow is sufficient
func findNamespaceFullyOpen(index *reachabilityIndex, _ []portProbe) []Finding {
	if index == nil || len(index.pods) < 2 {
		return nil
	}
	ids := sortedPodIDs(index)
	peers := buildNamespacePeers(index, ids)
	namespaces := make([]string, 0, len(peers))
	for ns := range peers {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	var findings []Finding
	for _, ns := range namespaces {
		nsPods := peers[ns]
		if len(nsPods) < 2 {
			continue
		}
		if !namespaceAllDefaultAllow(index, nsPods) {
			continue
		}
		primary := nsPods[0]
		findings = append(findings, Finding{
			Algorithm: "k8s_reachability",
			Severity:  SeverityWarning,
			Title:     fmt.Sprintf("Namespace fully open: %s", ns),
			Summary: fmt.Sprintf(
				"Namespace %q contains %d pods and every pod can reach every other pod on at least one (protocol, port) combination. No NetworkPolicy narrows the intra-namespace surface.",
				ns, len(nsPods),
			),
			Evidence: []string{primary},
			Metrics:  map[string]float64{"pod_count": float64(len(nsPods))},
			Metadata: map[string]string{"namespace": ns},
		})
	}
	return findings
}

// namespaceAllDefaultAllow reports whether every pod in the namespace is
// default-allow on both ingress and egress AND free of any ANP rule that
// could narrow traffic. This is the necessary AND sufficient precondition
// for the "fully open" classification:
//
//   - A pod flagged IngressRestricted / EgressRestricted is selected by a
//     regular NetworkPolicy → at least one edge is narrowed.
//   - A pod carrying any ANP entry may have an Allow/Deny/Pass rule that
//     narrows a specific (protocol, port) even without the regular flags.
//
// If both conditions hold for every pod in the namespace, canReach returns
// true for every intra-namespace ordered pair on every probe, so the
// namespace is trivially fully open. No per-pair walk is required.
func namespaceAllDefaultAllow(index *reachabilityIndex, nsPods []string) bool {
	for _, id := range nsPods {
		pod := index.pods[id]
		if pod == nil {
			continue
		}
		if pod.IngressRestricted || pod.EgressRestricted {
			return false
		}
		if len(pod.ANPIngressFrom) > 0 || len(pod.ANPEgressTo) > 0 {
			return false
		}
	}
	return true
}

// sortedPodIDs returns every pod ID in the index sorted lexicographically.
// Callers depend on the stable order for deterministic finding emission.
func sortedPodIDs(index *reachabilityIndex) []string {
	ids := make([]string, 0, len(index.pods))
	for id := range index.pods {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// buildNamespacePeers groups pod IDs by namespace. Returns a map from
// namespace → sorted pod-ID slice.
func buildNamespacePeers(index *reachabilityIndex, ids []string) map[string][]string {
	out := map[string][]string{}
	for _, id := range ids {
		pod := index.pods[id]
		if pod == nil {
			continue
		}
		out[pod.Namespace] = append(out[pod.Namespace], id)
	}
	for ns := range out {
		sort.Strings(out[ns])
	}
	return out
}
