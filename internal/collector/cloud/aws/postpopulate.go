// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// postPopulate is the AWS CollectResult.PostPopulate hook. It runs after all
// subcollector nodes and raw attachment edges have been written to the graph
// and derives higher-level structural edges that require cross-node lookups:
//
//  1. Security Group rule pre-resolution — re-parses each security-group
//     node's IpPermissions/IpPermissionsEgress JSON and emits directional
//     EdgeAllowsIngressFrom / EdgeAllowsEgressTo edges for SG-to-SG
//     references and CIDR rules (see postpopulate_sg.go).
//  2. Network ACL rule pre-resolution — re-parses each network-acl node's
//     entries and emits subnet-scoped ALLOWS edges with is_nacl metadata
//     (see postpopulate_nacl.go).
//  3. Cross-VPC SG reference resolution — resolves UserIdGroupPairs whose
//     peer VPC differs from the host VPC against peering/TGW/endpoint
//     connectivity (see postpopulate_crossvpc.go).
//  4. EKS IRSA resolution — matches EKS cluster OIDC providers against IAM
//     role trust policies and emits EdgeWorkloadIdentity edges from k8s
//     ServiceAccounts to IAM roles (see postpopulate_irsa.go).
//
// All helpers tolerate missing prerequisite data (e.g., no NACL nodes
// yet, no peering connections collected) and log-and-continue rather than
// returning hard errors so postPopulate never fails the whole collection
// for a partial gap. Each helper is independently unit-tested.
func postPopulate(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	if err := resolveSecurityGroupRules(ctx, gc, graphName); err != nil {
		slog.Warn("aws postPopulate: resolveSecurityGroupRules failed", "err", err)
	}
	if err := resolveNetworkACLRules(ctx, gc, graphName); err != nil {
		slog.Warn("aws postPopulate: resolveNetworkACLRules failed", "err", err)
	}
	if err := resolveCrossVpcSgReferences(ctx, gc, graphName); err != nil {
		slog.Warn("aws postPopulate: resolveCrossVpcSgReferences failed", "err", err)
	}
	if err := resolveCrossAccountTrust(ctx, gc, graphName); err != nil {
		slog.Warn("aws postPopulate: resolveCrossAccountTrust failed", "err", err)
	}
	// EKS IRSA: match OIDC providers against IAM role trust policies.
	if err := resolveIRSA(ctx, gc, graphName); err != nil {
		slog.Warn("aws postPopulate: resolveIRSA failed", "err", err)
	}
	// ECS image lineage: match workload container images against ECR repos.
	if err := resolveECSImageLineage(ctx, gc, graphName); err != nil {
		slog.Warn("aws postPopulate: resolveECSImageLineage failed", "err", err)
	}
	// Route53 DNS alias resolution: rewrite dangling DNS hostname targets to ARNs.
	if err := resolveRoute53AliasTargets(ctx, gc, graphName); err != nil {
		slog.Warn("aws postPopulate: resolveRoute53AliasTargets failed", "err", err)
	}
	return nil
}
