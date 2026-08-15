// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// methodAWSCrossVPC is the Edge.Method discriminator stamped on cross-VPC
// reachability edges produced by resolveCrossVpcSgReferences.
const methodAWSCrossVPC = "aws-cross-vpc"

// vpcPeeringSpec models the subset of DescribeVpcPeeringConnections
// response fields this resolver needs.
type vpcPeeringSpec struct {
	VpcPeeringConnectionId *string            `json:"VpcPeeringConnectionId,omitempty"`
	Status                 *vpcPeeringStatus  `json:"Status,omitempty"`
	RequesterVpcInfo       *vpcPeeringVpcInfo `json:"RequesterVpcInfo,omitempty"`
	AccepterVpcInfo        *vpcPeeringVpcInfo `json:"AccepterVpcInfo,omitempty"`
}

type vpcPeeringStatus struct {
	Code *string `json:"Code,omitempty"`
}

type vpcPeeringVpcInfo struct {
	VpcId     *string `json:"VpcId,omitempty"`
	OwnerId   *string `json:"OwnerId,omitempty"`
	CidrBlock *string `json:"CidrBlock,omitempty"`
}

// resolveCrossVpcSgReferences wires cross-VPC SG-to-SG references and
// route-table-based peer reachability into the graph. The resolver is
// intentionally minimal — it focuses on emitting peering edges between
// the VPCs that own the SGs so downstream analyzers can answer "is
// there a valid network path between these two SGs?" by walking
// EdgePeeredWith + EdgeUsesNetwork.
//
// Steps:
//
//  1. Build an index of active VPC peering connections
//     (vpc_peering nodes) keyed by (vpcA, vpcB) -> peering ID.
//  2. For each peering entry, emit a bidirectional EdgePeeredWith edge
//     between the two VPC nodes (the collector has already written
//     both VPCs).
//  3. For each SG-to-SG rule crossing VPC boundaries (the UserIdGroupPair
//     lists a VpcId different from the host SG's VpcId), verify a
//     peering edge exists and, if so, emit an EdgeAllowsIngressFrom /
//     EdgeAllowsEgressTo edge stamped with "cross_vpc":true metadata.
//
// Missing peering data is tolerated — the resolver logs and skips.
func resolveCrossVpcSgReferences(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	// Read every collected cloud account's node-ID set once so cross-account
	// VPC existence checks are set lookups (replaces the per-VPC by-id graph
	// query). Keyed by account ID; includes the local account.
	accountNodeIDs := buildAllAccountNodeSets(ctx, gc)

	peerEdges, peerIndex, err := buildPeeringEdges(ctx, gc, graphName, accountNodeIDs)
	if err != nil {
		return err
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, peerEdges); err != nil {
		slog.Warn("aws crossvpc: link peering edges", "err", err)
	}

	crossEdges, err := buildCrossVpcSgEdges(ctx, gc, graphName, peerIndex)
	if err != nil {
		return err
	}
	if len(crossEdges) == 0 {
		return nil
	}
	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, crossEdges); err != nil {
		return fmt.Errorf("aws crossvpc: link cross-vpc edges: %w", err)
	}
	slog.Debug("aws crossvpc: emitted edges", "peering", len(peerEdges), "cross_sg", len(crossEdges))
	return nil
}

// buildAllAccountNodeSets reads every collected cloud account's cloud graph once
// and returns account ID → set of node IDs present in that graph. Cross-account
// existence checks (e.g. peer VPC resolvable?) become set lookups instead of a
// per-id by-id graph query.
func buildAllAccountNodeSets(ctx context.Context, gc postpopulate.GraphCaller) map[string]map[string]struct{} {
	names, err := postpopulate.ListGraphNames(ctx, gc, kgtypes.GraphCloud)
	if err != nil {
		slog.Warn("aws crossvpc: list cloud graphs failed", "err", err)
		return nil
	}
	out := make(map[string]map[string]struct{})
	for _, name := range names {
		if name == "" {
			continue
		}
		nodes, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, name, map[string]any{
			"type": string(kgtypes.NodeCloudResource),
		})
		if err != nil {
			slog.Debug("aws crossvpc: browse account failed", "account", name, "err", err)
			continue
		}
		ids := make(map[string]struct{}, len(nodes))
		for _, n := range nodes {
			ids[n.Id] = struct{}{}
		}
		out[name] = ids
	}
	return out
}

// buildPeeringEdges walks every vpc-peering-connection node and emits a
// bidirectional EdgePeeredWith edge between the two VPCs involved.
// The returned peerIndex maps a VPC id to the set of peer VPC ids it
// has an active peering with — callers use it to validate cross-VPC
// SG references.
func buildPeeringEdges(ctx context.Context, gc postpopulate.GraphCaller, graphName string, accountNodeIDs map[string]map[string]struct{}) ([]knowledgev1.Edge, map[string]map[string]bool, error) {
	peerings, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "vpc-peering-connection"},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("aws crossvpc: query peerings: %w", err)
	}

	var edges []knowledgev1.Edge
	index := make(map[string]map[string]bool)

	for _, node := range peerings {
		pEdges, pIdx := peeringEdgesForNode(node, accountNodeIDs)
		edges = append(edges, pEdges...)
		for k, v := range pIdx {
			if index[k] == nil {
				index[k] = make(map[string]bool)
			}
			for kk := range v {
				index[k][kk] = true
			}
		}
	}
	return edges, index, nil
}

// peeringEdgesForNode parses a single vpc-peering-connection node and returns
// bidirectional PEERED_WITH edges. Uses OwnerId from RequesterVpcInfo /
// AccepterVpcInfo to construct correct per-account ARNs for cross-account
// peerings. Falls back to the peering node's own account when OwnerId is absent.
func peeringEdgesForNode(node *knowledgev1.Node, accountNodeIDs map[string]map[string]struct{}) ([]knowledgev1.Edge, map[string]map[string]bool) {
	if len(node.Content) == 0 {
		return nil, nil
	}
	var spec vpcPeeringSpec
	if err := json.Unmarshal([]byte(node.Content), &spec); err != nil {
		slog.Debug("aws crossvpc: unmarshal peering content", "id", node.Id, "err", err)
		return nil, nil
	}
	if spec.Status == nil || spec.Status.Code == nil || *spec.Status.Code != "active" {
		return nil, nil
	}
	if spec.RequesterVpcInfo == nil || spec.AccepterVpcInfo == nil {
		return nil, nil
	}
	reqVPC := deref(spec.RequesterVpcInfo.VpcId)
	accVPC := deref(spec.AccepterVpcInfo.VpcId)
	if reqVPC == "" || accVPC == "" {
		return nil, nil
	}

	region := kgtypes.Value(node, "region")
	if region == "" {
		region = regionFromARN(node.Id)
	}
	nodeAccount := accountFromARN(node.Id)
	if region == "" || nodeAccount == "" {
		return nil, nil
	}

	reqAccount := derefOrDefault(spec.RequesterVpcInfo.OwnerId, nodeAccount)
	accAccount := derefOrDefault(spec.AccepterVpcInfo.OwnerId, nodeAccount)

	reqARN := ec2ARN(region, reqAccount, "vpc", reqVPC)
	accARN := ec2ARN(region, accAccount, "vpc", accVPC)

	// For cross-account peerings, verify both VPC nodes are resolvable.
	if reqAccount != accAccount && !crossAccountVPCResolvable(accountNodeIDs, reqAccount, reqARN, accAccount, accARN) {
		return nil, nil
	}

	index := map[string]map[string]bool{
		reqVPC: {accVPC: true},
		accVPC: {reqVPC: true},
	}

	evidence := peeringMetadata(spec)

	edges := []knowledgev1.Edge{
		{FromId: reqARN, ToId: accARN, Type: string(kgtypes.EdgePeeredWith), Method: methodAWSCrossVPC, Evidence: evidence},
		{FromId: accARN, ToId: reqARN, Type: string(kgtypes.EdgePeeredWith), Method: methodAWSCrossVPC, Evidence: evidence},
	}

	// EdgeRoutesToPeer: each VPC can route to the peer's CIDR via this peering.
	// These edges answer "what CIDRs can this VPC reach via peering connections?"
	reqCIDR := deref(spec.RequesterVpcInfo.CidrBlock)
	accCIDR := deref(spec.AccepterVpcInfo.CidrBlock)
	if reqCIDR != "" && accCIDR != "" {
		edges = append(edges,
			knowledgev1.Edge{FromId: reqARN, ToId: accCIDR, Type: string(kgtypes.EdgeRoutesToPeer), Method: methodAWSCrossVPC, Evidence: evidence},
			knowledgev1.Edge{FromId: accARN, ToId: reqCIDR, Type: string(kgtypes.EdgeRoutesToPeer), Method: methodAWSCrossVPC, Evidence: evidence},
		)
	}

	return edges, index
}

// peeringMetadata serializes cross-VPC peering metadata as a JSON string
// for the Edge.Evidence field. Includes owner IDs, CIDR blocks, and status.
func peeringMetadata(spec vpcPeeringSpec) string {
	meta := map[string]string{}
	if spec.RequesterVpcInfo != nil {
		if spec.RequesterVpcInfo.OwnerId != nil {
			meta["requester_owner"] = *spec.RequesterVpcInfo.OwnerId
		}
		if spec.RequesterVpcInfo.CidrBlock != nil {
			meta["requester_cidr"] = *spec.RequesterVpcInfo.CidrBlock
		}
	}
	if spec.AccepterVpcInfo != nil {
		if spec.AccepterVpcInfo.OwnerId != nil {
			meta["accepter_owner"] = *spec.AccepterVpcInfo.OwnerId
		}
		if spec.AccepterVpcInfo.CidrBlock != nil {
			meta["accepter_cidr"] = *spec.AccepterVpcInfo.CidrBlock
		}
	}
	if spec.Status != nil && spec.Status.Code != nil {
		meta["status_code"] = *spec.Status.Code
	}
	if len(meta) == 0 {
		return ""
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(raw)
}

// derefOrDefault returns the value of a string pointer or the fallback.
func derefOrDefault(s *string, fallback string) string {
	if s == nil || *s == "" {
		return fallback
	}
	return *s
}

// crossAccountVPCResolvable checks that both VPC nodes exist in their
// respective account graphs, using the prefetched per-account node-ID sets
// (one browse per account, built by buildAllAccountNodeSets) instead of a
// per-id by-id graph query.
func crossAccountVPCResolvable(accountNodeIDs map[string]map[string]struct{}, reqAccount, reqARN, accAccount, accARN string) bool {
	return vpcNodeExists(accountNodeIDs, reqAccount, reqARN) &&
		vpcNodeExists(accountNodeIDs, accAccount, accARN)
}

// vpcNodeExists reports whether a VPC node id is present in its account's
// prefetched node-ID set.
func vpcNodeExists(accountNodeIDs map[string]map[string]struct{}, vpcAccount, vpcARN string) bool {
	ids, ok := accountNodeIDs[vpcAccount]
	if !ok {
		return false
	}
	_, exists := ids[vpcARN]
	return exists
}

// buildCrossVpcSgEdges walks every security-group node, looks for SG
// rules whose UserIdGroupPair references a peer in a different VPC,
// validates the peering, and emits cross-VPC reachability edges.
func buildCrossVpcSgEdges(ctx context.Context, gc postpopulate.GraphCaller, graphName string, peerIndex map[string]map[string]bool) ([]knowledgev1.Edge, error) {
	sgs, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "security-group"},
	})
	if err != nil {
		return nil, fmt.Errorf("aws crossvpc: query sgs: %w", err)
	}

	var edges []knowledgev1.Edge
	for _, sg := range sgs {
		if len(sg.Content) == 0 {
			continue
		}
		var spec sgSpec
		if err := json.Unmarshal([]byte(sg.Content), &spec); err != nil {
			continue
		}
		hostVPC := deref(spec.VpcId)
		if hostVPC == "" {
			continue
		}
		edges = append(edges, crossVpcEdgesForPerms(sg, hostVPC, spec.IpPermissions, false, peerIndex)...)
		edges = append(edges, crossVpcEdgesForPerms(sg, hostVPC, spec.IpPermissionsEgress, true, peerIndex)...)
	}
	return edges, nil
}

// crossVpcEdgesForPerms returns the cross-VPC edges implied by one
// IpPermissions array. Only rules whose peer references a VPC that is
// actively peered with the host VPC produce edges.
func crossVpcEdgesForPerms(sg *knowledgev1.Node, hostVPC string, perms []sgIPPermission, egress bool, peerIndex map[string]map[string]bool) []knowledgev1.Edge {
	var out []knowledgev1.Edge
	region := kgtypes.Value(sg, "region")
	if region == "" {
		region = regionFromARN(sg.Id)
	}
	hostAccount := accountFromARN(sg.Id)
	if region == "" || hostAccount == "" {
		return nil
	}
	for _, perm := range perms {
		for _, peer := range perm.UserIdGroupPairs {
			peerVPC := deref(peer.VpcId)
			if peerVPC == "" || peerVPC == hostVPC {
				continue // same-VPC handled by postpopulate_sg.go
			}
			if !peerIndex[hostVPC][peerVPC] {
				continue
			}
			if peer.GroupId == nil || *peer.GroupId == "" {
				continue
			}
			// Use UserId (peer account) when available for correct
			// cross-account SG ARN construction.
			peerAccount := derefOrDefault(peer.UserId, hostAccount)
			peerARN := ec2ARN(region, peerAccount, "security-group", *peer.GroupId)
			md := baseMetadata(perm, egress)
			edgeType := kgtypes.EdgeAllowsIngressFrom
			if egress {
				edgeType = kgtypes.EdgeAllowsEgressTo
			}
			out = append(out, knowledgev1.Edge{
				FromId:   sg.Id,
				ToId:     peerARN,
				Type:     string(edgeType),
				Method:   methodAWSCrossVPC,
				Evidence: md.encode(),
			})
		}
	}
	return out
}

// deref returns the value of a string pointer or "".
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
