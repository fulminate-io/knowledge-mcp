// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// methodAzureNSGRule is the Edge.Method discriminator stamped on every
// reachability edge produced by resolveNSGRules so downstream consumers can
// recognize the nsgRuleMetadata schema below.
const methodAzureNSGRule = "azure-nsg-rule"

// resourceTypeAzureCIDR is the resource_type value for CIDR sentinel nodes
// created by the Azure NSG resolver.
const resourceTypeAzureCIDR = "azure-cidr-block"

// nsgSpec is a partial JSON schema covering the fields of the Azure
// SecurityGroup response that the NSG resolver cares about. It mirrors the
// armnetwork.SecurityGroup struct so an NSG node's Content blob (produced by
// nsgCollector) round-trips cleanly.
type nsgSpec struct {
	Properties *nsgProperties `json:"properties,omitempty"`
}

type nsgProperties struct {
	SecurityRules []*nsgRule `json:"securityRules,omitempty"`
}

type nsgRule struct {
	Properties *nsgRuleProperties `json:"properties,omitempty"`
}

type nsgRuleProperties struct {
	Access                     *string   `json:"access,omitempty"`
	Direction                  *string   `json:"direction,omitempty"`
	Protocol                   *string   `json:"protocol,omitempty"`
	Priority                   *int32    `json:"priority,omitempty"`
	SourceAddressPrefix        *string   `json:"sourceAddressPrefix,omitempty"`
	SourceAddressPrefixes      []*string `json:"sourceAddressPrefixes,omitempty"`
	DestinationAddressPrefix   *string   `json:"destinationAddressPrefix,omitempty"`
	DestinationAddressPrefixes []*string `json:"destinationAddressPrefixes,omitempty"`
	DestinationPortRange       *string   `json:"destinationPortRange,omitempty"`
	DestinationPortRanges      []*string `json:"destinationPortRanges,omitempty"`
}

// nsgRuleMetadata is the decoded form of Edge.Evidence for NSG rule edges.
type nsgRuleMetadata struct {
	Protocol string `json:"protocol,omitempty"`
	PortFrom int    `json:"port_from,omitempty"`
	PortTo   int    `json:"port_to,omitempty"`
	CIDR     string `json:"cidr,omitempty"`
	Egress   bool   `json:"egress,omitempty"`
}

func (m nsgRuleMetadata) encode() string {
	if m.Protocol == "" && m.PortFrom == 0 && m.PortTo == 0 &&
		m.CIDR == "" && !m.Egress {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return `{"encode_error":true}`
	}
	return string(b)
}

// resolveNSGRules walks every NSG node in the cloud graph, parses its
// SecurityRules JSON, and emits EdgeAllowsIngressFrom / EdgeAllowsEgressTo
// edges. Only Allow rules produce edges; Deny rules are skipped. CIDR rules
// emit edges to stable "azure:cidr:<prefix>" sentinel nodes. ASG references
// are skipped (not collected yet).
func resolveNSGRules(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	nsgs, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "Microsoft.Network/networkSecurityGroups"},
		"limit": 0,
	})
	if err != nil {
		return fmt.Errorf("azure nsg: query NSGs: %w", err)
	}
	if len(nsgs) == 0 {
		return nil
	}

	edges, cidrNodes := buildNSGRuleEdges(nsgs)
	if len(edges) == 0 {
		return nil
	}
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, azureCIDRSentinelNodes(cidrNodes), edges); err != nil {
		return fmt.Errorf("azure nsg: link batch: %w", err)
	}
	slog.Debug("azure nsg: emitted reachability edges", "count", len(edges))
	return nil
}

// buildNSGRuleEdges is the pure (testable) core of resolveNSGRules. It returns
// every edge implied by the given NSG nodes plus the set of distinct CIDR
// sentinel node IDs that must exist before LinkBatch runs.
func buildNSGRuleEdges(nodes []*knowledgev1.Node) ([]knowledgev1.Edge, map[string]string) {
	var edges []knowledgev1.Edge
	cidrNodes := make(map[string]string)
	for _, nsg := range nodes {
		if len(nsg.Content) == 0 {
			continue
		}
		var spec nsgSpec
		if err := json.Unmarshal([]byte(nsg.Content), &spec); err != nil {
			slog.Debug("azure nsg: unmarshal nsg content", "nsg", nsg.Id, "err", err)
			continue
		}
		edges = append(edges, edgesForNSGRules(nsg, spec, cidrNodes)...)
	}
	return edges, cidrNodes
}

// edgesForNSGRules extracts edges from a single NSG's security rules.
func edgesForNSGRules(nsg *knowledgev1.Node, spec nsgSpec, cidrNodes map[string]string) []knowledgev1.Edge {
	if spec.Properties == nil {
		return nil
	}
	var out []knowledgev1.Edge
	for _, rule := range spec.Properties.SecurityRules {
		out = append(out, edgesForSingleNSGRule(nsg, rule, cidrNodes)...)
	}
	return out
}

// edgesForSingleNSGRule processes one NSG security rule and returns edges for
// Allow rules. Deny rules are skipped.
func edgesForSingleNSGRule(
	nsg *knowledgev1.Node, rule *nsgRule, cidrNodes map[string]string,
) []knowledgev1.Edge {
	if rule == nil || rule.Properties == nil {
		return nil
	}
	props := rule.Properties
	if !isAllowRule(props.Access) {
		return nil
	}

	egress := isOutbound(props.Direction)
	base := nsgBaseMetadata(props, egress)

	cidrs := nsgExtractCIDRs(props, egress)
	var out []knowledgev1.Edge
	for _, cidr := range cidrs {
		sentinelID := azureCIDRSentinelID(cidr)
		cidrNodes[sentinelID] = cidr
		md := base
		md.CIDR = cidr
		out = append(out, buildNSGEdge(nsg.Id, sentinelID, egress, md))
	}
	return out
}

func isAllowRule(access *string) bool {
	return access != nil && strings.EqualFold(*access, "Allow")
}

func isOutbound(direction *string) bool {
	return direction != nil && strings.EqualFold(*direction, "Outbound")
}

// nsgBaseMetadata extracts port/protocol info from an NSG rule.
func nsgBaseMetadata(props *nsgRuleProperties, egress bool) nsgRuleMetadata {
	var md nsgRuleMetadata
	md.Egress = egress
	if props.Protocol != nil {
		p := *props.Protocol
		// Azure uses "*" for "all protocols" -- normalize to empty.
		if p != "*" {
			md.Protocol = strings.ToLower(p)
		}
	}
	md.PortFrom, md.PortTo = nsgParsePorts(props)
	return md
}

// nsgParsePorts extracts destination port range into numeric from/to.
func nsgParsePorts(props *nsgRuleProperties) (int, int) {
	if props.DestinationPortRange != nil {
		return parsePortRange(*props.DestinationPortRange)
	}
	for _, r := range props.DestinationPortRanges {
		if r != nil {
			from, to := parsePortRange(*r)
			if from > 0 {
				return from, to
			}
		}
	}
	return 0, 0
}

// parsePortRange handles "443", "80-443", and "*" (returns 0,0).
func parsePortRange(s string) (int, int) {
	if s == "" || s == "*" {
		return 0, 0
	}
	if found := strings.Contains(s, "-"); found {
		var from, to int
		if _, err := fmt.Sscanf(s, "%d-%d", &from, &to); err == nil {
			return from, to
		}
		return 0, 0
	}
	var port int
	if _, err := fmt.Sscanf(s, "%d", &port); err == nil {
		return port, port
	}
	return 0, 0
}

// nsgExtractCIDRs collects the relevant address prefixes from a rule. For
// inbound rules, source addresses are the "peer"; for outbound, destination
// addresses are the peer. Wildcard "*" is normalized to "0.0.0.0/0".
func nsgExtractCIDRs(props *nsgRuleProperties, egress bool) []string {
	if egress {
		return collectPrefixes(
			props.DestinationAddressPrefix, props.DestinationAddressPrefixes,
		)
	}
	return collectPrefixes(
		props.SourceAddressPrefix, props.SourceAddressPrefixes,
	)
}

func collectPrefixes(single *string, multi []*string) []string {
	var out []string
	if single != nil && *single != "" {
		out = append(out, normalizeAddress(*single))
	}
	for _, p := range multi {
		if p != nil && *p != "" {
			out = append(out, normalizeAddress(*p))
		}
	}
	return out
}

// normalizeAddress maps Azure's wildcard "*" to "0.0.0.0/0" and passes
// everything else through unchanged.
func normalizeAddress(addr string) string {
	if addr == "*" {
		return "0.0.0.0/0"
	}
	return addr
}

func azureCIDRSentinelID(cidr string) string {
	return "azure:cidr:" + cidr
}

// buildNSGEdge assembles the directional Edge value. Inbound edges go from
// the NSG back to the source peer (CIDR sentinel); outbound edges go from
// the NSG out to the destination peer.
func buildNSGEdge(nsgID, peerID string, egress bool, md nsgRuleMetadata) knowledgev1.Edge {
	edgeType := kgtypes.EdgeAllowsIngressFrom
	if egress {
		edgeType = kgtypes.EdgeAllowsEgressTo
	}
	return knowledgev1.Edge{
		FromId:   nsgID,
		ToId:     peerID,
		Type:     string(edgeType),
		Method:   methodAzureNSGRule,
		Evidence: md.encode(),
	}
}

// azureCIDRSentinelNodes materializes the sentinel cloud-resource nodes for
// every distinct CIDR referenced by any NSG rule. They are emitted in the SAME
// create_batch as the edges that reference them (LinkNodesAndEdgesBatch); a
// re-emitted sentinel is an idempotent upsert keyed by ID.
func azureCIDRSentinelNodes(cidrNodes map[string]string) []*knowledgev1.Node {
	if len(cidrNodes) == 0 {
		return nil
	}
	out := make([]*knowledgev1.Node, 0, len(cidrNodes))
	for id, cidr := range cidrNodes {
		node := &knowledgev1.Node{
			Id:         id,
			Type:       string(kgtypes.NodeCloudResource),
			SymbolName: cidr,
			Summary:    resourceTypeAzureCIDR + " " + cidr,
		}
		kgtypes.SetValue(node, "resource_type", resourceTypeAzureCIDR)
		kgtypes.SetValue(node, "cidr", cidr)
		out = append(out, node)
	}
	return out
}
