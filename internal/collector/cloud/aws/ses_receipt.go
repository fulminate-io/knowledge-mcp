// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"fmt"
	"log/slog"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	sestypes "github.com/aws/aws-sdk-go-v2/service/ses/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collectReceiptRules fetches the active receipt rule set (account-scoped)
// and emits a resource for each rule plus edges for Lambda/S3/SNS actions.
// Returns nil slices if there is no active rule set or on error (fail-open).
func (c *sesCollector) collectReceiptRules(ctx context.Context) ([]cloud.ResourceSpec, []cloud.EdgeSpec) {
	out, err := c.v1client.DescribeActiveReceiptRuleSet(ctx, &ses.DescribeActiveReceiptRuleSetInput{})
	if err != nil {
		slog.Debug("ses: describe active receipt rule set", "error", err)
		return nil, nil
	}
	if out == nil || out.Metadata == nil {
		return nil, nil
	}

	setName := awssdk.ToString(out.Metadata.Name)
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	for _, rule := range out.Rules {
		ruleName := awssdk.ToString(rule.Name)
		ruleID := fmt.Sprintf("arn:aws:ses:%s:%s:receipt-rule-set/%s:receipt-rule/%s",
			c.region, c.accountID, setName, ruleName)

		resources = append(resources, cloud.ResourceSpec{
			ID:           ruleID,
			Name:         ruleName,
			ResourceType: "ses-receipt-rule",
			Region:       c.region,
			Metadata:     sesReceiptRuleMetadata(rule),
		})

		edges = append(edges, receiptRuleEdges(ruleID, rule.Actions)...)
	}

	return resources, edges
}

// receiptRuleEdges extracts edges from a single receipt rule's action list.
func receiptRuleEdges(ruleID string, actions []sestypes.ReceiptAction) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec
	for _, action := range actions {
		edges = append(edges, extractActionEdges(ruleID, action)...)
	}
	return edges
}

// extractActionEdges converts a single ReceiptAction into zero or more
// cloud edges. Handles Lambda, S3, and SNS action types.
func extractActionEdges(ruleID string, action sestypes.ReceiptAction) []cloud.EdgeSpec {
	var edges []cloud.EdgeSpec

	if action.LambdaAction != nil {
		funcARN := awssdk.ToString(action.LambdaAction.FunctionArn)
		if funcARN != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     ruleID,
				TargetID:     funcARN,
				Relationship: kgtypes.EdgeTriggers,
			})
		}
	}

	if action.S3Action != nil {
		bucket := awssdk.ToString(action.S3Action.BucketName)
		if bucket != "" {
			bucketARN := fmt.Sprintf("arn:aws:s3:::%s", bucket)
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     ruleID,
				TargetID:     bucketARN,
				Relationship: kgtypes.EdgeTriggers,
			})
		}
		kmsKey := awssdk.ToString(action.S3Action.KmsKeyArn)
		if kmsKey != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     ruleID,
				TargetID:     kmsKey,
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}
	}

	if action.SNSAction != nil {
		topicARN := awssdk.ToString(action.SNSAction.TopicArn)
		if topicARN != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     ruleID,
				TargetID:     topicARN,
				Relationship: kgtypes.EdgeNotifiesVia,
			})
		}
	}

	return edges
}

// sesReceiptRuleMetadata extracts discriminating fields from a receipt rule.
func sesReceiptRuleMetadata(r sestypes.ReceiptRule) map[string]string {
	m := make(map[string]string, 2)
	if r.Enabled {
		m["enabled"] = "true"
	}
	if r.ScanEnabled {
		m["scan_enabled"] = "true"
	}
	return m
}
