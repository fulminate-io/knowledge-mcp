// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// k8s_reachability_findings_ipblock.go implements the ipBlock world-exposure
// classifier. NetworkPolicy ingress rules can admit traffic from raw CIDR
// blocks; 0.0.0.0/0 literally means "the entire internet" and is the single
// highest-impact misconfiguration this analyzer can surface.
//
// The classifier re-parses each NetworkPolicy node's Content field via a
// LOCAL anonymous struct — no cloud/k8s import — to extract the ipBlock CIDR
// list. Classification is delegated to classifyCIDR, which returns a Severity
// (or "" to suppress) based on the CIDR size and whether the block is a
// public range, an RFC1918 private range, link-local, or loopback.
//
// LAYERING. topology/ must not import cloud/k8s/ (see topology.go package
// comment). Re-parsing NetworkPolicy JSON with a local anonymous struct keeps
// the layering rule intact: we depend only on the JSON schema Kubernetes
// itself documents, not on Go types from the collector package.

// netpolIngressDoc is the local anonymous-struct mirror of the subset of the
// NetworkPolicy JSON schema this classifier cares about. Only the ingress[]
// → from[] → ipBlock.cidr path is populated; every other field is decoded
// into interface{} or dropped. Field tags match the canonical Kubernetes
// NetworkingV1 NetworkPolicy wire format.
type netpolIngressDoc struct {
	Spec struct {
		Ingress []struct {
			From []struct {
				IPBlock *struct {
					CIDR   string   `json:"cidr"`
					Except []string `json:"except,omitempty"`
				} `json:"ipBlock,omitempty"`
			} `json:"from,omitempty"`
		} `json:"ingress,omitempty"`
	} `json:"spec"`
}

// findWorldExposedPods walks every NetworkPolicy cached on the index, decodes
// its Content JSON, and emits one finding per ipBlock CIDR that classifyCIDR
// rates above the suppress floor. The classifier runs AFTER the rest of the
// reachability pipeline so callers always see pod-pair findings first; the
// ipBlock findings carry the policy node ID as their evidence.
func findWorldExposedPods(ctx context.Context, scoped *cloudReader, index *reachabilityIndex) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/k8s_reachability: %w", err)
	}
	if index == nil || scoped == nil {
		return nil, nil
	}

	ids := make([]string, 0, len(index.policies))
	for id := range index.policies {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var findings []Finding
	for _, id := range ids {
		policy := index.policies[id]
		if policy.Content == "" {
			continue
		}
		var doc netpolIngressDoc
		if err := json.Unmarshal([]byte(policy.Content), &doc); err != nil {
			continue
		}
		for _, ingress := range doc.Spec.Ingress {
			for _, from := range ingress.From {
				if from.IPBlock == nil || from.IPBlock.CIDR == "" {
					continue
				}
				severity := classifyCIDR(from.IPBlock.CIDR)
				if severity == "" {
					continue
				}
				findings = append(findings, buildIPBlockFinding(policy, from.IPBlock.CIDR, severity))
			}
		}
	}
	return findings, nil
}

// classifyCIDR returns the Severity an ipBlock CIDR should trigger, or ""
// when the CIDR is private/link-local/loopback and should be suppressed.
// Rules:
//   - 0.0.0.0/0                 → critical (literal internet)
//   - public /1..15             → high   (rendered as SeverityCritical)
//   - public /16..23            → warning
//   - public /24 and narrower   → notice
//   - 10.0.0.0/8, 172.16/12,
//     192.168/16                → info  (RFC1918)
//   - 127.0.0.0/8, 169.254/16   → ""    (suppress)
//
// "high" is mapped to SeverityCritical because the Severity enum does not
// include a distinct high tier; the summary text distinguishes the two.
func classifyCIDR(cidr string) Severity {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	if isLoopbackOrLinkLocal(ipNet) {
		return ""
	}
	if isRFC1918(ipNet) {
		return SeverityInfo
	}
	ones, _ := ipNet.Mask.Size()
	if ones == 0 {
		return SeverityCritical
	}
	if ones <= 15 {
		return SeverityCritical
	}
	if ones <= 23 {
		return SeverityWarning
	}
	return SeverityNotice
}

// isLoopbackOrLinkLocal reports whether ipNet is the loopback range
// (127.0.0.0/8) or the link-local range (169.254.0.0/16). These are the
// only two ranges the classifier suppresses.
func isLoopbackOrLinkLocal(ipNet *net.IPNet) bool {
	loopback := &net.IPNet{IP: net.IPv4(127, 0, 0, 0), Mask: net.CIDRMask(8, 32)}
	linkLocal := &net.IPNet{IP: net.IPv4(169, 254, 0, 0), Mask: net.CIDRMask(16, 32)}
	return cidrContained(ipNet, loopback) || cidrContained(ipNet, linkLocal)
}

// isRFC1918 reports whether ipNet falls entirely inside one of the three
// RFC1918 private ranges.
func isRFC1918(ipNet *net.IPNet) bool {
	ranges := []*net.IPNet{
		{IP: net.IPv4(10, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
		{IP: net.IPv4(172, 16, 0, 0), Mask: net.CIDRMask(12, 32)},
		{IP: net.IPv4(192, 168, 0, 0), Mask: net.CIDRMask(16, 32)},
	}
	for _, r := range ranges {
		if cidrContained(ipNet, r) {
			return true
		}
	}
	return false
}

// cidrContained reports whether inner is fully contained within outer.
// Used to classify CIDRs as RFC1918 or loopback/link-local regardless of
// the inner block's size.
func cidrContained(inner, outer *net.IPNet) bool {
	if !outer.Contains(inner.IP) {
		return false
	}
	innerOnes, _ := inner.Mask.Size()
	outerOnes, _ := outer.Mask.Size()
	return innerOnes >= outerOnes
}

// buildIPBlockFinding constructs one Finding for a single
// (NetworkPolicy, CIDR) classification result. The policy ID goes into
// Evidence so downstream edges link back to the source policy; Metadata
// carries the raw CIDR so consumers can re-classify without re-parsing.
func buildIPBlockFinding(policy *knowledgev1.Node, cidr string, severity Severity) Finding {
	title := fmt.Sprintf("NetworkPolicy admits ingress from %s", cidr)
	summary := fmt.Sprintf(
		"NetworkPolicy %q in namespace %q allows ingress from the CIDR block %s. Severity %q reflects how wide the block is relative to the public internet; narrow the block or scope it to explicit peers to close the exposure.",
		policy.SymbolName, nodeMeta(policy, "namespace"), cidr, severity,
	)
	return Finding{
		Algorithm: "k8s_reachability",
		Severity:  severity,
		Title:     title,
		Summary:   summary,
		Evidence:  []string{policy.Id},
		Metrics:   map[string]float64{},
		Metadata: map[string]string{
			"cidr":      cidr,
			"policy_id": policy.Id,
			"namespace": nodeMeta(policy, "namespace"),
		},
	}
}
