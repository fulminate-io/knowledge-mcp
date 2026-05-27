// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_trust_modification.go implements trust-modification PMapper rules.
// In the v2 PMapper edge taxonomy, the dominant rule in this family is:
//
//   - updateAssumeRolePolicyRule (iam:UpdateAssumeRolePolicy) — confidence 1.0
//
// This is potentially the highest-impact PMapper rule we ship. Any principal P
// that holds iam:UpdateAssumeRolePolicy on a target role R can rewrite R's
// trust policy to allow P (or any other principal) to assume R. One action
// translates to "effective admin of R" — which, if R itself is privileged,
// transitively elevates P. Confidence is 1.0 because trust hijack is
// definitional: the AWS API performs no extra checks beyond the action grant
// itself.
//
// Resource scoping is load-bearing for this rule. AWS IAM policy resource
// patterns matter:
//
//   - Resource: "*"                                  → every iam-role in account
//   - Resource: "arn:aws:iam::123:role/specific"     → just that role
//   - Resource: "arn:aws:iam::123:role/env-*"        → every role under env-
//
// The IAMPolicy.AllowsAction(action, resource) helper already implements every
// pattern AWS supports (exact match, "*" wildcard, suffix-* prefix match,
// NotResource inversion, and explicit-Deny override), so the rule body simply
// asks the parser one question per (principal, role) pair: "does this
// principal's effective policy set allow iam:UpdateAssumeRolePolicy on
// THIS role's ARN?". Re-implementing pattern matching here would duplicate
// logic that already exists and is tested in iam_parser_eval.go.
//
// The rule emits iamEdgeExecuteAs (the "assume via trust hijack" kind in v2):
// after the trust rewrite, the attacker assumes the role and operates as it.
//
// NOTE: A separate Phase 9 integration concern is the IsTrustPolicyWideOpen()
// path — a role whose existing trust policy is already wide-open is
// definitionally executable by any principal in the account regardless of
// iam:UpdateAssumeRolePolicy. That belongs in the BFS / wiring layer, not in
// this rule. This file restricts itself to the action-based path.

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// updateAssumeRolePolicyRule emits one iamEdgeExecuteAs edge per (principal,
// role) pair where the principal's effective policy set allows
// iam:UpdateAssumeRolePolicy on the role's ARN. Resource scoping is delegated
// to IAMPolicy.AllowsAction so wildcards, prefix patterns, and explicit Deny
// all behave identically to AWS IAM evaluation.
//
// Self-pairs (principal == role, only possible if a role has the action on
// its own ARN) are emitted normally — a role rewriting its own trust to add
// other principals is still useful escalation evidence, and the BFS treats
// self-loops on roles as no-ops anyway.
func updateAssumeRolePolicyRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rctx.Roles) == 0 {
		return nil, nil
	}
	const action = "iam:UpdateAssumeRolePolicy"
	var edges []iamInferredEdge
	for _, p := range allPrincipals(rctx) {
		targets := rolesInScope(ctx, rctx, p, action)
		for i := range targets {
			role := targets[i]
			edges = append(edges, iamInferredEdge{
				FromID: p.Id,
				ToID:   role.Id,
				Kind:   iamEdgeExecuteAs,
				Reason: "principal can rewrite the trust policy of " + role.Id +
					" via iam:UpdateAssumeRolePolicy and then assume it (confidence 1.0)",
			})
		}
	}
	return edges, nil
}

// rolesInScope returns every iam-role from rctx.Roles whose ARN is matched
// by the principal's effective policy set for the given action. The match
// test delegates to IAMPolicy.AllowsAction, which already handles:
//
//   - Resource: "*"                  → every role
//   - Resource: <exact role ARN>     → that role only
//   - Resource: <prefix>"*"          → roles whose ARN starts with prefix
//   - NotResource inversion          → matches everything except listed ARNs
//   - explicit Deny override         → Allow + matching Deny → no match
//
// Iterating roles and asking the parser per role keeps the rule trivially
// O(principals × roles) without re-implementing any pattern code here.
func rolesInScope(
	ctx context.Context,
	rctx *iamRuleContext,
	principal *knowledgev1.Node,
	action string,
) []*knowledgev1.Node {
	var out []*knowledgev1.Node
	for i := range rctx.Roles {
		role := rctx.Roles[i]
		if principalAllowsActionOnResource(ctx, rctx, principal, action, role.Id) {
			out = append(out, role)
		}
	}
	return out
}

// principalAllowsActionOnResource returns true if any policy attached to or
// inherited by the principal allows action on the specific resource ARN.
// Mirrors principalAllowsAction in iam_rules_passrole.go but threads the
// resource through instead of hard-coding "*". The two helpers are kept
// separate so the existing ANY-resource semantic stays the obvious default
// for rules that don't care about scoping.
func principalAllowsActionOnResource(
	ctx context.Context,
	rctx *iamRuleContext,
	principal *knowledgev1.Node,
	action, resource string,
) bool {
	for _, policy := range iterPrincipalPolicies(ctx, rctx, principal) {
		if policy.AllowsAction(action, resource) {
			return true
		}
	}
	return false
}

// init registers the trust-modification rule. update_assume_role_policy is
// direct admin-equivalent (1.0) per OQ-4: rewriting a role's trust policy
// is definitionally effective admin of the role — AWS performs no extra
// checks beyond the iam:UpdateAssumeRolePolicy grant.
func init() {
	registerIAMRule("update_assume_role_policy", 1.0, []string{"iam:UpdateAssumeRolePolicy"}, updateAssumeRolePolicyRule)
}
