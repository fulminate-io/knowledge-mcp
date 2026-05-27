// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_policy_modification.go implements five PMapper policy-modification
// rules. Each reaches effective admin in one hop by mutating a policy document:
//
//   - putUserPolicyRule            (iam:PutUserPolicy)            confidence 1.0
//   - putGroupPolicyRule           (iam:PutGroupPolicy)           confidence 1.0
//   - putRolePolicyRule            (iam:PutRolePolicy)            confidence 1.0
//   - createPolicyVersionRule      (iam:CreatePolicyVersion)      confidence 1.0
//   - setDefaultPolicyVersionRule  (iam:SetDefaultPolicyVersion)  confidence 0.7
//
// The three Put*Policy rules stamp an inline policy onto a user/group/role;
// all three also emit an iamEdgeAttachPolicy self-loop on the caller (writing
// an inline policy onto yourself is self-promotion, matching attachPolicyRule
// for iam:Attach*Policy) plus per-target edges (iamEdgeImpersonate for users
// and group members, iamEdgeExecuteAs for roles).
//
// createPolicyVersionRule emits impersonate edges to every principal holding
// at least one customer-managed policy — the attacker can publish a new
// default version rewriting that policy's permissions. AWS-managed policies
// (arn:aws:iam::aws:policy/...) are immutable and excluded.
// setDefaultPolicyVersionRule targets the same set with 0.7 confidence: the
// attacker only reactivates existing versions and the collector does not
// gather version history, so the rule fires pessimistically.

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// Shared helpers (allGroupMembers, principalsWithCustomerManagedPolicies,
// holdsCustomerManagedPolicy, and the awsManagedPolicyPrefix constant) live
// in iam_rules_policy_modification_helpers.go to keep this file under the
// 300-line production cap.

// putInlinePolicyRule is the shared body for the three Put*Policy rules. It
// cross-products callers-that-allow-action with the target list, emitting one
// edge per (caller, target) pair of edgeKind. Self-target pairs are skipped;
// the attachPolicy self-loop promotion is emitted separately.
func putInlinePolicyRule(
	ctx context.Context, rctx *iamRuleContext, action string,
	targets []*knowledgev1.Node, edgeKind iamInferredEdgeKind, reason string,
) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, nil
	}
	var edges []iamInferredEdge
	for _, p := range allPrincipals(rctx) {
		if !principalAllowsAction(ctx, rctx, p, action) {
			continue
		}
		for i := range targets {
			target := targets[i]
			if target.Id == p.Id {
				continue
			}
			edges = append(edges, iamInferredEdge{
				FromID: p.Id,
				ToID:   target.Id,
				Kind:   edgeKind,
				Reason: reason,
			})
		}
	}
	return edges, nil
}

// emitSelfAttachPolicy returns an iamEdgeAttachPolicy self-loop for every
// principal that allows action. The BFS in iam_escalation.go treats
// iamEdgeAttachPolicy as automatic admin promotion (OQ-6), so this is how a
// Put*Policy caller self-promotes to effective admin.
func emitSelfAttachPolicy(
	ctx context.Context,
	rctx *iamRuleContext,
	action, reason string,
) []iamInferredEdge {
	var edges []iamInferredEdge
	for _, p := range allPrincipals(rctx) {
		if !principalAllowsAction(ctx, rctx, p, action) {
			continue
		}
		edges = append(edges, iamInferredEdge{
			FromID: p.Id,
			ToID:   p.Id,
			Kind:   iamEdgeAttachPolicy,
			Reason: reason,
		})
	}
	return edges
}

// putUserPolicyRule — iam:PutUserPolicy. Impersonate edges to every other
// iam-user plus an attachPolicy self-loop on the caller.
func putUserPolicyRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	edges, err := putInlinePolicyRule(ctx, rctx, "iam:PutUserPolicy", rctx.Users, iamEdgeImpersonate,
		"principal can stamp an inline policy onto IAM users (confidence 1.0)")
	if err != nil {
		return nil, err
	}
	edges = append(edges, emitSelfAttachPolicy(ctx, rctx, "iam:PutUserPolicy",
		"principal can write an inline policy onto itself via iam:PutUserPolicy (confidence 1.0)")...)
	return edges, nil
}

// putGroupPolicyRule — iam:PutGroupPolicy. Impersonate edges to every user
// in every group (members inherit their group's policies via
// iterPrincipalPolicies) plus an attachPolicy self-loop on the caller.
func putGroupPolicyRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rctx.Groups) == 0 {
		return nil, nil
	}
	members := allGroupMembers(ctx, rctx)
	edges, err := putInlinePolicyRule(ctx, rctx, "iam:PutGroupPolicy", members, iamEdgeImpersonate,
		"principal can stamp an inline policy onto IAM groups; every member inherits (confidence 1.0)")
	if err != nil {
		return nil, err
	}
	edges = append(edges, emitSelfAttachPolicy(ctx, rctx, "iam:PutGroupPolicy",
		"principal can promote any group it belongs to via iam:PutGroupPolicy (confidence 1.0)")...)
	return edges, nil
}

// putRolePolicyRule — iam:PutRolePolicy. Execute_as edges to every iam-role
// plus an attachPolicy self-loop on the caller.
func putRolePolicyRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	edges, err := putInlinePolicyRule(ctx, rctx, "iam:PutRolePolicy", rctx.Roles, iamEdgeExecuteAs,
		"principal can stamp an inline policy onto IAM roles (confidence 1.0)")
	if err != nil {
		return nil, err
	}
	edges = append(edges, emitSelfAttachPolicy(ctx, rctx, "iam:PutRolePolicy",
		"principal can write an inline policy onto itself via iam:PutRolePolicy (confidence 1.0)")...)
	return edges, nil
}

// createPolicyVersionRule — iam:CreatePolicyVersion. Confidence 1.0.
func createPolicyVersionRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return policyVersionRule(ctx, rctx, "iam:CreatePolicyVersion",
		"principal can create a new version of any customer-managed policy attached to the target (confidence 1.0)",
		"principal can rewrite any customer-managed policy it holds via iam:CreatePolicyVersion (confidence 1.0)",
	)
}

// setDefaultPolicyVersionRule — iam:SetDefaultPolicyVersion. Confidence 0.7
// because the attacker can only reactivate existing versions; historical
// version bodies are not collected in v1.1 so the rule fires pessimistically.
func setDefaultPolicyVersionRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return policyVersionRule(ctx, rctx, "iam:SetDefaultPolicyVersion",
		"principal can roll back any customer-managed policy attached to the target to a prior version (confidence 0.7)",
		"principal can roll back any customer-managed policy it holds via iam:SetDefaultPolicyVersion (confidence 0.7)",
	)
}

// policyVersionRule is the shared body for createPolicyVersionRule and
// setDefaultPolicyVersionRule. It enumerates every principal holding a
// customer-managed policy, cross-products with callers that allow action,
// skips self-impersonation, and emits an attachPolicy self-loop when the
// caller itself holds a customer-managed policy.
func policyVersionRule(
	ctx context.Context, rctx *iamRuleContext, action, reason, selfReason string,
) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	targets := principalsWithCustomerManagedPolicies(ctx, rctx)
	if len(targets) == 0 {
		return nil, nil
	}
	var edges []iamInferredEdge
	for _, p := range allPrincipals(rctx) {
		if !principalAllowsAction(ctx, rctx, p, action) {
			continue
		}
		for i := range targets {
			target := targets[i]
			if target.Id == p.Id {
				continue
			}
			edges = append(edges, iamInferredEdge{
				FromID: p.Id,
				ToID:   target.Id,
				Kind:   iamEdgeImpersonate,
				Reason: reason,
			})
		}
		if holdsCustomerManagedPolicy(ctx, rctx.scoped, p.Id) {
			edges = append(edges, iamInferredEdge{
				FromID: p.Id,
				ToID:   p.Id,
				Kind:   iamEdgeAttachPolicy,
				Reason: selfReason,
			})
		}
	}
	return edges, nil
}

// init registers the five policy-modification rules with OQ-4 confidence.
// put_user_policy, put_group_policy, put_role_policy, create_policy_version
// are all direct admin-equivalent (1.0): the attacker rewrites a policy to
// Allow * *. set_default_policy_version is 0.7 because v1.1 does not
// collect version history, so the rule fires pessimistically — the
// attacker may be activating an already-benign version.
func init() {
	registerIAMRule("put_user_policy", 1.0, []string{"iam:PutUserPolicy"}, putUserPolicyRule)
	registerIAMRule("put_group_policy", 1.0, []string{"iam:PutGroupPolicy"}, putGroupPolicyRule)
	registerIAMRule("put_role_policy", 1.0, []string{"iam:PutRolePolicy"}, putRolePolicyRule)
	registerIAMRule("create_policy_version", 1.0, []string{"iam:CreatePolicyVersion"}, createPolicyVersionRule)
	registerIAMRule("set_default_policy_version", 0.7, []string{"iam:SetDefaultPolicyVersion"}, setDefaultPolicyVersionRule)
}
