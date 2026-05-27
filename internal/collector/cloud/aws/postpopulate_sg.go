// SPDX-License-Identifier: Apache-2.0

package aws

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

// methodAWSSGRule is the Edge.Method discriminator stamped on every
// reachability edge produced by resolveSecurityGroupRules so downstream
// consumers can recognize the sgRuleMetadata schema below.
const methodAWSSGRule = "aws-sg-rule"

// resourceTypeCIDRBlock is the resource_type value used for sentinel
// CIDR nodes. SG rules that reference a bare CIDR range (no source SG)
// are wired to a sentinel node so both endpoints of an ALLOWS edge exist
// in the graph. This keeps the SG reachability analyzer's lookup path
// uniform: every edge endpoint is a *knowledgev1.Node query away, whether
// the "peer" is another SG or an arbitrary CIDR range.
const resourceTypeCIDRBlock = "cidr-block"

// sgSpec is a partial JSON schema covering the fields of the AWS
// DescribeSecurityGroups response that the reachability analyzer cares
// about. It mirrors the ec2/types.SecurityGroup struct so a SG node's
// Content blob (produced by securityGroupCollector) round-trips cleanly.
type sgSpec struct {
	VpcId               *string          `json:"VpcId,omitempty"`
	IpPermissions       []sgIPPermission `json:"IpPermissions,omitempty"`
	IpPermissionsEgress []sgIPPermission `json:"IpPermissionsEgress,omitempty"`
}

// sgIPPermission models a single ingress or egress rule entry. Fields
// are named to match the JSON tags that aws-sdk-go-v2 produces when it
// marshals an ec2types.IpPermission.
type sgIPPermission struct {
	IpProtocol       *string             `json:"IpProtocol,omitempty"`
	FromPort         *int32              `json:"FromPort,omitempty"`
	ToPort           *int32              `json:"ToPort,omitempty"`
	IpRanges         []sgIPRange         `json:"IpRanges,omitempty"`
	Ipv6Ranges       []sgIPv6Range       `json:"Ipv6Ranges,omitempty"`
	UserIdGroupPairs []sgUserIDGroupPair `json:"UserIdGroupPairs,omitempty"`
}

// sgIPRange models a single CIDR entry in an IpPermission.
type sgIPRange struct {
	CidrIp *string `json:"CidrIp,omitempty"`
}

// sgIPv6Range models a single IPv6 CIDR entry.
type sgIPv6Range struct {
	CidrIpv6 *string `json:"CidrIpv6,omitempty"`
}

// sgUserIDGroupPair references a peer security group by ID (+ VPC id for
// cross-VPC references). For same-VPC rules, VpcId is empty and the
// reference resolves to any SG in the current VPC with the given GroupId.
type sgUserIDGroupPair struct {
	GroupId *string `json:"GroupId,omitempty"`
	UserId  *string `json:"UserId,omitempty"`
	VpcId   *string `json:"VpcId,omitempty"`
}

// sgRuleMetadata is the decoded form of Edge.Evidence for SG rule edges.
// An empty/zero value signals "all ports, all protocols" (fully open).
type sgRuleMetadata struct {
	Protocol string `json:"protocol,omitempty"`
	PortFrom int    `json:"port_from,omitempty"`
	PortTo   int    `json:"port_to,omitempty"`
	CIDR     string `json:"cidr,omitempty"`
	IsNACL   bool   `json:"is_nacl,omitempty"`
	// Egress is true for EdgeAllowsEgressTo edges that logically target a
	// CIDR or cross-VPC SG — helps downstream code distinguish layer
	// without walking back to the edge type.
	Egress bool `json:"egress,omitempty"`
}

// encode returns the compact JSON string stored on Edge.Evidence. A
// fully-open metadata value encodes to the empty string so the common
// case skips the JSON allocation.
func (m sgRuleMetadata) encode() string {
	if m.Protocol == "" && m.PortFrom == 0 && m.PortTo == 0 &&
		m.CIDR == "" && !m.IsNACL && !m.Egress {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return `{"encode_error":true}`
	}
	return string(b)
}

// resolveSecurityGroupRules walks every security-group node in the
// cloud graph, parses its IpPermissions/IpPermissionsEgress JSON, and
// emits EdgeAllowsIngressFrom (dst_sg → src) / EdgeAllowsEgressTo
// (src_sg → dst) edges. SG-to-SG references emit SG→SG edges; CIDR
// rules emit edges to a stable "aws:cidr:<cidr>" sentinel node created
// on demand so LinkBatch sees both endpoints. Edge metadata stores
// protocol, port range, and CIDR text so the analyzer can evaluate
// port intersections without re-parsing the SG Content blob.
func resolveSecurityGroupRules(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	sgs, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type":  string(kgtypes.NodeCloudResource),
		"meta":  map[string]string{"resource_type": "security-group"},
		"limit": 0,
	})
	if err != nil {
		return fmt.Errorf("aws sg: query security groups: %w", err)
	}
	if len(sgs) == 0 {
		return nil
	}

	edges, cidrNodes := buildSGRuleEdges(sgs)
	if len(edges) == 0 {
		return nil
	}
	// CIDR sentinel nodes the edges reference + the edges land in ONE
	// create_batch so both endpoints exist for every edge.
	if err := postpopulate.LinkNodesAndEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, sentinelCIDRNodes(cidrNodes), edges); err != nil {
		return fmt.Errorf("aws sg: link batch: %w", err)
	}
	slog.Debug("aws sg: emitted reachability edges", "count", len(edges))
	return nil
}

// buildSGRuleEdges is the pure (testable) core of resolveSecurityGroupRules.
// It returns every edge implied by the given SG nodes plus the set of
// distinct CIDR sentinel node IDs that must exist before LinkBatch runs.
func buildSGRuleEdges(nodes []*knowledgev1.Node) ([]knowledgev1.Edge, map[string]string) {
	var edges []knowledgev1.Edge
	cidrNodes := make(map[string]string)
	for _, sg := range nodes {
		if len(sg.Content) == 0 {
			continue
		}
		var spec sgSpec
		if err := json.Unmarshal([]byte(sg.Content), &spec); err != nil {
			slog.Debug("aws sg: unmarshal sg content", "sg", sg.Id, "err", err)
			continue
		}
		edges = append(edges, edgesForPermissions(sg, spec.IpPermissions, false, cidrNodes)...)
		edges = append(edges, edgesForPermissions(sg, spec.IpPermissionsEgress, true, cidrNodes)...)
	}
	return edges, cidrNodes
}

// edgesForPermissions returns the edges implied by one IpPermissions
// array. egress=false emits ingress edges; egress=true emits egress
// edges.
func edgesForPermissions(sg *knowledgev1.Node, perms []sgIPPermission, egress bool, cidrNodes map[string]string) []knowledgev1.Edge {
	var out []knowledgev1.Edge
	for _, perm := range perms {
		base := baseMetadata(perm, egress)
		for _, peer := range perm.UserIdGroupPairs {
			peerARN := sgPeerARN(sg, peer)
			if peerARN == "" {
				continue
			}
			out = append(out, buildSGEdge(sg.Id, peerARN, egress, base))
		}
		for _, cidr := range extractCIDRs(perm) {
			sentinelID := cidrSentinelID(cidr)
			cidrNodes[sentinelID] = cidr
			md := base
			md.CIDR = cidr
			out = append(out, buildSGEdge(sg.Id, sentinelID, egress, md))
		}
	}
	return out
}

// extractCIDRs collapses the IPv4 and IPv6 CIDR slices on an IpPermission
// into a single flat list, dropping nil pointers and empty strings.
func extractCIDRs(perm sgIPPermission) []string {
	var out []string
	for _, ipr := range perm.IpRanges {
		if ipr.CidrIp != nil && *ipr.CidrIp != "" {
			out = append(out, *ipr.CidrIp)
		}
	}
	for _, ipr := range perm.Ipv6Ranges {
		if ipr.CidrIpv6 != nil && *ipr.CidrIpv6 != "" {
			out = append(out, *ipr.CidrIpv6)
		}
	}
	return out
}

// baseMetadata extracts port/protocol info from an IpPermission into a
// partially filled sgRuleMetadata. Egress is stamped separately so the
// caller can reuse this base across SG-ref / CIDR edges.
func baseMetadata(perm sgIPPermission, egress bool) sgRuleMetadata {
	var md sgRuleMetadata
	if perm.IpProtocol != nil {
		p := *perm.IpProtocol
		// AWS uses "-1" for "all protocols" — normalize to empty.
		if p != "-1" {
			md.Protocol = p
		}
	}
	if perm.FromPort != nil {
		md.PortFrom = int(*perm.FromPort)
	}
	if perm.ToPort != nil {
		md.PortTo = int(*perm.ToPort)
	}
	md.Egress = egress
	return md
}

// buildSGEdge assembles the directional Edge value. Ingress edges go
// from the target SG (the one declaring the rule) back to the source
// peer; egress edges go from the source SG out to the destination peer.
func buildSGEdge(sgID, peerID string, egress bool, md sgRuleMetadata) knowledgev1.Edge {
	edgeType := kgtypes.EdgeAllowsIngressFrom
	from, to := sgID, peerID
	if egress {
		edgeType = kgtypes.EdgeAllowsEgressTo
		// egress direction: declarer → dst (already sgID → peerID)
	}
	return knowledgev1.Edge{
		FromId:   from,
		ToId:     to,
		Type:     string(edgeType),
		Method:   methodAWSSGRule,
		Evidence: md.encode(),
	}
}

// sgPeerARN resolves a UserIdGroupPair into the ARN of the referenced
// security group. If the peer lives in a different VPC, the crossvpc
// helper handles it later — here we still emit an edge because the SG
// ARN is computable from the GroupId alone (ARNs embed account+region,
// and within one collection run both SGs live under the same account).
func sgPeerARN(host *knowledgev1.Node, peer sgUserIDGroupPair) string {
	if peer.GroupId == nil || *peer.GroupId == "" {
		return ""
	}
	region := kgtypes.Value(host, "region")
	if region == "" {
		// Fall back to parsing the ARN: arn:aws:ec2:<region>:<acct>:security-group/<id>
		region = regionFromARN(host.Id)
	}
	accountID := accountFromARN(host.Id)
	if region == "" || accountID == "" {
		return ""
	}
	return ec2ARN(region, accountID, "security-group", *peer.GroupId)
}

// cidrSentinelID returns the stable cloud-resource ID for a CIDR range.
// Both IPv4 and IPv6 ranges go through the same helper.
func cidrSentinelID(cidr string) string {
	return "aws:cidr:" + cidr
}

// sentinelCIDRNodes materializes the sentinel cloud-resource nodes for every
// distinct CIDR referenced by any SG/NACL rule. The CIDR sentinels are emitted
// in the SAME create_batch as the edges that reference them (via
// LinkNodesAndEdgesBatch) so both endpoints of every ALLOWS edge exist; a
// re-emitted sentinel is an idempotent upsert keyed by ID. Returns nil for an
// empty set (no nodes to materialize).
func sentinelCIDRNodes(cidrNodes map[string]string) []*knowledgev1.Node {
	if len(cidrNodes) == 0 {
		return nil
	}
	out := make([]*knowledgev1.Node, 0, len(cidrNodes))
	for id, cidr := range cidrNodes {
		node := &knowledgev1.Node{
			Id:         id,
			Type:       string(kgtypes.NodeCloudResource),
			SymbolName: cidr,
			Summary:    resourceTypeCIDRBlock + " " + cidr,
		}
		kgtypes.SetValue(node, "resource_type", resourceTypeCIDRBlock)
		kgtypes.SetValue(node, "cidr", cidr)
		out = append(out, node)
	}
	return out
}

// regionFromARN extracts the region segment from an AWS EC2 ARN of
// the form arn:aws:ec2:<region>:<acct>:<resource-type>/<id>. Returns
// an empty string on any parsing failure.
func regionFromARN(arn string) string {
	segs := strings.SplitN(arn, ":", 6)
	if len(segs) < 4 {
		return ""
	}
	return segs[3]
}

// accountFromARN extracts the account ID segment from an AWS ARN.
func accountFromARN(arn string) string {
	segs := strings.SplitN(arn, ":", 6)
	if len(segs) < 5 {
		return ""
	}
	return segs[4]
}
