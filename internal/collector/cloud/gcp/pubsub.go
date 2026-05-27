// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"encoding/json"
	"strconv"

	"cloud.google.com/go/pubsub"
	"google.golang.org/api/iterator"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// pubsubTopicsSubCollector collects Pub/Sub topics.
type pubsubTopicsSubCollector struct {
	client    *pubsub.Client
	projectID string
}

func newPubSubTopicsSubCollector(client *pubsub.Client, projectID string) *pubsubTopicsSubCollector {
	return &pubsubTopicsSubCollector{client: client, projectID: projectID}
}

func (c *pubsubTopicsSubCollector) Name() string { return "gcp-pubsub-topics" }

func (c *pubsubTopicsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.Topics(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		topic, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		// Topic.String() returns the full resource name: projects/P/topics/T
		topicName := topic.String()
		if topicName == "" {
			continue
		}

		contentMap := map[string]string{
			"name": topicName,
			"id":   topic.ID(),
		}

		// Fetch topic config to get CMEK information (per-topic RPC).
		var kmsKeyName string
		if cfg, cfgErr := topic.Config(ctx); cfgErr == nil {
			kmsKeyName = cfg.KMSKeyName
			if kmsKeyName != "" {
				contentMap["kms_key_name"] = kmsKeyName
			}
		}

		content, err := json.Marshal(contentMap)
		if err != nil {
			continue
		}

		spec := cloud.ResourceSpec{
			ID:           topicName,
			Name:         topic.ID(),
			ResourceType: "gcp:pubsub:topic",
			Content:      content,
		}
		result.Resources = append(result.Resources, spec)

		// ENCRYPTS_WITH edge when CMEK is configured.
		if kmsKeyName != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     topicName,
				TargetID:     kmsKeyName,
				Relationship: kgtypes.EdgeEncryptsWith,
			})
		}
	}

	return result, nil
}

// pubsubSubscriptionsSubCollector collects Pub/Sub subscriptions.
type pubsubSubscriptionsSubCollector struct {
	client    *pubsub.Client
	projectID string
}

func newPubSubSubscriptionsSubCollector(client *pubsub.Client, projectID string) *pubsubSubscriptionsSubCollector {
	return &pubsubSubscriptionsSubCollector{client: client, projectID: projectID}
}

func (c *pubsubSubscriptionsSubCollector) Name() string { return "gcp-pubsub-subscriptions" }

func (c *pubsubSubscriptionsSubCollector) Collect(ctx context.Context) (cloud.SubCollectorResult, error) {
	var result cloud.SubCollectorResult

	it := c.client.Subscriptions(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		sub, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return result, err
		}

		subName := sub.String()
		if subName == "" {
			continue
		}

		// Get the subscription config to access the topic reference.
		cfg, err := sub.Config(ctx)
		if err != nil {
			// Best-effort: still create the node without the topic edge.
			content, err := json.Marshal(map[string]string{
				"name": subName,
				"id":   sub.ID(),
			})
			if err != nil {
				continue
			}
			result.Resources = append(result.Resources, cloud.ResourceSpec{
				ID:           subName,
				Name:         sub.ID(),
				ResourceType: "gcp:pubsub:subscription",
				Content:      content,
			})
			continue
		}

		content, err := json.Marshal(map[string]string{
			"name":  subName,
			"id":    sub.ID(),
			"topic": cfg.Topic.String(),
		})
		if err != nil {
			continue
		}

		spec := cloud.ResourceSpec{
			ID:           subName,
			Name:         sub.ID(),
			ResourceType: "gcp:pubsub:subscription",
			Content:      content,
		}
		result.Resources = append(result.Resources, spec)

		// Subscription -> Topic (SUBSCRIBES_TO).
		if cfg.Topic != nil {
			topicName := cfg.Topic.String()
			if topicName != "" {
				result.Edges = append(result.Edges, cloud.EdgeSpec{
					SourceID:     subName,
					TargetID:     topicName,
					Relationship: kgtypes.EdgeSubscribesTo,
				})
			}
		}

		// Dead letter topic edge if configured.
		if cfg.DeadLetterPolicy != nil && cfg.DeadLetterPolicy.DeadLetterTopic != "" {
			result.Edges = append(result.Edges, cloud.EdgeSpec{
				SourceID:     subName,
				TargetID:     cfg.DeadLetterPolicy.DeadLetterTopic,
				Relationship: kgtypes.EdgeDeadLettersTo,
				Metadata: map[string]string{
					"max_delivery_attempts": strconv.Itoa(cfg.DeadLetterPolicy.MaxDeliveryAttempts),
				},
			})
		}
	}

	return result, nil
}
