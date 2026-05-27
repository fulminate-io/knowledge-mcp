// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSQSQueue(t *testing.T) {
	got := summarizeSQSQueue(cloud.ResourceSpec{
		Name: "tasks", Region: "us-east-1",
		Metadata: map[string]string{"fifo_queue": "true", "visibility_timeout": "30", "approximate_number_of_messages": "10"},
	})
	assert.Contains(t, got, "SQS queue tasks")
	assert.Contains(t, got, "FIFO")
	assert.Contains(t, got, "vt=30s")
	assert.Contains(t, got, "messages=10")
}

func TestSummarizeSQSQueue_EmptyMeta(t *testing.T) {
	assert.Equal(t, "SQS queue x", summarizeSQSQueue(cloud.ResourceSpec{Name: "x"}))
}
