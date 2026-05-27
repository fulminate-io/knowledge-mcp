// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// orphan_rules_aws_singletons.go registers orphan rules for AWS singleton
// subcollectors: S3, KMS, ACM, ECR, SES, CloudWatch, DynamoDB, VPC peering.

const (
	confidenceS3Bucket        = 0.8
	confidenceKMSKey          = 0.7
	confidenceACMCertificate  = 0.7
	confidenceECRRepository   = 0.8
	confidenceSESIdentity     = 0.7
	confidenceSESReceiptRule  = 0.8
	confidenceCloudWatchAlarm = 0.9
	confidenceDynamoDBTable   = 0.7
	confidenceVPCPeering      = 0.8
)

// --- s3-bucket ---

// s3BucketRule reports an S3 bucket as orphaned when it has zero outbound
// edges of any kind. S3 emits EdgeTriggers, EdgeEncryptsWith, EdgeGrants.
func s3BucketRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasAnyOutgoing(node.Id) {
		return false, confidenceS3Bucket, "", nil
	}
	return true, confidenceS3Bucket,
		fmt.Sprintf("S3 bucket %s has no outbound edges (no encryption, policy grants, or notification targets).", displayName(node)),
		nil
}

// --- kms-key ---

// kmsKeyRule reports a KMS key as orphaned when it has no outbound edges
// (no grants, no aliases) AND no inbound EdgeEncryptsWith references.
func kmsKeyRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasAnyOutgoing(node.Id) {
		return false, confidenceKMSKey, "", nil
	}
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeEncryptsWith) {
		return false, confidenceKMSKey, "", nil
	}
	return true, confidenceKMSKey,
		fmt.Sprintf("KMS key %s has no grants, aliases, or encryption references.", displayName(node)),
		nil
}

// --- acm-certificate ---

// acmCertificateRule reports an ACM certificate as orphaned when it has
// no inbound USES_CERT and no outbound ValidatedBy edges.
func acmCertificateRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeUsesCert) {
		return false, confidenceACMCertificate, "", nil
	}
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeValidatedBy) {
		return false, confidenceACMCertificate, "", nil
	}
	return true, confidenceACMCertificate,
		fmt.Sprintf("ACM certificate %s has no inbound USES_CERT references and no outbound validation edges.", displayName(node)),
		nil
}

// --- ecr-repository ---

// ecrRepositoryRule reports an ECR repository as orphaned when it has no
// inbound USES_IMAGE edges (no workload references) AND no outbound
// EdgeGrants. Confidence 0.8.
func ecrRepositoryRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeUsesImage) {
		return false, confidenceECRRepository, "", nil
	}
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeGrants) {
		return false, confidenceECRRepository, "", nil
	}
	return true, confidenceECRRepository,
		fmt.Sprintf("ECR repository %s has no image consumers and no policy grants.", displayName(node)),
		nil
}

// --- ses-identity ---

// sesIdentityRule reports an SES identity as orphaned when it has no
// outbound edges of any kind (no notification topics, no triggers).
func sesIdentityRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasAnyOutgoing(node.Id) {
		return false, confidenceSESIdentity, "", nil
	}
	return true, confidenceSESIdentity,
		fmt.Sprintf("SES identity %s has no notification targets or other outbound edges.", displayName(node)),
		nil
}

// --- ses-receipt-rule ---

// sesReceiptRuleRule reports an SES receipt rule as orphaned when it has
// no outbound edges (no Lambda, S3, or SNS action targets).
func sesReceiptRuleRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasAnyOutgoing(node.Id) {
		return false, confidenceSESReceiptRule, "", nil
	}
	return true, confidenceSESReceiptRule,
		fmt.Sprintf("SES receipt rule %s has no action targets (Lambda, S3, or SNS).", displayName(node)),
		nil
}

// --- cloudwatch-alarm ---

// cloudwatchAlarmRule reports an alarm as orphaned when it has no outbound
// EdgeMonitors AND no outbound EdgeNotifiesVia — a dead alarm.
func cloudwatchAlarmRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeMonitors) {
		return false, confidenceCloudWatchAlarm, "", nil
	}
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeNotifiesVia) {
		return false, confidenceCloudWatchAlarm, "", nil
	}
	return true, confidenceCloudWatchAlarm,
		fmt.Sprintf("CloudWatch alarm %s has no monitoring targets and no notification destinations.", displayName(node)),
		nil
}

func init() {
	registerOrphanRule("s3-bucket", s3BucketRule)
	registerOrphanRule("kms-key", kmsKeyRule)
	registerOrphanRule("acm-certificate", acmCertificateRule)
	registerOrphanRule("ecr-repository", ecrRepositoryRule)
	registerOrphanRule("ses-identity", sesIdentityRule)
	registerOrphanRule("ses-receipt-rule", sesReceiptRuleRule)
	registerOrphanRule("cloudwatch-alarm", cloudwatchAlarmRule)
	registerOrphanRule("dynamodb-table", dynamodbTableRule)
	registerOrphanRule("vpc-peering-connection", vpcPeeringRule)
}

// --- vpc-peering-connection ---

// vpcPeeringRule reports a VPC peering connection as orphaned when it has
// no outbound EdgePeeredWith edges (meaning PostPopulate didn't resolve
// the VPC endpoints, or the peering is inactive).
func vpcPeeringRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasAnyOutgoing(node.Id) {
		return false, confidenceVPCPeering, "", nil
	}
	return true, confidenceVPCPeering,
		fmt.Sprintf("VPC peering connection %s has no resolved peering edges.", displayName(node)),
		nil
}

// --- dynamodb-table ---

// dynamodbTableRule reports a DynamoDB table as orphaned when it has no
// outbound edges at all (no encryption, no streams, no backups).
func dynamodbTableRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasAnyOutgoing(node.Id) {
		return false, confidenceDynamoDBTable, "", nil
	}
	return true, confidenceDynamoDBTable,
		fmt.Sprintf("DynamoDB table %s has no outbound edges (no encryption, streams, or backups).", displayName(node)),
		nil
}
