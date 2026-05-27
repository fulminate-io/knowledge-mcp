// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type sqsCollector struct {
	client    *sqs.Client
	region    string
	accountID string
}

func newSQSCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &sqsCollector{
		client:    sqs.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *sqsCollector) Name() string { return "sqs" }

func (c *sqsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := sqs.NewListQueuesPaginator(c.client, &sqs.ListQueuesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("sqs: list queues: %w", err)
		}

		for _, queueURL := range page.QueueUrls {
			res, qEdges, err := c.collectQueue(ctx, queueURL)
			if err != nil {
				return cloud.SubCollectorResult{}, err
			}
			resources = append(resources, res)
			edges = append(edges, qEdges...)
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// collectQueue fetches attributes for a single SQS queue and builds its resource + edges.
func (c *sqsCollector) collectQueue(ctx context.Context, queueURL string) (cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	attrs, err := c.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       awssdk.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameAll},
	})
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("sqs: get queue attributes for %s: %w", queueURL, err)
	}

	queueARN := attrs.Attributes["QueueArn"]

	// Extract queue name from URL (last path segment).
	parts := strings.Split(queueURL, "/")
	queueName := parts[len(parts)-1]

	content, err := json.Marshal(attrs.Attributes)
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("sqs: marshal: %w", err)
	}

	res := cloud.ResourceSpec{
		ID:           queueARN,
		Name:         queueName,
		ResourceType: "sqs-queue",
		Region:       c.region,
		Content:      content,
		Metadata:     sqsQueueMetadata(attrs.Attributes),
	}

	var edges []cloud.EdgeSpec

	// SQS → KMS key (server-side encryption)
	if kmsKeyID := attrs.Attributes["KmsMasterKeyId"]; kmsKeyID != "" {
		kmsARN := resolveKMSKeyARN(kmsKeyID, c.region, c.accountID)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     queueARN,
			TargetID:     kmsARN,
			Relationship: kgtypes.EdgeEncryptsWith,
			Metadata:     map[string]string{"encryption_scope": "queue"},
		})
	}

	// SQS → Dead-letter queue (RedrivePolicy)
	if redriveJSON := attrs.Attributes["RedrivePolicy"]; redriveJSON != "" {
		var redrive struct {
			DeadLetterTargetArn string `json:"deadLetterTargetArn"`
		}
		if json.Unmarshal([]byte(redriveJSON), &redrive) == nil && redrive.DeadLetterTargetArn != "" {
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     queueARN,
				TargetID:     redrive.DeadLetterTargetArn,
				Relationship: kgtypes.EdgeDeadLettersTo,
			})
		}
	}

	return res, edges, nil
}

// resolveKMSKeyARN converts a KMS key ID or alias to a full ARN.
// If the value is already an ARN (starts with "arn:"), it is returned as-is.
// Otherwise, it's treated as a key ID and constructed into an ARN.
func resolveKMSKeyARN(keyIDOrARN, region, accountID string) string {
	if strings.HasPrefix(keyIDOrARN, "arn:") {
		return keyIDOrARN
	}
	// Could be a key ID (UUID) or an alias (alias/aws/sqs).
	if strings.HasPrefix(keyIDOrARN, "alias/") {
		return fmt.Sprintf("arn:aws:kms:%s:%s:%s", region, accountID, keyIDOrARN)
	}
	return fmt.Sprintf("arn:aws:kms:%s:%s:key/%s", region, accountID, keyIDOrARN)
}

// sqsQueueMetadata extracts discriminating fields from SQS queue attributes.
func sqsQueueMetadata(attrs map[string]string) map[string]string {
	m := make(map[string]string, 3)
	if v := attrs["FifoQueue"]; v != "" {
		m["fifo_queue"] = v
	}
	if v := attrs["VisibilityTimeout"]; v != "" {
		m["visibility_timeout"] = v
	}
	if v := attrs["ApproximateNumberOfMessages"]; v != "" {
		m["approximate_number_of_messages"] = v
	}
	return m
}
