// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"fmt"
	"sort"
)

// aws_sg_reachability_findings_chains.go implements the transitive SG chain
// and isolated-resource classifiers. Split from aws_sg_reachability_findings.go
// to keep every classifier file under the 300-line soft cap.

// findTransitiveSGChains discovers SG → SG → SG escalation paths longer
// than one hop. For every SG-to-SG edge, it looks for a chained edge from
// the middle SG to a third SG. Emits one finding per distinct chain
// (A, B, C). The classifier is edge-driven — it walks idx.sgIngress
// directly and never enumerates pair-wise.
func findTransitiveSGChains(index *sgReachabilityIndex) []Finding {
	if index == nil || len(index.sgIngress) == 0 {
		return nil
	}
	fwd := buildSGForwardGraph(index)
	starts := make([]string, 0, len(fwd))
	for k := range fwd {
		starts = append(starts, k)
	}
	sort.Strings(starts)
	var findings []Finding
	for _, a := range starts {
		for b := range fwd[a] {
			for c := range fwd[b] {
				if c == a || c == b {
					continue
				}
				findings = append(findings, buildChainFinding(a, b, c))
			}
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		return findings[i].Title < findings[j].Title
	})
	return findings
}

// buildSGForwardGraph turns the idx.sgIngress map into a forward graph
// keyed by src SG → set of dst SGs. EdgeAllowsIngressFrom points dst →
// src, so we flip the edge direction to get the forward attack-path view.
func buildSGForwardGraph(index *sgReachabilityIndex) map[string]map[string]struct{} {
	fwd := make(map[string]map[string]struct{}, len(index.sgIngress))
	for dstSG, entries := range index.sgIngress {
		for _, entry := range entries {
			if isCIDRSentinel(entry.PeerID) {
				continue
			}
			srcSG := entry.PeerID
			if fwd[srcSG] == nil {
				fwd[srcSG] = map[string]struct{}{}
			}
			fwd[srcSG][dstSG] = struct{}{}
		}
	}
	return fwd
}

// buildChainFinding constructs the Finding for one SG chain A → B → C.
func buildChainFinding(a, b, c string) Finding {
	title := fmt.Sprintf("Transitive SG chain: %s → %s → %s", shortSGID(a), shortSGID(b), shortSGID(c))
	return Finding{
		Algorithm: "aws_sg_reachability",
		Severity:  SeverityWarning,
		Title:     title,
		Summary: fmt.Sprintf(
			"Traffic permitted by SG %s can reach SG %s, which in turn permits traffic to SG %s. Multi-hop SG chains make it hard to reason about the effective attack surface — consider consolidating the rules or narrowing the intermediate SG's ingress set.",
			a, b, c,
		),
		Evidence: []string{a, b, c},
		Metrics: map[string]float64{
			"chain_length": 3,
		},
		Metadata: map[string]string{
			"chain_head": a,
			"chain_mid":  b,
			"chain_tail": c,
		},
	}
}

// findIsolatedResources walks every resource and emits an info finding
// for those with no allow edges in either direction.
func findIsolatedResources(index *sgReachabilityIndex) []Finding {
	if index == nil {
		return nil
	}
	ids := sortedResourceIDs(index)
	var findings []Finding
	for _, id := range ids {
		res := index.resources[id]
		if res == nil {
			continue
		}
		if len(res.AllowsIngressFrom) > 0 || len(res.AllowsEgressTo) > 0 {
			continue
		}
		findings = append(findings, Finding{
			Algorithm: "aws_sg_reachability",
			Severity:  SeverityInfo,
			Title:     fmt.Sprintf("%s is network-isolated", resourceLabel(res)),
			Summary: fmt.Sprintf(
				"Resource %s (%s) has no allow edges in either direction. It is either intentionally isolated or its SG attachments were not collected.",
				res.ID, res.Type,
			),
			Evidence: []string{res.ID},
			Metadata: map[string]string{
				"resource_type": res.Type,
			},
		})
	}
	return findings
}

// shortSGID returns the last path segment of an SG ARN (or the ID itself
// if no "/" is present). Used to keep chain finding titles concise.
func shortSGID(sg string) string {
	for i := len(sg) - 1; i >= 0; i-- {
		if sg[i] == '/' {
			return sg[i+1:]
		}
	}
	return sg
}
