// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeKinesisStream(t *testing.T) {
	got := summarizeKinesisStream(cloud.ResourceSpec{
		Name: "events", Region: "us-east-1",
		Metadata: map[string]string{"status": "ACTIVE", "retention_hours": "24", "encryption_type": "KMS"},
	})
	assert.Contains(t, got, "Kinesis stream events")
	assert.Contains(t, got, "status=ACTIVE")
	assert.Contains(t, got, "retention=24h")
	assert.Contains(t, got, "encryption=KMS")
}

func TestSummarizeKinesisStream_EmptyMeta(t *testing.T) {
	assert.Equal(t, "Kinesis stream x", summarizeKinesisStream(cloud.ResourceSpec{Name: "x"}))
}
