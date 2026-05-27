// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

type snsCollector struct {
	client    *sns.Client
	region    string
	accountID string
}

func newSNSCollector(cfg awssdk.Config, region, accountID string) cloud.SubCollector {
	return &snsCollector{
		client:    sns.NewFromConfig(cfg),
		region:    region,
		accountID: accountID,
	}
}

func (c *snsCollector) Name() string { return "sns" }

func (c *snsCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var (
		resources []cloud.ResourceSpec
		edges     []cloud.EdgeSpec
	)

	paginator := sns.NewListTopicsPaginator(c.client, &sns.ListTopicsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return cloud.SubCollectorResult{}, fmt.Errorf("sns: list topics: %w", err)
		}

		for _, topic := range page.Topics {
			topicARN := awssdk.ToString(topic.TopicArn)

			res, topicEdges, err := c.collectTopic(ctx, topicARN)
			if err != nil {
				return cloud.SubCollectorResult{}, err
			}
			resources = append(resources, res)
			edges = append(edges, topicEdges...)
		}
	}

	return cloud.SubCollectorResult{
		Resources: resources,
		Edges:     edges,
	}, nil
}

// collectTopic fetches topic attributes and subscriptions, returning the resource and edges.
func (c *snsCollector) collectTopic(ctx context.Context, topicARN string) (cloud.ResourceSpec, []cloud.EdgeSpec, error) {
	attrs, err := c.client.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{
		TopicArn: awssdk.String(topicARN),
	})
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("sns: get topic attributes for %s: %w", topicARN, err)
	}

	// Extract topic name from ARN (last segment after ':').
	parts := strings.Split(topicARN, ":")
	topicName := parts[len(parts)-1]

	content, err := json.Marshal(attrs.Attributes)
	if err != nil {
		return cloud.ResourceSpec{}, nil, fmt.Errorf("sns: marshal: %w", err)
	}

	res := cloud.ResourceSpec{
		ID:           topicARN,
		Name:         topicName,
		ResourceType: "sns-topic",
		Region:       c.region,
		Content:      content,
		Metadata:     snsTopicMetadata(attrs.Attributes),
	}

	var edges []cloud.EdgeSpec

	// SNS → KMS key (server-side encryption)
	if kmsKeyID := attrs.Attributes["KmsMasterKeyId"]; kmsKeyID != "" {
		kmsARN := resolveKMSKeyARN(kmsKeyID, c.region, c.accountID)
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     topicARN,
			TargetID:     kmsARN,
			Relationship: kgtypes.EdgeEncryptsWith,
			Metadata:     map[string]string{"encryption_scope": "topic"},
		})
	}

	// SNS → subscriptions (all protocols).
	subEdges, err := c.collectSubscriptions(ctx, topicARN)
	if err != nil {
		return cloud.ResourceSpec{}, nil, err
	}
	edges = append(edges, subEdges...)

	return res, edges, nil
}

// collectSubscriptions lists all subscriptions for a topic and emits a
// TARGETS edge per subscription with protocol metadata.
func (c *snsCollector) collectSubscriptions(ctx context.Context, topicARN string) ([]cloud.EdgeSpec, error) {
	var edges []cloud.EdgeSpec

	paginator := sns.NewListSubscriptionsByTopicPaginator(c.client, &sns.ListSubscriptionsByTopicInput{
		TopicArn: awssdk.String(topicARN),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("sns: list subscriptions for %s: %w", topicARN, err)
		}

		for _, sub := range page.Subscriptions {
			protocol := awssdk.ToString(sub.Protocol)
			endpoint := awssdk.ToString(sub.Endpoint)
			if endpoint == "" || protocol == "" {
				continue
			}
			edges = append(edges, cloud.EdgeSpec{
				SourceID:     topicARN,
				TargetID:     endpoint,
				Relationship: kgtypes.EdgeTargets,
				Metadata:     map[string]string{"protocol": protocol},
			})
		}
	}

	return edges, nil
}

// snsTopicMetadata extracts discriminating fields from SNS topic attributes.
func snsTopicMetadata(attrs map[string]string) map[string]string {
	m := make(map[string]string, 3)
	if v := attrs["DisplayName"]; v != "" {
		m["display_name"] = v
	}
	if v := attrs["FifoTopic"]; v != "" {
		m["fifo_topic"] = v
	}
	if v := attrs["SubscriptionsConfirmed"]; v != "" {
		m["subscriptions_confirmed"] = v
	}
	return m
}
