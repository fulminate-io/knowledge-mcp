// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// orphan_rules_aws_test.go covers the v1 AWS orphan rules. Each rule has a
// positive test (orphan detected) and a negative test (rule does NOT fire
// when the resource is referenced). The cross-account iam-role behavior gets
// a dedicated multi-account test that exercises the FetchGraphNames path the
// rule walks for cross-account trust.

const acctA = "111111111111"
const acctB = "222222222222"

// TestAWSRulesRegistered asserts that all v1 AWS rules are present in the
// dispatch table. This catches accidental deletion of an init() call.
func TestAWSRulesRegistered(t *testing.T) {
	expected := []string{
		"elbv2-loadbalancer",
		"elbv2-targetgroup",
		"ebs-volume",
		"security-group",
		"iam-role",
		"iam-policy",
		"s3-bucket",
		"kms-key",
		"acm-certificate",
		"ecr-repository",
		"ses-identity",
		"ses-receipt-rule",
		"cloudwatch-alarm",
		"dynamodb-table",
		"vpc-peering-connection",
	}
	for _, rt := range expected {
		_, ok := lookupOrphanRule(rt)
		assert.Truef(t, ok, "expected orphan rule registered for %q", rt)
	}
}

// --- elbv2-loadbalancer ---

func TestELBv2LoadBalancerRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "lb-1", "lb-1", "elbv2-loadbalancer", nil)

	orphan, conf, summary, err := elbv2LoadBalancerRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "lb-1"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 1.0, conf, 0.0001)
	assert.Contains(t, summary, "lb-1")
}

func TestELBv2LoadBalancerRule_HasTarget_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "lb-2", "lb-2", "elbv2-loadbalancer", nil)
	fx.AddCloudResource(acctA, "tg-2", "tg-2", "elbv2-targetgroup", nil)
	fx.AddEdge(acctA, "lb-2", "tg-2", kgtypes.EdgeTargets)

	orphan, _, _, err := elbv2LoadBalancerRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "lb-2"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- elbv2-targetgroup ---

func TestELBv2TargetGroupRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "tg-1", "tg-1", "elbv2-targetgroup", nil)

	orphan, conf, _, err := elbv2TargetGroupRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "tg-1"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.9, conf, 0.0001)
}

func TestELBv2TargetGroupRule_AttachedLB_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "tg-3", "tg-3", "elbv2-targetgroup", nil)
	fx.AddCloudResource(acctA, "lb-3", "lb-3", "elbv2-loadbalancer", nil)
	fx.AddEdge(acctA, "lb-3", "tg-3", kgtypes.EdgeTargets)

	orphan, _, _, err := elbv2TargetGroupRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "tg-3"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- ebs-volume ---

func TestEBSVolumeRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "vol-1", "vol-1", "ebs-volume", nil)

	orphan, conf, _, err := ebsVolumeRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "vol-1"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 1.0, conf, 0.0001)
}

func TestEBSVolumeRule_Attached_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "vol-2", "vol-2", "ebs-volume", nil)
	fx.AddCloudResource(acctA, "i-2", "i-2", "ec2-instance", nil)
	fx.AddEdge(acctA, "vol-2", "i-2", kgtypes.EdgeBoundTo)

	orphan, _, _, err := ebsVolumeRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "vol-2"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- security-group ---

func TestSecurityGroupRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "sg-1", "sg-1", "security-group", nil)

	orphan, conf, _, err := securityGroupRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "sg-1"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.9, conf, 0.0001)
}

func TestSecurityGroupRule_Used_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "sg-2", "sg-2", "security-group", nil)
	fx.AddCloudResource(acctA, "i-3", "i-3", "ec2-instance", nil)
	fx.AddEdge(acctA, "i-3", "sg-2", kgtypes.EdgeUsesSecurityGroup)

	orphan, _, _, err := securityGroupRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "sg-2"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- iam-role: same-account positive/negative + cross-account ---

func TestIAMRoleRule_Orphan_NoReferences(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:iam::111:role/r1", "r1", "iam-role", nil)

	orphan, conf, _, err := iamRoleRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:iam::111:role/r1"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.7, conf, 0.0001)
}

func TestIAMRoleRule_AssumedSameAccount_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:iam::111:role/r2", "r2", "iam-role", nil)
	fx.AddCloudResource(acctA, "arn:aws:lambda::111:function/f", "f", "lambda-function", nil)
	fx.AddEdge(acctA, "arn:aws:lambda::111:function/f", "arn:aws:iam::111:role/r2", kgtypes.EdgeAssumesRole)

	orphan, _, _, err := iamRoleRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:iam::111:role/r2"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// TestIAMRoleRule_AssumedFromOtherAccount_NotOrphan exercises the
// cross-account walk. A role lives in account A; a Lambda in account B
// assumes it. The rule must walk FetchGraphNames(GraphCloud), find the
// inbound EdgeAssumesRole in account B's graph, and return non-orphan.
func TestIAMRoleRule_AssumedFromOtherAccount_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	roleARN := "arn:aws:iam::111:role/cross-account-role"
	fx.AddCloudResource(acctA, roleARN, "cross-account-role", "iam-role", nil)

	// Account B has a Lambda that assumes the role from account A.
	fx.AddCloudResource(acctB, "arn:aws:lambda::222:function/consumer", "consumer", "lambda-function", nil)
	fx.AddCloudResource(acctB, roleARN, "cross-account-role", "iam-role", nil)
	fx.AddEdge(acctB, "arn:aws:lambda::222:function/consumer", roleARN, kgtypes.EdgeAssumesRole)

	orphan, _, _, err := iamRoleRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, roleARN))
	require.NoError(t, err)
	assert.False(t, orphan, "iam-role assumed from another account must not be flagged as orphaned")
}

// --- iam-policy ---

func TestIAMPolicyRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:iam::111:policy/p1", "p1", "iam-policy", nil)

	orphan, conf, _, err := iamPolicyRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:iam::111:policy/p1"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.9, conf, 0.0001)
}

func TestIAMPolicyRule_AttachedToRole_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:iam::111:policy/p2", "p2", "iam-policy", nil)
	fx.AddCloudResource(acctA, "arn:aws:iam::111:role/role-with-policy", "role-with-policy", "iam-role", nil)
	fx.AddEdge(acctA, "arn:aws:iam::111:role/role-with-policy", "arn:aws:iam::111:policy/p2", kgtypes.EdgeGrants)

	orphan, _, _, err := iamPolicyRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:iam::111:policy/p2"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- s3-bucket ---

func TestS3BucketRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:s3:::lonely-bucket", "lonely-bucket", "s3-bucket", nil)

	orphan, conf, _, err := s3BucketRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:s3:::lonely-bucket"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.8, conf, 0.0001)
}

func TestS3BucketRule_HasEdge_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:s3:::connected", "connected", "s3-bucket", nil)
	fx.AddCloudResource(acctA, "arn:aws:kms:us-east-1:111:key/k1", "k1", "kms-key", nil)
	fx.AddEdge(acctA, "arn:aws:s3:::connected", "arn:aws:kms:us-east-1:111:key/k1", kgtypes.EdgeEncryptsWith)

	orphan, _, _, err := s3BucketRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:s3:::connected"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- kms-key ---

func TestKMSKeyRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:kms:us-east-1:111:key/k-orphan", "k-orphan", "kms-key", nil)

	orphan, conf, _, err := kmsKeyRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:kms:us-east-1:111:key/k-orphan"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.7, conf, 0.0001)
}

func TestKMSKeyRule_HasGrant_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:kms:us-east-1:111:key/k-grants", "k-grants", "kms-key", nil)
	fx.AddCloudResource(acctA, "arn:aws:iam::111:root", "root", "iam-user", nil)
	fx.AddEdge(acctA, "arn:aws:kms:us-east-1:111:key/k-grants", "arn:aws:iam::111:root", kgtypes.EdgeGrants)

	orphan, _, _, err := kmsKeyRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:kms:us-east-1:111:key/k-grants"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

func TestKMSKeyRule_HasEncryptionRef_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:kms:us-east-1:111:key/k-enc", "k-enc", "kms-key", nil)
	fx.AddCloudResource(acctA, "arn:aws:s3:::encrypted-bucket", "encrypted-bucket", "s3-bucket", nil)
	fx.AddEdge(acctA, "arn:aws:s3:::encrypted-bucket", "arn:aws:kms:us-east-1:111:key/k-enc", kgtypes.EdgeEncryptsWith)

	orphan, _, _, err := kmsKeyRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:kms:us-east-1:111:key/k-enc"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- acm-certificate ---

func TestACMCertificateRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:acm:us-east-1:111:certificate/c1", "c1", "acm-certificate", nil)

	orphan, conf, _, err := acmCertificateRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:acm:us-east-1:111:certificate/c1"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.7, conf, 0.0001)
}

func TestACMCertificateRule_HasUsesCert_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:acm:us-east-1:111:certificate/c2", "c2", "acm-certificate", nil)
	fx.AddCloudResource(acctA, "arn:aws:elasticloadbalancing:us-east-1:111:listener/l1", "l1", "elbv2-listener", nil)
	fx.AddEdge(acctA, "arn:aws:elasticloadbalancing:us-east-1:111:listener/l1", "arn:aws:acm:us-east-1:111:certificate/c2", kgtypes.EdgeUsesCert)

	orphan, _, _, err := acmCertificateRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:acm:us-east-1:111:certificate/c2"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

func TestACMCertificateRule_HasValidatedBy_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:acm:us-east-1:111:certificate/c3", "c3", "acm-certificate", nil)
	fx.AddCloudResource(acctA, "example.com", "example.com", "route53-zone", nil)
	fx.AddEdge(acctA, "arn:aws:acm:us-east-1:111:certificate/c3", "example.com", kgtypes.EdgeValidatedBy)

	orphan, _, _, err := acmCertificateRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:acm:us-east-1:111:certificate/c3"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- ecr-repository ---

func TestECRRepositoryRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:ecr:us-east-1:111:repository/orphan", "orphan", "ecr-repository", nil)

	orphan, conf, _, err := ecrRepositoryRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:ecr:us-east-1:111:repository/orphan"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.8, conf, 0.0001)
}

func TestECRRepositoryRule_HasImageConsumer_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:ecr:us-east-1:111:repository/used", "used", "ecr-repository", nil)
	fx.AddCloudResource(acctA, "arn:aws:ecs:us-east-1:111:service/svc", "svc", "ecs-service", nil)
	fx.AddEdge(acctA, "arn:aws:ecs:us-east-1:111:service/svc", "arn:aws:ecr:us-east-1:111:repository/used", kgtypes.EdgeUsesImage)

	orphan, _, _, err := ecrRepositoryRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:ecr:us-east-1:111:repository/used"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

func TestECRRepositoryRule_HasGrant_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:ecr:us-east-1:111:repository/granted", "granted", "ecr-repository", nil)
	fx.AddCloudResource(acctA, "arn:aws:iam::111:root", "root", "iam-user", nil)
	fx.AddEdge(acctA, "arn:aws:ecr:us-east-1:111:repository/granted", "arn:aws:iam::111:root", kgtypes.EdgeGrants)

	orphan, _, _, err := ecrRepositoryRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:ecr:us-east-1:111:repository/granted"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- ses-identity ---

func TestSESIdentityRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:ses:us-east-1:111:identity/example.com", "example.com", "ses-identity", nil)

	orphan, conf, _, err := sesIdentityRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:ses:us-east-1:111:identity/example.com"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.7, conf, 0.0001)
}

func TestSESIdentityRule_HasNotify_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:ses:us-east-1:111:identity/example.com", "example.com", "ses-identity", nil)
	fx.AddCloudResource(acctA, "arn:aws:sns:us-east-1:111:bounces", "bounces", "sns-topic", nil)
	fx.AddEdge(acctA, "arn:aws:ses:us-east-1:111:identity/example.com", "arn:aws:sns:us-east-1:111:bounces", kgtypes.EdgeNotifiesVia)

	orphan, _, _, err := sesIdentityRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:ses:us-east-1:111:identity/example.com"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- ses-receipt-rule ---

func TestSESReceiptRuleRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:ses:us-east-1:111:receipt-rule/empty", "empty", "ses-receipt-rule", nil)

	orphan, conf, _, err := sesReceiptRuleRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:ses:us-east-1:111:receipt-rule/empty"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.8, conf, 0.0001)
}

func TestSESReceiptRuleRule_HasTrigger_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:ses:us-east-1:111:receipt-rule/lambda", "lambda", "ses-receipt-rule", nil)
	fx.AddCloudResource(acctA, "arn:aws:lambda:us-east-1:111:function/f", "f", "lambda-function", nil)
	fx.AddEdge(acctA, "arn:aws:ses:us-east-1:111:receipt-rule/lambda", "arn:aws:lambda:us-east-1:111:function/f", kgtypes.EdgeTriggers)

	orphan, _, _, err := sesReceiptRuleRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:ses:us-east-1:111:receipt-rule/lambda"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- cloudwatch-alarm ---

func TestCloudwatchAlarmRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:cloudwatch:us-east-1:111:alarm:dead", "dead", "cloudwatch-alarm", nil)

	orphan, conf, _, err := cloudwatchAlarmRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:cloudwatch:us-east-1:111:alarm:dead"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.9, conf, 0.0001)
}

func TestCloudwatchAlarmRule_HasMonitors_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:cloudwatch:us-east-1:111:alarm:monitors", "monitors", "cloudwatch-alarm", nil)
	fx.AddCloudResource(acctA, "arn:aws:ec2:us-east-1:111:instance/i-1", "i-1", "ec2-instance", nil)
	fx.AddEdge(acctA, "arn:aws:cloudwatch:us-east-1:111:alarm:monitors", "arn:aws:ec2:us-east-1:111:instance/i-1", kgtypes.EdgeMonitors)

	orphan, _, _, err := cloudwatchAlarmRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:cloudwatch:us-east-1:111:alarm:monitors"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

func TestCloudwatchAlarmRule_HasNotifiesVia_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:cloudwatch:us-east-1:111:alarm:notifies", "notifies", "cloudwatch-alarm", nil)
	fx.AddCloudResource(acctA, "arn:aws:sns:us-east-1:111:alerts", "alerts", "sns-topic", nil)
	fx.AddEdge(acctA, "arn:aws:cloudwatch:us-east-1:111:alarm:notifies", "arn:aws:sns:us-east-1:111:alerts", kgtypes.EdgeNotifiesVia)

	orphan, _, _, err := cloudwatchAlarmRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:cloudwatch:us-east-1:111:alarm:notifies"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- dynamodb-table ---

func TestDynamoDBTableRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:dynamodb:us-east-1:111:table/empty", "empty", "dynamodb-table", nil)

	orphan, conf, _, err := dynamodbTableRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:dynamodb:us-east-1:111:table/empty"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.7, conf, 0.0001)
}

func TestDynamoDBTableRule_HasBackup_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:dynamodb:us-east-1:111:table/backed", "backed", "dynamodb-table", nil)
	fx.AddCloudResource(acctA, "aws:dynamodb:pitr/backed", "pitr", "dynamodb-pitr", nil)
	fx.AddEdge(acctA, "arn:aws:dynamodb:us-east-1:111:table/backed", "aws:dynamodb:pitr/backed", kgtypes.EdgeBackedUpBy)

	orphan, _, _, err := dynamodbTableRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:dynamodb:us-east-1:111:table/backed"))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- vpc-peering-connection ---

func TestVPCPeeringRule_Orphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:ec2:us-east-1:111:vpc-peering-connection/pcx-orphan", "pcx-orphan", "vpc-peering-connection", nil)

	orphan, conf, _, err := vpcPeeringRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:ec2:us-east-1:111:vpc-peering-connection/pcx-orphan"))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, 0.8, conf, 0.0001)
}

func TestVPCPeeringRule_HasPeeredWith_NotOrphan(t *testing.T) {
	fx := newCloudFixture(t)
	fx.AddCloudResource(acctA, "arn:aws:ec2:us-east-1:111:vpc-peering-connection/pcx-ok", "pcx-ok", "vpc-peering-connection", nil)
	fx.AddCloudResource(acctA, "arn:aws:ec2:us-east-1:111:vpc/vpc-1", "vpc-1", "vpc", nil)
	fx.AddEdge(acctA, "arn:aws:ec2:us-east-1:111:vpc-peering-connection/pcx-ok", "arn:aws:ec2:us-east-1:111:vpc/vpc-1", kgtypes.EdgePeeredWith)

	orphan, _, _, err := vpcPeeringRule(context.Background(), fx, acctA, fx.orphanGraphFor(t, acctA), fx.nodeFor(t, acctA, "arn:aws:ec2:us-east-1:111:vpc-peering-connection/pcx-ok"))
	require.NoError(t, err)
	assert.False(t, orphan)
}
