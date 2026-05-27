// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeDynamoDBPITR(t *testing.T) {
	got := summarizeDynamoDBPITR(cloud.ResourceSpec{
		Name: "PITR for users", Region: "us-east-1",
		Metadata: map[string]string{"status": "ENABLED"},
	})
	assert.Contains(t, got, "DynamoDB PITR PITR for users")
	assert.Contains(t, got, "status=ENABLED")
}

func TestSummarizeDynamoDBPITR_EmptyMeta(t *testing.T) {
	assert.Equal(t, "DynamoDB PITR x", summarizeDynamoDBPITR(cloud.ResourceSpec{Name: "x"}))
}

func TestSummarizeDynamoDBBackup(t *testing.T) {
	got := summarizeDynamoDBBackup(cloud.ResourceSpec{
		Name: "users-bkp", Region: "us-east-1",
		Metadata: map[string]string{"backup_status": "AVAILABLE", "backup_type": "USER"},
	})
	assert.Contains(t, got, "DynamoDB backup users-bkp")
	assert.Contains(t, got, "status=AVAILABLE")
	assert.Contains(t, got, "type=USER")
}

func TestSummarizeDynamoDBBackup_EmptyMeta(t *testing.T) {
	assert.Equal(t, "DynamoDB backup x", summarizeDynamoDBBackup(cloud.ResourceSpec{Name: "x"}))
}
