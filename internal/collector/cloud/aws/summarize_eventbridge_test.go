// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeEventBridgeRule(t *testing.T) {
	got := summarizeEventBridgeRule(cloud.ResourceSpec{
		Name: "nightly-job", Region: "us-east-1",
		Metadata: map[string]string{"state": "ENABLED", "event_bus_name": "my-bus", "schedule_expression": "cron(0 0 * * ? *)"},
	})
	assert.Contains(t, got, "EventBridge rule nightly-job")
	assert.Contains(t, got, "state=ENABLED")
	assert.Contains(t, got, "bus=my-bus")
	assert.Contains(t, got, "schedule=cron(0 0 * * ? *)")
}

func TestSummarizeEventBridgeRule_EmptyMeta(t *testing.T) {
	assert.Equal(t, "EventBridge rule x", summarizeEventBridgeRule(cloud.ResourceSpec{Name: "x"}))
}
