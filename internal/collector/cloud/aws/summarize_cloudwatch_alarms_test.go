// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeCloudWatchAlarm(t *testing.T) {
	got := summarizeCloudWatchAlarm(cloud.ResourceSpec{
		Name: "high-cpu", Region: "us-east-1",
		Metadata: map[string]string{"namespace": "AWS/EC2", "metric_name": "CPUUtilization", "state": "OK"},
	})
	assert.Contains(t, got, "CloudWatch alarm high-cpu")
	assert.Contains(t, got, "on AWS/EC2/CPUUtilization")
	assert.Contains(t, got, "state=OK")
}

func TestSummarizeCloudWatchAlarm_EmptyMeta(t *testing.T) {
	assert.Equal(t, "CloudWatch alarm x", summarizeCloudWatchAlarm(cloud.ResourceSpec{Name: "x"}))
}
