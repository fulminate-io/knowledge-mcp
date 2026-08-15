// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

const methodAWSCrossAccountTrust = "aws-cross-account-trust"

// resolveCrossAccountTrust parses IAM role trust policies and emits TRUSTS
// edges when a principal in another collected AWS account is referenced. The
// TRUSTS direction is FROM trusted principal TO role (matching topology's
// iamEdgeAssumeRole direction).
//
// Per decision: edges are written to BOTH the current account's graph and the
// other account's graph for full bidirectional query coverage. All graph I/O
// rides the wire (postpopulate.BrowseAllNodes / ListGraphNames / LinkEdgesBatch) —
// graphName is the current account's cloud graph; peer accounts are enumerated
// via ListGraphNames(GraphCloud) and read/written by name.
func resolveCrossAccountTrust(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	roles, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, graphName, iamRoleQuery())
	if err != nil {
		return err
	}
	if len(roles) == 0 {
		return nil
	}

	currentAccount := detectCurrentAccount(roles)
	if currentAccount == "" {
		return nil
	}

	// Build a per-peer-account principal index in ONE browse per peer graph
	// (replaces the prior per-principal db.Query existence check). Each peer's
	// cloud graph is read once and its node IDs collected into a set.
	peerPrincipals := buildPeerPrincipalSets(ctx, gc, currentAccount)

	var localEdges []knowledgev1.Edge
	remoteEdgesByAccount := make(map[string][]knowledgev1.Edge)

	for _, role := range roles {
		for _, principal := range parseTrustPrincipals(role) {
			acct := extractAccountFromARN(principal)
			if acct == "" || acct == currentAccount {
				continue
			}
			ids, ok := peerPrincipals[acct]
			if !ok {
				continue
			}
			if _, exists := ids[principal]; !exists {
				continue
			}
			// Append fresh literals to both slices — copylocks forbids copying
			// an existing knowledgev1.Edge value (proto value-embed flip).
			localEdges = append(localEdges, knowledgev1.Edge{
				FromId: principal,
				ToId:   role.Id,
				Type:   string(kgtypes.EdgeTrusts),
				Method: methodAWSCrossAccountTrust,
			})
			remoteEdgesByAccount[acct] = append(remoteEdgesByAccount[acct], knowledgev1.Edge{
				FromId: principal,
				ToId:   role.Id,
				Type:   string(kgtypes.EdgeTrusts),
				Method: methodAWSCrossAccountTrust,
			})
		}
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, graphName, localEdges); err != nil {
		return err
	}
	for acct, edges := range remoteEdgesByAccount {
		if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCloud, acct, edges); err != nil {
			slog.Warn("aws trust: remote link failed", "account", acct, "err", err)
		}
	}

	slog.Debug("aws trust: emitted edges", "local", len(localEdges), "remote_accounts", len(remoteEdgesByAccount))
	return nil
}

// iamRoleQuery is the browse filter for iam-role cloud nodes. It carries no
// limit: its callers drain, and the drain sets the per-page limit itself.
func iamRoleQuery() map[string]any {
	return map[string]any{
		"type": string(kgtypes.NodeCloudResource),
		"meta": map[string]string{"resource_type": "iam-role"},
	}
}

// buildPeerPrincipalSets reads every OTHER collected AWS account's cloud graph
// once and returns a map of account ID → set of node IDs present in that graph.
// A trust principal is "resolvable" iff its ID appears in its account's set.
// This replaces the per-principal db.Query existence check + the scoped-DB cache
// with one bounded browse per peer graph.
func buildPeerPrincipalSets(ctx context.Context, gc postpopulate.GraphCaller, currentAccount string) map[string]map[string]struct{} {
	names, err := postpopulate.ListGraphNames(ctx, gc, kgtypes.GraphCloud)
	if err != nil {
		slog.Warn("aws trust: list cloud graphs failed", "err", err)
		return nil
	}
	out := make(map[string]map[string]struct{})
	for _, name := range names {
		if name == "" || name == currentAccount {
			continue
		}
		nodes, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCloud, name, map[string]any{
			"type": string(kgtypes.NodeCloudResource),
		})
		if err != nil {
			slog.Debug("aws trust: browse peer account failed", "account", name, "err", err)
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

// detectCurrentAccount extracts the account ID from the first role with a valid ARN.
func detectCurrentAccount(roles []*knowledgev1.Node) string {
	for _, r := range roles {
		if acct := accountFromARN(r.Id); acct != "" {
			return acct
		}
	}
	return ""
}

// extractAccountFromARN extracts the account ID from an IAM ARN of the form
// arn:aws:iam::ACCOUNT:... The IAM ARN has an empty region segment, so the
// account is at index 4.
func extractAccountFromARN(arn string) string {
	return accountFromARN(arn)
}

// parseTrustPrincipals extracts AWS principal ARNs from a role's trust policy.
func parseTrustPrincipals(role *knowledgev1.Node) []string {
	policy := extractTrustPolicy(role)
	if policy == nil {
		return nil
	}
	return trustPolicyPrincipals(policy)
}
