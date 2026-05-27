// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_lambda_update.go covers the two PMapper Lambda "update" edges
// that v1.1's iam_rules_passrole.go did not model:
//
//	update_function_code          — lambda:UpdateFunctionCode
//	update_function_configuration — lambda:UpdateFunctionConfiguration + iam:PassRole
//
// These are SHAPED DIFFERENTLY from the v1.1 / Phase 7A PassRole rules, and
// from each other:
//
//   - UpdateFunctionCode does NOT require iam:PassRole. The function already
//     has its execution role attached at create time; UpdateFunctionCode just
//     overwrites the deployment package. The escalation is "inject code into a
//     privileged function and run it as the function's existing role." The
//     target set is therefore PER-FUNCTION (each Lambda function's current
//     execution role) rather than the cross-product of (callers, roles trusting
//     a service). updateFunctionCodeRule enumerates rctx.Functions and resolves
//     each function's execution role via the EdgeAssumesRole edge that the
//     cloud/aws/lambda.go collector emits at function -> role.
//
//   - UpdateFunctionConfiguration DOES require iam:PassRole. The attacker swaps
//     in a new execution role on an existing function, and Lambda still
//     requires the new role to trust lambda.amazonaws.com. Structurally that
//     is identical to the v1.1 createFunctionRule + iam:PassRole pattern, so
//     this rule reuses passRoleServiceMultiActionRule from
//     iam_rules_compute_passrole.go: the multi-action helper intersects
//     "principal allows lambda:UpdateFunctionConfiguration" AND "principal
//     allows iam:PassRole" against "every role trusting lambda.amazonaws.com".
//
// Both rules emit iamEdgeExecuteAs edges (the caller can effectively run as
// the resolved role).

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// updateFunctionCodeRule emits execute_as edges from every principal that
// allows lambda:UpdateFunctionCode to every Lambda function's execution role.
// One edge per (caller, function-execution-role) pair. Functions that have no
// execution role link (no EdgeAssumesRole) are skipped silently — the rule
// cannot describe an escalation against a function with no role to inherit.
func updateFunctionCodeRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rctx.Functions) == 0 {
		return nil, nil
	}
	// Resolve every function -> execution role exactly once. The same role
	// may back multiple functions; we still emit one edge per (caller,
	// function) pair so the BFS narrative can attribute the escalation to a
	// specific function. The escalation finding generator can collapse
	// duplicates downstream if desired.
	type funcRole struct {
		funcID string
		roleID string
	}
	pairs := make([]funcRole, 0, len(rctx.Functions))
	for _, fn := range rctx.Functions {
		roleID := functionExecutionRoleID(ctx, rctx.scoped, fn)
		if roleID == "" {
			continue
		}
		pairs = append(pairs, funcRole{funcID: fn.Id, roleID: roleID})
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	var edges []iamInferredEdge
	for _, p := range allPrincipals(rctx) {
		if !principalAllowsAction(ctx, rctx, p, "lambda:UpdateFunctionCode") {
			continue
		}
		for _, pr := range pairs {
			edges = append(edges, iamInferredEdge{
				FromID: p.Id,
				ToID:   pr.roleID,
				Kind:   iamEdgeExecuteAs,
				Reason: "principal can overwrite Lambda function code and execute as its attached role",
			})
		}
	}
	return edges, nil
}

// updateFunctionConfigurationRule emits execute_as edges when a principal
// allows BOTH lambda:UpdateFunctionConfiguration AND iam:PassRole and a
// target role trusts lambda.amazonaws.com. The attacker swaps the existing
// function's execution role for any role they can pass that lambda will
// accept. Reuses the multi-action helper from iam_rules_compute_passrole.go
// because the trust-shape requirement is identical to the v1.1 lambda
// create_function flow.
func updateFunctionConfigurationRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return passRoleServiceMultiActionRule(ctx, rctx, "lambda.amazonaws.com",
		[]string{"lambda:UpdateFunctionConfiguration", "iam:PassRole"},
		"principal can update a Lambda function's configuration to attach a new role and execute as it")
}

// functionExecutionRoleID walks the EdgeAssumesRole edge from a lambda-function
// node to its execution role and returns the role's node ID. Returns empty
// when the function has no execution role link or the lookup fails — both
// cases mean "no escalation edge to emit." Mirrors how lambdaCollector emits
// the relationship in cloud/aws/lambda.go::functionEdges.
func functionExecutionRoleID(ctx context.Context, scoped *cloudReader, fn *knowledgev1.Node) string {
	if scoped == nil || fn.Id == "" {
		return ""
	}
	edges, _ := scoped.iterEdges(ctx, fn.Id, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeAssumesRole})
	for _, e := range edges {
		if e.ToId != "" {
			return e.ToId
		}
	}
	return ""
}

// init registers the two Lambda update rules. Names follow the v1.1
// lower_snake_case convention used elsewhere in the rule registry. Both
// are PassRole-family (confidence 0.9 per OQ-4): update_function_code
// inherits the function's existing execution role, and
// update_function_configuration reattaches a role that lambda.amazonaws.com
// already trusts — both depend on the same trust-policy gating pattern.
func init() {
	registerIAMRule("update_function_code", 0.9, []string{"lambda:UpdateFunctionCode"}, updateFunctionCodeRule)
	registerIAMRule("update_function_configuration", 0.9, []string{"lambda:UpdateFunctionConfiguration", "iam:PassRole"}, updateFunctionConfigurationRule)
}
