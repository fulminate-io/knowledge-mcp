// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeCloudWatchLogGroup(t *testing.T) {
	got := summarizeCloudWatchLogGroup(cloud.ResourceSpec{
		Name: "/aws/lambda/foo", Region: "us-east-1",
		Metadata: map[string]string{"retention_days": "30", "kms_key_id": "key-1"},
	})
	assert.Contains(t, got, "CloudWatch log group /aws/lambda/foo")
	assert.Contains(t, got, "retention=30 days")
	assert.Contains(t, got, "kms-encrypted")
	assert.Contains(t, got, "in us-east-1")
}

func TestSummarizeCloudWatchLogGroup_EmptyMeta(t *testing.T) {
	assert.Equal(t, "CloudWatch log group x", summarizeCloudWatchLogGroup(cloud.ResourceSpec{Name: "x"}))
}
