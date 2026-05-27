// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
)

// k8s_reachability_findings.go implements the Phase 4 classifier that turns a
// reachabilityIndex into Finding values. The entry point classifyReachability
// dispatches to four sub-classifiers (isolated pods, over-exposed pods,
// asymmetric reachability, namespace-fully-open), one matrix emitter, and the
// ipBlock world-exposure classifier (k8s_reachability_findings_ipblock.go).
//
// SENTINEL HANDLING. When the index was short-circuited by the
// reachabilityPodCap hard cap (index.skipped == true), classifyReachability
// emits a single reachability_skipped notice finding and returns without
// running any sub-classifier. This mirrors the Phase 3 stub contract.
//
// PROBE ENUMERATION. Sub-classifiers that key off (protocol, port) walk
// collectPortProbes(index) which returns the sorted, deduplicated list of
// (protocol, port) tuples present on any allow edge. For a pod-pair whose
// reachability result is identical across every probe, sub-classifiers emit
// a single port-less finding. When the result differs by probe, they emit one
// finding per distinct (protocol, port) with Metadata["protocol"] and
// Metadata["port"] populated. This prevents cardinality explosion while
// preserving Phase 2.5 Step 4's per-port granularity requirement.
//
// CONFIGURABLE ASYMMETRIC SEVERITY. req.Extra["k8s_emit_asymmetric"] is
// consulted by findAsymmetricReachability. Values: "info" (default),
// "warning", "suppress". "suppress" skips the classifier entirely. Invalid
// values fall back to "info" with a log warning. See the analyzer's Run
// godoc for the full user-facing contract.

// asymmetricEmitMode controls how findAsymmetricReachability surfaces its
// findings. The three values map 1:1 to the values accepted by the
// k8s_emit_asymmetric req.Extra key.
type asymmetricEmitMode int

const (
	asymmetricEmitInfo asymmetricEmitMode = iota
	asymmetricEmitWarning
	asymmetricEmitSuppress
)

// portProbe identifies a (protocol, port) tuple used when enumerating the
// reachability space for per-port classifier results. A zero-value probe
// represents "any protocol, any port" and is used as the fallback when the
// index contains no explicit port metadata.
type portProbe struct {
	Protocol string
	Port     int
}

// classifyReachability is the Phase 4 entry point for the K8s reachability
// analyzer. The skipped-sentinel path emits exactly one reachability_skipped
// notice finding so operators know the analyzer ran but declined to build the
// full matrix. The normal path dispatches to four sub-classifiers, the matrix
// emitter, and the ipBlock world-exposure classifier; the combined findings
// are sorted deterministically before return.
func classifyReachability(
	ctx context.Context,
	req Request,
	scoped *cloudReader,
	index *reachabilityIndex,
) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/k8s_reachability: %w", err)
	}
	if index == nil {
		return nil, nil
	}
	if index.skipped {
		return []Finding{skippedSentinelFinding(req, index)}, nil
	}

	probes := collectPortProbes(index)
	mode := readAsymmetricEmitMode(req)

	results, worldErr := fanOutClassifiers(ctx, req, scoped, index, probes, mode)
	if worldErr != nil {
		return nil, worldErr
	}

	findings := make([]Finding, 0, 16)
	for _, r := range results {
		findings = append(findings, r...)
	}

	sortReachabilityFindings(findings)
	return findings, nil
}

// fanOutClassifiers runs all 9 sub-classifiers as parallel goroutines,
// collects their results into indexed slots, and returns the merged array.
// Each classifier is read-only over *reachabilityIndex. The worldErr return
// carries the error from findWorldExposedPods (the only classifier that
// performs additional DB queries).
func fanOutClassifiers(
	ctx context.Context,
	req Request,
	scoped *cloudReader,
	index *reachabilityIndex,
	probes []portProbe,
	mode asymmetricEmitMode,
) ([9][]Finding, error) {
	var results [9][]Finding
	var worldErr error
	var wg sync.WaitGroup
	wg.Add(9)
	go func() { defer wg.Done(); results[0] = findIsolatedPods(index, probes) }()
	go func() { defer wg.Done(); results[1] = findOverExposedPods(index, probes) }()
	go func() { defer wg.Done(); results[2] = findAsymmetricReachability(index, probes, mode) }()
	go func() { defer wg.Done(); results[3] = findPartialReachability(index, probes) }()
	go func() { defer wg.Done(); results[4] = findNamespaceFullyOpen(index, probes) }()
	go func() { defer wg.Done(); results[5] = classifyServiceReachability(index, probes) }()
	go func() { defer wg.Done(); results[6] = classifyIngressReachability(index, probes) }()
	go func() {
		defer wg.Done()
		if matrix, ok := emitReachabilityMatrix(req, index, probes); ok {
			results[7] = []Finding{matrix}
		}
	}()
	go func() {
		defer wg.Done()
		world, err := findWorldExposedPods(ctx, scoped, index)
		if err != nil {
			worldErr = err
			return
		}
		results[8] = world
	}()
	wg.Wait()
	return results, worldErr
}

// skippedSentinelFinding is the single notice finding emitted when the index
// short-circuited on the hard pod cap. Callers detect index.skipped and
// route around every classifier; this helper keeps the title/summary text
// in one place so tests can pin it.
func skippedSentinelFinding(req Request, index *reachabilityIndex) Finding {
	title := "Reachability analysis skipped: pod count exceeds hard cap"
	summary := fmt.Sprintf(
		"Cluster in account %q has %d pods, which exceeds the %d-pod hard cap for the k8s_reachability analyzer. The full reachability matrix was not built. Clusters up to the cap run native streaming classifiers; only pathologically large clusters fall through to this sentinel.",
		req.Name, index.podCount, reachabilityPodCap,
	)
	return Finding{
		Algorithm: "k8s_reachability",
		Severity:  SeverityNotice,
		Title:     title,
		Summary:   summary,
		Metrics: map[string]float64{
			"pod_count":    float64(index.podCount),
			"pod_cap":      float64(reachabilityPodCap),
			"skipped_flag": 1,
		},
	}
}

// readAsymmetricEmitMode parses req.Extra["k8s_emit_asymmetric"] into the
// asymmetricEmitMode enum. Unknown values fall back to info with a single
// log warning so misconfigured callers notice.
func readAsymmetricEmitMode(req Request) asymmetricEmitMode {
	if req.Extra == nil {
		return asymmetricEmitInfo
	}
	raw, ok := req.Extra["k8s_emit_asymmetric"]
	if !ok {
		return asymmetricEmitInfo
	}
	switch raw {
	case "info", "":
		return asymmetricEmitInfo
	case "warning":
		return asymmetricEmitWarning
	case "suppress":
		return asymmetricEmitSuppress
	default:
		slog.Warn("topology/k8s_reachability: invalid k8s_emit_asymmetric value, defaulting to info",
			"value", raw)
		return asymmetricEmitInfo
	}
}

// collectPortProbes walks every allow edge in the index and returns the
// sorted, deduplicated set of (protocol, port) probes the per-pair
// classifiers should evaluate. Zero-value (empty Protocol, zero Port)
// represents "any / any" and is synthesized as a universal probe when no
// edge carries explicit port metadata. A ranged edge contributes only its
// lower bound (PortFrom) — the classifier cares about the qualitative
// difference between "reachable on TCP/80" and "reachable on TCP/443", not
// the full range enumeration which would explode cardinality.
func collectPortProbes(index *reachabilityIndex) []portProbe {
	if index == nil || index.skipped || len(index.pods) == 0 {
		return []portProbe{{}}
	}
	seen := map[portProbe]struct{}{}
	add := func(r portRange) {
		port := r.PortFrom
		if port == 0 && r.PortTo != 0 {
			port = r.PortTo
		}
		seen[portProbe{Protocol: r.Protocol, Port: port}] = struct{}{}
	}
	for _, info := range index.pods {
		for _, ranges := range info.AllowedIngressFrom {
			for _, r := range ranges {
				add(r)
			}
		}
		for _, ranges := range info.AllowedEgressTo {
			for _, r := range ranges {
				add(r)
			}
		}
	}
	if len(seen) == 0 {
		return []portProbe{{}}
	}
	out := make([]portProbe, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Protocol != out[j].Protocol {
			return out[i].Protocol < out[j].Protocol
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// findIsolatedPods, findOverExposedPods, and findAsymmetricReachability live
// in k8s_reachability_findings_rules.go. findPartialReachability lives in
// k8s_reachability_findings_partial.go. This file holds only the entry
// point, the sentinel + flag plumbing, the probe enumerator, the namespace-
// fully-open classifier, and the shared helpers.

// findNamespaceFullyOpen, namespaceAllDefaultAllow, sortedPodIDs, and
// buildNamespacePeers are implemented in k8s_reachability_findings_namespace.go.
// They were extracted in Phase 6 Step 3 to keep this file under the 300-line
// soft cap.

// probeLabel renders a probe as "TCP/80" or "any" for titles and summaries.
func probeLabel(p portProbe) string {
	if p.Protocol == "" && p.Port == 0 {
		return "any"
	}
	if p.Protocol == "" {
		return strconv.Itoa(p.Port)
	}
	if p.Port == 0 {
		return p.Protocol
	}
	return p.Protocol + "/" + strconv.Itoa(p.Port)
}

// probeMetadata returns the protocol/port Metadata map attached to per-probe
// findings. Keys mirror the plan's criterion text: "protocol" and "port".
func probeMetadata(p portProbe) map[string]string {
	m := map[string]string{}
	if p.Protocol != "" {
		m["protocol"] = p.Protocol
	}
	if p.Port != 0 {
		m["port"] = strconv.Itoa(p.Port)
	}
	return m
}

// podDisplay renders a podInfo as a short human-readable label for finding
// titles. Namespace is prefixed when non-empty.
func podDisplay(pod *podInfo) string {
	if pod == nil {
		return ""
	}
	if pod.Namespace == "" {
		return pod.ID
	}
	return pod.Namespace + "/" + pod.ID
}

// severityOrder ranks severities for deterministic sort: critical first,
// info last. Callers pass the severity string through strictly, so new
// levels added in the future will sort to the end unless this map grows.
var severityOrder = map[Severity]int{
	SeverityCritical: 0,
	SeverityWarning:  1,
	SeverityNotice:   2,
	SeverityInfo:     3,
}

// sortReachabilityFindings orders findings deterministically: severity
// (critical → info), then by first evidence ID, then by title. Matches the
// convention in sortOrphanFindings but keys off severity instead of
// confidence because reachability findings share a fixed severity scheme.
func sortReachabilityFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		si := severityOrder[findings[i].Severity]
		sj := severityOrder[findings[j].Severity]
		if si != sj {
			return si < sj
		}
		pi := primaryEvidence(findings[i])
		pj := primaryEvidence(findings[j])
		if pi != pj {
			return pi < pj
		}
		return findings[i].Title < findings[j].Title
	})
}
