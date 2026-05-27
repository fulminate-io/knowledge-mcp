// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_passrole.go implements three rules that compose iam:PassRole or
// service-creation actions with a target role's trust policy:
//
//   - passRoleLambdaRule:    iam:PassRole + lambda.amazonaws.com trust → execute_as
//   - runInstancesRule:      iam:PassRole + ec2.amazonaws.com    trust → execute_as
//   - createFunctionRule:    lambda:CreateFunction + lambda trust       → execute_as
//
// All three emit iamEdgeExecuteAs edges from the calling principal to the
// target role: the principal can launch a service (Lambda function or EC2
// instance) with that role attached and then operate as the role.

import (
	"context"
	"slices"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// passRoleLambdaRule emits execute_as edges when a principal can pass a role
// to Lambda. The target role must trust lambda.amazonaws.com (its trust
// policy contains a Service principal "lambda.amazonaws.com").
func passRoleLambdaRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return passRoleServiceRule(ctx, rctx, "lambda.amazonaws.com", "iam:PassRole", "principal can pass role to Lambda")
}

// runInstancesRule emits execute_as edges when a principal can pass a role
// to EC2. The target role must trust ec2.amazonaws.com.
func runInstancesRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return passRoleServiceRule(ctx, rctx, "ec2.amazonaws.com", "iam:PassRole", "principal can pass role to EC2")
}

// createFunctionRule emits execute_as edges when a principal can call
// lambda:CreateFunction (which implicitly passes a role to Lambda).
func createFunctionRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return passRoleServiceRule(ctx, rctx, "lambda.amazonaws.com", "lambda:CreateFunction", "principal can create Lambda functions")
}

// passRoleServiceRule is the shared implementation for all three pass-role
// style rules. It cross-products the set of principals that can call action
// with the set of roles that trust serviceName, emitting one execute_as edge
// per (principal, role) pair.
func passRoleServiceRule(
	ctx context.Context,
	rctx *iamRuleContext,
	serviceName string,
	action string,
	reason string,
) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	targets := rolesTrustingService(rctx, serviceName)
	if len(targets) == 0 {
		return nil, nil
	}
	var edges []iamInferredEdge
	for _, p := range allPrincipals(rctx) {
		if !principalAllowsAction(ctx, rctx, p, action) {
			continue
		}
		for _, role := range targets {
			edges = append(edges, iamInferredEdge{
				FromID: p.Id,
				ToID:   role.Id,
				Kind:   iamEdgeExecuteAs,
				Reason: reason,
			})
		}
	}
	return edges, nil
}

// rolesTrustingService returns every iam-role in the rule context whose
// trust policy contains the given service principal in any Effect=Allow
// statement.
func rolesTrustingService(rctx *iamRuleContext, serviceName string) []*knowledgev1.Node {
	var out []*knowledgev1.Node
	for i := range rctx.Roles {
		role := rctx.Roles[i]
		policy := extractTrustPolicyFromRoleNode(role)
		if policy == nil {
			continue
		}
		if slices.Contains(policy.ServicePrincipals(), serviceName) {
			out = append(out, role)
		}
	}
	return out
}

// principalAllowsAction returns true if any policy attached to or inherited
// by the principal allows the given action on any resource ("*").
func principalAllowsAction(ctx context.Context, rctx *iamRuleContext, principal *knowledgev1.Node, action string) bool {
	for _, policy := range iterPrincipalPolicies(ctx, rctx, principal) {
		if policy.AllowsAction(action, "*") {
			return true
		}
	}
	return false
}
