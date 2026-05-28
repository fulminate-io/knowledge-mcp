// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// methodAWSNACL is the Edge.Method discriminator stamped on every NACL
// reachability edge produced by resolveNetworkACLRules.
const methodAWSNACL = "aws-nacl-rule"

// naclSpec models the subset of the EC2 DescribeNetworkAcls response
// fields this collector cares about. Shape matches the JSON that
// aws-sdk-go-v2 produces for ec2types.NetworkAcl — unused fields like
// Tags and OwnerId are dropped.
type naclSpec struct {
	NetworkACLID *string           `json:"NetworkAclId,omitempty"`
	VpcId        *string           `json:"VpcId,omitempty"`
	Associations []naclAssociation `json:"Associations,omitempty"`
	Entries      []naclEntry       `json:"Entries,omitempty"`
}

type naclAssociation struct {
	NetworkACLAssociationID *string `json:"NetworkAclAssociationId,omitempty"`
	NetworkACLID            *string `json:"NetworkAclId,omitempty"`
	SubnetId                *string `json:"SubnetId,omitempty"`
}

type naclEntry struct {
	RuleNumber *int32         `json:"RuleNumber,omitempty"`
	Protocol   *string        `json:"Protocol,omitempty"`
	RuleAction *string        `json:"RuleAction,omitempty"`
	Egress     *bool          `json:"Egress,omitempty"`
	CidrBlock  *string        `json:"CidrBlock,omitempty"`
	PortRange  *naclPortRange `json:"PortRange,omitempty"`
}

type naclPortRange struct {
	From *int32 `json:"From,omitempty"`
	To   *int32 `json:"To,omitempty"`
}

// resolveNetworkACLRules walks every network-acl node in the cloud
// graph, parses its entries, and emits:
//
//  1. EdgeAssociatedWithSubnet edges subnet → NACL for every subnet
//     association (the raw subnet ↔ NACL pairing the AWS API returns).
//  2. Subnet-scoped ALLOWS edges (EdgeAllowsIngressFrom /
//     EdgeAllowsEgressTo) for every ALLOW rule, from the subnet to a
//     CIDR sentinel. DENY rules are NOT emitted as edges — the
//     analyzer consults rule_number ordering from the metadata below
//     and short-circuits deny decisions inline.
//
// Edge metadata marks is_nacl=true and stamps rule_number on the
// Evidence so the analyzer can rank rules by their numeric priority.
func resolveNetworkACLRules(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	nacls, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "network-acl"},
		"limit": 0,
	})
	if err != nil {
		return fmt.Errorf("aws nacl: query network acls: %w", err)
	}
	if len(nacls) == 0 {
		return nil
	}

	edges, cidrNodes := buildNACLEdges(nacls)
	if len(edges) == 0 {
		return nil
	}
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, sentinelCIDRNodes(cidrNodes), edges); err != nil {
		return fmt.Errorf("aws nacl: link batch: %w", err)
	}
	slog.Debug("aws nacl: emitted reachability edges", "count", len(edges))
	return nil
}

// buildNACLEdges is the pure core of resolveNetworkACLRules. It walks
// every NACL node and dispatches per-rule-type helpers that emit the
// association and rule edges implied by its entries.
func buildNACLEdges(nodes []*knowledgev1.Node) ([]knowledgev1.Edge, map[string]string) {
	var edges []knowledgev1.Edge
	cidrNodes := make(map[string]string)
	for _, nacl := range nodes {
		naclEdges := buildEdgesForNACL(nacl, cidrNodes)
		edges = append(edges, naclEdges...)
	}
	return edges, cidrNodes
}

// buildEdgesForNACL parses a single NACL node and returns every edge it
// implies. The cidrNodes map is mutated in place so callers can collect
// the distinct CIDR sentinels across the whole node set.
func buildEdgesForNACL(nacl *knowledgev1.Node, cidrNodes map[string]string) []knowledgev1.Edge {
	if len(nacl.Content) == 0 {
		return nil
	}
	var spec naclSpec
	if err := json.Unmarshal([]byte(nacl.Content), &spec); err != nil {
		slog.Debug("aws nacl: unmarshal content", "nacl", nacl.Id, "err", err)
		return nil
	}
	region := kgtypes.Value(nacl, "region")
	if region == "" {
		region = regionFromARN(nacl.Id)
	}
	accountID := accountFromARN(nacl.Id)
	if region == "" || accountID == "" {
		return nil
	}

	// Subnet → NACL association edges are emitted in collect-time
	// (cmd/knowledge/internal/collector/cloud/aws/networkacl.go:networkACLLocalEdges). Postpopulate
	// only adds the subnet-scoped ALLOWS edges that need CIDR sentinel writes.
	sort.SliceStable(spec.Entries, func(i, j int) bool {
		return ruleNumber(spec.Entries[i]) < ruleNumber(spec.Entries[j])
	})
	return buildNACLRuleEdges(spec.Entries, spec.Associations, region, accountID, cidrNodes)
}

// buildNACLRuleEdges returns one subnet-scoped ALLOWS edge per associated
// subnet per ALLOW rule. DENY rules and malformed entries are skipped.
// cidrNodes is mutated in place with every referenced CIDR sentinel.
func buildNACLRuleEdges(entries []naclEntry, assocs []naclAssociation, region, accountID string, cidrNodes map[string]string) []knowledgev1.Edge {
	var edges []knowledgev1.Edge
	for _, entry := range entries {
		if !naclEntryIsAllow(entry) {
			continue
		}
		cidr := *entry.CidrBlock
		sentinelID := cidrSentinelID(cidr)
		cidrNodes[sentinelID] = cidr

		md := naclEntryMetadata(entry, cidr)
		egress := entry.Egress != nil && *entry.Egress
		md.Egress = egress

		for _, assoc := range assocs {
			if assoc.SubnetId == nil || *assoc.SubnetId == "" {
				continue
			}
			subnetARN := ec2ARN(region, accountID, "subnet", *assoc.SubnetId)
			edges = append(edges, buildNACLEdge(subnetARN, sentinelID, egress, md, ruleNumber(entry)))
		}
	}
	return edges
}

// naclEntryIsAllow returns true when the entry is a well-formed ALLOW
// rule with a non-empty CIDR block. DENY rules and entries missing the
// required fields are rejected.
func naclEntryIsAllow(entry naclEntry) bool {
	if entry.RuleAction == nil || *entry.RuleAction != "allow" {
		return false
	}
	if entry.CidrBlock == nil || *entry.CidrBlock == "" {
		return false
	}
	return true
}

// naclEntryMetadata builds the shared sgRuleMetadata payload carried by
// every edge derived from the given NACL entry. Callers overwrite Egress
// to honor the per-direction flag on the entry itself.
func naclEntryMetadata(entry naclEntry, cidr string) sgRuleMetadata {
	md := sgRuleMetadata{
		Protocol: naclProtocolName(entry.Protocol),
		CIDR:     cidr,
		IsNACL:   true,
	}
	if entry.PortRange != nil {
		if entry.PortRange.From != nil {
			md.PortFrom = int(*entry.PortRange.From)
		}
		if entry.PortRange.To != nil {
			md.PortTo = int(*entry.PortRange.To)
		}
	}
	return md
}

// buildNACLEdge assembles a subnet-scoped ALLOWS edge with rule number
// stamped into the Evidence payload.
func buildNACLEdge(subnetID, peerID string, egress bool, md sgRuleMetadata, rn int) knowledgev1.Edge {
	edgeType := kgtypes.EdgeAllowsIngressFrom
	if egress {
		edgeType = kgtypes.EdgeAllowsEgressTo
	}

	// Inline extend the metadata with rule_number by re-marshaling a
	// small wrapper so we don't pollute sgRuleMetadata with NACL-only
	// fields.
	wrapper := struct {
		sgRuleMetadata
		RuleNumber int `json:"rule_number,omitempty"`
	}{
		sgRuleMetadata: md,
		RuleNumber:     rn,
	}
	evidence, err := json.Marshal(wrapper)
	if err != nil {
		evidence = []byte(`{"encode_error":true}`)
	}
	return knowledgev1.Edge{
		FromId:   subnetID,
		ToId:     peerID,
		Type:     string(edgeType),
		Method:   methodAWSNACL,
		Evidence: string(evidence),
	}
}

// ruleNumber returns the NACL entry's rule number or the sentinel
// 32767 (the AWS "default deny" rule number) when unset.
func ruleNumber(e naclEntry) int {
	if e.RuleNumber == nil {
		return 32767
	}
	return int(*e.RuleNumber)
}

// naclProtocolName maps the numeric protocol string on a NACL entry to
// the canonical AWS protocol name. "-1" or empty means all protocols.
func naclProtocolName(p *string) string {
	if p == nil {
		return ""
	}
	switch *p {
	case "-1", "":
		return ""
	case "1":
		return "icmp"
	case "6":
		return "tcp"
	case "17":
		return "udp"
	case "58":
		return "icmpv6"
	default:
		return *p
	}
}
