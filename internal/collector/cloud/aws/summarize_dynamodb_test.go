// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeDynamoDBTable(t *testing.T) {
	got := summarizeDynamoDBTable(cloud.ResourceSpec{
		Name: "users", Region: "us-east-1",
		Metadata: map[string]string{"status": "ACTIVE", "billing_mode": "PAY_PER_REQUEST", "item_count": "1234"},
	})
	assert.Contains(t, got, "DynamoDB table users")
	assert.Contains(t, got, "status=ACTIVE")
	assert.Contains(t, got, "billing=PAY_PER_REQUEST")
	assert.Contains(t, got, "items=1234")
}

func TestSummarizeDynamoDBTable_EmptyMeta(t *testing.T) {
	assert.Equal(t, "DynamoDB table x", summarizeDynamoDBTable(cloud.ResourceSpec{Name: "x"}))
}
