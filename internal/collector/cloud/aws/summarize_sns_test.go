// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSNSTopic(t *testing.T) {
	got := summarizeSNSTopic(cloud.ResourceSpec{
		Name: "events", Region: "us-east-1",
		Metadata: map[string]string{"display_name": "Events", "fifo_topic": "true", "subscriptions_confirmed": "5"},
	})
	assert.Contains(t, got, "SNS topic events")
	assert.Contains(t, got, "(Events)")
	assert.Contains(t, got, "FIFO")
	assert.Contains(t, got, "subs=5")
}

func TestSummarizeSNSTopic_EmptyMeta(t *testing.T) {
	assert.Equal(t, "SNS topic x", summarizeSNSTopic(cloud.ResourceSpec{Name: "x"}))
}
