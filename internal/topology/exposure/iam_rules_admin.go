// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_admin.go implements two rules that promote a principal to
// effective admin:
//
//   - wildcardActionRule: any attached/inline/inherited policy with
//     Effect=Allow Action=* Resource=* (the strict definition per OQ-4).
//   - attachPolicyRule: any attached/inline/inherited policy that allows
//     iam:AttachUserPolicy or iam:AttachRolePolicy (per OQ-6, conflated
//     into automatic admin promotion in the BFS).
//
// Both rules emit self-loops (FromID == ToID) — the BFS reads the inferred
// edge map's iamEdgeEffectiveAdmin and iamEdgeAttachPolicy entries when
// constructing the admin set, then expands the BFS frontier from there.

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// allPrincipals returns Roles + Users + Groups for the rule context. Used
// by every "principal-walking" rule.
func allPrincipals(rctx *iamRuleContext) []*knowledgev1.Node {
	out := make([]*knowledgev1.Node, 0, len(rctx.Roles)+len(rctx.Users)+len(rctx.Groups))
	out = append(out, rctx.Roles...)
	out = append(out, rctx.Users...)
	out = append(out, rctx.Groups...)
	return out
}

// wildcardActionRule emits an effective_admin self-loop on every principal
// whose attached/inline/inherited policies include a strict admin statement
// (Allow * *).
func wildcardActionRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var edges []iamInferredEdge
	for _, p := range allPrincipals(rctx) {
		if hasEffectiveAdminPolicy(ctx, rctx, p) {
			edges = append(edges, iamInferredEdge{
				FromID: p.Id,
				ToID:   p.Id,
				Kind:   iamEdgeEffectiveAdmin,
				Reason: "principal has policy with Effect=Allow Action=* Resource=*",
			})
		}
	}
	return edges, nil
}

// hasEffectiveAdminPolicy returns true if any policy attached to or inherited
// by the principal is a strict effective admin (Allow * *).
func hasEffectiveAdminPolicy(ctx context.Context, rctx *iamRuleContext, principal *knowledgev1.Node) bool {
	for _, policy := range iterPrincipalPolicies(ctx, rctx, principal) {
		if policy.IsEffectiveAdmin() {
			return true
		}
	}
	return false
}

// attachPolicyRule emits an attach_policy self-loop on every principal that
// can attach managed policies (iam:AttachUserPolicy or iam:AttachRolePolicy).
// The BFS in iam_escalation.go treats this as automatic admin promotion per
// OQ-6: a principal that can attach policies can attach AdministratorAccess
// to itself, so the practical capability is identical to effective admin.
func attachPolicyRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var edges []iamInferredEdge
	for _, p := range allPrincipals(rctx) {
		if hasAttachPolicyCapability(ctx, rctx, p) {
			edges = append(edges, iamInferredEdge{
				FromID: p.Id,
				ToID:   p.Id,
				Kind:   iamEdgeAttachPolicy,
				Reason: "principal can attach managed policies (iam:Attach*Policy)",
			})
		}
	}
	return edges, nil
}

// hasAttachPolicyCapability returns true if any policy attached to or
// inherited by the principal allows iam:AttachUserPolicy or
// iam:AttachRolePolicy on any resource.
func hasAttachPolicyCapability(ctx context.Context, rctx *iamRuleContext, principal *knowledgev1.Node) bool {
	for _, policy := range iterPrincipalPolicies(ctx, rctx, principal) {
		if policy.AllowsAction("iam:AttachUserPolicy", "*") ||
			policy.AllowsAction("iam:AttachRolePolicy", "*") ||
			policy.AllowsAction("iam:AttachGroupPolicy", "*") {
			return true
		}
	}
	return false
}
