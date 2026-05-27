// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"fmt"
	"sort"
)

// k8s_reachability_findings_rules.go holds the three pod-centric
// sub-classifiers: findIsolatedPods, findOverExposedPods, and
// findAsymmetricReachability. Separated from k8s_reachability_findings.go to
// keep individual files under the 300-line soft cap; shared helpers
// (sortedPodIDs, buildNamespacePeers, probeLabel, probeMetadata) live in the
// main findings file.
//
// STREAMING CLASSIFIERS. These three classifiers all scale linearly with the
// number of allow edges — not quadratically with the number of pod pairs —
// by iterating each pod's own allow maps (populated by populatePodEdges)
// rather than brute-forcing every pair through canReach. Reverse-allow
// lookups on the index answer "is there any src that permits reaching me?"
// in O(1). See k8s_reachability_findings_streaming.go for the shared
// streaming helpers.

// findIsolatedPods returns a finding for each pod that can neither reach nor
// be reached by any other pod in the index across the full probe set. A pod
// that is not ingress-restricted (or not egress-restricted) always reaches
// and is reachable from its peers by default-allow semantics, so it is
// excluded before the per-peer walk runs. Isolation is a whole-pod property
// — port-less finding is always emitted here (no per-probe breakdown).
//
// Streaming implementation: for each fully-restricted pod, the only (other,
// probe) candidates that could reach or be reached are the pods listed in
// the pod's own AllowedEgressTo / AllowedIngressFrom maps. Pods not listed
// are blocked by default-deny regardless of the other side's default-allow
// status — so we never need to touch the full pod-pair matrix.
func findIsolatedPods(index *reachabilityIndex, probes []portProbe) []Finding {
	if index == nil || len(index.pods) < 2 {
		return nil
	}
	ids := sortedPodIDs(index)
	var findings []Finding
	for _, id := range ids {
		pod := index.pods[id]
		if pod == nil {
			continue
		}
		if !pod.IngressRestricted || !pod.EgressRestricted {
			continue
		}
		if !streamingIsolated(index, id, pod, probes) {
			continue
		}
		findings = append(findings, Finding{
			Algorithm: "k8s_reachability",
			Severity:  SeverityInfo,
			Title:     fmt.Sprintf("Pod fully isolated: %s", podDisplay(pod)),
			Summary: fmt.Sprintf(
				"Pod %s in namespace %q cannot reach any other pod and no other pod can reach it across the observed (protocol, port) set. Default-deny ingress and egress apply; no allow rule permits either direction.",
				id, pod.Namespace,
			),
			Evidence: []string{id},
			Metrics: map[string]float64{
				"probe_count": float64(len(probes)),
			},
		})
	}
	return findings
}

// findOverExposedPods returns a finding for each pod that is reachable from
// every OTHER pod in the same namespace. When the over-exposure is uniform
// across the probe set the finding is port-less; when only a subset of
// (protocol, port) tuples see the pod as fully-exposed, one finding per
// (protocol, port) is emitted with Metadata carrying the port context.
//
// Streaming short-circuit: an ingress-restricted pod whose AllowedIngressFrom
// map has fewer entries than the namespace peer count cannot possibly be
// uniformly over-exposed, so overExposedCandidate rejects it before the
// per-probe walk. Default-allow pods always trigger the walk since they
// reach every peer on every probe by default.
func findOverExposedPods(index *reachabilityIndex, probes []portProbe) []Finding {
	if index == nil || len(index.pods) < 3 {
		return nil
	}
	ids := sortedPodIDs(index)
	nsPeers := buildNamespacePeers(index, ids)
	var findings []Finding
	for _, dst := range ids {
		pod := index.pods[dst]
		if pod == nil {
			continue
		}
		peers := nsPeers[pod.Namespace]
		if len(peers) < 2 {
			continue
		}
		// Cheap streaming gate: reject pods whose allow-map size can't
		// cover every namespace peer. len(peers) includes dst itself, so
		// the number of OTHER peers is len(peers)-1.
		if !overExposedCandidate(pod, len(peers)-1) {
			continue
		}
		exposed := map[portProbe]bool{}
		for _, probe := range probes {
			if streamingOverExposedOnProbe(index, peers, dst, probe) {
				exposed[probe] = true
			}
		}
		findings = append(findings, overExposedFindingsFor(dst, pod, exposed, probes)...)
	}
	return findings
}

// overExposedFindingsFor turns the per-probe exposure map for one pod into
// the emitted findings. Uniform exposure collapses to a single port-less
// finding; partial exposure emits one finding per exposed probe.
func overExposedFindingsFor(dst string, pod *podInfo, exposed map[portProbe]bool, probes []portProbe) []Finding {
	if len(exposed) == 0 {
		return nil
	}
	if len(exposed) == len(probes) {
		return []Finding{{
			Algorithm: "k8s_reachability",
			Severity:  SeverityNotice,
			Title:     fmt.Sprintf("Pod over-exposed: %s", podDisplay(pod)),
			Summary: fmt.Sprintf(
				"Pod %s in namespace %q is reachable from every other pod in its namespace across all observed (protocol, port) combinations. NetworkPolicy does not narrow its ingress surface.",
				dst, pod.Namespace,
			),
			Evidence: []string{dst},
			Metrics:  map[string]float64{"probe_count": float64(len(probes))},
		}}
	}
	out := make([]Finding, 0, len(exposed))
	for probe := range exposed {
		out = append(out, Finding{
			Algorithm: "k8s_reachability",
			Severity:  SeverityNotice,
			Title:     fmt.Sprintf("Pod over-exposed on %s: %s", probeLabel(probe), podDisplay(pod)),
			Summary: fmt.Sprintf(
				"Pod %s in namespace %q is reachable from every other pod in its namespace on %s. Other (protocol, port) tuples are narrower.",
				dst, pod.Namespace, probeLabel(probe),
			),
			Evidence: []string{dst},
			Metrics:  map[string]float64{"port": float64(probe.Port)},
			Metadata: probeMetadata(probe),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// findAsymmetricReachability returns a finding for each pod pair (A, B)
// where canReach(A, B) != canReach(B, A). Handles the k8s_emit_asymmetric
// flag: suppress short-circuits, info (default) and warning control the
// emitted Severity. When the asymmetric pattern is uniform across the probe
// set, a single port-less finding is emitted per pair; when probes disagree
// on direction, per-probe findings are emitted with Metadata.
//
// Streaming implementation: the candidate set is built from forward allow
// edges plus the (small) set of mutually-restricted pods, rather than every
// ordered pod pair. Pairs where both pods are default-allow on every side
// are symmetric by construction and never enter the candidate set. See
// streamingAsymmetricCandidates in k8s_reachability_findings_streaming.go.
func findAsymmetricReachability(index *reachabilityIndex, probes []portProbe, mode asymmetricEmitMode) []Finding {
	if mode == asymmetricEmitSuppress || index == nil || len(index.pods) < 2 {
		return nil
	}
	severity := SeverityInfo
	if mode == asymmetricEmitWarning {
		severity = SeverityWarning
	}
	candidates := streamingAsymmetricCandidates(index)
	pairs := make([][2]string, 0, len(candidates))
	for pair := range candidates {
		pairs = append(pairs, pair)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	var findings []Finding
	for _, pair := range pairs {
		a, b := pair[0], pair[1]
		asym := asymmetricProbes(index, a, b, probes)
		findings = append(findings, buildAsymmetricFindings(a, b, asym, probes, severity)...)
	}
	return findings
}

// asymmetricProbes returns the subset of probes where canReach(a, b) differs
// from canReach(b, a). Used by findAsymmetricReachability to decide whether
// to collapse or split findings per probe.
func asymmetricProbes(index *reachabilityIndex, a, b string, probes []portProbe) []portProbe {
	out := probes[:0:0]
	for _, probe := range probes {
		if index.canReach(a, b, probe.Protocol, probe.Port) != index.canReach(b, a, probe.Protocol, probe.Port) {
			out = append(out, probe)
		}
	}
	return out
}

// buildAsymmetricFindings emits one port-less finding if every probe was
// asymmetric, or one per-probe finding when only a subset were.
func buildAsymmetricFindings(a, b string, asym []portProbe, probes []portProbe, severity Severity) []Finding {
	if len(asym) == 0 {
		return nil
	}
	if len(asym) == len(probes) {
		return []Finding{{
			Algorithm: "k8s_reachability",
			Severity:  severity,
			Title:     fmt.Sprintf("Asymmetric reachability: %s ⇄ %s", a, b),
			Summary: fmt.Sprintf(
				"Pods %s and %s have asymmetric reachability across every observed (protocol, port). One direction is allowed while the reverse is blocked, which is often a NetworkPolicy oversight.",
				a, b,
			),
			Evidence: []string{a, b},
			Metrics:  map[string]float64{"probe_count": float64(len(probes))},
		}}
	}
	out := make([]Finding, 0, len(asym))
	for _, probe := range asym {
		out = append(out, Finding{
			Algorithm: "k8s_reachability",
			Severity:  severity,
			Title:     fmt.Sprintf("Asymmetric reachability on %s: %s ⇄ %s", probeLabel(probe), a, b),
			Summary: fmt.Sprintf(
				"Pods %s and %s are asymmetric on %s only. Other (protocol, port) tuples are symmetric.",
				a, b, probeLabel(probe),
			),
			Evidence: []string{a, b},
			Metrics:  map[string]float64{"port": float64(probe.Port)},
			Metadata: probeMetadata(probe),
		})
	}
	return out
}
