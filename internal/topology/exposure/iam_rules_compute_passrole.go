// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_rules_compute_passrole.go ports the PMapper compute-service PassRole
// edges that v1.1 did not yet cover. v1.1 already ships passRoleLambdaRule,
// runInstancesRule, and createFunctionRule in iam_rules_passrole.go (which
// remains untouched per OQ-9). This file extends the matrix to seven more
// AWS compute services that accept an IAM role at resource-creation time
// plus ECS run-task, which is multi-action.
//
// All seven single-action rules are thin wrappers over the existing
// passRoleServiceRule helper from iam_rules_passrole.go: each picks the
// service principal the target role must trust and the AWS API action the
// caller must be allowed to invoke. The cross-product of "principals that
// can call action" × "roles that trust serviceName" produces one
// iamEdgeExecuteAs edge per (caller, role) pair.
//
// ecsRunTaskRule is multi-action: PMapper's ecs_edges.py models the run-task
// flow as principal-must-allow ecs:RunTask AND iam:PassRole, with the target
// role's trust policy referencing ecs-tasks.amazonaws.com. passRoleServiceRule
// only checks one action, so a small passRoleServiceMultiActionRule helper
// lives below — same shape, intersect over a list of actions.
//
// Rules registered:
//
//	glue_create_dev_endpoint     — glue:CreateDevEndpoint        + glue.amazonaws.com trust
//	sagemaker_create_notebook    — sagemaker:CreateNotebookInstance + sagemaker.amazonaws.com trust
//	sagemaker_create_training    — sagemaker:CreateTrainingJob   + sagemaker.amazonaws.com trust
//	codebuild_create_project     — codebuild:CreateProject       + codebuild.amazonaws.com trust
//	codebuild_update_project     — codebuild:UpdateProject       + codebuild.amazonaws.com trust
//	cloudformation_create_stack  — cloudformation:CreateStack    + cloudformation.amazonaws.com trust
//	datapipeline_create_pipeline — datapipeline:CreatePipeline   + datapipeline.amazonaws.com trust
//	ecs_run_task                 — ecs:RunTask + iam:PassRole    + ecs-tasks.amazonaws.com trust
//
// Deferred to follow-up:
//
//   - glue_update_dev_endpoint — UpdateDevEndpoint does NOT require
//     iam:PassRole; it mutates an existing endpoint that already has a role
//     attached. The escalation shape is closer to lambda:UpdateFunctionCode
//     (an "update existing resource" flow), so it belongs in the parallel
//     iam_rules_lambda_update.go workstream or its own follow-up rather
//     than this PassRole-gated file.

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// glueCreateDevEndpointRule emits execute_as edges when a principal can call
// glue:CreateDevEndpoint and a target role trusts glue.amazonaws.com. The
// dev-endpoint runs as that role and the caller can SSH into the endpoint to
// operate as the role.
func glueCreateDevEndpointRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return passRoleServiceRule(ctx, rctx, "glue.amazonaws.com", "glue:CreateDevEndpoint",
		"principal can create a Glue dev endpoint and exec into it as the attached role")
}

// sagemakerCreateNotebookRule emits execute_as edges when a principal can
// call sagemaker:CreateNotebookInstance and a target role trusts
// sagemaker.amazonaws.com. The notebook runs as that role and the caller can
// open a notebook session executing arbitrary code as the role.
func sagemakerCreateNotebookRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return passRoleServiceRule(ctx, rctx, "sagemaker.amazonaws.com", "sagemaker:CreateNotebookInstance",
		"principal can create a SageMaker notebook instance and execute code as the attached role")
}

// sagemakerCreateTrainingRule emits execute_as edges when a principal can
// call sagemaker:CreateTrainingJob and a target role trusts
// sagemaker.amazonaws.com. The training job runs as that role and the caller
// supplies the training image, so arbitrary code executes as the role.
func sagemakerCreateTrainingRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return passRoleServiceRule(ctx, rctx, "sagemaker.amazonaws.com", "sagemaker:CreateTrainingJob",
		"principal can create a SageMaker training job and execute container code as the attached role")
}

// codebuildCreateProjectRule emits execute_as edges when a principal can call
// codebuild:CreateProject and a target role trusts codebuild.amazonaws.com.
// CodeBuild executes the buildspec as the project's service role.
func codebuildCreateProjectRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return passRoleServiceRule(ctx, rctx, "codebuild.amazonaws.com", "codebuild:CreateProject",
		"principal can create a CodeBuild project and execute buildspec commands as the attached role")
}

// codebuildUpdateProjectRule emits execute_as edges when a principal can call
// codebuild:UpdateProject and a target role trusts codebuild.amazonaws.com.
// PMapper models this as a separate edge from CreateProject because
// UpdateProject takes the same iam:PassRole permission and lets an attacker
// repoint an existing project at any other role they can pass.
func codebuildUpdateProjectRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return passRoleServiceRule(ctx, rctx, "codebuild.amazonaws.com", "codebuild:UpdateProject",
		"principal can update a CodeBuild project to attach a new role and execute buildspec as that role")
}

// cloudformationCreateStackRule emits execute_as edges when a principal can
// call cloudformation:CreateStack and a target role trusts
// cloudformation.amazonaws.com. CloudFormation provisions the stack using the
// service role; the caller controls the template, so any resources the role
// can create become the attacker's leverage.
func cloudformationCreateStackRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return passRoleServiceRule(ctx, rctx, "cloudformation.amazonaws.com", "cloudformation:CreateStack",
		"principal can create a CloudFormation stack and provision arbitrary resources as the service role")
}

// datapipelineCreatePipelineRule emits execute_as edges when a principal can
// call datapipeline:CreatePipeline and a target role trusts
// datapipeline.amazonaws.com. Data Pipeline executes the pipeline definition
// as the service role; the caller supplies the activities (which can shell
// out), so arbitrary code runs as the role.
func datapipelineCreatePipelineRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return passRoleServiceRule(ctx, rctx, "datapipeline.amazonaws.com", "datapipeline:CreatePipeline",
		"principal can create a Data Pipeline and run shell activities as the attached role")
}

// ecsRunTaskRule emits execute_as edges when a principal can both call
// ecs:RunTask AND iam:PassRole and a target role trusts ecs-tasks.amazonaws.com
// (the trust principal AWS uses for ECS task roles, distinct from
// ecs.amazonaws.com which is the service role principal). PMapper's
// ecs_edges.py requires both actions on the calling principal because
// RunTask without PassRole only lets the attacker reuse a pre-existing
// task definition, and PassRole alone lets them attach the role somewhere
// else but not actually trigger an ECS task. The intersection captures
// the canonical "launch a task as this role" capability.
func ecsRunTaskRule(ctx context.Context, rctx *iamRuleContext) ([]iamInferredEdge, error) {
	return passRoleServiceMultiActionRule(ctx, rctx, "ecs-tasks.amazonaws.com",
		[]string{"ecs:RunTask", "iam:PassRole"},
		"principal can run an ECS task and pass an arbitrary role to it, executing the task container as that role")
}

// passRoleServiceMultiActionRule is the multi-action sibling of
// passRoleServiceRule from iam_rules_passrole.go. It emits one
// iamEdgeExecuteAs edge per (principal, role) pair where the principal
// allows EVERY action in actions and role trusts serviceName. The semantics
// match passRoleServiceRule otherwise: roles must be discovered via
// rolesTrustingService, principals via allPrincipals, action allowance via
// principalAllowsAction. Empty actions slice yields zero edges (vacuous AND
// over an empty set is true, but the rule is meaningless without at least
// one action and silently emitting nothing matches the v1.1 convention of
// "no inputs, no edges").
func passRoleServiceMultiActionRule(
	ctx context.Context,
	rctx *iamRuleContext,
	serviceName string,
	actions []string,
	reason string,
) ([]iamInferredEdge, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(actions) == 0 {
		return nil, nil
	}
	targets := rolesTrustingService(rctx, serviceName)
	if len(targets) == 0 {
		return nil, nil
	}
	var edges []iamInferredEdge
	for _, p := range allPrincipals(rctx) {
		if !principalAllowsAllActions(ctx, rctx, p, actions) {
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

// principalAllowsAllActions returns true if the principal allows EVERY
// listed action on resource "*". Short-circuits on the first denial. Used
// by ecsRunTaskRule via passRoleServiceMultiActionRule.
func principalAllowsAllActions(
	ctx context.Context, rctx *iamRuleContext, principal *knowledgev1.Node, actions []string,
) bool {
	for _, action := range actions {
		if !principalAllowsAction(ctx, rctx, principal, action) {
			return false
		}
	}
	return true
}

// init registers the eight compute PassRole rules. Registration name format
// matches the v1.1 convention (lower_snake_case, service-prefixed) so the
// rule dispatch table reads cleanly when sorted. All eight are PassRole
// family (confidence 0.9 per OQ-4): the escalation depends on the target
// role's trust policy also trusting the relevant service principal.
func init() {
	registerIAMRule("glue_create_dev_endpoint", 0.9, []string{"glue:CreateDevEndpoint"}, glueCreateDevEndpointRule)
	registerIAMRule("sagemaker_create_notebook", 0.9, []string{"sagemaker:CreateNotebookInstance"}, sagemakerCreateNotebookRule)
	registerIAMRule("sagemaker_create_training", 0.9, []string{"sagemaker:CreateTrainingJob"}, sagemakerCreateTrainingRule)
	registerIAMRule("codebuild_create_project", 0.9, []string{"codebuild:CreateProject"}, codebuildCreateProjectRule)
	registerIAMRule("codebuild_update_project", 0.9, []string{"codebuild:UpdateProject"}, codebuildUpdateProjectRule)
	registerIAMRule("cloudformation_create_stack", 0.9, []string{"cloudformation:CreateStack"}, cloudformationCreateStackRule)
	registerIAMRule("datapipeline_create_pipeline", 0.9, []string{"datapipeline:CreatePipeline"}, datapipelineCreatePipelineRule)
	registerIAMRule("ecs_run_task", 0.9, []string{"ecs:RunTask", "iam:PassRole"}, ecsRunTaskRule)
}
