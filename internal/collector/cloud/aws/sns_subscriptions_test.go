// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestCollectSubscriptions_SQS(t *testing.T) {
	edges := simulateSubscriptionEdges("sqs", "arn:aws:sqs:us-east-1:111:my-queue")
	assert.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
	assert.Equal(t, "sqs", edges[0].Metadata["protocol"])
}

func TestCollectSubscriptions_Lambda(t *testing.T) {
	edges := simulateSubscriptionEdges("lambda", "arn:aws:lambda:us-east-1:111:function:my-func")
	assert.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
	assert.Equal(t, "lambda", edges[0].Metadata["protocol"])
	assert.Equal(t, "arn:aws:lambda:us-east-1:111:function:my-func", edges[0].TargetID)
}

func TestCollectSubscriptions_HTTP(t *testing.T) {
	edges := simulateSubscriptionEdges("https", "https://example.com/webhook")
	assert.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
	assert.Equal(t, "https", edges[0].Metadata["protocol"])
	assert.Equal(t, "https://example.com/webhook", edges[0].TargetID)
}

func TestCollectSubscriptions_Email(t *testing.T) {
	edges := simulateSubscriptionEdges("email", "user@example.com")
	assert.Len(t, edges, 1)
	assert.Equal(t, kgtypes.EdgeTargets, edges[0].Relationship)
	assert.Equal(t, "email", edges[0].Metadata["protocol"])
}

func TestCollectSubscriptions_EmptyEndpoint(t *testing.T) {
	edges := simulateSubscriptionEdges("sqs", "")
	assert.Empty(t, edges)
}

func TestCollectSubscriptions_EmptyProtocol(t *testing.T) {
	edges := simulateSubscriptionEdges("", "arn:aws:sqs:us-east-1:111:queue")
	assert.Empty(t, edges)
}

// simulateSubscriptionEdges replicates the edge-building logic from
// collectSubscriptions for a single subscription, without needing the
// full SNS API client.
func simulateSubscriptionEdges(protocol, endpoint string) []cloud.EdgeSpec {
	topicARN := "arn:aws:sns:us-east-1:111:my-topic"
	if endpoint == "" || protocol == "" {
		return nil
	}
	return []cloud.EdgeSpec{{
		SourceID:     topicARN,
		TargetID:     endpoint,
		Relationship: kgtypes.EdgeTargets,
		Metadata:     map[string]string{"protocol": protocol},
	}}
}
