// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// orphan_rules_aws.go registers the v1 AWS orphan rules:
//
//   - elbv2-loadbalancer  → no outgoing TARGETS edges       (conf 1.0)
//   - elbv2-targetgroup   → no inbound  TARGETS edges       (conf 0.9)
//   - ebs-volume          → no outgoing BOUND_TO edges      (conf 1.0)
//   - security-group      → no inbound  USES_SECURITY_GROUP (conf 0.9)
//   - iam-role            → no inbound  ASSUMES_ROLE in own
//                            account AND no inbound from
//                            any other cloud account        (conf 0.7)
//   - iam-policy          → no inbound  GRANTS edges        (conf 0.9)
//
// Cross-account behavior: the iam-role rule walks every other cloud graph via
// foundation.FetchGraphNames(GraphCloud) and looks for inbound ASSUMES_ROLE
// edges to the candidate role. This catches roles that are referenced by
// trust policies in another account (a common production case for
// cross-account access). The rule deliberately skips IAM policy JSON parsing;
// the 0.7 confidence reflects the remaining uncertainty.

// Confidence constants — single source of truth so future tuning is one edit.
const (
	confidenceELBLoadBalancer = 1.0
	confidenceELBTargetGroup  = 0.9
	confidenceEBSVolume       = 1.0
	confidenceSecurityGroup   = 0.9
	confidenceIAMRole         = 0.7
	confidenceIAMPolicy       = 0.9
)

// elbv2LoadBalancerRule reports an ELBv2 load balancer as orphaned when it
// has no outgoing TARGETS edges. The collector emits LB → TG edges via
// EdgeTargets, so a load balancer with zero TG attachments is, by
// definition, routing traffic nowhere.
func elbv2LoadBalancerRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeTargets) {
		return false, confidenceELBLoadBalancer, "", nil
	}
	return true, confidenceELBLoadBalancer,
		fmt.Sprintf("Load balancer %s has no target groups attached.", displayName(node)),
		nil
}

// elbv2TargetGroupRule reports an ELBv2 target group as orphaned when no
// load balancer points at it. The collector creates LB → TG edges via
// EdgeTargets, so we look for inbound TARGETS edges on the target group.
func elbv2TargetGroupRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeTargets) {
		return false, confidenceELBTargetGroup, "", nil
	}
	return true, confidenceELBTargetGroup,
		fmt.Sprintf("Target group %s is not attached to any load balancer.", displayName(node)),
		nil
}

// ebsVolumeRule reports an EBS volume as orphaned when it has no outgoing
// BOUND_TO edge to any EC2 instance. The collector emits Volume → Instance
// edges from each volume.Attachments entry, so an unattached volume has
// zero outgoing BOUND_TO edges.
func ebsVolumeRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeBoundTo) {
		return false, confidenceEBSVolume, "", nil
	}
	return true, confidenceEBSVolume,
		fmt.Sprintf("EBS volume %s has no instance attachment.", displayName(node)),
		nil
}

// securityGroupRule reports a security group as orphaned when no resource
// (EC2, ELB, RDS, Lambda, ECS, EKS) references it. Collectors emit
// Consumer → SG edges via EdgeUsesSecurityGroup, so we check for inbound
// USES_SECURITY_GROUP edges.
func securityGroupRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeUsesSecurityGroup) {
		return false, confidenceSecurityGroup, "", nil
	}
	return true, confidenceSecurityGroup,
		fmt.Sprintf("Security group %s has no inbound references.", displayName(node)),
		nil
}

// iamRoleRule reports an IAM role as orphaned when (a) no resource in its
// own account assumes it AND (b) no resource in any OTHER cloud account
// assumes it either. The cross-account walk uses foundation.FetchGraphNames
// to find all loaded cloud graphs and checks each one for inbound
// ASSUMES_ROLE edges to this role's ARN.
//
// This is the only rule that needs the wire caller. The 0.7 confidence
// reflects two known false-positive sources: (1) we skip IAM policy JSON
// parsing so a role referenced only by an inline trust policy in another
// account's policy document looks unused, and (2) cross-account checks only
// cover currently-loaded cloud graphs.
func iamRoleRule(
	ctx context.Context,
	caller foundation.GraphCaller,
	currentAccount string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	// (a) Same-account check.
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeAssumesRole) {
		return false, confidenceIAMRole, "", nil
	}

	// (b) Cross-account check. Walk every loaded cloud graph except the
	// current account; if any inbound ASSUMES_ROLE edge to this role exists
	// in any other graph, the role is referenced and not orphaned.
	if caller != nil {
		referencedBy, err := findCrossAccountTrustReference(ctx, caller, currentAccount, node.Id)
		if err != nil {
			return false, confidenceIAMRole, "", err
		}
		if referencedBy != "" {
			return false, confidenceIAMRole, "", nil
		}
	}

	return true, confidenceIAMRole,
		fmt.Sprintf("IAM role %s is not assumed by any resource in any known account.", displayName(node)),
		nil
}

// iamPolicyRule reports an IAM customer-managed policy as orphaned when no
// role attaches to it. The collector emits Role → Policy edges via
// EdgeGrants, so we check for inbound GRANTS edges.
func iamPolicyRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeGrants) {
		return false, confidenceIAMPolicy, "", nil
	}
	return true, confidenceIAMPolicy,
		fmt.Sprintf("IAM policy %s is not attached to any role.", displayName(node)),
		nil
}

// findCrossAccountTrustReference walks every loaded cloud graph except the
// current account and returns the name of the first account whose graph has
// an inbound ASSUMES_ROLE edge to roleID. Returns ("", nil) when no reference
// is found anywhere. Returns a non-nil error only on a graph-name enumeration
// failure — individual graphs that fail to fetch are skipped silently rather
// than aborting the whole orphan run (a single bad graph should not block
// detection across the rest of the fleet).
func findCrossAccountTrustReference(ctx context.Context, caller foundation.GraphCaller, currentAccount, roleID string) (string, error) {
	infos, err := foundation.FetchGraphNames(ctx, caller, kgtypes.GraphCloud)
	if err != nil {
		return "", fmt.Errorf("topology/orphan: list cloud graphs: %w", err)
	}
	for _, gi := range infos {
		name := gi.GetName()
		if name == "" || name == currentAccount {
			continue
		}
		// Fetch the candidate role's incoming ASSUMES_ROLE edges in the other
		// account's graph. A failure for one graph is skipped, matching the
		// prior posture of silently skipping unreadable graphs.
		edges, ferr := foundation.FetchEdges(ctx, caller, kgtypes.GraphCloud, name, []string{roleID}, []kgtypes.EdgeType{kgtypes.EdgeAssumesRole})
		if ferr != nil {
			continue
		}
		idx := newEdgeIndex(edges)
		if idx.hasIncoming(roleID, kgtypes.EdgeAssumesRole) {
			return name, nil
		}
	}
	return "", nil
}

// init self-registers the v1 AWS orphan rules with the dispatch table.
// Resource type strings match the values emitted by cloud/aws/* collectors.
func init() {
	registerOrphanRule("elbv2-loadbalancer", elbv2LoadBalancerRule)
	registerOrphanRule("elbv2-targetgroup", elbv2TargetGroupRule)
	registerOrphanRule("ebs-volume", ebsVolumeRule)
	registerOrphanRule("security-group", securityGroupRule)
	registerOrphanRule("iam-role", iamRoleRule)
	registerOrphanRule("iam-policy", iamPolicyRule)
}
