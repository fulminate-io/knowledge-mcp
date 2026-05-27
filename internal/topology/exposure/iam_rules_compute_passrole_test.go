// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iam_rules_compute_passrole_test.go covers the eight Phase 7 PassRole rules
// added in iam_rules_compute_passrole.go. Each rule shares the same shape:
//
//	(principal can call action) AND (target role trusts service principal)
//	  →  one execute_as edge per (principal, role) pair
//
// so a single table-driven TestComputePassRoleRules suffices for the seven
// single-action rules. ecs_run_task is multi-action and is exercised by the
// same table (its action field is the union "ecs:RunTask, iam:PassRole" but
// only one inline policy granting both is needed).
//
// TestComputePassRoleRules_Negative confirms that none of the eight rules
// fire when no principal allows any of their actions.
//
// TestIAMRulesRegistered already pins the registration set; this file does
// not duplicate that check.

// computePassRoleCase describes one row in the TestComputePassRoleRules table.
// fixtureBuilder receives a fresh fixture, registers the attacker (with
// whatever inline policy the rule needs) plus a target role trusting
// serviceTrust, and returns the attacker's expected ID.
type computePassRoleCase struct {
	name         string
	rule         iamRule
	inlineDoc    string
	serviceTrust string
	wantEdges    int
	wantKind     iamInferredEdgeKind
}

// makeTrustPolicy returns a minimal trust policy JSON that allows the named
// AWS service principal to call sts:AssumeRole.
func makeTrustPolicy(servicePrincipal string) string {
	return fmt.Sprintf(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":%q},"Action":"sts:AssumeRole"}]}`,
		servicePrincipal,
	)
}

// TestComputePassRoleRules table-tests every Phase 7 compute PassRole rule.
// Each row builds a fresh cloud fixture with one attacker (inline policy
// granting the rule's action(s)) and one target role (trust policy referencing
// the rule's service principal), invokes the rule, and asserts the expected
// edge count + kind plus that the edge originates from the attacker and
// terminates at the target role.
func TestComputePassRoleRules(t *testing.T) {
	cases := []computePassRoleCase{
		{
			name:         "glue_create_dev_endpoint",
			rule:         glueCreateDevEndpointRule,
			inlineDoc:    `{"Statement":[{"Effect":"Allow","Action":"glue:CreateDevEndpoint","Resource":"*"}]}`,
			serviceTrust: "glue.amazonaws.com",
			wantEdges:    1,
			wantKind:     iamEdgeExecuteAs,
		},
		{
			name:         "sagemaker_create_notebook",
			rule:         sagemakerCreateNotebookRule,
			inlineDoc:    `{"Statement":[{"Effect":"Allow","Action":"sagemaker:CreateNotebookInstance","Resource":"*"}]}`,
			serviceTrust: "sagemaker.amazonaws.com",
			wantEdges:    1,
			wantKind:     iamEdgeExecuteAs,
		},
		{
			name:         "sagemaker_create_training",
			rule:         sagemakerCreateTrainingRule,
			inlineDoc:    `{"Statement":[{"Effect":"Allow","Action":"sagemaker:CreateTrainingJob","Resource":"*"}]}`,
			serviceTrust: "sagemaker.amazonaws.com",
			wantEdges:    1,
			wantKind:     iamEdgeExecuteAs,
		},
		{
			name:         "codebuild_create_project",
			rule:         codebuildCreateProjectRule,
			inlineDoc:    `{"Statement":[{"Effect":"Allow","Action":"codebuild:CreateProject","Resource":"*"}]}`,
			serviceTrust: "codebuild.amazonaws.com",
			wantEdges:    1,
			wantKind:     iamEdgeExecuteAs,
		},
		{
			name:         "codebuild_update_project",
			rule:         codebuildUpdateProjectRule,
			inlineDoc:    `{"Statement":[{"Effect":"Allow","Action":"codebuild:UpdateProject","Resource":"*"}]}`,
			serviceTrust: "codebuild.amazonaws.com",
			wantEdges:    1,
			wantKind:     iamEdgeExecuteAs,
		},
		{
			name:         "cloudformation_create_stack",
			rule:         cloudformationCreateStackRule,
			inlineDoc:    `{"Statement":[{"Effect":"Allow","Action":"cloudformation:CreateStack","Resource":"*"}]}`,
			serviceTrust: "cloudformation.amazonaws.com",
			wantEdges:    1,
			wantKind:     iamEdgeExecuteAs,
		},
		{
			name:         "datapipeline_create_pipeline",
			rule:         datapipelineCreatePipelineRule,
			inlineDoc:    `{"Statement":[{"Effect":"Allow","Action":"datapipeline:CreatePipeline","Resource":"*"}]}`,
			serviceTrust: "datapipeline.amazonaws.com",
			wantEdges:    1,
			wantKind:     iamEdgeExecuteAs,
		},
		{
			name: "ecs_run_task",
			rule: ecsRunTaskRule,
			// ecs_run_task requires BOTH actions on the principal — single
			// inline policy with both Allow statements.
			inlineDoc:    `{"Statement":[{"Effect":"Allow","Action":["ecs:RunTask","iam:PassRole"],"Resource":"*"}]}`,
			serviceTrust: "ecs-tasks.amazonaws.com",
			wantEdges:    1,
			wantKind:     iamEdgeExecuteAs,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCloudFixture(t)
			attackerARN := "arn:aws:iam::111111111111:user/attacker_" + tc.name
			roleARN := "arn:aws:iam::111111111111:role/target_" + tc.name

			attacker := addIAMUserWithInline(t, fx, accountA,
				attackerARN, "attacker_"+tc.name, "pol", tc.inlineDoc)
			role := addIAMRoleWithTrust(t, fx, accountA,
				roleARN, "target_"+tc.name, makeTrustPolicy(tc.serviceTrust))

			rctx := newTestRuleContext(t, fx, accountA)
			edges, err := tc.rule(newTestCtx(t), rctx)
			require.NoError(t, err)
			require.Len(t, edges, tc.wantEdges, "rule %s edge count", tc.name)

			for _, e := range edges {
				assert.Equal(t, attacker.Id, e.FromID, "rule %s FromID", tc.name)
				assert.Equal(t, role.Id, e.ToID, "rule %s ToID", tc.name)
				assert.Equal(t, tc.wantKind, e.Kind, "rule %s edge kind", tc.name)
			}
		})
	}
}

// TestComputePassRoleRules_Negative verifies every compute PassRole rule
// emits zero edges when one principal exists with no relevant actions and
// no target role trusts the rule's service principal. Mirrors the v1.1
// negative-shape pattern: rule must not fire on a benign user.
func TestComputePassRoleRules_Negative(t *testing.T) {
	rules := map[string]iamRule{
		"glue_create_dev_endpoint":     glueCreateDevEndpointRule,
		"sagemaker_create_notebook":    sagemakerCreateNotebookRule,
		"sagemaker_create_training":    sagemakerCreateTrainingRule,
		"codebuild_create_project":     codebuildCreateProjectRule,
		"codebuild_update_project":     codebuildUpdateProjectRule,
		"cloudformation_create_stack":  cloudformationCreateStackRule,
		"datapipeline_create_pipeline": datapipelineCreatePipelineRule,
		"ecs_run_task":                 ecsRunTaskRule,
	}

	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/benign", "benign",
		"ro", `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`)
	rctx := newTestRuleContext(t, fx, accountA)

	for name, rule := range rules {
		t.Run(name, func(t *testing.T) {
			edges, err := rule(newTestCtx(t), rctx)
			require.NoError(t, err)
			assert.Empty(t, edges, "rule %s must not fire on benign principal with no target role", name)
		})
	}
}

// TestEcsRunTaskRule_PartialActionsNegative verifies ecs_run_task does NOT
// fire when the principal allows only one of the two required actions. The
// rule semantics are: principal must allow BOTH ecs:RunTask AND iam:PassRole.
// Single-action rules cannot exercise this branch — only the multi-action
// helper does — so this test covers passRoleServiceMultiActionRule's AND
// short-circuit specifically.
func TestEcsRunTaskRule_PartialActionsNegative(t *testing.T) {
	cases := []struct {
		name      string
		inlineDoc string
	}{
		{
			name:      "only_run_task",
			inlineDoc: `{"Statement":[{"Effect":"Allow","Action":"ecs:RunTask","Resource":"*"}]}`,
		},
		{
			name:      "only_pass_role",
			inlineDoc: `{"Statement":[{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCloudFixture(t)
			addIAMUserWithInline(t, fx, accountA,
				"arn:aws:iam::111111111111:user/partial", "partial", "p", tc.inlineDoc)
			addIAMRoleWithTrust(t, fx, accountA,
				"arn:aws:iam::111111111111:role/ecs-task", "ecs-task",
				makeTrustPolicy("ecs-tasks.amazonaws.com"))

			rctx := newTestRuleContext(t, fx, accountA)
			edges, err := ecsRunTaskRule(newTestCtx(t), rctx)
			require.NoError(t, err)
			assert.Empty(t, edges, "ecs_run_task must require BOTH actions; %s should yield no edges", tc.name)
		})
	}
}

// TestPassRoleServiceMultiActionRule_NoActions verifies the multi-action
// helper short-circuits to zero edges when actions is empty (no inputs, no
// edges) — matches the v1.1 convention used by passRoleServiceRule.
func TestPassRoleServiceMultiActionRule_NoActions(t *testing.T) {
	fx := newCloudFixture(t)
	addIAMUserWithInline(t, fx, accountA, "arn:aws:iam::111111111111:user/admin", "admin",
		"adm", `{"Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)
	addIAMRoleWithTrust(t, fx, accountA, "arn:aws:iam::111111111111:role/svc", "svc",
		makeTrustPolicy("ecs-tasks.amazonaws.com"))

	rctx := newTestRuleContext(t, fx, accountA)
	edges, err := passRoleServiceMultiActionRule(context.Background(), rctx,
		"ecs-tasks.amazonaws.com", nil, "no actions")
	require.NoError(t, err)
	assert.Empty(t, edges)
}
