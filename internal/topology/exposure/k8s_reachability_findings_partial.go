// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"fmt"
	"sort"
)

// sortOrderedPairs sorts a slice of [2]string ordered pod pairs by their
// two components in ascending lex order so downstream finding emission is
// deterministic regardless of map iteration order.
func sortOrderedPairs(pairs [][2]string) {
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
}

// k8s_reachability_findings_partial.go holds the Phase 2.5 Step 4 partial-
// reachability sub-classifier. It emits per-(protocol, port) findings when
// a pod pair's forward reachability result VARIES across the probe set.
// Collapse rule in findPartialReachability prevents cardinality explosion:
// uniform pairs produce zero findings here because other sub-classifiers
// (isolated, over-exposed, asymmetric) already surface whole-pair properties
// with port-less findings.
//
// EDGE-DRIVEN COMPLETENESS. The classifier enumerates candidate pairs only
// from forward allow/ANP edges rather than the full O(P²) pod-pair product.
// This is provably complete, not an approximation, because a pair's
// reachability can only VARY across probes when at least one allow-shaped
// rule (regular NetworkPolicy AllowedEgressTo/AllowedIngressFrom or
// AdminNetworkPolicy ANPEgressTo/ANPIngressFrom) mentions the pair with a
// port/protocol restriction. Proof by cases:
//
//  1. Both pods default-allow with no selecting policy → canReach = true on
//     every probe → uniform → no finding. Never enumerated, correctly skipped.
//  2. src egress-restricted with NO allow edge listing dst → canReach = false
//     on every probe → uniform → no finding. Never enumerated, skipped.
//  3. dst ingress-restricted with NO allow edge listing src → symmetric
//     case 2 → uniform false → skipped.
//  4. Both restricted with no edges between them → uniform false → skipped.
//  5. At least one allow-shaped edge between them (regular or ANP) → the
//     edge appears in AllowedEgressTo/AllowedIngressFrom or ANPEgressTo/
//     ANPIngressFrom, so the pair enters the enumeration.
//
// Therefore the edge-driven walk examines every pair that could possibly
// produce a finding. The quadratic full-pair fallback that once lived here
// was pure waste and has been removed.

// findPartialReachability returns per-(protocol, port) findings for each
// ordered pod pair whose forward reachability result VARIES across the
// probe set. Example: a policy allowing web→api on TCP/80 only produces one
// "reachable on TCP/80" finding and one "unreachable on TCP/443" finding
// for (web, api), each carrying Metadata["protocol"] and Metadata["port"].
//
// Only emitted when there are at least two probes in the set (a single
// probe cannot disagree with itself).
func findPartialReachability(index *reachabilityIndex, probes []portProbe) []Finding {
	if index == nil || len(index.pods) < 2 || len(probes) < 2 {
		return nil
	}
	pairs := collectPartialReachabilityPairs(index)
	var findings []Finding
	for _, pair := range pairs {
		src, dst := pair[0], pair[1]
		if src == dst {
			continue
		}
		results := probeReachabilityResults(index, src, dst, probes)
		if allAgree(results) {
			continue
		}
		findings = append(findings, buildPartialFindings(src, dst, probes, results)...)
	}
	return findings
}

// collectPartialReachabilityPairs enumerates every ordered pod pair that
// COULD have probe-dependent reachability. A pair qualifies when at least
// one allow-shaped edge (regular NetworkPolicy or AdminNetworkPolicy)
// mentions it — see the file header for the correctness proof. The returned
// slice is sorted lexicographically for deterministic finding emission.
func collectPartialReachabilityPairs(index *reachabilityIndex) [][2]string {
	seen := map[[2]string]struct{}{}
	for id, pod := range index.pods {
		for dst := range pod.AllowedEgressTo {
			seen[[2]string{id, dst}] = struct{}{}
		}
		for src := range pod.AllowedIngressFrom {
			seen[[2]string{src, id}] = struct{}{}
		}
		for dst := range pod.ANPEgressTo {
			seen[[2]string{id, dst}] = struct{}{}
		}
		for src := range pod.ANPIngressFrom {
			seen[[2]string{src, id}] = struct{}{}
		}
	}
	pairs := make([][2]string, 0, len(seen))
	for pair := range seen {
		pairs = append(pairs, pair)
	}
	sortOrderedPairs(pairs)
	return pairs
}

// probeReachabilityResults evaluates canReach for each probe in order and
// returns the result slice. Aligned index-for-index with probes.
func probeReachabilityResults(index *reachabilityIndex, src, dst string, probes []portProbe) []bool {
	out := make([]bool, len(probes))
	for i, probe := range probes {
		out[i] = index.canReach(src, dst, probe.Protocol, probe.Port)
	}
	return out
}

// allAgree reports whether every entry of results holds the same boolean.
// An empty slice returns true (vacuously uniform).
func allAgree(results []bool) bool {
	if len(results) == 0 {
		return true
	}
	first := results[0]
	for _, r := range results[1:] {
		if r != first {
			return false
		}
	}
	return true
}

// buildPartialFindings emits one finding per probe when forward reachability
// varies across the probe set. Each finding carries protocol/port in
// Metadata so downstream consumers can filter on the distinction.
func buildPartialFindings(src, dst string, probes []portProbe, results []bool) []Finding {
	out := make([]Finding, 0, len(probes))
	for i, probe := range probes {
		state := "unreachable"
		if results[i] {
			state = "reachable"
		}
		out = append(out, Finding{
			Algorithm: "k8s_reachability",
			Severity:  SeverityInfo,
			Title:     fmt.Sprintf("Partial reachability %s → %s on %s: %s", src, dst, probeLabel(probe), state),
			Summary: fmt.Sprintf(
				"Pod %s has mixed reachability to %s across the observed (protocol, port) set. On %s the traffic is %s; other combinations differ. This is usually intentional but surfacing it lets auditors confirm the policy matches expectations.",
				src, dst, probeLabel(probe), state,
			),
			Evidence: []string{src, dst},
			Metrics:  map[string]float64{"port": float64(probe.Port)},
			Metadata: probeMetadata(probe),
		})
	}
	return out
}
