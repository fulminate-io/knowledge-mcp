// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// aws_sg_reachability_index.go builds the per-account reachability lookup
// structure consumed by the AWSSGReachabilityAnalyzer. The index walks:
//
//   - every cloud-resource node that can attach to a security group (EC2,
//     RDS, Lambda, ELBv2, ElastiCache, OpenSearch, EFS) — to populate the
//     resources map.
//   - every security-group node — cached so classifiers can cite SGs.
//   - every EdgeUsesSecurityGroup edge — to map each resource to its SGs.
//   - every SG's EdgeAllowsIngressFrom / EdgeAllowsEgressTo outgoing edges
//     — parsing Evidence as sgEdgeMetadata.
//   - every subnet's allow edges with is_nacl=true — NACL layer.
//   - every VPC's EdgePeeredWith edges — cross-VPC reachability set.
//
// Type declarations live in aws_sg_reachability_index_types.go; per-layer
// reachability filter functions live in aws_sg_reachability_reach.go.

// buildSGReachabilityIndex walks the scoped cloud graph and returns an
// sgReachabilityIndex ready for classification. The builder counts
// reachability-eligible resources BEFORE allocating the full index so the
// hard-cap path can short-circuit without paying the O(R * edges) walk
// cost. Exceeding sgReachabilityResourceCap returns a sentinel index with
// skipped=true and resourceCount populated.
func buildSGReachabilityIndex(ctx context.Context, scoped *cloudReader) (*sgReachabilityIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/aws_sg_reachability_index: %w", err)
	}
	if scoped == nil {
		return nil, fmt.Errorf("topology/aws_sg_reachability_index: scoped reader must not be nil")
	}

	allNodes, err := scoped.cloudResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list cloud resources: %w", err)
	}

	var resourceNodes []*knowledgev1.Node
	var sgNodes []*knowledgev1.Node
	var subnetNodes []*knowledgev1.Node
	var vpcNodes []*knowledgev1.Node
	for i := range allNodes {
		n := allNodes[i]
		switch rt := nodeMeta(n, "resource_type"); {
		case sgReachabilityResourceTypes[rt]:
			resourceNodes = append(resourceNodes, n)
		case rt == "security-group":
			sgNodes = append(sgNodes, n)
		case rt == "subnet":
			subnetNodes = append(subnetNodes, n)
		case rt == "vpc":
			vpcNodes = append(vpcNodes, n)
		}
	}
	resourceCount := len(resourceNodes)

	if resourceCount > sgReachabilityResourceCap {
		return &sgReachabilityIndex{
			skipped:       true,
			resourceCount: resourceCount,
		}, nil
	}

	idx := &sgReachabilityIndex{
		resources:     make(map[string]*resourceInfo, resourceCount),
		sgs:           make(map[string]*knowledgev1.Node, len(sgNodes)),
		sgIngress:     make(map[string][]sgAllowEntry, len(sgNodes)),
		sgEgress:      make(map[string][]sgAllowEntry, len(sgNodes)),
		naclIngress:   make(map[string][]sgAllowEntry, len(subnetNodes)),
		naclEgress:    make(map[string][]sgAllowEntry, len(subnetNodes)),
		vpcPeerings:   make(map[string]map[string]bool),
		resourceCount: resourceCount,
	}

	for i := range sgNodes {
		idx.sgs[sgNodes[i].Id] = sgNodes[i]
	}
	for i := range resourceNodes {
		n := resourceNodes[i]
		idx.resources[n.Id] = &resourceInfo{
			ID:                n.Id,
			Type:              nodeMeta(n, "resource_type"),
			VPC:               nodeMeta(n, "vpc_id"),
			Subnet:            nodeMeta(n, "subnet_id"),
			AllowsIngressFrom: map[string][]allowedRange{},
			AllowsEgressTo:    map[string][]allowedRange{},
		}
	}

	if err := populateSGAllowEdges(ctx, scoped, idx); err != nil {
		return nil, err
	}
	if err := populateNACLEdges(ctx, scoped, idx, subnetNodes); err != nil {
		return nil, err
	}
	if err := populateVPCPeerings(ctx, scoped, idx, vpcNodes, subnetNodes); err != nil {
		return nil, err
	}
	if err := populateResourceAttachments(ctx, scoped, idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// populateSGAllowEdges walks every SG's outbound EdgeAllowsIngressFrom and
// EdgeAllowsEgressTo edges, decoding Evidence and populating idx.sgIngress /
// idx.sgEgress. NACL edges (is_nacl=true) are skipped here — they are
// handled by populateNACLEdges which walks from subnet nodes.
func populateSGAllowEdges(ctx context.Context, scoped *cloudReader, idx *sgReachabilityIndex) error {
	for sgID := range idx.sgs {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("topology/aws_sg_reachability_index: %w", err)
		}
		ingress, _ := scoped.iterEdges(ctx, sgID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeAllowsIngressFrom})
		for _, e := range ingress {
			meta := parseSGEdgeMetadata(e.Evidence)
			if meta.IsNACL {
				continue
			}
			idx.sgIngress[sgID] = append(idx.sgIngress[sgID], sgAllowEntry{
				PeerID: e.ToId,
				Range:  allowedRange{Protocol: meta.Protocol, PortFrom: meta.PortFrom, PortTo: meta.PortTo, CIDR: meta.CIDR},
			})
		}
		egress, _ := scoped.iterEdges(ctx, sgID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeAllowsEgressTo})
		for _, e := range egress {
			meta := parseSGEdgeMetadata(e.Evidence)
			if meta.IsNACL {
				continue
			}
			idx.sgEgress[sgID] = append(idx.sgEgress[sgID], sgAllowEntry{
				PeerID: e.ToId,
				Range:  allowedRange{Protocol: meta.Protocol, PortFrom: meta.PortFrom, PortTo: meta.PortTo, CIDR: meta.CIDR},
			})
		}
	}
	return nil
}

// populateNACLEdges walks every subnet's outbound EdgeAllowsIngressFrom and
// EdgeAllowsEgressTo edges whose Evidence has is_nacl=true and populates
// idx.naclIngress / idx.naclEgress.
func populateNACLEdges(ctx context.Context, scoped *cloudReader, idx *sgReachabilityIndex, subnets []*knowledgev1.Node) error {
	for i := range subnets {
		subnetID := subnets[i].Id
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("topology/aws_sg_reachability_index: %w", err)
		}
		ingress, _ := scoped.iterEdges(ctx, subnetID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeAllowsIngressFrom})
		for _, e := range ingress {
			meta := parseSGEdgeMetadata(e.Evidence)
			if !meta.IsNACL {
				continue
			}
			idx.naclIngress[subnetID] = append(idx.naclIngress[subnetID], sgAllowEntry{
				PeerID: e.ToId,
				Range:  allowedRange{Protocol: meta.Protocol, PortFrom: meta.PortFrom, PortTo: meta.PortTo, CIDR: meta.CIDR},
				IsNACL: true,
			})
		}
		egress, _ := scoped.iterEdges(ctx, subnetID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeAllowsEgressTo})
		for _, e := range egress {
			meta := parseSGEdgeMetadata(e.Evidence)
			if !meta.IsNACL {
				continue
			}
			idx.naclEgress[subnetID] = append(idx.naclEgress[subnetID], sgAllowEntry{
				PeerID: e.ToId,
				Range:  allowedRange{Protocol: meta.Protocol, PortFrom: meta.PortFrom, PortTo: meta.PortTo, CIDR: meta.CIDR},
				IsNACL: true,
			})
		}
	}
	return nil
}

// populateVPCPeerings walks every VPC and subnet node's outbound
// cross-VPC connectivity edges (EdgePeeredWith for VPCs, EdgeRoutesVia for
// subnets, EdgeExposedVia for services) and records the resulting VPC ↔
// VPC reachability set. Any of the three edges is sufficient proof that
// traffic can cross between the two VPCs at the routing layer.
func populateVPCPeerings(ctx context.Context, scoped *cloudReader, idx *sgReachabilityIndex, vpcs, subnets []*knowledgev1.Node) error {
	walk := func(nodeID string, edgeType kgtypes.EdgeType) {
		edges, _ := scoped.iterEdges(ctx, nodeID, outgoingEdges, []kgtypes.EdgeType{edgeType})
		for _, e := range edges {
			if idx.vpcPeerings[e.FromId] == nil {
				idx.vpcPeerings[e.FromId] = map[string]bool{}
			}
			idx.vpcPeerings[e.FromId][e.ToId] = true
		}
	}
	for i := range vpcs {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("topology/aws_sg_reachability_index: %w", err)
		}
		walk(vpcs[i].Id, kgtypes.EdgePeeredWith)
	}
	for i := range subnets {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("topology/aws_sg_reachability_index: %w", err)
		}
		walk(subnets[i].Id, kgtypes.EdgeRoutesVia)
		walk(subnets[i].Id, kgtypes.EdgeExposedVia)
	}
	return nil
}

// populateResourceAttachments walks each resource's outbound
// EdgeUsesSecurityGroup and EdgeUsesSubnet edges, then resolves each
// subnet's EdgeUsesNetwork edge to recover the VPC. Populates
// resourceInfo.SGs, Subnet, VPC, plus the aggregated AllowsIngressFrom /
// AllowsEgressTo maps by copying rules from every attached SG.
//
// Fallback: when a resource's node metadata carries "vpc_id" or
// "subnet_id" (the fixture helper contract), that takes precedence over
// edge-based resolution — this keeps the test fixtures terse without
// forcing every test to emit explicit subnet/VPC nodes and edges.
func populateResourceAttachments(ctx context.Context, scoped *cloudReader, idx *sgReachabilityIndex) error {
	for id, info := range idx.resources {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("topology/aws_sg_reachability_index: %w", err)
		}
		if err := populateSGAttachments(ctx, scoped, idx, id, info); err != nil {
			return err
		}
		if err := populateSubnetAttachment(ctx, scoped, info); err != nil {
			return err
		}
	}
	return nil
}

// populateSGAttachments walks the EdgeUsesSecurityGroup edges from a
// single resource, copies rules from each attached SG into the
// resource's aggregate allow maps, populates info.SGs, and builds the
// pre-indexed ingress/egress lookups (info.ingressBySGPeer /
// ingressCIDRIndex / egressBySGPeer / egressCIDRIndex). The pre-indexes
// pay a small per-resource construction cost up front so the matrix
// emitter's hot path becomes O(1) bucket lookup + O(small slice) port
// scan instead of O(M) map iteration per (src, dst, probe) call.
func populateSGAttachments(ctx context.Context, scoped *cloudReader, idx *sgReachabilityIndex, id string, info *resourceInfo) error {
	edges, _ := scoped.iterEdges(ctx, id, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeUsesSecurityGroup})
	for _, e := range edges {
		info.SGs = append(info.SGs, e.ToId)
		for _, entry := range idx.sgIngress[e.ToId] {
			info.AllowsIngressFrom[entry.PeerID] = append(info.AllowsIngressFrom[entry.PeerID], entry.Range)
			addToDirectionalIndex(entry, &info.ingressCIDRIndex, &info.ingressBySGPeer)
			info.hasIngressRules = true
		}
		for _, entry := range idx.sgEgress[e.ToId] {
			info.AllowsEgressTo[entry.PeerID] = append(info.AllowsEgressTo[entry.PeerID], entry.Range)
			addToDirectionalIndex(entry, &info.egressCIDRIndex, &info.egressBySGPeer)
			info.hasEgressRules = true
		}
	}
	finalizeRuleIndexes(info)
	return nil
}

// populateSubnetAttachment resolves the primary subnet and VPC for a
// resource. Metadata ("subnet_id" / "vpc_id") wins when set (fixture
// shortcut); otherwise walks EdgeUsesSubnet forward to find a subnet and
// then the subnet's EdgeUsesNetwork edge to find the VPC.
func populateSubnetAttachment(ctx context.Context, scoped *cloudReader, info *resourceInfo) error {
	if info.Subnet != "" && info.VPC != "" {
		return nil
	}
	subnetEdges, _ := scoped.iterEdges(ctx, info.ID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeUsesSubnet})
	for _, e := range subnetEdges {
		if info.Subnet == "" {
			info.Subnet = e.ToId
		}
		if info.VPC != "" {
			continue
		}
		netEdges, _ := scoped.iterEdges(ctx, e.ToId, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeUsesNetwork})
		for _, ve := range netEdges {
			info.VPC = ve.ToId
			break
		}
	}
	return nil
}
