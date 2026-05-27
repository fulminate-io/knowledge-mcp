// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_assume.go implements assumeRoleTrustPolicyRule.
//
// For each iam-role in the current account: parse its trust policy
// (Role.AssumeRolePolicyDocument inside node.Content) and emit one
// iamEdgeAssumeRole edge from each AWS principal listed in the trust policy
// to the role. Cross-account principals (ARNs that mention an account other
// than rctx.Account) are also emitted — the BFS in iam_escalation.go uses
// the cross-account flag to surface them as cross-account findings.

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// assumeRoleTrustPolicyRule emits assume_role inferred edges from every
// principal listed in a role's trust policy to that role. Service principals
// (lambda.amazonaws.com, ec2.amazonaws.com, ...) are NOT emitted by this
// rule; the passrole/runinstances/createfunction rules handle those.
func assumeRoleTrustPolicyRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var edges []iamInferredEdge
	for i := range rctx.Roles {
		role := rctx.Roles[i]
		policy := extractTrustPolicyFromRoleNode(role)
		if policy == nil {
			continue
		}
		conditional := policy.HasCondition()
		for _, principal := range policy.TrustPrincipals() {
			if principal == "" {
				continue
			}
			// Resolve principal ARN to a principal node ID. The trust policy may
			// reference users or roles in this account or any other account.
			fromID := resolvePrincipalARN(ctx, rctx, principal)
			if fromID == "" {
				continue
			}
			edges = append(edges, iamInferredEdge{
				FromID:      fromID,
				ToID:        role.Id,
				Kind:        iamEdgeAssumeRole,
				Reason:      "trust policy allows " + principal + " to assume " + role.Id,
				Conditional: conditional,
			})
		}
	}
	return edges, nil
}

// extractTrustPolicyFromRoleNode parses an iam-role node's Content field
// (a JSON-marshaled iamtypes.Role) and returns the parsed AssumeRolePolicyDocument.
// Topology cannot import the AWS SDK types package — we use a local struct
// that captures only the field we need from the role JSON shape.
//
// Returns nil if the role has no trust policy or any parse step fails.
func extractTrustPolicyFromRoleNode(role *knowledgev1.Node) *IAMPolicy {
	if role.Content == "" {
		return nil
	}
	var raw struct {
		AssumeRolePolicyDocument *string `json:"AssumeRolePolicyDocument,omitempty"`
	}
	if err := json.Unmarshal([]byte(role.Content), &raw); err != nil {
		return nil
	}
	if raw.AssumeRolePolicyDocument == nil || *raw.AssumeRolePolicyDocument == "" {
		return nil
	}
	encoded := *raw.AssumeRolePolicyDocument
	// The SDK may return the trust policy URL-encoded OR plain JSON depending
	// on serializer choice. Try parsing as plain first, then fall back to
	// URL-decode + parse.
	if p, err := ParseIAMPolicy([]byte(encoded)); err == nil {
		return p
	}
	if decoded, derr := url.QueryUnescape(encoded); derr == nil {
		if p, err := ParseIAMPolicy([]byte(decoded)); err == nil {
			return p
		}
	}
	return nil
}

// resolvePrincipalARN maps an AWS principal ARN string from a trust policy
// to the node ID of the principal in either the current account or any
// other loaded cloud graph.
//
// Resolution order:
//  1. If the ARN equals an existing iam-user, iam-role, or iam-group node in
//     the current account, return that ID.
//  2. Otherwise walk every loaded cloud graph (FetchGraphNames(GraphCloud))
//     and try to find the ARN in another account's graph.
//  3. Account root principals ("arn:aws:iam::ACCT:root") return the literal
//     ARN — these are still useful as escalation sources, the BFS will treat
//     them as terminal sources rather than expanding them further.
//
// Returns the empty string when no resolution succeeds — the caller drops
// the inferred edge silently.
func resolvePrincipalARN(ctx context.Context, rctx *iamRuleContext, principal string) string {
	if principal == "" {
		return ""
	}
	// Account root principal — keep as-is. The BFS will treat it as a source.
	if strings.HasSuffix(principal, ":root") {
		return principal
	}
	// Same-account match: every node we hold is keyed by ARN.
	if matchPrincipal(rctx.Users, principal) || matchPrincipal(rctx.Roles, principal) || matchPrincipal(rctx.Groups, principal) {
		return principal
	}
	// Cross-account match: walk other cloud graphs.
	if rctx.caller == nil {
		return ""
	}
	infos, err := foundation.FetchGraphNames(ctx, rctx.caller, kgtypes.GraphCloud)
	if err != nil {
		return ""
	}
	for _, gi := range infos {
		if ctx.Err() != nil {
			return ""
		}
		if gi.Name == "" || gi.Name == rctx.Account {
			continue
		}
		other, ok, qerr := foundation.FetchNodeByID(ctx, rctx.caller, kgtypes.GraphCloud, gi.Name, principal)
		if qerr != nil {
			continue
		}
		if ok && other != nil {
			return principal
		}
	}
	// Last-chance: if the ARN looks like a real principal we can still
	// surface it as a cross-account source even if we don't have the graph.
	if strings.HasPrefix(principal, "arn:aws:iam::") {
		return principal
	}
	return ""
}

// matchPrincipal returns true if any node in nodes has ID equal to the
// principal ARN. Used to test current-account presence quickly without a
// store query.
func matchPrincipal(nodes []*knowledgev1.Node, arn string) bool {
	for i := range nodes {
		if nodes[i].Id == arn {
			return true
		}
	}
	return false
}
