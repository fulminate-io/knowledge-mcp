// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// methodGCPFirewall is the Edge.Method discriminator stamped on every
// reachability edge produced by resolveFirewallRules.
const methodGCPFirewall = "gcp-firewall-rule"

// resourceTypeGCPCIDR is the resource_type for CIDR sentinel nodes created
// by the GCP firewall resolver.
const resourceTypeGCPCIDR = "gcp-cidr-block"

// fwRuleMetadata is the decoded form of Edge.Evidence for firewall edges.
type fwRuleMetadata struct {
	Protocol string `json:"protocol,omitempty"`
	Ports    string `json:"ports,omitempty"`
	CIDR     string `json:"cidr,omitempty"`
	Egress   bool   `json:"egress,omitempty"`
}

func (m fwRuleMetadata) encode() string {
	if m.Protocol == "" && m.Ports == "" && m.CIDR == "" && !m.Egress {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return `{"encode_error":true}`
	}
	return string(b)
}

// resolveFirewallRules queries all GCP firewall and instance nodes, builds an
// instance index, and emits instance-to-instance and instance-to-CIDR edges.
func resolveFirewallRules(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	firewalls, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "gcp:compute:firewall"},
	})
	if err != nil {
		return fmt.Errorf("gcp firewall: query firewalls: %w", err)
	}
	if len(firewalls) == 0 {
		return nil
	}

	instances, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "gcp:compute:instance"},
	})
	if err != nil {
		return fmt.Errorf("gcp firewall: query instances: %w", err)
	}

	idx := buildInstanceIndex(instances)
	edges, cidrNodes := buildFirewallEdges(firewalls, idx)
	if len(edges) == 0 {
		return nil
	}
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, gcpCIDRSentinelNodes(cidrNodes), edges); err != nil {
		return fmt.Errorf("gcp firewall: link batch: %w", err)
	}
	slog.Debug("gcp firewall: emitted reachability edges", "count", len(edges))
	return nil
}

// buildFirewallEdges is the pure (testable) core of resolveFirewallRules.
func buildFirewallEdges(
	firewalls []*knowledgev1.Node, idx instanceIndex,
) ([]knowledgev1.Edge, map[string]string) {
	var edges []knowledgev1.Edge
	cidrNodes := make(map[string]string)
	for _, fw := range firewalls {
		if len(fw.Content) == 0 {
			continue
		}
		var spec firewallContent
		if err := json.Unmarshal([]byte(fw.Content), &spec); err != nil {
			slog.Debug("gcp firewall: unmarshal", "fw", fw.Id, "err", err)
			continue
		}
		if spec.Disabled != nil && *spec.Disabled {
			continue
		}
		if len(spec.Allowed) == 0 {
			continue // deny-only rules don't create connectivity
		}
		edges = append(edges, edgesForFirewall(fw, spec, idx, cidrNodes)...)
	}
	return edges, cidrNodes
}

// edgesForFirewall emits instance-to-instance and instance-to-CIDR edges for
// a single firewall rule.
func edgesForFirewall(
	_ *knowledgev1.Node, spec firewallContent, idx instanceIndex, cidrNodes map[string]string,
) []knowledgev1.Edge {
	egress := spec.Direction != nil && *spec.Direction == "EGRESS"
	targets := resolveTargets(spec, idx)
	base := fwBaseMetadata(spec, egress)

	if egress {
		return egressEdges(targets, spec, base, cidrNodes)
	}
	return ingressEdges(targets, spec, idx, base, cidrNodes)
}

func fwBaseMetadata(spec firewallContent, egress bool) fwRuleMetadata {
	var md fwRuleMetadata
	md.Egress = egress
	if len(spec.Allowed) > 0 {
		a := spec.Allowed[0]
		md.Protocol = a.IPProtocol
		if len(a.Ports) > 0 {
			md.Ports = a.Ports[0]
		}
	}
	return md
}

func egressEdges(
	targets []instanceRef, spec firewallContent, base fwRuleMetadata,
	cidrNodes map[string]string,
) []knowledgev1.Edge {
	var out []knowledgev1.Edge
	for _, cidr := range spec.DestinationRanges {
		sid := gcpCIDRSentinelID(cidr)
		cidrNodes[sid] = cidr
		md := base
		md.CIDR = cidr
		for _, t := range targets {
			out = append(out, buildFWEdge(t.id, sid, true, md))
		}
	}
	return out
}

func ingressEdges(
	targets []instanceRef, spec firewallContent, idx instanceIndex,
	base fwRuleMetadata, cidrNodes map[string]string,
) []knowledgev1.Edge {
	var out []knowledgev1.Edge
	// Source CIDRs -> edges from target to CIDR sentinel.
	for _, cidr := range spec.SourceRanges {
		sid := gcpCIDRSentinelID(cidr)
		cidrNodes[sid] = cidr
		md := base
		md.CIDR = cidr
		for _, t := range targets {
			out = append(out, buildFWEdge(t.id, sid, false, md))
		}
	}
	// Source tags/SAs -> instance-to-instance edges.
	sources := resolveSources(spec, idx)
	for _, t := range targets {
		for _, s := range sources {
			if t.id != s.id {
				out = append(out, buildFWEdge(t.id, s.id, false, base))
			}
		}
	}
	return out
}

func gcpCIDRSentinelID(cidr string) string {
	return "gcp:cidr:" + cidr
}

func buildFWEdge(fromID, toID string, egress bool, md fwRuleMetadata) knowledgev1.Edge {
	edgeType := kgtypes.EdgeAllowsIngressFrom
	if egress {
		edgeType = kgtypes.EdgeAllowsEgressTo
	}
	return knowledgev1.Edge{
		FromId:   fromID,
		ToId:     toID,
		Type:     string(edgeType),
		Method:   methodGCPFirewall,
		Evidence: md.encode(),
	}
}

// gcpCIDRSentinelNodes materializes the sentinel cloud-resource nodes for every
// distinct CIDR referenced by any GCP firewall rule. They are emitted in the
// SAME create_batch as the edges that reference them (LinkNodesAndEdgesBatch) so
// both endpoints exist; a re-emitted sentinel is an idempotent upsert keyed by ID.
func gcpCIDRSentinelNodes(cidrNodes map[string]string) []*knowledgev1.Node {
	if len(cidrNodes) == 0 {
		return nil
	}
	out := make([]*knowledgev1.Node, 0, len(cidrNodes))
	for id, cidr := range cidrNodes {
		node := &knowledgev1.Node{
			Id:         id,
			Type:       string(kgtypes.NodeCloudResource),
			SymbolName: cidr,
			Summary:    resourceTypeGCPCIDR + " " + cidr,
		}
		kgtypes.SetValue(node, "resource_type", resourceTypeGCPCIDR)
		kgtypes.SetValue(node, "cidr", cidr)
		out = append(out, node)
	}
	return out
}
