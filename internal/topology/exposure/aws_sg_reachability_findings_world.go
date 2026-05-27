// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"fmt"
	"sort"
)

// aws_sg_reachability_findings_world.go implements the world-open and
// wide-CIDR classifiers. World-open surfaces 0.0.0.0/0 ingress rules on
// privileged ports; wide-CIDR surfaces non-0.0.0.0/0 but still public-wide
// CIDR ranges on sensitive ports.

// privilegedPortInfo is one entry in the privileged-port table consulted
// by findWorldOpenPrivilegedPorts. Name is used in the finding title.
type privilegedPortInfo struct {
	Port int
	Name string
}

// privilegedPorts is the ordered list of world-exposure-sensitive ports
// the world-open classifier scans. Order determines tie-breaking in the
// finding output.
var privilegedPorts = []privilegedPortInfo{
	{22, "SSH"},
	{3389, "RDP"},
	{3306, "MySQL"},
	{5432, "Postgres"},
	{27017, "MongoDB"},
	{6379, "Redis"},
	{2049, "NFS/EFS"},
}

// findWorldOpenPrivilegedPorts walks every resource in the index and
// emits a finding for each (resource, port) combination where a 0.0.0.0/0
// (or ::/0) ingress rule covers the privileged port. Severity is
// conditional on the resource's attachment type — see
// severityForAttachment.
func findWorldOpenPrivilegedPorts(index *sgReachabilityIndex) []Finding {
	if index == nil || len(index.resources) == 0 {
		return nil
	}
	ids := sortedResourceIDs(index)
	var findings []Finding
	for _, id := range ids {
		res := index.resources[id]
		if res == nil {
			continue
		}
		for _, p := range privilegedPorts {
			if !index.worldReachableOn(id, "tcp", p.Port) && !index.worldReachableOn(id, "", p.Port) {
				continue
			}
			sev := severityForAttachment(res.Type, p.Port)
			if sev == "" {
				continue
			}
			findings = append(findings, buildWorldOpenFinding(res, p, sev))
		}
	}
	return findings
}

// buildWorldOpenFinding constructs a world-open finding for one (resource,
// port) combination. Extracted so findWorldOpenPrivilegedPorts stays
// under the 80-line function cap.
func buildWorldOpenFinding(res *resourceInfo, p privilegedPortInfo, sev Severity) Finding {
	title := fmt.Sprintf("%s reachable from 0.0.0.0/0 on %s (%d)", resourceLabel(res), p.Name, p.Port)
	summary := fmt.Sprintf(
		"Resource %s (%s) accepts %s traffic on port %d from 0.0.0.0/0. Severity %q reflects the attachment type and the port's sensitivity. Scope the rule to a specific CIDR or peer SG to close the exposure.",
		res.ID, res.Type, p.Name, p.Port, sev,
	)
	return Finding{
		Algorithm: "aws_sg_reachability",
		Severity:  sev,
		Title:     title,
		Summary:   summary,
		Evidence:  []string{res.ID},
		Metrics: map[string]float64{
			"port": float64(p.Port),
		},
		Metadata: map[string]string{
			"protocol":      "tcp",
			"port":          fmt.Sprintf("%d", p.Port),
			"resource_type": res.Type,
			"port_name":     p.Name,
		},
	}
}

// findWideCIDR walks every SG ingress rule whose peer is a CIDR sentinel
// and classifies the CIDR via classifyCIDR (reused from the K8s ipBlock
// classifier — both analyzers apply the same public-space heuristics).
// 0.0.0.0/0 is handled by findWorldOpenPrivilegedPorts; this classifier
// surfaces wide-but-not-world CIDRs on sensitive ports.
func findWideCIDR(index *sgReachabilityIndex) []Finding {
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
		findings = append(findings, wideCIDRForResource(res)...)
	}
	sort.SliceStable(findings, func(i, j int) bool {
		return findings[i].Title < findings[j].Title
	})
	return findings
}

// wideCIDRForResource returns the wide-CIDR findings for one resource.
// Extracted from findWideCIDR to keep that function under the 80-line cap.
func wideCIDRForResource(res *resourceInfo) []Finding {
	var out []Finding
	for peerID, ranges := range res.AllowsIngressFrom {
		if !isCIDRSentinel(peerID) {
			continue
		}
		cidr := cidrFromSentinel(peerID)
		if cidr == "0.0.0.0/0" || cidr == "::/0" {
			continue
		}
		sev := classifyCIDR(cidr)
		if sev == "" || sev == SeverityInfo {
			continue
		}
		for _, r := range ranges {
			if !isPortSensitive(r.PortFrom, r.PortTo) {
				continue
			}
			out = append(out, Finding{
				Algorithm: "aws_sg_reachability",
				Severity:  sev,
				Title:     fmt.Sprintf("%s ingress from wide CIDR %s", resourceLabel(res), cidr),
				Summary: fmt.Sprintf(
					"Resource %s accepts traffic from the wide public CIDR %s on ports %d-%d. Narrow the CIDR to a specific network or peer SG.",
					res.ID, cidr, r.PortFrom, r.PortTo,
				),
				Evidence: []string{res.ID},
				Metadata: map[string]string{
					"cidr":          cidr,
					"resource_type": res.Type,
				},
			})
		}
	}
	return out
}

// isPortSensitive reports whether the given (from, to) port range
// intersects any privileged port in the world-open table.
func isPortSensitive(from, to int) bool {
	if from == 0 && to == 0 {
		return true
	}
	lo, hi := from, to
	if hi == 0 {
		hi = lo
	}
	for _, p := range privilegedPorts {
		if p.Port >= lo && p.Port <= hi {
			return true
		}
	}
	return false
}
