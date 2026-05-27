// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"fmt"
	"sort"
)

// k8s_reachability_findings_services.go holds the Phase 4.5 Service and
// Ingress cross-namespace classifiers. Both classifiers compose the Phase 3
// pod→pod reachability predicate with the SELECTS / ROUTES_TO edges cached
// on the reachabilityIndex to answer "can external traffic reach pods via
// this Service / Ingress?" without duplicating selector logic in topology/.
//
// CLASSIFIER CONTRACT. Each classifier emits a Finding per Service / Ingress
// that has at least one pod OUTSIDE its namespace able to reach a backing
// pod via the reachability predicate. A Service whose backing pods are
// fully isolated yields ZERO findings — that's the negative recipe the plan
// criterion pins.
//
// SEVERITY. Service cross-namespace exposure is Warning. Ingress is also
// Warning because the Ingress routing layer is typically a public entry
// point and its composition with intra-cluster policy deserves elevated
// attention. ipBlock/world exposure is still handled by the separate
// k8s_reachability_findings_ipblock.go classifier.

// classifyServiceReachability returns one finding per Service that has at
// least one source pod OUTSIDE its namespace able to reach any backing pod
// via the reachability index. Services with zero backing pods, zero
// cross-namespace sources, or whose backing pods are fully isolated emit
// no findings.
//
// The classifier walks every probe in the probe set and emits a port-less
// finding: a single boolean "at least one external pod can reach the
// service" is the most useful summary at this level — per-port breakdown
// would duplicate the over-exposed classifier's work without adding
// signal. Downstream consumers (unified_public_exposure) call
// canReachService directly when they need per-port precision.
func classifyServiceReachability(index *reachabilityIndex, probes []portProbe) []Finding {
	if index == nil || index.skipped || len(index.services) == 0 || len(index.pods) == 0 {
		return nil
	}
	svcIDs := make([]string, 0, len(index.services))
	for id := range index.services {
		svcIDs = append(svcIDs, id)
	}
	sort.Strings(svcIDs)
	var findings []Finding
	for _, svcID := range svcIDs {
		svc := index.services[svcID]
		if svc == nil || len(svc.BackingPods) == 0 {
			continue
		}
		externalSources := externalReachingPods(index, svc, probes)
		if len(externalSources) == 0 {
			continue
		}
		findings = append(findings, serviceCrossNsFinding(svc, externalSources))
	}
	return findings
}

// externalReachingPods returns the sorted list of pod IDs that (a) live
// outside the service's namespace and (b) can reach at least one backing
// pod on at least one probe. Used by classifyServiceReachability and by
// classifyIngressReachability indirectly via the Ingress → service chain.
func externalReachingPods(index *reachabilityIndex, svc *serviceInfo, probes []portProbe) []string {
	if len(svc.BackingPods) == 0 {
		return nil
	}
	var out []string
	ids := sortedPodIDs(index)
	for _, srcID := range ids {
		srcPod := index.pods[srcID]
		if srcPod == nil {
			continue
		}
		if srcPod.Namespace == svc.Namespace {
			continue
		}
		if !anyProbeReachesService(index, srcID, svc, probes) {
			continue
		}
		out = append(out, srcID)
	}
	return out
}

// anyProbeReachesService reports whether srcID can reach any backing pod of
// svc on any probe in the probe set. Thin wrapper over canReachService that
// iterates probes — kept separate so the outer loop reads cleanly.
func anyProbeReachesService(index *reachabilityIndex, srcID string, svc *serviceInfo, probes []portProbe) bool {
	for _, probe := range probes {
		if index.canReachService(srcID, svc.ID, probe.Protocol, probe.Port) {
			return true
		}
	}
	return false
}

// serviceCrossNsFinding builds the single Finding emitted for a Service
// with at least one external reacher. Evidence lists the service ID first
// followed by every external source pod ID — the sort is stable because
// externalReachingPods returns a sorted slice.
func serviceCrossNsFinding(svc *serviceInfo, externalSources []string) Finding {
	evidence := make([]string, 0, 1+len(externalSources))
	evidence = append(evidence, svc.ID)
	evidence = append(evidence, externalSources...)
	return Finding{
		Algorithm: "k8s_reachability",
		Severity:  SeverityWarning,
		Title:     fmt.Sprintf("Service cross-namespace reachable: %s", serviceDisplay(svc)),
		Summary: fmt.Sprintf(
			"Service %s in namespace %q has %d backing pod(s) and is reachable from %d pod(s) outside its namespace via at least one (protocol, port) combination. Cross-namespace exposure via SELECTS composition.",
			svc.ID, svc.Namespace, len(svc.BackingPods), len(externalSources),
		),
		Evidence: evidence,
		Metrics: map[string]float64{
			"backing_pod_count":     float64(len(svc.BackingPods)),
			"external_source_count": float64(len(externalSources)),
		},
		Metadata: map[string]string{
			"namespace":  svc.Namespace,
			"service_id": svc.ID,
		},
	}
}

// classifyIngressReachability returns one finding per Ingress that routes
// to at least one Service backed by pods reachable from outside the
// Ingress's namespace. Ingress nodes are optional: when the scoped graph
// contains no Ingress resources (index.ingresses == nil) the classifier
// returns nil without walking.
//
// The classifier walks Ingress → Service → Pod via the cached chains on
// the index, so every reachability query still routes through the shared
// canReachService helper — no new K8s-specific logic is introduced.
func classifyIngressReachability(index *reachabilityIndex, probes []portProbe) []Finding {
	if index == nil || index.skipped || len(index.ingresses) == 0 || len(index.pods) == 0 {
		return nil
	}
	ingIDs := make([]string, 0, len(index.ingresses))
	for id := range index.ingresses {
		ingIDs = append(ingIDs, id)
	}
	sort.Strings(ingIDs)
	var findings []Finding
	for _, ingID := range ingIDs {
		ing := index.ingresses[ingID]
		if ing == nil || len(ing.BackingServices) == 0 {
			continue
		}
		externalSources := externalReachingIngressSources(index, ing, probes)
		if len(externalSources) == 0 {
			continue
		}
		findings = append(findings, ingressCrossNsFinding(ing, externalSources))
	}
	return findings
}

// externalReachingIngressSources returns the sorted list of pod IDs that
// (a) live outside the Ingress's namespace and (b) can reach a backing pod
// of at least one of the Ingress's backing services on at least one probe.
func externalReachingIngressSources(index *reachabilityIndex, ing *ingressInfo, probes []portProbe) []string {
	seen := map[string]struct{}{}
	for _, svcID := range ing.BackingServices {
		svc := index.services[svcID]
		if svc == nil {
			continue
		}
		for _, src := range externalReachingPods(index, svc, probes) {
			// Additional filter: the source pod must also be outside the
			// Ingress's namespace (not just the service's). Ingress and
			// service often share a namespace, but not always.
			srcPod := index.pods[src]
			if srcPod == nil || srcPod.Namespace == ing.Namespace {
				continue
			}
			seen[src] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ingressCrossNsFinding builds the single Finding emitted for an Ingress
// with at least one external reacher. Evidence lists the ingress ID first
// followed by every external source pod ID.
func ingressCrossNsFinding(ing *ingressInfo, externalSources []string) Finding {
	evidence := make([]string, 0, 1+len(externalSources))
	evidence = append(evidence, ing.ID)
	evidence = append(evidence, externalSources...)
	return Finding{
		Algorithm: "k8s_reachability",
		Severity:  SeverityWarning,
		Title:     fmt.Sprintf("Ingress cross-namespace reachable: %s", ing.ID),
		Summary: fmt.Sprintf(
			"Ingress %s in namespace %q routes to %d Service(s) whose backing pods are reachable from %d pod(s) outside the Ingress's namespace. Ingress → Service → Pod chain composition.",
			ing.ID, ing.Namespace, len(ing.BackingServices), len(externalSources),
		),
		Evidence: evidence,
		Metrics: map[string]float64{
			"backing_service_count": float64(len(ing.BackingServices)),
			"external_source_count": float64(len(externalSources)),
		},
		Metadata: map[string]string{
			"namespace":  ing.Namespace,
			"ingress_id": ing.ID,
		},
	}
}

// serviceDisplay renders a serviceInfo as "namespace/id" for finding titles,
// falling back to just the ID when the namespace is empty.
func serviceDisplay(svc *serviceInfo) string {
	if svc == nil {
		return ""
	}
	if svc.Namespace == "" {
		return svc.ID
	}
	return svc.Namespace + "/" + svc.ID
}
